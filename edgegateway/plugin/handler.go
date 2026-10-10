package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Textos da Tabela C.1 — os mesmos do catalogo do tv3ws (src/util/error.ts).
// 101, 105 e 300 so saem das APIs respondidas pela borda (edge.go).
var errorText = map[int]string{
	100: "API not found",
	101: "Illegal argument value",
	104: "Access not authorized by the broadcaster",
	105: "Missing argument",
	106: "API unavailable for this runtime environment",
	107: "Invalid or outdated access token",
	108: "Invalid or revoked bind token",
	200: "Platform resource unavailable",
	300: "No DTV service currently in use",
}

const warnHeader = "X-TV30-Auth-Warn"

// verdict eh a decisao sobre uma requisicao. Code 0 = liberada.
type verdict struct {
	Code   int
	Detail string
	Class  string
	Route  string
	rt     *route            // rota casada (nil = nao declarada)
	params map[string]string // valores dos {param} do caminho
}

func (v verdict) fail(code int, detail string) verdict {
	v.Code, v.Detail = code, detail
	return v
}

type authHandler struct {
	cfg   *config
	store store
	keys  *keyCache
	next  http.Handler
	now   func() time.Time
}

func newHandler(cfg *config, st store, next http.Handler) *authHandler {
	return &authHandler{cfg: cfg, store: st, keys: &keyCache{}, next: next, now: time.Now}
}

func (h *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// C.4.1.9.2: "Access-Control-Allow-Origin: *" em TODA resposta das APIs.
	// O modulo CORS do KrakenD so o acrescenta quando a requisicao traz
	// Origin; definido aqui, vale tambem para cliente fora do navegador. Set
	// (e nao Add): o modulo CORS tambem usa Set, entao o cabecalho sai uma
	// vez so (duplicado, o Chrome recusa — por isso o tv3ws nao o envia,
	// tv3ws/src/middleware/basic.ts).
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		// preflight CORS (com Access-Control-Request-Method): passa sempre;
		// o modulo CORS do KrakenD responde.
		if r.Header.Get("Access-Control-Request-Method") != "" {
			h.serveNext(w, r)
			return
		}
		// OPTIONS que nao eh preflight numa API declarada (qualquer metodo no
		// mesmo caminho): a C.4.1.9.3 manda responder com ACAO, ACAM e ACAH.
		// Caminho nao declarado segue a tabela e da 100 (formato C.3.2, em
		// vez do texto do Gin).
		if h.cfg.Routes.pathDeclared(r.URL.Path) {
			writeOptions(w, r, h.cfg.CORSAllowHeaders)
			return
		}
	}
	v := h.evaluate(r)
	if v.Code != 0 {
		class := v.Class
		if class == "" {
			class = "-"
		}
		// caminho com %q: r.URL.Path ja vem decodificado, e um %0A nele
		// forjaria linhas [tv30-auth] no log.
		if h.cfg.Mode == modeEnforce || v.Code == 100 {
			logf("DENY surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
			writeError(w, r, v.Code, v.Detail)
			return
		}
		logf("WARN surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
		w.Header().Set(warnHeader, strconv.Itoa(v.Code))
	}
	// API respondida pela propria borda (C.6.8, C.6.7.8/C.6.7.9): nao vai ao
	// roteador do KrakenD.
	if v.rt != nil && v.rt.Edge != "" {
		h.serveEdge(w, r, v)
		return
	}
	h.serveNext(w, r)
}

// serveNext repassa ao roteador do KrakenD e troca por 404 + {error:200}
// (C.3.2.1; Tabela C.1, 200 = "Platform resource unavailable", "dependence
// on an unavailable resource") as duas falhas que sairiam fora do formato:
//
//   - resposta 5xx: o KrakenD responde 500 SEM corpo quando o backend nao
//     responde dentro do timeout do endpoint (padrao de 2 s; so as rotas com
//     "timeout" no routes.json tem outro) ou esta fora do ar (conexao
//     recusada, nome que nao resolve). O tv3ws nunca responde 5xx: a camada
//     comum de erro dele (src/util/error.ts) so emite 404, e nenhum handler
//     escreve 5xx. Logo, todo 5xx aqui vem do KrakenD; como as rotas sao
//     no-op, um 5xx de backend tambem seria trocado. Os timeouts nao mudam;
//   - panic no roteador: em vez de conexao resetada.
//
// Vale nos dois modos, como o 100: nao eh decisao de credencial.
func (h *authHandler) serveNext(w http.ResponseWriter, r *http.Request) {
	gw := &guardWriter{ResponseWriter: w}
	start := h.now()
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler {
			panic(p)
		}
		logf("PANIC recuperado surface=%s %s %q: %v", h.cfg.Surface, r.Method, r.URL.Path, p)
		if gw.sent || gw.hijacked {
			// o cabecalho ja foi ao cliente: nao ha como trocar a resposta.
			// Aborta a conexao em vez de emendar um corpo de erro no meio.
			panic(http.ErrAbortHandler)
		}
		gw.replace(r, "falha interna do gateway")
	}()
	h.next.ServeHTTP(gw, r)
	if gw.held != 0 && !gw.hijacked {
		ms := h.now().Sub(start).Milliseconds()
		logf("BACKEND surface=%s code=200 %s %q status_gateway=%d ms=%d", h.cfg.Surface, r.Method, r.URL.Path, gw.held, ms)
		gw.replace(r, fmt.Sprintf("%d do gateway em %d ms (backend lento ou fora do ar)", gw.held, ms))
	}
}

// guardWriter fica entre o plugin e o roteador do KrakenD. Um status 5xx eh
// RETIDO (nem cabecalho nem corpo chegam ao cliente) para o serveNext troca-
// lo pelo erro C.3.2; qualquer outro status passa intacto, sem copia.
type guardWriter struct {
	// interface embutida: so Header, Write e WriteHeader sao promovidos (um
	// ReadFrom do writer de baixo, usado pelo io.Copy, furaria a retencao)
	http.ResponseWriter
	status   int  // status final escrito (0 = nenhum ainda)
	held     int  // status 5xx retido (0 = nenhum)
	sent     bool // status final ja repassado ao cliente
	hijacked bool
}

// O gin.ResponseWriter (gin v1.9.1, a do KrakenD 2.7.2: response_writer.go)
// faz type assertion SEM ok para estas tres interfaces no writer de baixo:
// faltando uma, quem a chamasse levaria panic.
var (
	_ http.Flusher       = (*guardWriter)(nil)
	_ http.Hijacker      = (*guardWriter)(nil)
	_ http.CloseNotifier = (*guardWriter)(nil)
)

func (g *guardWriter) WriteHeader(code int) {
	if g.status != 0 {
		if g.held == 0 {
			g.ResponseWriter.WriteHeader(code) // superfluo: o net/http avisa
		}
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		g.ResponseWriter.WriteHeader(code) // 1xx informativo: nao fecha o status
		return
	}
	g.status = code
	if code >= 500 {
		g.held = code
		return
	}
	g.sent = true
	g.ResponseWriter.WriteHeader(code)
}

func (g *guardWriter) Write(b []byte) (int, error) {
	if g.status == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if g.held != 0 {
		return len(b), nil // descartado: o corpo do 5xx nao sai
	}
	return g.ResponseWriter.Write(b)
}

// Flush com 5xx retido nao faz nada: repassado, mandaria o cabecalho.
func (g *guardWriter) Flush() {
	if g.held != 0 {
		return
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		if g.status == 0 {
			g.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func (g *guardWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := g.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	c, rw, err := hj.Hijack()
	if err == nil {
		g.hijacked = true
	}
	return c, rw, err
}

func (g *guardWriter) CloseNotify() <-chan bool {
	if cn, ok := g.ResponseWriter.(http.CloseNotifier); ok {
		return cn.CloseNotify()
	}
	return make(chan bool) // nunca dispara
}

// Unwrap: para o http.ResponseController (prazos de leitura/escrita).
func (g *guardWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// replace escreve o erro 200 no lugar da resposta retida (ou da que o panic
// interrompeu antes do cabecalho). Os cabecalhos ja definidos saem — os de
// backend (Content-Encoding, Content-Range, X-Powered-By...) nao descrevem o
// corpo novo —, menos os de CORS (o navegador precisa deles para ler o
// corpo), os do KrakenD (X-Krakend*) e o aviso do modo warn: ele diz o que o
// enforce faria com a requisicao, e isso nao muda porque o backend falhou.
func (g *guardWriter) replace(r *http.Request, detail string) {
	hd := g.ResponseWriter.Header()
	for k := range hd {
		if !keptOnReplace(k) {
			delete(hd, k)
		}
	}
	g.sent = true
	writeC32(g.ResponseWriter, r, 200, detail)
}

func keptOnReplace(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return strings.HasPrefix(k, "Access-Control-") || k == "Vary" ||
		strings.HasPrefix(k, "X-Krakend") || k == http.CanonicalHeaderKey(warnHeader)
}

// evaluate decide, na ordem da especificacao: rota (100) -> classe (106) ->
// access token (107) -> bind-token (104/108). Falha no Redis => 200.
func (h *authHandler) evaluate(r *http.Request) verdict {
	rt, params := h.cfg.Routes.match(r.Method, r.URL.Path)
	if rt == nil {
		return verdict{Code: 100, Detail: r.Method + " " + r.URL.Path}
	}
	v := verdict{Route: rt.String(), rt: rt, params: params}
	if rt.Auth == authNone && rt.Classes == nil {
		return v
	}
	now := h.now()

	// -- classe do cliente (D7) --
	authz := r.Header.Get("Authorization")
	var claims *accessClaims
	var tokenErr error
	if authz != "" {
		if claims, tokenErr = verifyAccessToken(authz, h.cfg.Secret, h.cfg.Issuer, now); tokenErr == nil {
			v.Class = claims.Class
		}
	}
	// DECIDIDO (Luis, 03/10): risco aceito — lacuna L1. Requisicao SEM
	// Authorization cujo Origin esta em origins:associated eh tratada como
	// local associado. Risco aceito: o Origin eh forjavel fora do navegador,
	// e quem o forja passa como associado (sem access token nem bind-token).
	// A norma (C.4.1.7) fala na porta de origem atribuida pelo gerenciador e
	// deixa o mecanismo a cargo da implementacao; o Origin tambem nao
	// discrimina as apps de emissora servidas por proxy na origem do AoP.
	// Com Authorization presente mas INVALIDO, o Origin tambem eh consultado,
	// so para a checagem de classe (106): senao bastaria mandar qualquer
	// Authorization para escapar do 106 da L4 (o tv3ws classifica pelo
	// Origin). Isso nao dispensa credencial — o 107 continua valendo.
	originAssoc := false
	if origin := r.Header.Get("Origin"); origin != "" && claims == nil && (authz == "" || rt.Classes != nil) {
		assoc, err := h.store.IsAssociatedOrigin(origin)
		if err != nil {
			return v.fail(200, "redis indisponivel ("+err.Error()+")")
		}
		originAssoc = assoc
	}
	if authz == "" && originAssoc {
		v.Class = classAssociated
	}

	// -- classe permitida na rota (106) --
	// PENDENTE (Joel): lacuna L4 — em /authorize e /token o 106 ao
	// associado so bloqueia em enforce; o tv3ws continua emitindo token ao
	// associado. Limites deste provisorio: o 106 depende do Origin (um
	// /tv3/token chamado fora do navegador, sem Origin, passa e o tv3ws
	// emite o token com a classe gravada do cliente), e um access token
	// VALIDO de outra classe prevalece sobre o Origin.
	// PENDENTE (Joel): lacuna L3 — sem TLS na borda (44643 em HTTP), o 106
	// por protocolo (nao local fora de HTTPS, C.4.1.6) nao eh aplicado aqui
	// (o tv3ws o aplicava ate a reuniao de 05/10; com a D-0510-1 deixa de).
	classForRoute := v.Class
	if classForRoute == "" && originAssoc {
		classForRoute = classAssociated
	}
	if !rt.allows(classForRoute) {
		who := classForRoute
		if who == "" {
			who = "cliente nao identificado como " + classAssociated
		}
		return v.fail(106, "rota nao disponivel para "+who)
	}

	// -- associado reconhecido pelo Origin: usa as APIs sem access token e
	// sem bind-token (C.4.1.1, D8) --
	// PENDENTE (Joel): a norma dispensa o bind-token "referencing their own
	// service context"; o acesso do associado a contexto de OUTRA emissora
	// (rotas /tv3/{serviceContextId}/...) nao eh restringido aqui.
	if v.Class == classAssociated && claims == nil {
		return v
	}
	if rt.Auth == authNone {
		return v
	}

	// -- access token (107) --
	if authz == "" {
		return v.fail(107, "accessToken ausente")
	}
	if tokenErr != nil {
		return v.fail(107, tokenErr.Error())
	}
	// cliente bloqueado (C.4.2.2): vale para toda credencial valida,
	// inclusive token com class local-associated.
	if claims.Sub != "" {
		blocked, err := h.store.IsBlocked(claims.Sub)
		if err != nil {
			return v.fail(200, "redis indisponivel ("+err.Error()+")")
		}
		if blocked {
			return v.fail(107, "cliente bloqueado pelo usuario (C.4.2.2)")
		}
	}
	// token com class local-associated: dispensa o bind-token (D8)
	if v.Class == classAssociated || rt.Auth == authToken {
		return v
	}

	// -- bind-token (104/108) --
	bt := r.Header.Get("bind-token")
	if bt == "" {
		return v.fail(104, "bind-token ausente")
	}
	if scid, ok := params["serviceContextId"]; ok && !h.isCurrentSCID(scid) {
		return v.fail(108, "service-context-id '"+scid+"' nao corresponde ao servico corrente (provisorio, lacuna L2)")
	}
	sid, err := h.store.CurrentServiceID()
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if sid == "" {
		// PENDENTE (Joel): sem servico corrente nao ha chave registrada que
		// valide o token => 108 (a norma tambem preve 300 nessas APIs).
		return v.fail(108, "sem servico corrente: nenhuma chave registrada")
	}
	raw, err := h.store.BindKeys(sid)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	// D4: so valem as chaves registradas para o SERVICO CORRENTE.
	if err := verifyBindToken(bt, h.keys.decodeStoredKeys(sid, raw), now); err != nil {
		return v.fail(108, err.Error())
	}
	return v
}

func (h *authHandler) isCurrentSCID(scid string) bool {
	if scid == "current-service" {
		return true
	}
	for _, s := range h.cfg.CurrentSCIDs {
		if scid == s {
			return true
		}
	}
	return false
}

// writeError responde a requisicao BLOQUEADA pela decisao do plugin: sem o
// aviso do modo warn (a resposta ja eh o erro).
func writeError(w http.ResponseWriter, r *http.Request, code int, detail string) {
	w.Header().Del(warnHeader)
	writeC32(w, r, code, detail)
}

// writeC32 responde no formato C.3.2: status 404 + {error, description},
// application/json, com Access-Control-Allow-Origin: * (o plugin roda ANTES
// do modulo CORS do KrakenD; sem isso o navegador veria erro de CORS em vez
// do corpo) e API-Version (C.3.6.6).
func writeC32(w http.ResponseWriter, r *http.Request, code int, detail string) {
	desc := errorText[code]
	if detail != "" {
		desc += ": " + detail
	}
	body, _ := json.Marshal(struct {
		Error       int    `json:"error"`
		Description string `json:"description"`
	}{code, desc})
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("API-Version", apiVersion(r))
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusNotFound)
	w.Write(body)
}

// writeOptions responde o OPTIONS que nao eh preflight numa API declarada
// (C.4.1.9.3): os tres cabecalhos com os valores padrao da norma (ACAH com o
// key do DELETE /tv3/bind-context, como o modulo CORS). Status 200, o de
// sucesso da C.3.2.1; sem corpo.
func writeOptions(w http.ResponseWriter, r *http.Request, allowHeaders string) {
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("Access-Control-Allow-Methods", "*")
	hd.Set("Access-Control-Allow-Headers", allowHeaders)
	hd.Set("API-Version", apiVersion(r))
	hd.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// apiVersion: o API-Version das respostas que a propria borda escreve fora do
// sucesso de uma API dela (erros C.3.2 e o OPTIONS que nao e preflight), pela
// negociacao de negotiateVersion (edge.go), a mesma do tv3ws:
//   - versao pedida e suportada: ela (2.0 sem Accept-Version, C.3.6.5);
//   - versao pedida fora do conjunto (o erro 100 da negociacao): a mais
//     recente suportada. C.3.6.6: quando o servidor nao consegue responder de
//     forma compativel com a versao pedida, "the 'API-Version' header is
//     assigned to the latest version supported by the server";
//   - Accept-Version malformado (o erro 101): segue a 2.0, como antes. A
//     excecao da C.3.6.6 fala de versao pedida, e um cabecalho fora do
//     formato X.Y nao pede versao nenhuma.
func apiVersion(r *http.Request) string {
	switch v, code, _ := negotiateVersion(r); code {
	case 0:
		return v
	case 100:
		return latestVersion
	}
	return defaultVersion
}
