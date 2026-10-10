package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// APIs RESPONDIDAS pela propria borda (campo "edge" da rota no routes.json),
// decisoes da reuniao de 05/10 com o Joel: C.6.8 (D-0510-2) e C.6.7.8/C.6.7.9
// (D-0510-3). A requisicao passa antes pela mesma decisao de credencial das
// demais rotas (evaluate: 100, 106, 107; em warn so avisa) e depois chega ao
// handler daqui, sem repasse ao roteador do KrakenD. O endpoint dessas rotas
// continua gerado no KrakenD (generate.js), mas nunca eh alcancado: ele so
// mantem o caminho registrado no roteador, como o de qualquer outra rota.

const (
	edgeBindRegister = "bind-context-register" // POST   /tv3/bind-context (C.6.8.2)
	edgeBindList     = "bind-context-list"     // GET    /tv3/bind-context (C.6.8.3)
	edgeBindRemove   = "bind-context-remove"   // DELETE /tv3/bind-context (C.6.8.4)
	edgeAPIInfo      = "api-info"              // GET    /tv3/api-info/{apiId} (C.6.7.8)
	edgeAPIList      = "api-list"              // GET    /tv3/api-info (C.6.7.9)
)

// edgeReply: Code 0 = sucesso (status 200 + Body em JSON); senao erro C.3.2.
type edgeReply struct {
	Code   int
	Detail string
	Body   interface{}
}

type edgeFunc func(h *authHandler, r *http.Request, params map[string]string) edgeReply

var edgeHandlers = map[string]edgeFunc{
	edgeBindRegister: (*authHandler).bindRegister,
	edgeBindList:     (*authHandler).bindList,
	edgeBindRemove:   (*authHandler).bindRemove,
	edgeAPIInfo:      (*authHandler).apiInfo,
	edgeAPIList:      (*authHandler).apiList,
}

func edgeHandlerNames() string {
	names := make([]string, 0, len(edgeHandlers))
	for n := range edgeHandlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Negociacao de versao (C.3.6.5), o MESMO contrato do tv3ws
// (tv3ws/src/middleware/basic.ts), que continua fazendo isso nas rotas dele:
// sem Accept-Version vale a 2.0; fora do formato X.Y => 101; fora do
// conjunto => 100. A 2.1 eh a proposta do Forum (fluxo de remote-device por
// handle); as APIs daqui respondem igual nas duas.
// A lista vai em ordem crescente: a ultima eh a mais recente (latestVersion,
// o API-Version do erro 100, C.3.6.6).
var (
	supportedVersions = []string{"2.0", "2.1"}
	latestVersion     = supportedVersions[len(supportedVersions)-1]
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

const defaultVersion = "2.0"

func negotiateVersion(r *http.Request) (version string, code int, detail string) {
	vals := r.Header.Values("Accept-Version")
	if len(vals) == 0 {
		return defaultVersion, 0, ""
	}
	// varios cabecalhos: o node junta com ", " (e o resultado eh malformado)
	v := strings.Join(vals, ", ")
	if !versionPattern.MatchString(v) {
		return "", 101, "malformed Accept-Version '" + v + "'"
	}
	for _, s := range supportedVersions {
		if v == s {
			return v, 0, ""
		}
	}
	return "", 100, "unsupported version '" + v + "' (supported: " + strings.Join(supportedVersions, ", ") + ")"
}

// serveEdge responde uma rota da borda. O aviso do modo warn (se houver)
// ja esta nos cabecalhos e fica: ele diz o que o enforce faria.
func (h *authHandler) serveEdge(w http.ResponseWriter, r *http.Request, v verdict) {
	version, code, detail := negotiateVersion(r)
	reply := edgeReply{Code: code, Detail: detail}
	if code == 0 {
		reply = h.runEdge(edgeHandlers[v.rt.Edge], r, v.params)
	}
	if reply.Code != 0 {
		logf("API surface=%s code=%d %s %q detalhe=%q", h.cfg.Surface, reply.Code, r.Method, r.URL.Path, reply.Detail)
		writeC32(w, r, reply.Code, reply.Detail)
		return
	}
	body, err := encodeJSON(reply.Body)
	if err != nil {
		logf("API surface=%s code=200 %s %q resposta nao serializa: %v", h.cfg.Surface, r.Method, r.URL.Path, err)
		writeC32(w, r, 200, "falha interna da borda")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("API-Version", version)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// runEdge: um panic no handler vira 404 + {error:200}, como o do roteador
// (serveNext), em vez de conexao fechada.
func (h *authHandler) runEdge(fn edgeFunc, r *http.Request, params map[string]string) (reply edgeReply) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler {
			panic(p)
		}
		logf("PANIC recuperado na API da borda surface=%s %s %q: %v", h.cfg.Surface, r.Method, r.URL.Path, p)
		reply = edgeReply{Code: 200, Detail: "falha interna da borda"}
	}()
	return fn(h, r, params)
}

// redisDown: falha do Redis numa API da borda => 200 (Tabela C.1, "Platform
// resource unavailable"), nos dois modos.
func redisDown(err error) edgeReply {
	return edgeReply{Code: 200, Detail: "redis indisponivel (" + err.Error() + ")"}
}

// encodeJSON sem o escape de <, > e & do json.Marshal (o JSON.stringify do
// tv3ws, que gravava as mesmas chaves no Redis, nao escapa) e sem a quebra
// de linha final do Encoder.
func encodeJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
