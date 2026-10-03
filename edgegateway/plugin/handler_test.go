package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	tSecret = "segredo-de-teste"
	tSCID   = "c08b2c72-fd14-4095-adaf-2e5810850c57"
	tSvc    = "urn:tv30:service:webmedia"
	tAssoc  = "http://app-associada:8080"
)

// tabela no formato que o generate.js entrega (extra_config)
func testExtra() map[string]interface{} {
	r := func(method, path, auth string, classes ...string) map[string]interface{} {
		m := map[string]interface{}{"method": method, "path": path, "auth": auth}
		if classes != nil {
			l := []interface{}{}
			for _, c := range classes {
				l = append(l, c)
			}
			m["classes"] = l
		}
		return m
	}
	return map[string]interface{}{
		"surface":                    "internal",
		"current_service_context_id": tSCID,
		"routes": []interface{}{
			r("GET", "/health", authNone),
			r("GET", "/manifest", authNone),
			r("GET", "/tv3/authorize", authNone, classAutonomous, classNonLocal),
			r("POST", "/tv3/bind-context", authNone, classAssociated),
			r("GET", "/tv3/bind-context", authToken, classAutonomous, classNonLocal),
			r("GET", "/tv3/current-service", authToken),
			r("GET", "/tv3/current-service/users/current-user", authTokenBind),
			r("POST", "/tv3/current-service/users", authTokenBind),
			r("POST", "/tv3/{serviceContextId}/users", authTokenBind),
			r("GET", "/tv3/{serviceContextId}/users/{userid}", authTokenBind),
		},
	}
}

func testConfig(t *testing.T, mode string) *config {
	t.Helper()
	env := map[string]string{"JWT_SECRET": tSecret, "AUTH_ENFORCE": mode}
	cfg, err := loadConfig(testExtra(), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func accessFor(t *testing.T, sub, class string) string {
	return "Bearer " + signJWT(t, "HS256", []byte(tSecret), map[string]interface{}{
		"iat": t0.Unix(), "nbf": t0.Unix(), "exp": t0.Add(time.Hour).Unix(),
		"iss": "GenericIssuer", "sub": sub, "class": class,
	})
}

func newTestHandler(t *testing.T, mode string, st *fakeStore, next http.Handler) *authHandler {
	t.Helper()
	if next == nil {
		next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("backend")) })
	}
	h := newHandler(testConfig(t, mode), st, next)
	h.now = func() time.Time { return t0 }
	return h
}

func baseStore() *fakeStore {
	return &fakeStore{
		blocked:    map[string]bool{"cli-bloq": true},
		associated: map[string]bool{tAssoc: true},
		current:    tSvc,
		keys:       map[string][]string{tSvc: {storedJSON("HS256", "chave-webmedia")}, "urn:outra": {storedJSON("HS256", "chave-outra")}},
	}
}

func TestDecisao(t *testing.T) {
	bindOK := signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0, t0, t0.Add(time.Hour)))
	bindOutra := signJWT(t, "HS256", []byte("chave-outra"), timeClaims(t0, t0, t0.Add(time.Hour)))
	bindExp := signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0.Add(-2*time.Hour), t0.Add(-2*time.Hour), t0.Add(-time.Hour)))
	auto := accessFor(t, "cli-1", classAutonomous)
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	assocTok := accessFor(t, "cli-3", classAssociated)
	blocked := accessFor(t, "cli-bloq", classAutonomous)
	blockedAssoc := accessFor(t, "cli-bloq", classAssociated)
	expired := "Bearer " + signJWT(t, "HS256", []byte(tSecret), map[string]interface{}{"exp": t0.Add(-time.Second).Unix(), "iss": "GenericIssuer", "sub": "x", "class": classNonLocal})

	type hdr map[string]string
	cases := []struct {
		name, method, path string
		h                  hdr
		want               int
	}{
		// rota (100)
		{"nao declarada", "GET", "/tv3/abc", nil, 100},
		{"metodo nao declarado", "DELETE", "/health", nil, 100},
		{"health livre", "GET", "/health", nil, 0},
		{"manifest livre", "GET", "/manifest", nil, 0},
		// classe (106)
		{"authorize autonomo", "GET", "/tv3/authorize", hdr{"Origin": "http://qualquer"}, 0},
		{"authorize sem Origin", "GET", "/tv3/authorize", nil, 0},
		{"authorize associado (L4)", "GET", "/tv3/authorize", hdr{"Origin": tAssoc}, 106},
		{"authorize com token de associado", "GET", "/tv3/authorize", hdr{"Authorization": assocTok}, 106},
		{"POST bind-context associado", "POST", "/tv3/bind-context", hdr{"Origin": tAssoc}, 0},
		{"POST bind-context nao associado", "POST", "/tv3/bind-context", hdr{"Origin": "http://outro"}, 106},
		{"POST bind-context com token autonomo", "POST", "/tv3/bind-context", hdr{"Authorization": auto}, 106},
		{"GET bind-context associado", "GET", "/tv3/bind-context", hdr{"Origin": tAssoc}, 106},
		{"GET bind-context autonomo", "GET", "/tv3/bind-context", hdr{"Authorization": auto, "bind-token": "x"}, 0},
		{"GET bind-context sem token", "GET", "/tv3/bind-context", nil, 107},
		// Authorization invalido nao esconde o Origin associado do 106 (L4)
		{"authorize associado + Authorization lixo", "GET", "/tv3/authorize", hdr{"Origin": tAssoc, "Authorization": "x"}, 106},
		{"authorize associado + token expirado", "GET", "/tv3/authorize", hdr{"Origin": tAssoc, "Authorization": expired}, 106},
		{"GET bind-context associado + Authorization lixo", "GET", "/tv3/bind-context", hdr{"Origin": tAssoc, "Authorization": "x", "bind-token": "x"}, 106},
		{"authorize Authorization lixo, Origin qualquer", "GET", "/tv3/authorize", hdr{"Origin": "http://qualquer", "Authorization": "x"}, 0},
		// token (107)
		{"token ausente", "GET", "/tv3/current-service", nil, 107},
		{"token valido", "GET", "/tv3/current-service", hdr{"Authorization": nonLocal}, 0},
		{"token expirado", "GET", "/tv3/current-service", hdr{"Authorization": expired}, 107},
		{"token lixo", "GET", "/tv3/current-service", hdr{"Authorization": "Bearer lixo"}, 107},
		{"cliente bloqueado", "GET", "/tv3/current-service", hdr{"Authorization": blocked}, 107},
		{"bloqueado com token de classe associado", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": blockedAssoc}, 107},
		{"token de associado dispensa bind", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": assocTok}, 0},
		{"associado sem token", "GET", "/tv3/current-service", hdr{"Origin": tAssoc}, 0},
		{"token invalido + Origin associado", "GET", "/tv3/current-service", hdr{"Authorization": "Bearer lixo", "Origin": tAssoc}, 107},
		// bind (104/108)
		{"bind ausente", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto}, 104},
		{"bind valido", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
		{"bind de outra emissora (D4)", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto, "bind-token": bindOutra}, 108},
		{"bind expirado", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto, "bind-token": bindExp}, 108},
		{"bind nao-JWT", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto, "bind-token": "abc"}, 108},
		{"bind sem token: 107 vem antes", "GET", "/tv3/current-service/users/current-user", hdr{"bind-token": bindOK}, 107},
		{"associado dispensa bind (D8)", "GET", "/tv3/current-service/users/current-user", hdr{"Origin": tAssoc}, 0},
		{"scid constante = corrente", "GET", "/tv3/" + tSCID + "/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
		{"scid current-service", "GET", "/tv3/current-service/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
		{"scid de outro servico (L2)", "GET", "/tv3/urn:outra/users/u1", hdr{"Authorization": auto, "bind-token": bindOutra}, 108},
		// POST {scid}/users chega ao mesmo handler da C.6.14.1 (bind shall)
		{"POST current-service/users sem bind", "POST", "/tv3/current-service/users", hdr{"Authorization": auto}, 104},
		{"POST {scid}/users sem bind", "POST", "/tv3/xyz/users", hdr{"Authorization": auto}, 104},
		{"POST {scid}/users scid qualquer (L2)", "POST", "/tv3/xyz/users", hdr{"Authorization": auto, "bind-token": bindOK}, 108},
		{"POST Current-Service/users (caixa trocada, L2)", "POST", "/tv3/Current-Service/users", hdr{"Authorization": auto, "bind-token": bindOK}, 108},
		{"POST {scid}/users scid constante", "POST", "/tv3/" + tSCID + "/users", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
	}
	for _, c := range cases {
		h := newTestHandler(t, modeEnforce, baseStore(), nil)
		req := httptest.NewRequest(c.method, c.path, nil)
		for k, v := range c.h {
			req.Header.Set(k, v)
		}
		if got := h.evaluate(req); got.Code != c.want {
			t.Errorf("%s: codigo %d (%s), esperado %d", c.name, got.Code, got.Detail, c.want)
		}
	}
}

func TestDecisaoSemServicoCorrente(t *testing.T) {
	st := baseStore()
	st.current = ""
	h := newTestHandler(t, modeEnforce, st, nil)
	req := httptest.NewRequest("GET", "/tv3/current-service/users/current-user", nil)
	req.Header.Set("Authorization", accessFor(t, "cli-1", classAutonomous))
	req.Header.Set("bind-token", signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0, t0, t0.Add(time.Hour))))
	if v := h.evaluate(req); v.Code != 108 {
		t.Fatalf("sem servico corrente: esperado 108, veio %d", v.Code)
	}
}

func TestRotaLivreNaoConsultaRedis(t *testing.T) {
	st := baseStore()
	st.err = errors.New("fora")
	h := newTestHandler(t, modeEnforce, st, nil)
	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("Origin", tAssoc)
	if v := h.evaluate(req); v.Code != 0 || st.calls != 0 {
		t.Fatalf("health: codigo %d, chamadas ao redis %d", v.Code, st.calls)
	}
}

func serve(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func checkC32(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, esperado 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	if o := rec.Header().Get("Access-Control-Allow-Origin"); o != "*" {
		t.Errorf("Access-Control-Allow-Origin %q", o)
	}
	if v := rec.Header().Get("API-Version"); v == "" {
		t.Errorf("sem API-Version")
	}
	var body struct {
		Error       *int    `json:"error"`
		Description *string `json:"description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == nil || body.Description == nil {
		t.Fatalf("corpo fora do C.3.2: %q", rec.Body.String())
	}
	if *body.Error != code {
		t.Errorf("error %d, esperado %d (%s)", *body.Error, code, *body.Description)
	}
}

func TestEnforceBloqueia(t *testing.T) {
	h := newTestHandler(t, modeEnforce, baseStore(), nil)
	rec := serve(h, "GET", "/tv3/current-service", nil)
	checkC32(t, rec, 107)
	if rec.Header().Get(warnHeader) != "" {
		t.Error("enforce nao deveria mandar o cabecalho de aviso")
	}
}

func TestWarnSoAvisa(t *testing.T) {
	h := newTestHandler(t, modeWarn, baseStore(), nil)
	for _, c := range []struct {
		path string
		hdr  map[string]string
		code string
	}{
		{"/tv3/current-service", nil, "107"},
		{"/tv3/current-service/users/current-user", map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)}, "104"},
		{"/tv3/authorize", map[string]string{"Origin": tAssoc}, "106"},
	} {
		rec := serve(h, "GET", c.path, c.hdr)
		if rec.Code != 200 || rec.Body.String() != "backend" {
			t.Errorf("%s: warn bloqueou (status %d)", c.path, rec.Code)
		}
		if got := rec.Header().Get(warnHeader); got != c.code {
			t.Errorf("%s: %s=%q, esperado %q", c.path, warnHeader, got, c.code)
		}
	}
	// requisicao valida nao recebe aviso
	rec := serve(h, "GET", "/tv3/current-service", map[string]string{"Authorization": accessFor(t, "cli-1", classNonLocal)})
	if rec.Header().Get(warnHeader) != "" {
		t.Error("requisicao valida recebeu aviso")
	}
}

func TestRotaNaoDeclaradaNosDoisModos(t *testing.T) {
	for _, mode := range []string{modeWarn, modeEnforce} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
		h := newTestHandler(t, mode, baseStore(), next)
		rec := serve(h, "GET", "/tv3/abc", nil)
		checkC32(t, rec, 100)
		if called {
			t.Errorf("%s: rota nao declarada chegou ao roteador", mode)
		}
	}
}

func TestOptionsPassaSempre(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })
	h := newTestHandler(t, modeEnforce, baseStore(), next)
	rec := serve(h, "OPTIONS", "/tv3/qualquer-coisa", map[string]string{"Access-Control-Request-Method": "GET"})
	if !called || rec.Code != 204 {
		t.Fatalf("OPTIONS nao passou (status %d)", rec.Code)
	}
}

// OPTIONS sem Access-Control-Request-Method nao eh preflight. Numa API
// declarada (qualquer metodo no caminho) a C.4.1.9.3 manda responder com
// ACAO, ACAM e ACAH; em caminho nao declarado eh 100 (C.3.2). Nos dois modos
// e sem chegar ao roteador.
func TestOptionsSemPreflight(t *testing.T) {
	for _, mode := range []string{modeWarn, modeEnforce} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
		h := newTestHandler(t, mode, baseStore(), next)
		for _, p := range []string{"/tv3/current-service", "/tv3/" + tSCID + "/users/u1", "/tv3/bind-context/"} {
			rec := serve(h, "OPTIONS", p, nil)
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s: status %d, esperado 200", mode, p, rec.Code)
			}
			for k, want := range map[string]string{
				"Access-Control-Allow-Origin":  "*",
				"Access-Control-Allow-Methods": "*",
				"Access-Control-Allow-Headers": defaultCORSAllowHeaders,
			} {
				if got := rec.Header().Get(k); got != want {
					t.Errorf("%s %s: %s=%q, esperado %q", mode, p, k, got, want)
				}
			}
		}
		checkC32(t, serve(h, "OPTIONS", "/tv3/xyz/abc", map[string]string{"Origin": "http://x"}), 100)
		if called {
			t.Errorf("%s: OPTIONS sem preflight chegou ao roteador", mode)
		}
	}
}

// C.4.1.9.2: ACAO * em toda resposta, inclusive a repassada ao backend e a
// requisicao sem Origin (o modulo CORS do KrakenD so age com Origin).
func TestACAOEmTodaResposta(t *testing.T) {
	h := newTestHandler(t, modeWarn, baseStore(), nil)
	for _, hd := range []map[string]string{nil, {"Origin": "http://x"}} {
		rec := serve(h, "GET", "/health", hd)
		if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "*" {
			t.Errorf("Origin=%q: Access-Control-Allow-Origin=%q, esperado um unico *", hd["Origin"], got)
		}
	}
}

// Caminho com quebra de linha (ja decodificada em r.URL.Path) nao forja
// linha no log.
func TestLogNaoQuebraLinha(t *testing.T) {
	var buf bytes.Buffer
	old := logger
	logger = log.New(&buf, "", 0)
	defer func() { logger = old }()
	h := newTestHandler(t, modeWarn, baseStore(), nil)
	serve(h, "GET", "/tv3/abc%0A[tv30-auth]%20WARN%20forjado", nil)
	if n := strings.Count(strings.TrimRight(buf.String(), "\n"), "\n"); n != 0 {
		t.Fatalf("uma requisicao gerou %d linhas de log:\n%s", n+1, buf.String())
	}
}

func TestPanicVira200(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("invalid node type") })
	h := newTestHandler(t, modeWarn, baseStore(), next)
	rec := serve(h, "GET", "/health", nil)
	checkC32(t, rec, 200)
}

func TestRedisFora(t *testing.T) {
	st := baseStore()
	st.err = errors.New("dial tcp: connection refused")
	tok := accessFor(t, "cli-1", classAutonomous)

	h := newTestHandler(t, modeEnforce, st, nil)
	checkC32(t, serve(h, "GET", "/tv3/current-service", map[string]string{"Authorization": tok}), 200)

	h = newTestHandler(t, modeWarn, st, nil)
	rec := serve(h, "GET", "/tv3/current-service", map[string]string{"Authorization": tok})
	if rec.Code != 200 || rec.Header().Get(warnHeader) != "200" {
		t.Fatalf("warn com redis fora: status %d aviso %q", rec.Code, rec.Header().Get(warnHeader))
	}
}

func TestConfigInvalida(t *testing.T) {
	ok := map[string]string{"JWT_SECRET": "s"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := loadConfig(testExtra(), env(ok)); err != nil {
		t.Fatalf("config valida rejeitada: %v", err)
	}
	if c, _ := loadConfig(testExtra(), env(ok)); c.Mode != modeWarn || c.Issuer != "GenericIssuer" || c.RedisAddr != "redis:6379" {
		t.Fatalf("padroes errados: %+v", c)
	}
	bad := []map[string]string{
		{}, // sem JWT_SECRET
		{"JWT_SECRET": "s", "AUTH_ENFORCE": "enfroce"},
		{"JWT_SECRET": "s", "REDIS_PORT": "abc"},
		{"JWT_SECRET": "s", "REDIS_TIMEOUT_MS": "0"},
	}
	for i, b := range bad {
		if _, err := loadConfig(testExtra(), env(b)); err == nil {
			t.Errorf("env %d (%v) deveria ser rejeitado", i, b)
		}
	}
	for name, extra := range map[string]interface{}{
		"sem bloco":   nil,
		"sem surface": map[string]interface{}{"routes": testExtra()["routes"]},
		"sem rotas":   map[string]interface{}{"surface": "internal"},
		"auth ruim":   map[string]interface{}{"surface": "x", "routes": []interface{}{map[string]interface{}{"method": "GET", "path": "/a", "auth": "jwt"}}},
		"classe ruim": map[string]interface{}{"surface": "x", "routes": []interface{}{map[string]interface{}{"method": "GET", "path": "/a", "classes": []interface{}{"local-standalone"}}}},
		"cors ruim":   map[string]interface{}{"surface": "x", "routes": testExtra()["routes"], "cors_allow_headers": []interface{}{"Content-Type", ""}},
		"cors vazio":  map[string]interface{}{"surface": "x", "routes": testExtra()["routes"], "cors_allow_headers": []interface{}{}},
	} {
		if _, err := loadConfig(extra, env(ok)); err == nil {
			t.Errorf("%s: deveria ser rejeitado", name)
		}
	}
	// auth ausente => token (padrao da especificacao)
	c, _ := loadConfig(map[string]interface{}{"surface": "x", "routes": []interface{}{map[string]interface{}{"method": "get", "path": "/a"}}}, env(ok))
	if rt, _ := c.Routes.match("GET", "/a"); rt == nil || rt.Auth != authToken {
		t.Fatalf("auth ausente deveria virar token: %+v", rt)
	}
	// cors_allow_headers (do routes.json) vira o ACAH do OPTIONS
	c, err := loadConfig(map[string]interface{}{"surface": "x", "routes": testExtra()["routes"],
		"cors_allow_headers": []interface{}{"Content-Type", " bind-token "}}, env(ok))
	if err != nil || c.CORSAllowHeaders != "Content-Type, bind-token" {
		t.Fatalf("cors_allow_headers: %v %q", err, c.CORSAllowHeaders)
	}
}
