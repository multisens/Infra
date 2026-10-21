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

// Textos da Tabela C.1, os mesmos do tv3ws (src/util/error.ts).
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
	// as versoes da rota dao o API-Version das respostas escritas pela borda
	vi := versionInfo{served: h.cfg.AllVersions, latest: h.cfg.LatestVersion}
	if rt, _ := h.cfg.Routes.match(r.Method, r.URL.Path); rt != nil {
		vi.served = rt.Versions
	}
	r = withVersionInfo(r, vi)
	// O CORS e todo do plugin (C.4.1.9): o modulo CORS do KrakenD envolve o
	// plugin e responderia o preflight antes dele. ACAO com Set: duplicado, o
	// Chrome recusa (o tv3ws nao o envia). O Expose-Headers de uma resposta do
	// backend (o do /manifest) chega somado, numa segunda linha.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if h.cfg.CORSExposeHeaders != "" {
		w.Header().Set("Access-Control-Expose-Headers", h.cfg.CORSExposeHeaders)
	}
	if r.Method == http.MethodOptions {
		// Preflight: respondido em qualquer caminho, com a lista fixa. O modulo
		// CORS do KrakenD 2.7.2 (rs/cors 1.11.0) recusaria Access-Control-Request-
		// Headers fora de ordem lexicografica. Num caminho nao declarado o
		// navegador segue para a requisicao e le o 100 dela.
		if r.Header.Get("Access-Control-Request-Method") != "" {
			writeOptions(w, r, h.cfg.CORSAllowHeaders, h.cfg.CORSMaxAge)
			return
		}
		// OPTIONS sem preflight numa API declarada: ACAO, ACAM e ACAH
		// (C.4.1.9.3). Caminho nao declarado segue e da 100.
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
	// Versao (C.3.6.5), depois da credencial; 100 e 101 saem nos dois modos.
	// Os cabecalhos internos apagam o que vier do cliente.
	version, vcode, vdetail := negotiateVersion(r, vi.served)
	if vcode != 0 {
		logf("DENY surface=%s code=%d %s %q detalhe=%q", h.cfg.Surface, vcode, r.Method, r.URL.Path, vdetail)
		writeError(w, r, vcode, vdetail)
		return
	}
	r.Header.Set(internalVersionHeader, version)
	r.Header.Set(internalHostHeader, r.Host)
	if v.rt != nil && v.rt.Edge != "" {
		h.serveEdge(w, r, v, version)
		return
	}
	h.serveNext(w, r)
}

// serveNext repassa ao roteador e troca por 404 + {error:200} (C.3.2.1,
// Tabela C.1) as falhas que sairiam fora do formato C.3.2, nos dois modos:
//   - 5xx: o KrakenD responde 500 sem corpo quando o backend estoura o timeout
//     do endpoint ou esta fora do ar (o tv3ws so responde erro com 404);
//   - panic no roteador, que resetaria a conexao.
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

// O gin.ResponseWriter (gin v1.9.1, do KrakenD 2.7.2) faz type assertion sem
// ok para estas tres interfaces no writer de baixo: faltando uma, panic.
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

// replace escreve o erro 200 no lugar da resposta retida. Os cabecalhos do
// backend saem (nao descrevem o corpo novo); ficam os de CORS, sem os quais o
// navegador nao le o corpo, os X-Krakend* e o aviso do modo warn.
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

// followsMode: so as recusas de credencial (104, 107, 108) obedecem ao modo;
// em warn viram aviso. 100, 101, 106, 300 e 200 saem nos dois modos.
func followsMode(code int) bool {
	return code == 104 || code == 107 || code == 108
}

func keptOnReplace(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return strings.HasPrefix(k, "Access-Control-") || k == "Vary" ||
		strings.HasPrefix(k, "X-Krakend") || k == http.CanonicalHeaderKey(warnHeader)
}

// evaluate decide, nesta ordem: rota (100), superficie (106), classe (106),
// nao local por HTTP (106), access token (107), servico selecionado (300) e
// bind-token (104/108). Falha no Redis => 200.
func (h *authHandler) evaluate(r *http.Request) verdict {
	rt, params := h.cfg.Routes.match(r.Method, r.URL.Path)
	if rt == nil {
		return verdict{Code: 100, Detail: r.Method + " " + r.URL.Path}
	}
	v := verdict{Route: rt.String(), rt: rt, params: params}
	if rt.SurfaceError != 0 {
		return v.fail(rt.SurfaceError, "API exclusiva da superficie HTTP (Server-BaseURL); nesta superficie, 106 a qualquer cliente (C.4.1.8)")
	}
	if rt.Auth == authNone && rt.Classes == nil {
		return v
	}
	now := h.now()

	authz := r.Header.Get("Authorization")
	var claims *accessClaims
	var tokenErr error
	if authz != "" {
		if claims, tokenErr = verifyAccessToken(authz, h.cfg.Secret, h.cfg.Issuer, now); tokenErr == nil {
			v.Class = claims.Class
		}
	}
	// Sem Authorization, um Origin em origins:associated identifica o local
	// associado (a C.4.1.7 deixa o mecanismo a implementacao; fora do navegador
	// o Origin e forjavel). Com Authorization invalido o Origin tambem e
	// consultado, senao bastaria um Authorization qualquer para escapar do 106;
	// nas rotas que excluem o associado, ele da 106 mesmo com token valido.
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
	if h.cfg.NonLocalHTTPSOnly && v.Class == classNonLocal && !rt.NonLocalHTTP {
		return v.fail(106, "cliente nao local por HTTP fora da C.6.1.2/C.6.1.3: use o Server-SecureBaseURL (C.4.1.6)")
	}

	// Associado pelo Origin: sem access token e, no proprio contexto de
	// servico, sem bind-token (C.4.1.1).
	if v.Class == classAssociated && claims == nil {
		if v = h.requireService(v); v.Code != 0 || rt.Auth != authTokenBind {
			return v
		}
		// C.4.1.1, "referencing their own service context": fora do servico
		// que lancou a app (origins:associated), a regra geral do bind-token.
		ctx, err := h.pathContext(params)
		if err != nil {
			return v.fail(200, "redis indisponivel ("+err.Error()+")")
		}
		if ctx != "" && ctx == originSvc {
			return v
		}
		return h.checkBindToken(v, r, ctx, now, "app associada fora do proprio contexto de servico")
	}
	if rt.Auth == authNone {
		return v
	}

	if authz == "" {
		return v.fail(107, "accessToken ausente")
	}
	if tokenErr != nil {
		return v.fail(107, tokenErr.Error())
	}
	// Bloqueado (C.4.2.2) ou fora de clients:authorized: 107, tambem com token
	// de classe local-associated. O bloqueio e conferido antes e mantem a
	// mensagem dele; token sem sub nunca esta no conjunto.
	if claims.Sub == "" {
		return v.fail(107, "access token sem sub: cliente fora de clients:authorized")
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
		return v.fail(107, "cliente fora de clients:authorized: autorizacao do espectador ausente ou removida")
	}
	// token de classe local-associated dispensa o bind-token
	if v.Class == classAssociated || rt.Auth == authToken {
		return h.requireService(v)
	}

	// Servico DTV selecionado (300) antes do bind-token: sem servico nao ha
	// chave registrada, e tudo daria 108.
	if v = h.requireService(v); v.Code != 0 {
		return v
	}

	ctx, err := h.pathContext(params)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	return h.checkBindToken(v, r, ctx, now, "")
}

// pathContext: o {serviceContextId} do caminho, que e o globalServiceId do BAM
// (Tabela 6; C.3.5), ou o servico corrente em current-service e nas rotas sem
// o parametro ("" = nenhum selecionado). Todo servico explicito e aceito: o
// 304 das Tabelas C.71 e C.74 (fora do Application Catalog) nao e aplicado.
func (h *authHandler) pathContext(params map[string]string) (string, error) {
	if scid, ok := params["serviceContextId"]; ok && scid != "current-service" {
		return scid, nil
	}
	return h.store.CurrentServiceID()
}

func (h *authHandler) checkBindToken(v verdict, r *http.Request, ctx string, now time.Time, why string) verdict {
	if why != "" {
		why = " (" + why + ")"
	}
	bt := r.Header.Get("bind-token")
	if bt == "" {
		return v.fail(104, "bind-token ausente"+why)
	}
	if ctx == "" {
		// rota sem NoService (C.6.16.3): sem servico nao ha chave que valide
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

// requireService: nas rotas com NoService, sem servico DTV selecionado responde
// esse codigo ("300: If the DTV function is not in use in the receiver"), para
// todo cliente, inclusive o associado.
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

// writeC32 responde no formato C.3.2 (404 + {error, description}), com ACAO *
// (sem ele o navegador veria erro de CORS em vez do corpo) e API-Version.
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

// writeOptions responde o OPTIONS (C.4.1.9.3) com ACAO, ACAM e ACAH fixos, sem
// olhar o que o preflight pede: quem decide se a lista cobre o pedido e o
// navegador. maxAge so no preflight. Status 200 (C.3.2.1), sem corpo.
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

// apiVersion: o API-Version dos erros C.3.2 e do OPTIONS escritos pela borda.
// A versao negociada; no 100, a mais recente do servidor (C.3.6.6, "the latest
// version supported by the server"); no 101, a 2.0, porque um cabecalho fora
// do formato X.Y nao pede versao nenhuma.
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
