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
	// C.4.1.9.2: "Access-Control-Allow-Origin: *" em TODA resposta das APIs,
	// tambem para cliente fora do navegador. Set: o cabecalho sai uma vez so
	// (duplicado, o Chrome recusa — por isso o tv3ws nao o envia,
	// tv3ws/src/middleware/basic.ts). Desde 12/10 (decisao do Luis, opcao b)
	// o CORS e todo do plugin: o modulo CORS do KrakenD saiu da config,
	// porque ele envolve o plugin e respondia o preflight antes dele. Daqui
	// sai tambem o Access-Control-Expose-Headers que o modulo mandava
	// (cors.expose_headers); o de uma resposta do backend, como o do
	// /manifest (B3), vem somado, numa segunda linha.
	// versoes da rota (D2/D4): delas sai o API-Version das respostas que a
	// borda escreve, inclusive as de erro e o OPTIONS (apiVersion)
	vi := versionInfo{served: h.cfg.AllVersions, latest: h.cfg.LatestVersion}
	if rt, _ := h.cfg.Routes.match(r.Method, r.URL.Path); rt != nil {
		vi.served = rt.Versions
	}
	r = withVersionInfo(r, vi)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if h.cfg.CORSExposeHeaders != "" {
		w.Header().Set("Access-Control-Expose-Headers", h.cfg.CORSExposeHeaders)
	}
	if r.Method == http.MethodOptions {
		// Preflight CORS (com Access-Control-Request-Method): a borda responde
		// sozinha (decisao do Luis em 12/10, opcao b). Ate ali respondia o
		// modulo CORS do KrakenD (rs/cors 1.11.0 no KrakenD 2.7.2), que recusava
		// a lista de Access-Control-Request-Headers fora de ordem
		// lexicografica; a C.4.1.9.3 manda responder todo OPTIONS com ACAO,
		// ACAM e ACAH. Em qualquer caminho, como o modulo fazia: num caminho
		// nao declarado o navegador segue para a requisicao e le o 100 dela.
		if r.Header.Get("Access-Control-Request-Method") != "" {
			writeOptions(w, r, h.cfg.CORSAllowHeaders, h.cfg.CORSMaxAge)
			return
		}
		// OPTIONS que nao eh preflight numa API declarada (qualquer metodo no
		// mesmo caminho): a C.4.1.9.3 manda responder com ACAO, ACAM e ACAH.
		// Caminho nao declarado segue a tabela e da 100 (formato C.3.2, em
		// vez do texto do Gin).
		if h.cfg.Routes.pathDeclared(r.URL.Path) {
			writeOptions(w, r, h.cfg.CORSAllowHeaders, "")
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
		if h.cfg.Mode == modeEnforce || !followsMode(v.Code) {
			logf("DENY surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
			writeError(w, r, v.Code, v.Detail)
			return
		}
		logf("WARN surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
		w.Header().Set(warnHeader, strconv.Itoa(v.Code))
	}
	// -- versao (C.3.6.5; D2, Joel 15/10) --
	// Em toda rota declarada, depois da decisao de credencial (a mesma ordem
	// de antes: a credencial na borda, a versao no servico). O 100 e o 101
	// saem nos dois modos. A versao decidida vai ao servico no cabecalho
	// interno, que apaga o que vier do cliente.
	version, vcode, vdetail := negotiateVersion(r, vi.served)
	if vcode != 0 {
		logf("DENY surface=%s code=%d %s %q detalhe=%q", h.cfg.Surface, vcode, r.Method, r.URL.Path, vdetail)
		writeError(w, r, vcode, vdetail)
		return
	}
	r.Header.Set(internalVersionHeader, version)
	r.Header.Set(internalHostHeader, r.Host)
	// API respondida pela propria borda (C.6.8, C.6.7.8/C.6.7.9): nao vai ao
	// roteador do KrakenD.
	if v.rt != nil && v.rt.Edge != "" {
		h.serveEdge(w, r, v, version)
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

// followsMode diz se a recusa obedece ao modo (em warn so avisa). Regra do
// Joel em 15/10 ("Suas decisoes", modo warn): so as recusas de CREDENCIAL —
// 104 (bind-token ausente), 107 (access token) e 108 (bind-token invalido) —
// seguem o modo. As demais disparam nos dois: 100 (rota), 106 (tipo de
// cliente), 300 (estado do receptor) e o 200 do Redis fora do ar, que nao
// e recusa de credencial. Ate 15/10 so o 100 disparava em warn.
func followsMode(code int) bool {
	return code == 104 || code == 107 || code == 108
}

func keptOnReplace(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return strings.HasPrefix(k, "Access-Control-") || k == "Vary" ||
		strings.HasPrefix(k, "X-Krakend") || k == http.CanonicalHeaderKey(warnHeader)
}

// evaluate decide, nesta ordem: rota (100) -> superficie (106, F-1) ->
// classe (106; A8) -> nao local por HTTP (106, E7) -> access token (107) ->
// servico selecionado (300, H2) -> bind-token (104/108). Falha no Redis =>
// 200.
func (h *authHandler) evaluate(r *http.Request) verdict {
	rt, params := h.cfg.Routes.match(r.Method, r.URL.Path)
	if rt == nil {
		return verdict{Code: 100, Detail: r.Method + " " + r.URL.Path}
	}
	v := verdict{Route: rt.String(), rt: rt, params: params}
	// -- superficie (F-1, Joel 15/10; C.4.1.8) --
	// Na externa (Server-SecureBaseURL), as APIs de uso exclusivo do cliente
	// local e a C.6.1.2 ("can only be accessed via the HTTP protocol")
	// respondem 106 a qualquer requisicao, sem consultar a classe. Vale ja,
	// sem esperar o TLS da 44643, e nos dois modos. Ate 15/10 essas rotas nem
	// existiam na externa (100), e a C.6.1.2 era atendida la.
	if rt.SurfaceError != 0 {
		return v.fail(rt.SurfaceError, "API exclusiva da superficie HTTP (Server-BaseURL); nesta superficie, 106 a qualquer cliente (C.4.1.8)")
	}
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
	// A8 (Joel, 15/10): numa rota que nao admite o associado (C.6.1.2,
	// C.6.1.3, C.6.8.3), o Origin associado da 106 tambem com access token
	// VALIDO de outra classe (ate 15/10 o token prevalecia sobre o Origin).
	// A chamada sem Origin segue fora do alcance da borda: limite aceito em
	// Escopo e desenho 6.2 (A8). O tv3ws, como emissor, tambem recusa (106).
	excludesAssoc := rt.Classes != nil && !rt.allows(classAssociated)
	originAssoc, originSvc := false, ""
	if origin := r.Header.Get("Origin"); origin != "" && ((claims == nil && (authz == "" || rt.Classes != nil)) || excludesAssoc) {
		svc, assoc, err := h.store.AssociatedOrigin(origin)
		if err != nil {
			return v.fail(200, "redis indisponivel ("+err.Error()+")")
		}
		originAssoc, originSvc = assoc, svc
	}
	if authz == "" && originAssoc {
		v.Class = classAssociated
	}

	// -- classe permitida na rota (106) --
	// Dispara nos dois modos desde 15/10 (followsMode): o 106 e do tipo de
	// cliente, nao da credencial. Em /authorize e /token isso fecha a lacuna
	// L4 do lado da borda (A8).
	if originAssoc && excludesAssoc {
		return v.fail(106, "rota nao disponivel para "+classAssociated+" (Origin em origins:associated)")
	}
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
	// -- nao local por HTTP (106, C.4.1.6; E7, Joel 15/10) --
	// Na superficie HTTP (a interna, Server-BaseURL), o access token de classe
	// non-local so vale na C.6.1.2 e na C.6.1.3 ("the APIs that are not
	// authorization-related"); nas demais, 106. Ate 05/10 quem aplicava era o
	// tv3ws; saiu dele com a D-0510-1 e ficou sem ninguem ate aqui.
	if h.cfg.NonLocalHTTPSOnly && v.Class == classNonLocal && !rt.NonLocalHTTP {
		return v.fail(106, "cliente nao local por HTTP fora da C.6.1.2/C.6.1.3: use o Server-SecureBaseURL (C.4.1.6)")
	}

	// -- associado reconhecido pelo Origin: usa as APIs sem access token e,
	// no proprio contexto de servico, sem bind-token (C.4.1.1, D8) --
	if v.Class == classAssociated && claims == nil {
		if v = h.requireService(v); v.Code != 0 || rt.Auth != authTokenBind {
			return v
		}
		// H1 (Joel, 15/10): leitura literal da C.4.1.1 ("referencing their
		// own service context"). Nas rotas com bind-token, o contexto do
		// caminho (current-service = o servico corrente) e comparado com o
		// servico que lancou a app (origins:associated). Igual: sem
		// bind-token, como antes. Diferente: a regra geral — 104 sem
		// bind-token, 108 invalido ou revogado, e atende com bind-token
		// valido daquele contexto. 104 e 108 seguem o modo.
		ctx, err := h.pathContext(params)
		if err != nil {
			return v.fail(200, "redis indisponivel ("+err.Error()+")")
		}
		if ctx != "" && ctx == originSvc {
			return v
		}
		return h.checkBindToken(v, r, ctx, now, "app associada fora do proprio contexto de servico (H1)")
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
	// Cliente bloqueado (C.4.2.2) e cliente fora de clients:authorized (E6,
	// decisao do Luis em 11/10): 107 nos dois casos, para toda credencial
	// valida, inclusive token com class local-associated, e so nas rotas que
	// pedem token (as de auth none ja sairam acima). O bloqueado e conferido
	// antes e mantem a mensagem dele. Token sem sub nunca esta no conjunto (ate
	// 11/10 ele passava sem nenhuma das duas consultas). O tv3ws grava o
	// conjunto (auth-manager: autorizacao no pop-up, inclusao no boot) e tira
	// dele o id no bloqueio.
	if claims.Sub == "" {
		return v.fail(107, "access token sem sub: cliente fora de clients:authorized (E6)")
	}
	blocked, err := h.store.IsBlocked(claims.Sub)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if blocked {
		return v.fail(107, "cliente bloqueado pelo usuario (C.4.2.2)")
	}
	authorized, err := h.store.IsAuthorized(claims.Sub)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if !authorized {
		return v.fail(107, "cliente fora de clients:authorized (E6): autorizacao do espectador ausente ou removida")
	}
	// token com class local-associated: dispensa o bind-token (D8)
	if v.Class == classAssociated || rt.Auth == authToken {
		return h.requireService(v)
	}

	// -- servico DTV selecionado (300, H2) --
	// Antes do bind-token: sem servico nao ha chave registrada, e toda
	// verificacao cairia em 108 (Joel, 15/10).
	if v = h.requireService(v); v.Code != 0 {
		return v
	}

	// -- bind-token (104/108) --
	ctx, err := h.pathContext(params)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	return h.checkBindToken(v, r, ctx, now, "")
}

// pathContext: o contexto de servico da rota (A6, Joel 15/10). O
// {serviceContextId} do caminho e o globalServiceId do BAM (Tabela 6; C.3.5);
// current-service, ou rota sem o parametro, e o servico corrente. Qualquer
// servico explicito e aceito (as Tabelas C.71 e C.74 dao 304 a servico fora
// do Application Catalog, o que pressupoe aceitar os demais; o 304 espera o
// catalogo no testbed). Ate 15/10 o scid era uma constante igual para todo
// servico, e outro valor dava 108. "" = nenhum servico selecionado.
func (h *authHandler) pathContext(params map[string]string) (string, error) {
	if scid, ok := params["serviceContextId"]; ok && scid != "current-service" {
		return scid, nil
	}
	return h.store.CurrentServiceID()
}

// checkBindToken confere o bind-token contra as chaves registradas para o
// contexto ctx (bind-context:{ctx}; D4: so as daquele servico).
func (h *authHandler) checkBindToken(v verdict, r *http.Request, ctx string, now time.Time, why string) verdict {
	if why != "" {
		why = " (" + why + ")"
	}
	bt := r.Header.Get("bind-token")
	if bt == "" {
		return v.fail(104, "bind-token ausente"+why)
	}
	if ctx == "" {
		// H2 (Joel, 15/10): nas APIs cuja tabela nao lista o 300 (a C.6.16.3,
		// "This API only applies to the DTV service currently selected"), sem
		// servico selecionado nao ha chave que valide o token => 108.
		return v.fail(108, "sem servico corrente: nenhuma chave registrada"+why)
	}
	raw, err := h.store.BindKeys(ctx)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if err := verifyBindToken(bt, h.keys.decodeStoredKeys(ctx, raw), now); err != nil {
		return v.fail(108, err.Error()+why)
	}
	return v
}

// requireService: H2 (Joel, 15/10). Nas rotas com NoService (as APIs cuja
// tabela lista "300: If the DTV function is not in use in the receiver": a
// C.6.4.7 e da C.6.14.1 a C.6.14.6), sem servico DTV selecionado responde
// esse codigo — no testbed, o espectador esta no catalogo ou no seletor de
// perfil. Vale para todo cliente que chega ate aqui (o associado tambem) e
// dispara nos dois modos (followsMode). Nas demais rotas nao faz nada.
func (h *authHandler) requireService(v verdict) verdict {
	if v.rt == nil || v.rt.NoService == 0 {
		return v
	}
	sid, err := h.store.CurrentServiceID()
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if sid == "" {
		return v.fail(v.rt.NoService, "nenhum servico DTV selecionado")
	}
	return v
}

// writeError responde a requisicao BLOQUEADA pela decisao do plugin: sem o
// aviso do modo warn (a resposta ja eh o erro).
func writeError(w http.ResponseWriter, r *http.Request, code int, detail string) {
	w.Header().Del(warnHeader)
	writeC32(w, r, code, detail)
}

// writeC32 responde no formato C.3.2: status 404 + {error, description},
// application/json, com Access-Control-Allow-Origin: * (o CORS e todo do
// plugin desde 12/10; sem isso o navegador veria erro de CORS em vez do
// corpo) e API-Version (C.3.6.6).
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

// writeOptions responde o OPTIONS (C.4.1.9.3): o preflight, em qualquer
// caminho, e o que nao eh preflight, numa API declarada. Os tres cabecalhos
// com os valores padrao da norma (ACAH com o key do DELETE
// /tv3/bind-context, o mesmo cors.allow_headers do routes.json), sem olhar
// o que o preflight pede: a lista e fixa, e quem decide se ela cobre o
// pedido eh o navegador. maxAge (so no preflight) vai em
// Access-Control-Max-Age. Status 200, o de sucesso da C.3.2.1; sem corpo.
func writeOptions(w http.ResponseWriter, r *http.Request, allowHeaders, maxAge string) {
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("Access-Control-Allow-Methods", "*")
	hd.Set("Access-Control-Allow-Headers", allowHeaders)
	if maxAge != "" {
		hd.Set("Access-Control-Max-Age", maxAge)
	}
	hd.Set("API-Version", apiVersion(r))
	hd.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// apiVersion: o API-Version das respostas que a propria borda escreve fora do
// sucesso de uma API dela (erros C.3.2 e o OPTIONS que nao e preflight), pela
// negociacao de negotiateVersion (edge.go) com as versoes da rota (D4; caminho
// nao declarado: as de todas as rotas):
//   - versao pedida e servida: ela (2.0 sem Accept-Version, C.3.6.5);
//   - versao pedida que a rota nao serve (o erro 100 da negociacao): a mais
//     recente do servidor. C.3.6.6: quando o servidor nao consegue responder de
//     forma compativel com a versao pedida, "the 'API-Version' header is
//     assigned to the latest version supported by the server";
//   - Accept-Version malformado (o erro 101): segue a 2.0, como antes. A
//     excecao da C.3.6.6 fala de versao pedida, e um cabecalho fora do
//     formato X.Y nao pede versao nenhuma.
func apiVersion(r *http.Request) string {
	vi := versionInfoOf(r)
	switch v, code, _ := negotiateVersion(r, vi.served); code {
	case 0:
		return v
	case 100:
		return vi.latest
	}
	return defaultVersion
}
