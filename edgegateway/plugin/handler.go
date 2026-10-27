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

// Descriptions of the error codes of Table C.1.
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
	304: "DTV service not found",
}

const warnHeader = "X-TV30-Auth-Warn"

// verdict is the decision on a request; Code 0 lets it pass.
type verdict struct {
	Code   int
	Detail string
	Class  string
	// sub of a valid access token: the clientid.
	ClientID string
	Route    string
	// Matched route; nil if the path is not declared.
	rt *route
	// Values of the {param} segments of the path.
	params map[string]string
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
	// The versions of the route give the API-Version of the responses
	// written by the edge.
	vi := versionInfo{served: h.cfg.AllVersions, latest: h.cfg.LatestVersion}
	if rt, _ := h.cfg.Routes.match(r.Method, r.URL.Path); rt != nil {
		vi.served = rt.Versions
	}
	r = withVersionInfo(r, vi)
	// CORS (C.4.1.9). Set, not Add: the browser refuses a duplicated
	// Access-Control-Allow-Origin. An Access-Control-Expose-Headers of the
	// backend response is added as a second line.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if h.cfg.CORSExposeHeaders != "" {
		w.Header().Set("Access-Control-Expose-Headers", h.cfg.CORSExposeHeaders)
	}
	if r.Method == http.MethodOptions {
		// A preflight is answered on any path, with the fixed list.
		if r.Header.Get("Access-Control-Request-Method") != "" {
			writeOptions(w, r, h.cfg.CORSAllowHeaders, h.cfg.CORSMaxAge)
			return
		}
		// An OPTIONS that is not a preflight, on a declared API: ACAO, ACAM
		// and ACAH (C.4.1.9.3). On an undeclared path, it goes on and gets 100.
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
		// The path is logged with %q: r.URL.Path is decoded, and a %0A in it
		// would forge [tv30-auth] lines in the log.
		if h.cfg.Mode == modeEnforce || !followsMode(v.Code) {
			logf("DENY surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
			writeError(w, r, v.Code, v.Detail)
			return
		}
		logf("WARN surface=%s code=%d %s %q class=%s detalhe=%q", h.cfg.Surface, v.Code, r.Method, r.URL.Path, class, v.Detail)
		w.Header().Set(warnHeader, strconv.Itoa(v.Code))
	}
	// Version (C.3.6.5), after the credential; 100 and 101 are sent in both
	// modes. The internal headers overwrite those sent by the client.
	version, vcode, vdetail := negotiateVersion(r, vi.served)
	if vcode != 0 {
		logf("DENY surface=%s code=%d %s %q detalhe=%q", h.cfg.Surface, vcode, r.Method, r.URL.Path, vdetail)
		writeError(w, r, vcode, vdetail)
		return
	}
	r.Header.Set(internalVersionHeader, version)
	r.Header.Set(internalHostHeader, r.Host)
	setClientHeaders(r, v)
	if v.rt != nil && v.rt.Edge != "" {
		h.serveEdge(w, r, v, version)
		return
	}
	toInternalPath(r, version)
	h.serveNext(w, r)
}

// setClientHeaders writes the class and the identity of the local client
// decided by evaluate, removing those sent by the client: associated, only
// the class; standAlone, the class and the clientid; any other, neither.
func setClientHeaders(r *http.Request, v verdict) {
	r.Header.Del(internalClientClassHeader)
	r.Header.Del(internalClientIDHeader)
	switch v.Class {
	case classAssociated:
		r.Header.Set(internalClientClassHeader, classAssociated)
	case classStandAlone:
		r.Header.Set(internalClientClassHeader, classStandAlone)
		r.Header.Set(internalClientIDHeader, v.ClientID)
	}
}

// serveNext passes the request on to the router, and replaces with 404 and
// {error:200} (C.3.2.1, Table C.1), in both modes, the failures that would
// fall outside the format of C.3.2:
//   - a 5xx status, as the 500 without body that KrakenD sends when the
//     backend exceeds the endpoint timeout or is down;
//   - a panic in the router.
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
			// The header was already sent, so the response cannot be replaced:
			// aborts the connection instead of appending an error body.
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

// guardWriter wraps the writer of the KrakenD router. A 5xx status is held,
// with neither header nor body sent to the client, so that replace can send
// the C.3.2 error; any other status passes unchanged, without a copy.
type guardWriter struct {
	// Embedded interface: only Header, Write and WriteHeader are promoted. A
	// ReadFrom of the underlying writer, used by io.Copy, would bypass the
	// hold.
	http.ResponseWriter
	// Final status written; 0 for none yet.
	status int
	// Held 5xx status; 0 for none.
	held int
	// The final status was passed on to the client.
	sent     bool
	hijacked bool
}

// gin.ResponseWriter (gin v1.9.1, in KrakenD 2.7.2) asserts these three
// interfaces on the underlying writer without ok: a missing one panics.
var (
	_ http.Flusher       = (*guardWriter)(nil)
	_ http.Hijacker      = (*guardWriter)(nil)
	_ http.CloseNotifier = (*guardWriter)(nil)
)

func (g *guardWriter) WriteHeader(code int) {
	if g.status != 0 {
		if g.held == 0 {
			// A superfluous call, passed on for net/http to log it.
			g.ResponseWriter.WriteHeader(code)
		}
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		// An informational 1xx does not set the final status.
		g.ResponseWriter.WriteHeader(code)
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
		// The body of a held 5xx is discarded.
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

// Flush does nothing while a 5xx is held, since flushing would send the
// header.
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
	// A channel that never fires.
	return make(chan bool)
}

// Unwrap returns the underlying writer, for http.ResponseController.
func (g *guardWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// replace writes error 200 in place of the held response. The backend
// headers are removed, except CORS, without which the browser does not read
// the body, Vary, X-Krakend* and the warn mode header.
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

// followsMode reports whether the code follows the mode: only the
// credential refusals 104, 107 and 108, which in warn mode become a warning.
// Any other code is sent in both modes.
func followsMode(code int) bool {
	return code == 104 || code == 107 || code == 108
}

func keptOnReplace(k string) bool {
	k = http.CanonicalHeaderKey(k)
	return strings.HasPrefix(k, "Access-Control-") || k == "Vary" ||
		strings.HasPrefix(k, "X-Krakend") || k == http.CanonicalHeaderKey(warnHeader)
}

// evaluate decides, in this order: route (100), surface (106), class (106),
// non-local over HTTP (106), access token (107), selected service (300),
// service of the path in the catalog (304) and bind-token (104, 108). A
// Redis failure gives 200.
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
			v.ClientID = claims.Sub
		}
	}
	// Without Authorization, an Origin in origins:associated identifies a
	// local associated client. The Origin is also looked up with an invalid
	// Authorization on a route with classes, and with any Authorization on a
	// route that excludes the associated client, which then gets 106.
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

	// Associated by the Origin: no access token and, in its own service
	// context, no bind-token (C.4.1.1).
	if v.Class == classAssociated && claims == nil {
		if v = h.requireService(v); v.Code != 0 || rt.Auth != authTokenBind {
			return v
		}
		if v = h.requireCatalog(v); v.Code != 0 {
			return v
		}
		// Outside the service of its origin in origins:associated, the
		// bind-token is required.
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
	// Blocked (C.4.2.2) or not in clients:authorized: 107. The block is
	// checked first, with its own detail; a token without sub is never in the
	// set.
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
	if rt.Auth == authToken {
		return h.requireService(v)
	}

	// Selected DTV service (300) and service of the path in the catalog (304)
	// before the bind-token: without them there is no registered key, and
	// every request would get 108.
	if v = h.requireService(v); v.Code != 0 {
		return v
	}
	if v = h.requireCatalog(v); v.Code != 0 {
		return v
	}

	ctx, err := h.pathContext(params)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	return h.checkBindToken(v, r, ctx, now, "")
}

// pathContext returns the {serviceContextId} of the path, the
// globalServiceId of the BAM (Table 6; C.3.5), or the current service for
// current-service and for routes without the parameter; "" if none is
// selected.
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
		// On a route without NoService, as C.6.16.3, there is no key to
		// validate with.
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

// requireService fails with the NoService code of the route when no DTV
// service is selected, for every client, the associated one included.
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

// requireCatalog fails with the NotInCatalog code of the route when the
// {serviceContextId} of the path is not in services:announced, the services
// of the Application Catalog, for every client. current-service is not
// checked.
func (h *authHandler) requireCatalog(v verdict) verdict {
	if v.rt == nil || v.rt.NotInCatalog == 0 {
		return v
	}
	scid, ok := v.params["serviceContextId"]
	if !ok || scid == "current-service" {
		return v
	}
	in, err := h.store.ServiceInCatalog(scid)
	if err != nil {
		return v.fail(200, "redis indisponivel ("+err.Error()+")")
	}
	if !in {
		return v.fail(v.rt.NotInCatalog, "servico "+scid+" fora do Application Catalog")
	}
	return v
}

// writeError answers a request refused by the plugin, without the warn mode
// header.
func writeError(w http.ResponseWriter, r *http.Request, code int, detail string) {
	w.Header().Del(warnHeader)
	writeC32(w, r, code, detail)
}

// writeC32 answers in the format of C.3.2, 404 and {error, description},
// with API-Version and Access-Control-Allow-Origin: *, without which the
// browser shows a CORS error instead of the body.
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

// writeOptions answers OPTIONS (C.4.1.9.3) with fixed ACAO, ACAM and ACAH,
// whatever the preflight asks, and with Access-Control-Max-Age when maxAge
// is set. Status 200 (C.3.2.1), no body.
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

// apiVersion returns the API-Version of the C.3.2 errors and of the OPTIONS
// responses written by the edge: the negotiated version; on 100, the latest
// version of the server (C.3.6.6); on 101, 2.0.
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
