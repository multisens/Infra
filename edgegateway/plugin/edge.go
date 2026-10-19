package main

import (
	"bytes"
	"context"
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
// handler daqui, sem repasse ao roteador do KrakenD. A versao ja vem
// negociada (ServeHTTP, handler.go). O endpoint dessas rotas
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

// Negociacao de versao (C.3.6.5) — D2 e D4 (Joel, 15/10): a BORDA negocia a
// versao em toda rota, a partir das versoes que o routes.json declara para
// ela (campo versions; ausente = 2.0, a "current version" da C.3.6.3), e
// repassa a versao decidida ao servico no cabecalho interno
// X-TV30-API-Version, que apaga o que vier do cliente. O tv3ws deixou de
// interpretar o Accept-Version. Ate 15/10, o tv3ws negociava nas rotas dele e
// a borda nas que ela responde, com o mesmo conjunto ['2.0', '2.1'] escrito a
// mao nos dois lugares. Sem Accept-Version vale a 2.0; fora do formato X.Y =>
// 101; versao que a rota nao serve => 100. O 100 e o 101 disparam nos dois
// modos. As versoes maiores de 100 em diante sao as das propostas do testbed
// (D1): hoje so a 100.0, o fluxo de remote-device por handle.
const (
	defaultVersion        = "2.0"
	internalVersionHeader = "X-TV30-API-Version"
	// internalHostHeader: o Host original da requisicao do cliente, que o
	// KrakenD troca pelo do backend. O /manifest do tv3ws monta com ele o
	// Server-BaseURL, para sair no mesmo host que o anunciante nativo detecta
	// (complemento do Joel de 15/10, 3.4). Apaga o que vier do cliente.
	internalHostHeader = "X-TV30-Host"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// compareVersions: "X.Y" numerico (C.3.6.2): 2.10 e mais recente que 2.9.
func compareVersions(a, b string) int {
	am, an := splitVersion(a)
	bm, bn := splitVersion(b)
	if am != bm {
		return am - bm
	}
	return an - bn
}

func splitVersion(v string) (int, int) {
	maj, min, _ := strings.Cut(v, ".")
	x, _ := strconv.Atoi(maj)
	y, _ := strconv.Atoi(min)
	return x, y
}

func negotiateVersion(r *http.Request, served []string) (version string, code int, detail string) {
	vals := r.Header.Values("Accept-Version")
	v := defaultVersion
	if len(vals) > 0 {
		// varios cabecalhos: o node junta com ", " (e o resultado eh malformado)
		v = strings.Join(vals, ", ")
		if !versionPattern.MatchString(v) {
			return "", 101, "malformed Accept-Version '" + v + "'"
		}
	}
	for _, s := range served {
		if v == s {
			return v, 0, ""
		}
	}
	return "", 100, "unsupported version '" + v + "' for this API (served: " + strings.Join(served, ", ") + ")"
}

// versionInfo: as versoes da rota da requisicao e a mais recente do servidor,
// guardadas no contexto da requisicao para o API-Version das respostas que a
// borda escreve (apiVersion, handler.go).
type versionInfo struct {
	served []string
	latest string
}

type versionCtxKey struct{}

func withVersionInfo(r *http.Request, vi versionInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), versionCtxKey{}, vi))
}

func versionInfoOf(r *http.Request) versionInfo {
	if vi, ok := r.Context().Value(versionCtxKey{}).(versionInfo); ok {
		return vi
	}
	return versionInfo{served: []string{defaultVersion}, latest: defaultVersion}
}

// serveEdge responde uma rota da borda. O aviso do modo warn (se houver)
// ja esta nos cabecalhos e fica: ele diz o que o enforce faria.
func (h *authHandler) serveEdge(w http.ResponseWriter, r *http.Request, v verdict, version string) {
	reply := h.runEdge(edgeHandlers[v.rt.Edge], r, v.params)
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

// redisWriteFailed: falha numa escrita (C.6.8.2, C.6.8.4). A recusa da
// funcao do Redis por dado fora do esquema (P4, Luis em 10/10) e defeito da
// borda, nao Redis fora do ar: responde como a falha de Redis (404
// {error:200}) e o detalhe diz o que a funcao recusou (serveEdge loga).
func redisWriteFailed(err error) edgeReply {
	if msg, ok := schemaRefusal(err); ok {
		return edgeReply{Code: 200, Detail: "escrita recusada pelo esquema do Redis (" + msg + ")"}
	}
	return redisDown(err)
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
