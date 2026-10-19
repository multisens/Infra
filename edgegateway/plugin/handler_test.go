package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	edge := func(m map[string]interface{}, name string) map[string]interface{} { m["edge"] = name; return m }
	// campos que o generate.js deriva para a superficie interna (F-1, E7, H2)
	httpNL := func(m map[string]interface{}) map[string]interface{} { m["nonlocal_http"] = true; return m }
	semServ := func(m map[string]interface{}) map[string]interface{} { m["no_service"] = float64(300); return m }
	vers := func(m map[string]interface{}, vs ...string) map[string]interface{} {
		l := []interface{}{}
		for _, v := range vs {
			l = append(l, v)
		}
		m["versions"] = l
		return m
	}
	api := func(id, version string) map[string]interface{} {
		return map[string]interface{}{"id": id, "version": version}
	}
	return map[string]interface{}{
		"surface":             "internal",
		"nonlocal_https_only": true,
		"apis": []interface{}{
			api("tv3ws-application-authorization", "2.0"),
			api("tv3ws-current-service", "2.0"),
			api("tv3ws-api-info", "2.0"),
			api("tv3ws-bind-context-register", "2.0"),
			api("tv3ws-bind-context-list", "2.0"),
			api("tv3ws-bind-context-remove", "2.0"),
		},
		"routes": []interface{}{
			r("GET", "/health", authNone),
			r("GET", "/manifest", authNone),
			httpNL(r("GET", "/tv3/authorize", authNone, classAutonomous, classNonLocal)),
			httpNL(r("GET", "/tv3/token", authNone, classAutonomous, classNonLocal)),
			edge(r("POST", "/tv3/bind-context", authNone, classAssociated), edgeBindRegister),
			edge(r("GET", "/tv3/bind-context", authToken, classAutonomous, classNonLocal), edgeBindList),
			edge(r("DELETE", "/tv3/bind-context", authNone, classAssociated), edgeBindRemove),
			edge(r("GET", "/tv3/api-info", authToken), edgeAPIList),
			edge(r("GET", "/tv3/api-info/{apiId}", authToken), edgeAPIInfo),
			r("GET", "/tv3/current-service", authToken),
			semServ(r("GET", "/tv3/current-service/users/current-user", authTokenBind)),
			semServ(r("POST", "/tv3/current-service/users", authTokenBind)),
			// C.6.16.3: a tabela nao lista o 300 (sem servico => 108, H2)
			r("POST", "/tv3/sensory-effect-renderers/{rendererId}", authTokenBind),
			// D1/D4 (Joel, 15/10): a C.6.15.5 serve 2.0 e 100.0; a rota por
			// handle so existe na 100.0
			vers(r("GET", "/tv3/remote-device/devices/{classId}", authToken, classAssociated, classAutonomous), "2.0", "100.0"),
			vers(r("GET", "/tv3/remote-device/device/{handle}", authToken, classAssociated, classAutonomous), "100.0"),
			// POST /tv3/{serviceContextId}/users saiu da tabela (A4, decisao do
			// Luis em 11/10): fora da norma, a borda responde 100
			semServ(r("GET", "/tv3/{serviceContextId}/users/{userid}", authTokenBind)),
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
		blocked: map[string]bool{"cli-bloq": true},
		// E6 (Luis, 11/10): so passa quem esta em clients:authorized; o
		// bloqueado nunca esta (invariante clientes-disjuntos do esquema)
		authorized: map[string]bool{"cli-1": true, "cli-2": true, "cli-3": true},
		associated: map[string]string{tAssoc: tSvc},
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
	// E6: token valido de cliente fora de clients:authorized (desbloqueado sem
	// nova autorizacao, ou nunca autorizado) e token valido sem sub
	fora := accessFor(t, "cli-fora", classAutonomous)
	foraNL := accessFor(t, "cli-fora", classNonLocal)
	foraAssoc := accessFor(t, "cli-fora", classAssociated)
	semSub := "Bearer " + signJWT(t, "HS256", []byte(tSecret), map[string]interface{}{
		"iat": t0.Unix(), "exp": t0.Add(time.Hour).Unix(), "iss": "GenericIssuer", "class": classAutonomous})
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
		{"token valido", "GET", "/tv3/current-service", hdr{"Authorization": auto}, 0},
		// E7 (Joel, 15/10; C.4.1.6): na superficie HTTP o nao local so usa a
		// C.6.1.2 e a C.6.1.3; o 106 vem antes do 107 (tipo de cliente)
		{"E7: nao local por HTTP em rota comum", "GET", "/tv3/current-service", hdr{"Authorization": nonLocal}, 106},
		{"E7: nao local por HTTP numa API da borda", "GET", "/tv3/api-info", hdr{"Authorization": nonLocal}, 106},
		{"E7: nao local por HTTP no /authorize", "GET", "/tv3/authorize", hdr{"Authorization": nonLocal}, 0},
		{"E7: nao local por HTTP no /token", "GET", "/tv3/token", hdr{"Authorization": nonLocal}, 0},
		{"E7: nao local fora de clients:authorized (106 antes do 107)", "GET", "/tv3/current-service", hdr{"Authorization": foraNL}, 106},
		{"E7: token nao local invalido nao identifica a classe (107)", "GET", "/tv3/current-service", hdr{"Authorization": expired}, 107},
		// A8 (Joel, 15/10): o Origin associado da 106 em /authorize e /token
		// tambem com access token valido de outra classe
		{"A8: authorize com Origin associado e token autonomo valido", "GET", "/tv3/authorize", hdr{"Origin": tAssoc, "Authorization": auto}, 106},
		{"A8: token com Origin associado e token autonomo valido", "GET", "/tv3/token", hdr{"Origin": tAssoc, "Authorization": auto}, 106},
		{"A8: token com Origin associado, sem Authorization", "GET", "/tv3/token", hdr{"Origin": tAssoc}, 106},
		{"A8: rota que admite o associado nao muda (token autonomo + Origin associado)", "GET", "/tv3/current-service", hdr{"Origin": tAssoc, "Authorization": auto}, 0},
		{"token expirado", "GET", "/tv3/current-service", hdr{"Authorization": expired}, 107},
		{"token lixo", "GET", "/tv3/current-service", hdr{"Authorization": "Bearer lixo"}, 107},
		{"cliente bloqueado", "GET", "/tv3/current-service", hdr{"Authorization": blocked}, 107},
		{"bloqueado com token de classe associado", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": blockedAssoc}, 107},
		{"token de associado dispensa bind", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": assocTok}, 0},
		// E6 (decisao do Luis em 11/10): sub fora de clients:authorized = 107
		{"fora de clients:authorized (E6)", "GET", "/tv3/current-service", hdr{"Authorization": fora}, 107},
		{"fora de clients:authorized, rota token+bind (E6)", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": fora, "bind-token": bindOK}, 107},
		{"token de associado fora de clients:authorized (E6)", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": foraAssoc}, 107},
		{"token sem sub (E6)", "GET", "/tv3/current-service", hdr{"Authorization": semSub}, 107},
		{"fora de clients:authorized, API da borda com token (E6)", "GET", "/tv3/api-info", hdr{"Authorization": fora}, 107},
		// rotas auth none nao mudam com a E6
		{"authorize com token fora de clients:authorized (auth none)", "GET", "/tv3/authorize", hdr{"Authorization": fora}, 0},
		{"health com token fora de clients:authorized", "GET", "/health", hdr{"Authorization": fora}, 0},
		{"associado pelo Origin nao depende do conjunto", "GET", "/tv3/current-service", hdr{"Origin": tAssoc}, 0},
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
		// A6 (Joel, 15/10): o scid e o globalServiceId; qualquer servico e
		// aceito, e o bind-token vale contra as chaves DAQUELE servico (D4)
		{"scid = globalServiceId do corrente", "GET", "/tv3/" + tSvc + "/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
		{"scid current-service", "GET", "/tv3/current-service/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 0},
		{"scid de outro servico com bind-token dele (A6)", "GET", "/tv3/urn:outra/users/u1", hdr{"Authorization": auto, "bind-token": bindOutra}, 0},
		{"scid de outro servico com bind-token do corrente (D4)", "GET", "/tv3/urn:outra/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 108},
		{"a constante de antes nao e mais servico (A6)", "GET", "/tv3/" + tSCID + "/users/u1", hdr{"Authorization": auto, "bind-token": bindOK}, 108},
		// H1 (Joel, 15/10): o associado so dispensa o bind-token no proprio
		// contexto (o servico que lancou a app, origins:associated)
		{"H1: associado no proprio contexto (scid explicito)", "GET", "/tv3/" + tSvc + "/users/u1", hdr{"Origin": tAssoc}, 0},
		{"H1: associado no proprio contexto (current-service)", "GET", "/tv3/current-service/users/u1", hdr{"Origin": tAssoc}, 0},
		{"H1: associado em outro contexto sem bind-token", "GET", "/tv3/urn:outra/users/u1", hdr{"Origin": tAssoc}, 104},
		{"H1: associado em outro contexto com bind-token invalido", "GET", "/tv3/urn:outra/users/u1", hdr{"Origin": tAssoc, "bind-token": bindOK}, 108},
		{"H1: associado em outro contexto com bind-token daquele contexto", "GET", "/tv3/urn:outra/users/u1", hdr{"Origin": tAssoc, "bind-token": bindOutra}, 0},
		{"H1: rota sem bind-token nao muda", "GET", "/tv3/current-service", hdr{"Origin": tAssoc}, 0},
		// C.6.14.1 (bind shall); a variante POST {scid}/users saiu da tabela
		// (A4, Luis 11/10): 100 com qualquer scid, inclusive o constante e o
		// current-service com a caixa trocada
		{"POST current-service/users sem bind", "POST", "/tv3/current-service/users", hdr{"Authorization": auto}, 104},
		{"POST {scid}/users sem bind (A4)", "POST", "/tv3/xyz/users", hdr{"Authorization": auto}, 100},
		{"POST {scid}/users scid qualquer (A4)", "POST", "/tv3/xyz/users", hdr{"Authorization": auto, "bind-token": bindOK}, 100},
		{"POST Current-Service/users (caixa trocada, A4)", "POST", "/tv3/Current-Service/users", hdr{"Authorization": auto, "bind-token": bindOK}, 100},
		{"POST {scid}/users com o globalServiceId (A4)", "POST", "/tv3/" + tSvc + "/users", hdr{"Authorization": auto, "bind-token": bindOK}, 100},
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

// E6 (decisao do Luis em 11/10): o bloqueado continua 107 com a mensagem do
// bloqueio (conferido antes); o fora do conjunto tem a mensagem propria; o
// conjunto vale para o cliente que volta a ele (sem cache na borda); falha do
// Redis na consulta do conjunto da 200, como as outras consultas.
func TestDecisaoClientesAutorizados(t *testing.T) {
	req := func(tok string) *http.Request {
		r := httptest.NewRequest("GET", "/tv3/current-service", nil)
		r.Header.Set("Authorization", tok)
		return r
	}
	st := baseStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	if v := h.evaluate(req(accessFor(t, "cli-bloq", classAutonomous))); v.Code != 107 || !strings.Contains(v.Detail, "bloqueado pelo usuario") {
		t.Fatalf("bloqueado: %d %q", v.Code, v.Detail)
	}
	tok := accessFor(t, "cli-volta", classAutonomous)
	if v := h.evaluate(req(tok)); v.Code != 107 || !strings.Contains(v.Detail, "fora de clients:authorized (E6)") {
		t.Fatalf("fora do conjunto: %d %q", v.Code, v.Detail)
	}
	// token sem sub: 107 sem consultar o Redis (nao ha id para procurar)
	semSub := "Bearer " + signJWT(t, "HS256", []byte(tSecret), map[string]interface{}{
		"exp": t0.Add(time.Hour).Unix(), "iss": "GenericIssuer", "class": classAutonomous})
	antes := st.calls
	if v := h.evaluate(req(semSub)); v.Code != 107 || !strings.Contains(v.Detail, "sem sub") || st.calls != antes {
		t.Fatalf("sem sub: %d %q, consultas ao redis %d", v.Code, v.Detail, st.calls-antes)
	}
	st.authorized["cli-volta"] = true // o tv3ws o inclui de novo (boot ou nova autorizacao)
	if v := h.evaluate(req(tok)); v.Code != 0 {
		t.Fatalf("de volta ao conjunto: %d %q", v.Code, v.Detail)
	}
	delete(st.authorized, "cli-volta") // autorizacao removida
	if v := h.evaluate(req(tok)); v.Code != 107 {
		t.Fatalf("autorizacao removida: %d %q", v.Code, v.Detail)
	}
	// bloqueado E (por defeito do dado) ainda no conjunto: o bloqueio vence
	st.authorized["cli-bloq"] = true
	if v := h.evaluate(req(accessFor(t, "cli-bloq", classAutonomous))); v.Code != 107 || !strings.Contains(v.Detail, "bloqueado") {
		t.Fatalf("bloqueado no conjunto: %d %q", v.Code, v.Detail)
	}
	// Redis fora so na consulta do conjunto: 200
	st2 := baseStore()
	st2.authzErr = errors.New("conexao recusada")
	h2 := newTestHandler(t, modeEnforce, st2, nil)
	if v := h2.evaluate(req(accessFor(t, "cli-1", classAutonomous))); v.Code != 200 {
		t.Fatalf("redis fora no conjunto: %d %q", v.Code, v.Detail)
	}
	// warn: so avisa
	hw := newTestHandler(t, modeWarn, baseStore(), nil)
	rec := serve(hw, "GET", "/tv3/current-service", map[string]string{"Authorization": accessFor(t, "cli-fora", classAutonomous)})
	if rec.Code != 200 || rec.Header().Get(warnHeader) != "107" {
		t.Fatalf("warn: status %d, aviso %q", rec.Code, rec.Header().Get(warnHeader))
	}
}

// H2 (Joel, 15/10): sem servico DTV selecionado, 300 nas rotas cuja tabela o
// lista (no_service), conferido depois do access token (107) e antes do
// bind-token (104/108); 108 na C.6.16.3, cuja tabela nao lista o 300.
func TestDecisaoSemServicoCorrente(t *testing.T) {
	st := baseStore()
	st.current = ""
	h := newTestHandler(t, modeEnforce, st, nil)
	bind := signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0, t0, t0.Add(time.Hour)))
	auto := accessFor(t, "cli-1", classAutonomous)
	type hdr map[string]string
	for _, c := range []struct {
		name, method, path string
		h                  hdr
		want               int
	}{
		{"C.6.14.3 com token e bind-token", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto, "bind-token": bind}, 300},
		{"C.6.14.3 sem bind-token (300 antes do 104)", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": auto}, 300},
		{"C.6.14.3 sem access token (107 antes do 300)", "GET", "/tv3/current-service/users/current-user", nil, 107},
		{"C.6.14.1", "POST", "/tv3/current-service/users", hdr{"Authorization": auto}, 300},
		{"C.6.14.2 com scid no caminho", "GET", "/tv3/" + tSvc + "/users/u1", hdr{"Authorization": auto, "bind-token": bind}, 300},
		{"C.6.14.3 pelo associado (Origin)", "GET", "/tv3/current-service/users/current-user", hdr{"Origin": tAssoc}, 300},
		{"C.6.14.3 com token de classe associado", "GET", "/tv3/current-service/users/current-user", hdr{"Authorization": accessFor(t, "cli-3", classAssociated)}, 300},
		{"C.6.16.3: a tabela nao lista o 300", "POST", "/tv3/sensory-effect-renderers/r1", hdr{"Authorization": auto, "bind-token": bind}, 108},
		{"C.6.16.3 sem bind-token", "POST", "/tv3/sensory-effect-renderers/r1", hdr{"Authorization": auto}, 104},
		{"rota sem bind-token nao depende do servico", "GET", "/tv3/current-service", hdr{"Authorization": auto}, 0},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		for k, v := range c.h {
			req.Header.Set(k, v)
		}
		if v := h.evaluate(req); v.Code != c.want {
			t.Errorf("%s: codigo %d (%s), esperado %d", c.name, v.Code, v.Detail, c.want)
		}
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
	rec := serve(h, "GET", "/tv3/current-service", map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)})
	if rec.Header().Get(warnHeader) != "" {
		t.Error("requisicao valida recebeu aviso")
	}
}

// Regra do Joel em 15/10: so 104, 107 e 108 seguem o modo; o 106 (tipo de
// cliente), o 300 (estado do receptor) e o 200 (Redis fora do ar) bloqueiam
// tambem em warn, sem aviso, e nao chegam ao backend.
func TestWarnNaoAliviaForaDaCredencial(t *testing.T) {
	st := baseStore()
	st.current = ""
	h := newTestHandler(t, modeWarn, st, nil)
	for _, c := range []struct {
		name, method, path string
		hdr                map[string]string
		code               int
	}{
		{"106 ao associado em /authorize", "GET", "/tv3/authorize", map[string]string{"Origin": tAssoc}, 106},
		{"106 ao nao associado no POST da C.6.8", "POST", "/tv3/bind-context", nil, 106},
		{"106 ao nao local por HTTP (E7)", "GET", "/tv3/current-service", map[string]string{"Authorization": accessFor(t, "cli-1", classNonLocal)}, 106},
		{"300 sem servico na C.6.14.3", "GET", "/tv3/current-service/users/current-user", map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)}, 300},
	} {
		rec := serve(h, c.method, c.path, c.hdr)
		checkC32(t, rec, c.code)
		if rec.Header().Get(warnHeader) != "" {
			t.Errorf("%s: aviso %q num erro que bloqueia", c.name, rec.Header().Get(warnHeader))
		}
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

// API-Version (C.3.6.6) nas respostas que a borda escreve sem repassar:
// erros C.3.2 (rota nao declarada, credencial) e o OPTIONS que nao e
// preflight. Versao pedida e servida pela rota: ela; que a rota nao serve: a
// mais recente do servidor (100.0, D1); malformada ou ausente: 2.0.
func TestAPIVersionDasRespostasDaBorda(t *testing.T) {
	h := newTestHandler(t, modeEnforce, baseStore(), nil)
	if h.cfg.LatestVersion != "100.0" || strings.Join(h.cfg.AllVersions, ",") != "2.0,100.0" {
		t.Fatalf("versoes do servidor: %v, mais recente %s", h.cfg.AllVersions, h.cfg.LatestVersion)
	}
	for _, c := range []struct {
		accept []string // nil = sem Accept-Version
		want   string
	}{
		{nil, "2.0"}, {[]string{"2.0"}, "2.0"}, {[]string{"100.0"}, "100.0"},
		{[]string{"2.1"}, "100.0"}, {[]string{"3.0"}, "100.0"}, {[]string{"9.99"}, "100.0"},
		{[]string{"abc"}, "2.0"}, {[]string{"2"}, "2.0"}, {[]string{"2.1", "2.0"}, "2.0"},
	} {
		for _, rq := range []struct {
			name, method, path string
			code               int
		}{
			{"107 sem token", "GET", "/tv3/current-service", 107},
			{"100 rota nao declarada", "GET", "/tv3/abc", 100},
			{"OPTIONS sem preflight", "OPTIONS", "/tv3/current-service", 0},
		} {
			req := httptest.NewRequest(rq.method, rq.path, nil)
			for _, v := range c.accept {
				req.Header.Add("Accept-Version", v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rq.code != 0 {
				checkC32(t, rec, rq.code)
			}
			if v := rec.Header().Get("API-Version"); v != c.want {
				t.Errorf("%s com Accept-Version %v: API-Version %q, esperado %s", rq.name, c.accept, v, c.want)
			}
		}
	}
}

// D2 e D4 (Joel, 15/10): a borda negocia a versao em toda rota, pelas versoes
// que o routes.json declara para ela, e repassa a decidida ao servico em
// X-TV30-API-Version, que apaga o que vier do cliente. 100 (versao que a rota
// nao serve) e 101 (malformado) saem nos dois modos e nao chegam ao servico.
func TestVersaoNegociadaPorRota(t *testing.T) {
	auto := accessFor(t, "cli-1", classAutonomous)
	for _, mode := range []string{modeWarn, modeEnforce} {
		var got string
		calls := 0
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			got = r.Header.Get(internalVersionHeader)
			w.WriteHeader(200)
		})
		h := newTestHandler(t, mode, baseStore(), next)
		for _, c := range []struct {
			name, path string
			hdr        map[string]string
			code       int    // 0 = repassa
			version    string // o que o servico recebe
		}{
			{"rota so 2.0, sem Accept-Version", "/tv3/current-service", map[string]string{"Authorization": auto}, 0, "2.0"},
			{"rota so 2.0, pede 100.0", "/tv3/current-service", map[string]string{"Authorization": auto, "Accept-Version": "100.0"}, 100, ""},
			{"rota so 2.0, pede 2.1 (nao existe mais)", "/tv3/current-service", map[string]string{"Authorization": auto, "Accept-Version": "2.1"}, 100, ""},
			{"Accept-Version malformado", "/tv3/current-service", map[string]string{"Authorization": auto, "Accept-Version": "x"}, 101, ""},
			{"cabecalho interno do cliente e apagado", "/tv3/current-service", map[string]string{"Authorization": auto, internalVersionHeader: "100.0"}, 0, "2.0"},
			{"C.6.15.5 sem Accept-Version", "/tv3/remote-device/devices/x", map[string]string{"Authorization": auto}, 0, "2.0"},
			{"C.6.15.5 com 100.0", "/tv3/remote-device/devices/x", map[string]string{"Authorization": auto, "Accept-Version": "100.0"}, 0, "100.0"},
			{"handle sem Accept-Version (so 100.0)", "/tv3/remote-device/device/h1", map[string]string{"Authorization": auto}, 100, ""},
			{"handle com 2.0", "/tv3/remote-device/device/h1", map[string]string{"Authorization": auto, "Accept-Version": "2.0"}, 100, ""},
			{"handle com 100.0", "/tv3/remote-device/device/h1", map[string]string{"Authorization": auto, "Accept-Version": "100.0"}, 0, "100.0"},
		} {
			calls, got = 0, ""
			rec := serve(h, "GET", c.path, c.hdr)
			if c.code != 0 {
				checkC32(t, rec, c.code)
				if calls != 0 {
					t.Errorf("%s %s: chegou ao servico", mode, c.name)
				}
				if c.code == 100 && rec.Header().Get("API-Version") != "100.0" {
					t.Errorf("%s %s: API-Version %q no 100, esperado 100.0", mode, c.name, rec.Header().Get("API-Version"))
				}
				continue
			}
			if calls != 1 || got != c.version {
				t.Errorf("%s %s: repasses %d, %s=%q, esperado %q", mode, c.name, calls, internalVersionHeader, got, c.version)
			}
		}
	}
}

// Preflight CORS (opcao b, decisao do Luis em 12/10): a borda responde
// sozinha, em qualquer caminho e nos dois modos, sem chegar ao roteador (o
// modulo CORS do KrakenD recusava a lista fora de ordem). Lista de
// cabecalhos pedidos em qualquer ordem, ou ausente: a resposta e a mesma,
// com o Max-Age so quando configurado.
func TestPreflightRespondidoPelaBorda(t *testing.T) {
	for _, mode := range []string{modeWarn, modeEnforce} {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })
		h := newTestHandler(t, mode, baseStore(), next)
		h.cfg.CORSMaxAge = "43200"
		for _, p := range []string{"/tv3/current-service", "/tv3/bind-context", "/tv3/qualquer-coisa"} {
			for _, acrh := range []string{"", "bind-token,content-type", "content-type,bind-token", "Authorization, Accept-Version"} {
				hd := map[string]string{"Origin": "http://x", "Access-Control-Request-Method": "POST"}
				if acrh != "" {
					hd["Access-Control-Request-Headers"] = acrh
				}
				rec := serve(h, "OPTIONS", p, hd)
				if rec.Code != http.StatusOK {
					t.Errorf("%s %s [%s]: status %d, esperado 200", mode, p, acrh, rec.Code)
				}
				for k, want := range map[string]string{
					"Access-Control-Allow-Origin":  "*",
					"Access-Control-Allow-Methods": "*",
					"Access-Control-Allow-Headers": defaultCORSAllowHeaders,
					"Access-Control-Max-Age":       "43200",
				} {
					if got := rec.Header().Values(k); len(got) != 1 || got[0] != want {
						t.Errorf("%s %s [%s]: %s=%q, esperado um unico %q", mode, p, acrh, k, got, want)
					}
				}
			}
		}
		if called {
			t.Errorf("%s: preflight chegou ao roteador (modulo CORS do KrakenD)", mode)
		}
		// sem cors_max_age, o preflight sai sem Access-Control-Max-Age, e o
		// OPTIONS que nao eh preflight nunca o leva
		h.cfg.CORSMaxAge = ""
		if v := serve(h, "OPTIONS", "/tv3/current-service", map[string]string{"Access-Control-Request-Method": "GET"}).Header().Get("Access-Control-Max-Age"); v != "" {
			t.Errorf("%s: Max-Age %q sem cors_max_age", mode, v)
		}
		h.cfg.CORSMaxAge = "43200"
		if v := serve(h, "OPTIONS", "/tv3/current-service", nil).Header().Get("Access-Control-Max-Age"); v != "" {
			t.Errorf("%s: Max-Age %q no OPTIONS que nao eh preflight", mode, v)
		}
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

// Access-Control-Expose-Headers (cors.expose_headers) em toda resposta,
// uma linha so, desde que o modulo CORS do KrakenD saiu (opcao b, Luis
// 12/10): repassada ao backend, erro C.3.2 da borda, OPTIONS e preflight. O
// do backend (o /manifest do tv3ws, B3) vem numa segunda linha, somado.
func TestExposeHeadersEmTodaResposta(t *testing.T) {
	for _, mode := range []string{modeWarn, modeEnforce} {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/manifest" {
				w.Header().Add("Access-Control-Expose-Headers", "Server-BaseURL")
			}
			w.WriteHeader(200)
		})
		h := newTestHandler(t, mode, baseStore(), next)
		h.cfg.CORSExposeHeaders = "Content-Length, X-TV30-Auth-Warn"
		for _, c := range []struct {
			method, path string
			hd           map[string]string
		}{
			{"GET", "/health", nil},
			{"GET", "/tv3/naoexiste", map[string]string{"Origin": "http://x"}},
			{"OPTIONS", "/tv3/current-service", nil},
			{"OPTIONS", "/tv3/current-service", map[string]string{"Access-Control-Request-Method": "GET"}},
		} {
			rec := serve(h, c.method, c.path, c.hd)
			if got := rec.Header().Values("Access-Control-Expose-Headers"); len(got) != 1 || got[0] != "Content-Length, X-TV30-Auth-Warn" {
				t.Errorf("%s %s %s: Access-Control-Expose-Headers=%q", mode, c.method, c.path, got)
			}
		}
		if got := serve(h, "GET", "/manifest", nil).Header().Values("Access-Control-Expose-Headers"); len(got) != 2 || got[1] != "Server-BaseURL" {
			t.Errorf("%s /manifest: Access-Control-Expose-Headers=%q, esperado o da borda e o do backend", mode, got)
		}
		// sem cors_expose_headers, nenhum cabecalho da borda
		h.cfg.CORSExposeHeaders = ""
		if got := serve(h, "GET", "/health", nil).Header().Values("Access-Control-Expose-Headers"); len(got) != 0 {
			t.Errorf("%s: Expose-Headers %q sem cors_expose_headers", mode, got)
		}
	}
}

// C.4.1.9.2: ACAO * em toda resposta, inclusive a repassada ao backend e a
// requisicao sem Origin.
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

	// desde 15/10 (Joel) o 200 nao e recusa de credencial: sai tambem em warn
	h = newTestHandler(t, modeWarn, st, nil)
	rec := serve(h, "GET", "/tv3/current-service", map[string]string{"Authorization": tok})
	checkC32(t, rec, 200)
	if rec.Header().Get(warnHeader) != "" {
		t.Fatalf("warn com redis fora: aviso %q, esperado o erro sem aviso", rec.Header().Get(warnHeader))
	}
}

func TestConfigInvalida(t *testing.T) {
	ok := map[string]string{"JWT_SECRET": "s"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := loadConfig(testExtra(), env(ok)); err != nil {
		t.Fatalf("config valida rejeitada: %v", err)
	}
	// padrao enforce desde 15/10 (Joel); warn so explicito
	if c, _ := loadConfig(testExtra(), env(ok)); c.Mode != modeEnforce || c.Issuer != "GenericIssuer" || c.RedisAddr != "redis:6379" {
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
	// testExtra com um campo trocado (nil = removido)
	with := func(k string, v interface{}) map[string]interface{} {
		m := testExtra()
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	oneRoute := func(rt map[string]interface{}) []interface{} { return []interface{}{rt} }
	for name, extra := range map[string]interface{}{
		"sem bloco":   nil,
		"sem surface": with("surface", nil),
		"sem rotas":   with("routes", nil),
		"auth ruim":   map[string]interface{}{"surface": "x", "routes": oneRoute(map[string]interface{}{"method": "GET", "path": "/a", "auth": "jwt"})},
		"classe ruim": map[string]interface{}{"surface": "x", "routes": oneRoute(map[string]interface{}{"method": "GET", "path": "/a", "classes": []interface{}{"local-standalone"}})},
		"cors ruim":   with("cors_allow_headers", []interface{}{"Content-Type", ""}),
		"cors vazio":  with("cors_allow_headers", []interface{}{}),
		// Access-Control-Max-Age do preflight (cors_max_age)
		"max_age ruim":     with("cors_max_age", "doze horas"),
		"max_age zero":     with("cors_max_age", "0s"),
		"max_age negativo": with("cors_max_age", "-1h"),
		"max_age fracao":   with("cors_max_age", "1500ms"),
		"max_age numero":   with("cors_max_age", 43200),
		// Access-Control-Expose-Headers (cors_expose_headers)
		"expose nao lista": with("cors_expose_headers", "X-A"),
		"expose ruim":      with("cors_expose_headers", []interface{}{"X-A", " "}),
		// APIs respondidas pela borda
		"edge desconhecido":            map[string]interface{}{"surface": "x", "routes": oneRoute(map[string]interface{}{"method": "GET", "path": "/a", "edge": "proxy"})},
		"edge nao texto":               map[string]interface{}{"surface": "x", "routes": oneRoute(map[string]interface{}{"method": "GET", "path": "/a", "edge": true})},
		"constante de scid (saiu, A6)": with("current_service_context_id", tSCID),
		"api-info sem apis":            with("apis", nil),
		"apis nao lista":               with("apis", "tv3ws-api-info"),
		"api sem versao":               with("apis", []interface{}{map[string]interface{}{"id": "tv3ws-api-info"}}),
		"api versao ruim":              with("apis", []interface{}{map[string]interface{}{"id": "tv3ws-api-info", "version": "2"}}),
		"api id sem nome":              with("apis", []interface{}{map[string]interface{}{"id": "tv3ws", "version": "2.0"}}),
		"api subsistema":               with("apis", []interface{}{map[string]interface{}{"id": "dtv-api-info", "version": "2.0"}}),
		"api duplicada": with("apis", []interface{}{
			map[string]interface{}{"id": "tv3ws-api-info", "version": "2.0"},
			map[string]interface{}{"id": "tv3ws-api-info", "version": "2.0"},
		}),
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
	c, err := loadConfig(with("cors_allow_headers", []interface{}{"Content-Type", " bind-token "}), env(ok))
	if err != nil || c.CORSAllowHeaders != "Content-Type, bind-token" {
		t.Fatalf("cors_allow_headers: %v %+v", err, c)
	}
	// cors_max_age (do routes.json) vira o Access-Control-Max-Age do
	// preflight, em segundos; ausente, sem o cabecalho
	if c, err := loadConfig(with("cors_max_age", " 12h "), env(ok)); err != nil || c.CORSMaxAge != "43200" {
		t.Fatalf("cors_max_age: %v %+v", err, c)
	}
	if c, err := loadConfig(with("cors_max_age", nil), env(ok)); err != nil || c.CORSMaxAge != "" {
		t.Fatalf("sem cors_max_age: %v %+v", err, c)
	}
	// cors_expose_headers vira o Access-Control-Expose-Headers; lista vazia ou
	// ausente, sem o cabecalho
	if c, err := loadConfig(with("cors_expose_headers", []interface{}{"Content-Length", " X-TV30-Auth-Warn "}), env(ok)); err != nil || c.CORSExposeHeaders != "Content-Length, X-TV30-Auth-Warn" {
		t.Fatalf("cors_expose_headers: %v %+v", err, c)
	}
	if c, err := loadConfig(with("cors_expose_headers", []interface{}{}), env(ok)); err != nil || c.CORSExposeHeaders != "" {
		t.Fatalf("cors_expose_headers vazio: %v %+v", err, c)
	}
	// sem rota respondida pela borda, nem scid nem apis sao exigidos
	c, err = loadConfig(map[string]interface{}{"surface": "x", "routes": oneRoute(map[string]interface{}{"method": "GET", "path": "/a"})}, env(ok))
	if err != nil || len(c.APIs.list) != 0 {
		t.Fatalf("config sem rotas da borda: %v %+v", err, c)
	}
	// edge e apis chegam a rota e ao catalogo, na ordem dada
	c, err = loadConfig(testExtra(), env(ok))
	if err != nil {
		t.Fatal(err)
	}
	if rt, _ := c.Routes.match("DELETE", "/tv3/bind-context"); rt == nil || rt.Edge != edgeBindRemove {
		t.Errorf("DELETE /tv3/bind-context: edge %+v", rt)
	}
	if rt, _ := c.Routes.match("GET", "/tv3/current-service"); rt == nil || rt.Edge != "" {
		t.Errorf("GET /tv3/current-service nao eh da borda: %+v", rt)
	}
	if len(c.APIs.list) != 6 || c.APIs.list[0].ID != "tv3ws-application-authorization" {
		t.Errorf("catalogo: %+v", c.APIs.list)
	}
}

// --- P1: 5xx do gateway (backend lento ou fora do ar) vira 404 + {error:200} ---

// krakend500 imita o endpoint do KrakenD quando o proxy falha (timeout,
// conexao recusada): cabecalhos proprios e c.Status(500), sem corpo.
var krakend500 = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("X-Krakend", "Version 2.7.2")
	hd.Set("X-Krakend-Completed", "false")
	hd.Set("Access-Control-Expose-Headers", "Content-Length, X-TV30-Auth-Warn")
	hd.Set("Vary", "Origin")
	w.WriteHeader(http.StatusInternalServerError)
})

func c32Description(rec *httptest.ResponseRecorder) string {
	var body struct {
		Description string `json:"description"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Description
}

func TestGateway5xxVira200(t *testing.T) {
	backend503 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Type", "text/html")
		hd.Set("Content-Encoding", "gzip")
		hd.Set("Content-Length", "32")
		hd.Set("X-Powered-By", "Express")
		hd.Set("API-Version", "2.1")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("<html>Service Unavailable</html>"))
	})
	tok := map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)}
	for _, mode := range []string{modeWarn, modeEnforce} {
		for _, c := range []struct {
			name   string
			next   http.Handler
			status int
			kept   []string
		}{
			{"500 sem corpo", krakend500, 500, []string{"X-Krakend", "X-Krakend-Completed", "Access-Control-Expose-Headers", "Vary"}},
			{"503 com corpo", backend503, 503, nil},
		} {
			h := newTestHandler(t, mode, baseStore(), c.next)
			rec := serve(h, "GET", "/tv3/current-service", tok)
			checkC32(t, rec, 200)
			want := errorText[200] + ": " + strconv.Itoa(c.status) + " do gateway em 0 ms (backend lento ou fora do ar)"
			if d := c32Description(rec); d != want {
				t.Errorf("%s %s: description %q, esperado %q", mode, c.name, d, want)
			}
			if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
				t.Errorf("%s %s: Content-Length %q, corpo de %d bytes", mode, c.name, cl, rec.Body.Len())
			}
			// cabecalhos do backend nao descrevem o corpo novo
			for _, k := range []string{"Content-Encoding", "X-Powered-By"} {
				if v := rec.Header().Get(k); v != "" {
					t.Errorf("%s %s: %s=%q deveria ter saido", mode, c.name, k, v)
				}
			}
			if v := rec.Header().Get("API-Version"); v != "2.0" {
				t.Errorf("%s %s: API-Version %q, esperado o da borda (2.0)", mode, c.name, v)
			}
			for _, k := range c.kept {
				if rec.Header().Get(k) == "" {
					t.Errorf("%s %s: %s deveria ficar", mode, c.name, k)
				}
			}
			if v := rec.Header().Get(warnHeader); v != "" {
				t.Errorf("%s %s: requisicao valida recebeu aviso %q", mode, c.name, v)
			}
		}
	}
}

// Em warn, a requisicao sem credencial chega ao backend; se ele falha, o erro
// eh 200 e o aviso (o que o enforce faria) continua no cabecalho.
func TestGateway5xxMantemAvisoDoWarn(t *testing.T) {
	rec := serve(newTestHandler(t, modeWarn, baseStore(), krakend500), "GET", "/tv3/current-service", nil)
	checkC32(t, rec, 200)
	if v := rec.Header().Get(warnHeader); v != "107" {
		t.Errorf("%s=%q, esperado 107", warnHeader, v)
	}
	// em enforce a mesma requisicao nem chega ao backend
	checkC32(t, serve(newTestHandler(t, modeEnforce, baseStore(), krakend500), "GET", "/tv3/current-service", nil), 107)
}

// A descricao traz o tempo ate o 5xx: perto do timeout do endpoint (2 s) eh
// backend lento; perto de zero, backend fora do ar.
func TestGateway5xxDescricaoComTempo(t *testing.T) {
	h := newTestHandler(t, modeWarn, baseStore(), krakend500)
	calls := 0
	h.now = func() time.Time {
		calls++
		if calls == 1 {
			return t0
		}
		return t0.Add(2003 * time.Millisecond)
	}
	rec := serve(h, "GET", "/health", nil)
	checkC32(t, rec, 200)
	if d := c32Description(rec); !strings.Contains(d, ": 500 do gateway em 2003 ms") {
		t.Errorf("description %q", d)
	}
}

// Resposta que nao eh 5xx passa intacta: status, corpo e cabecalhos do
// backend (inclusive o erro C.3.2 do proprio tv3ws).
func TestRespostaNormalPassaIntacta(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int // 0 = sem WriteHeader (200 implicito)
		body   string
	}{
		{"200 implicito", 0, `{"status":"ok"}`},
		{"erro C.3.2 do tv3ws", 404, `{"error":101,"description":"Illegal argument value: clientid"}`},
		{"206 parcial", 206, "parte"},
		{"204 sem corpo", 204, ""},
	} {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Powered-By", "Express")
			if c.status != 0 {
				w.WriteHeader(c.status)
			}
			if c.body != "" {
				w.Write([]byte(c.body))
			}
		})
		rec := serve(newTestHandler(t, modeEnforce, baseStore(), next), "GET", "/health", nil)
		want := c.status
		if want == 0 {
			want = 200
		}
		if rec.Code != want || rec.Body.String() != c.body || rec.Header().Get("X-Powered-By") != "Express" {
			t.Errorf("%s: status %d corpo %q X-Powered-By %q", c.name, rec.Code, rec.Body.String(), rec.Header().Get("X-Powered-By"))
		}
	}
}

// Flush com 5xx retido nao manda o cabecalho; sem 5xx, repassa.
func TestFlush(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.(http.Flusher).Flush()
		w.Write([]byte("descartado"))
	})
	checkC32(t, serve(newTestHandler(t, modeWarn, baseStore(), next), "GET", "/health", nil), 200)

	next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("a"))
		w.(http.Flusher).Flush()
	})
	rec := serve(newTestHandler(t, modeWarn, baseStore(), next), "GET", "/health", nil)
	if !rec.Flushed || rec.Code != 200 || rec.Body.String() != "a" {
		t.Errorf("flush sem 5xx: flushed=%v status %d corpo %q", rec.Flushed, rec.Code, rec.Body.String())
	}
}

// Panic depois que o cabecalho saiu: a resposta nao tem como virar erro;
// a conexao eh abortada (http.ErrAbortHandler) em vez de receber um corpo
// de erro emendado no meio.
func TestPanicDepoisDoCabecalhoAborta(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("parcial"))
		panic("falha no meio")
	})
	h := newTestHandler(t, modeWarn, baseStore(), next)
	rec := httptest.NewRecorder()
	p := func() (p interface{}) {
		defer func() { p = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		return nil
	}()
	if p != http.ErrAbortHandler {
		t.Fatalf("panic %v, esperado http.ErrAbortHandler", p)
	}
	if rec.Code != 200 || rec.Body.String() != "parcial" {
		t.Errorf("status %d corpo %q: a resposta ja enviada nao deveria mudar", rec.Code, rec.Body.String())
	}
}

func TestPanicCom5xxRetido(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		panic("invalid node type")
	})
	rec := serve(newTestHandler(t, modeWarn, baseStore(), next), "GET", "/health", nil)
	checkC32(t, rec, 200)
	if d := c32Description(rec); d != errorText[200]+": falha interna do gateway" {
		t.Errorf("description %q", d)
	}
}

// hijackRec: writer com Hijacker e CloseNotifier, como o do net/http.
type hijackRec struct {
	*httptest.ResponseRecorder
	ch chan bool
}

func (h *hijackRec) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }
func (h *hijackRec) CloseNotify() <-chan bool                     { return h.ch }

// O gin.ResponseWriter faz type assertion sem ok para Hijacker e
// CloseNotifier: o guardWriter repassa os dois. Conexao sequestrada nao
// recebe resposta trocada.
func TestHijackECloseNotify(t *testing.T) {
	rec := &hijackRec{ResponseRecorder: httptest.NewRecorder(), ch: make(chan bool)}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.(http.CloseNotifier).CloseNotify() != rec.ch {
			t.Error("CloseNotify nao repassado")
		}
		if _, _, err := w.(http.Hijacker).Hijack(); err != nil {
			t.Errorf("Hijack: %v", err)
		}
		w.WriteHeader(500)
	})
	newTestHandler(t, modeWarn, baseStore(), next).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Body.Len() != 0 {
		t.Errorf("conexao sequestrada recebeu corpo %q", rec.Body.String())
	}

	// writer sem Hijacker/CloseNotifier (httptest.ResponseRecorder)
	next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := w.(http.Hijacker).Hijack(); err == nil {
			t.Error("Hijack sem suporte deveria falhar")
		}
		if w.(http.CloseNotifier).CloseNotify() == nil {
			t.Error("CloseNotify devolveu canal nulo")
		}
	})
	serve(newTestHandler(t, modeWarn, baseStore(), next), "GET", "/health", nil)
}

// Ponta a ponta com net/http de verdade: o "roteador" imita o endpoint no-op
// do KrakenD (espera o backend ate o timeout do endpoint; erro sem resposta
// => 500 sem corpo) diante de um backend lento e de um fora do ar.
func TestBordaComBackendLentoOuForaDoAr(t *testing.T) {
	lento := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer lento.Close()
	fora := httptest.NewServer(http.NotFoundHandler())
	foraURL := fora.URL
	fora.Close() // porta fechada: conexao recusada

	const timeout = 100 * time.Millisecond
	proxyTo := func(base string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, r.Method, base+r.URL.Path, nil)
			w.Header().Set("X-Krakend", "Version 2.7.2")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				w.Header().Set("X-Krakend-Completed", "false")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		})
	}
	for _, mode := range []string{modeWarn, modeEnforce} {
		for _, c := range []struct {
			name, base string
			minMS      int64
		}{
			{"backend lento", lento.URL, timeout.Milliseconds()},
			{"backend fora do ar", foraURL, 0},
		} {
			h := newTestHandler(t, mode, baseStore(), proxyTo(c.base))
			h.now = time.Now
			edge := httptest.NewServer(h)
			if edge.URL == foraURL {
				t.Fatalf("a borda de teste pegou a porta fechada do backend fora do ar (%s); rode de novo", foraURL)
			}
			resp, err := http.Get(edge.URL + "/health")
			if err != nil {
				t.Fatalf("%s %s: %v", mode, c.name, err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			edge.Close()
			var body struct {
				Error       *int   `json:"error"`
				Description string `json:"description"`
			}
			if resp.StatusCode != 404 || json.Unmarshal(raw, &body) != nil || body.Error == nil || *body.Error != 200 {
				t.Fatalf("%s %s: status %d corpo %q, esperado 404 + {error:200}", mode, c.name, resp.StatusCode, raw)
			}
			for k, want := range map[string]string{"Content-Type": "application/json", "Access-Control-Allow-Origin": "*", "API-Version": "2.0", "X-Krakend-Completed": "false"} {
				if got := resp.Header.Get(k); got != want {
					t.Errorf("%s %s: %s=%q, esperado %q", mode, c.name, k, got, want)
				}
			}
			var ms int64
			if _, err := fmt.Sscanf(strings.TrimPrefix(body.Description, errorText[200]+": "), "500 do gateway em %d ms", &ms); err != nil || ms < c.minMS {
				t.Errorf("%s %s: description %q (tempo minimo %d ms)", mode, c.name, body.Description, c.minMS)
			}
		}
	}
}

// Complemento do Joel (15/10), 3.4: a borda repassa ao servico o Host original
// do cliente em X-TV30-Host (o KrakenD troca o Host pelo do backend), e apaga
// o que vier do cliente. O /manifest do tv3ws monta o Server-BaseURL com ele.
func TestHostOriginalRepassado(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(internalHostHeader)
		w.WriteHeader(200)
	})
	h := newTestHandler(t, modeEnforce, baseStore(), next)
	req := httptest.NewRequest("GET", "/manifest", nil)
	req.Host = "192.168.0.23:44642"
	req.Header.Set(internalHostHeader, "forjado.example:1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || got != "192.168.0.23:44642" {
		t.Fatalf("status %d, %s=%q", rec.Code, internalHostHeader, got)
	}
}
