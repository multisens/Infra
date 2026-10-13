package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// P1.9 (decisao do Luis em 10/10): superficies com rotas distintas. A externa
// (44643) so expoe as rotas que admitem o cliente nao local; a interna
// (44642), todas. Quem aplica a regra eh o generate.js: o bloco do plugin de
// cada superficie ja chega filtrado (generate.test.js confere a derivacao).
// Aqui fica o comportamento do plugin com a tabela da externa: a rota que
// ficou so na interna responde 100, nos dois modos, sem consultar o Redis e
// sem chegar ao roteador, inclusive para o local associado (a quem POST e
// DELETE /tv3/bind-context sao restritos); o GET da mesma API continua.

// externalExtra: testExtra() como o generate.js o entrega a superficie
// externa: so as rotas cuja lista "classes" (ausente = todas) inclui
// non-local, e o catalogo de APIs sem as das rotas que sairam.
func externalExtra() map[string]interface{} {
	ex := testExtra()
	ex["surface"] = "external"
	var routes []interface{}
	for _, rr := range ex["routes"].([]interface{}) {
		m := rr.(map[string]interface{})
		if cl, ok := m["classes"].([]interface{}); ok && !hasValue(cl, classNonLocal) {
			continue
		}
		routes = append(routes, m)
	}
	ex["routes"] = routes
	var apis []interface{}
	for _, a := range ex["apis"].([]interface{}) {
		switch a.(map[string]interface{})["id"] {
		case "tv3ws-bind-context-register", "tv3ws-bind-context-remove":
			continue
		}
		apis = append(apis, a)
	}
	ex["apis"] = apis
	return ex
}

func hasValue(l []interface{}, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func externalHandler(t *testing.T, mode string, st *fakeStore, next http.Handler) *authHandler {
	t.Helper()
	env := map[string]string{"JWT_SECRET": tSecret, "AUTH_ENFORCE": mode}
	cfg, err := loadConfig(externalExtra(), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(cfg, st, next)
	h.now = func() time.Time { return t0 }
	return h
}

func TestExternaTabelaDerivada(t *testing.T) {
	h := externalHandler(t, modeEnforce, baseStore(), nil)
	if h.cfg.Surface != "external" {
		t.Fatalf("surface %q", h.cfg.Surface)
	}
	got := map[string]bool{}
	for _, rt := range h.cfg.Routes.routes {
		got[rt.String()] = true
	}
	for _, out := range []string{"POST /tv3/bind-context", "DELETE /tv3/bind-context"} {
		if got[out] {
			t.Errorf("%s nao deveria estar na externa", out)
		}
	}
	for _, in := range []string{"GET /health", "GET /manifest", "GET /tv3/authorize", "GET /tv3/bind-context", "GET /tv3/api-info", "GET /tv3/current-service"} {
		if !got[in] {
			t.Errorf("%s deveria estar na externa", in)
		}
	}
	if n, all := len(h.cfg.Routes.routes), len(testExtra()["routes"].([]interface{})); n != all-2 {
		t.Errorf("externa com %d rotas, esperado %d", n, all-2)
	}
}

// A rota que so existe na interna da 100 na externa: nos dois modos (o 100
// nao eh decisao de credencial), para qualquer classe, sem Redis, sem
// roteador (edgeCall falha o teste se o next for chamado) e sem mexer nas
// chaves registradas. Na interna, a mesma requisicao do associado funciona.
func TestExternaRotaSoDaInternaDa100(t *testing.T) {
	assocTok := accessFor(t, "cli-3", classAssociated)
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	cases := []struct {
		name, method string
		hdr          map[string]string
		body         string
	}{
		{"POST pelo associado (Origin)", "POST", jsonHdr, regBody("HS256", "nova")},
		{"POST com token de associado", "POST", map[string]string{"Authorization": assocTok, "Content-Type": "application/json"}, regBody("HS256", "nova")},
		{"POST pelo nao local", "POST", map[string]string{"Authorization": nonLocal, "Content-Type": "application/json"}, regBody("HS256", "nova")},
		{"DELETE pelo associado (Origin)", "DELETE", map[string]string{"Origin": tAssoc, "key": "chave-webmedia"}, ""},
		{"DELETE sem nada", "DELETE", map[string]string{}, ""},
	}
	for _, mode := range []string{modeWarn, modeEnforce} {
		st := baseStore()
		h := externalHandler(t, mode, st, nil)
		for _, c := range cases {
			st.calls = 0
			r := edgeCall(t, h, c.method, "/tv3/bind-context", c.hdr, c.body)
			wantErr(t, mode+" "+c.name, r, 100)
			if st.calls != 0 {
				t.Errorf("%s %s: consultou o Redis %d vez(es)", mode, c.name, st.calls)
			}
			if w := r.hdr.Get(warnHeader); w != "" {
				t.Errorf("%s %s: %s=%q no 100", mode, c.name, warnHeader, w)
			}
			if d, _ := r.json["description"].(string); !strings.Contains(d, "API not found: "+c.method+" /tv3/bind-context") {
				t.Errorf("%s %s: description %q", mode, c.name, d)
			}
		}
		if l := st.keys[tSvc]; len(l) != 1 {
			t.Errorf("%s: chaves de %s mudaram na externa: %v", mode, tSvc, l)
		}
	}
	// a mesma requisicao na interna: o registro funciona
	st := edgeStore()
	wantOK(t, "interna: POST pelo associado", postBind(t, newTestHandler(t, modeEnforce, st, nil), regBody("HS256", "nova")), okRegister)
	if len(st.keys[tSvc]) != 1 {
		t.Errorf("interna: registro nao gravou: %v", st.keys[tSvc])
	}
}

// O GET /tv3/bind-context (autonomo e nao local) fica na externa. O caminho
// continua declarado nela, entao o OPTIONS que nao e preflight recebe os
// cabecalhos da C.4.1.9.3; o preflight a borda concede como em qualquer
// caminho (opcao b, Luis 12/10), inclusive o do POST, e a requisicao de
// verdade da 100.
func TestExternaMantemGetEOptions(t *testing.T) {
	st := baseStore()
	bind := signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0, t0, t0.Add(time.Hour)))
	r := edgeCall(t, externalHandler(t, modeEnforce, st, nil), "GET", "/tv3/bind-context",
		map[string]string{"Authorization": accessFor(t, "cli-2", classNonLocal), "bind-token": bind}, "")
	if r.status != 200 || !strings.HasPrefix(r.body, `{"boundServices":[`) {
		t.Errorf("GET /tv3/bind-context na externa: status %d %s", r.status, r.body)
	}

	h := externalHandler(t, modeEnforce, st, nil)
	o := edgeCall(t, h, "OPTIONS", "/tv3/bind-context", nil, "")
	if o.status != http.StatusOK || o.hdr.Get("Access-Control-Allow-Methods") != "*" {
		t.Errorf("OPTIONS sem preflight na externa: status %d, ACAM %q", o.status, o.hdr.Get("Access-Control-Allow-Methods"))
	}

	// preflight: desde 12/10 (opcao b do Luis) a propria borda responde, sem
	// passar ao modulo CORS do KrakenD, tambem para o POST que nao existe na
	// externa (o navegador le o 100 da requisicao em seguida)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })
	rec := serve(externalHandler(t, modeEnforce, st, next), "OPTIONS", "/tv3/bind-context",
		map[string]string{"Origin": "http://x", "Access-Control-Request-Method": "POST"})
	if called || rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Methods") != "*" {
		t.Errorf("preflight POST na externa: chegou ao roteador=%v, status %d, ACAM %q", called, rec.Code, rec.Header().Get("Access-Control-Allow-Methods"))
	}
}

// C.6.7.8/C.6.7.9 na externa: o catalogo eh o das rotas da superficie (ver
// o DECIDIDO (Luis, 11/10; F-2) no generate.js, pluginConfig).
func TestExternaAPIInfo(t *testing.T) {
	h := externalHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "externa: lista", apiGet(t, h, "/tv3/api-info", nil), `{"receiverApis":[`+
		`{"id":"tv3ws-application-authorization","version":"2.0"},`+
		`{"id":"tv3ws-current-service","version":"2.0"},`+
		`{"id":"tv3ws-api-info","version":"2.0"},`+
		`{"id":"tv3ws-bind-context-list","version":"2.0"}]}`)
	wantOK(t, "externa: bind-context-list", apiGet(t, h, "/tv3/api-info/tv3ws-bind-context-list", nil),
		`{"receiverApi":{"id":"tv3ws-bind-context-list","version":"2.0"}}`)
	wantErr(t, "externa: bind-context-register", apiGet(t, h, "/tv3/api-info/tv3ws-bind-context-register", nil), 101)
	wantErr(t, "externa: bind-context-remove", apiGet(t, h, "/tv3/api-info/tv3ws-bind-context-remove", nil), 101)
	// na interna as duas continuam
	hi := newTestHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "interna: bind-context-register", apiGet(t, hi, "/tv3/api-info/tv3ws-bind-context-register", nil),
		`{"receiverApi":{"id":"tv3ws-bind-context-register","version":"2.0"}}`)
}
