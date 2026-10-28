package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Behavior of the plugin with the table of the external surface.

// externalExtra: testExtra() as for the external surface: the same routes
// and the same catalog of APIs, with the 106 of C.4.1.8 on the routes whose
// "classes" list does not include non-local and on /tv3/authorize, and
// without the marks of the HTTP surface.
func externalExtra() map[string]interface{} {
	ex := testExtra()
	ex["surface"] = "external"
	delete(ex, "nonlocal_https_only")
	for _, rr := range ex["routes"].([]interface{}) {
		m := rr.(map[string]interface{})
		delete(m, "nonlocal_http")
		cl, ok := m["classes"].([]interface{})
		if (ok && !hasValue(cl, classNonLocal)) || m["path"] == "/tv3/authorize" {
			m["surface_error"] = float64(106)
		}
	}
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

func TestExternaTodasAsRotas(t *testing.T) {
	h := externalHandler(t, modeEnforce, baseStore(), nil)
	if h.cfg.Surface != "external" || h.cfg.NonLocalHTTPSOnly {
		t.Fatalf("surface %q, nonlocal_https_only %v", h.cfg.Surface, h.cfg.NonLocalHTTPSOnly)
	}
	if n, all := len(h.cfg.Routes.routes), len(testExtra()["routes"].([]interface{})); n != all {
		t.Errorf("externa com %d rotas, esperado %d (todas)", n, all)
	}
	want106 := map[string]bool{"GET /tv3/authorize": true, "POST /tv3/bind-context": true, "DELETE /tv3/bind-context": true,
		"GET /tv3/remote-device/devices/{classId}": true, "GET /tv3/notifications/{className}": true}
	for _, rt := range h.cfg.Routes.routes {
		if got := rt.SurfaceError == 106; got != want106[rt.String()] {
			t.Errorf("%s: surface_error 106 = %v, esperado %v", rt, got, want106[rt.String()])
		}
	}
}

// The route for the exclusive use of the local client and C.6.1.2 give 106
// on the external surface to any request: in both modes, for any class,
// without Redis, without the router (edgeCall fails the test if next is
// called) and without changing the registered keys. On the internal
// surface, the same request of the associated client works.
func TestExternaRotaExclusivaDoLocalDa106(t *testing.T) {
	assocTok := accessFor(t, "cli-3", classAssociated)
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	cases := []struct {
		name, method, path string
		hdr                map[string]string
		body               string
	}{
		{"POST pelo associado (Origin)", "POST", "/tv3/bind-context", jsonHdr, regBody("HS256", "nova")},
		{"POST com token de associado", "POST", "/tv3/bind-context", map[string]string{"Authorization": assocTok, "Content-Type": "application/json"}, regBody("HS256", "nova")},
		{"POST pelo nao local", "POST", "/tv3/bind-context", map[string]string{"Authorization": nonLocal, "Content-Type": "application/json"}, regBody("HS256", "nova")},
		{"DELETE pelo associado (Origin)", "DELETE", "/tv3/bind-context", map[string]string{"Origin": tAssoc, "key": "chave-webmedia"}, ""},
		{"DELETE sem nada", "DELETE", "/tv3/bind-context", map[string]string{}, ""},
		{"/tv3/authorize sem nada", "GET", "/tv3/authorize", map[string]string{}, ""},
		{"/tv3/authorize pelo nao local", "GET", "/tv3/authorize", map[string]string{"Authorization": nonLocal}, ""},
	}
	for _, mode := range []string{modeWarn, modeEnforce} {
		st := baseStore()
		h := externalHandler(t, mode, st, nil)
		for _, c := range cases {
			st.calls = 0
			r := edgeCall(t, h, c.method, c.path, c.hdr, c.body)
			wantErr(t, mode+" "+c.name, r, 106)
			if st.calls != 0 {
				t.Errorf("%s %s: consultou o Redis %d vez(es)", mode, c.name, st.calls)
			}
			if w := r.hdr.Get(warnHeader); w != "" {
				t.Errorf("%s %s: %s=%q no 106", mode, c.name, warnHeader, w)
			}
			if d, _ := r.json["description"].(string); !strings.Contains(d, "C.4.1.8") {
				t.Errorf("%s %s: description %q", mode, c.name, d)
			}
		}
		if l := st.keys[tSvc]; len(l) != 1 {
			t.Errorf("%s: chaves de %s mudaram na externa: %v", mode, tSvc, l)
		}
	}
	// The same request on the internal surface: the registration works.
	st := edgeStore()
	wantOK(t, "interna: POST pelo associado", postBind(t, newTestHandler(t, modeEnforce, st, nil), regBody("HS256", "nova")), okRegister)
	if len(st.keys[tSvc]) != 1 {
		t.Errorf("interna: registro nao gravou: %v", st.keys[tSvc])
	}
}

// On the external surface a non-local client uses the APIs (the 106 of
// C.4.1.6 is of the HTTP surface). The POST path is still declared: the
// OPTIONS that is not a preflight gets the headers of C.4.1.9.3, the
// preflight is granted as on any path, and the actual request gets 106.
func TestExternaMantemGetEOptions(t *testing.T) {
	st := baseStore()
	bind := signJWT(t, "HS256", []byte("chave-webmedia"), timeClaims(t0, t0, t0.Add(time.Hour)))
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	r := edgeCall(t, externalHandler(t, modeEnforce, st, nil), "GET", "/tv3/bind-context",
		map[string]string{"Authorization": nonLocal, "bind-token": bind}, "")
	if r.status != 200 || !strings.HasPrefix(r.body, `{"boundServices":[`) {
		t.Errorf("GET /tv3/bind-context na externa: status %d %s", r.status, r.body)
	}
	req, _ := http.NewRequest("GET", "/tv3/current-service", nil)
	req.Header.Set("Authorization", nonLocal)
	if v := externalHandler(t, modeEnforce, st, nil).evaluate(req); v.Code != 0 {
		t.Errorf("nao local na externa: codigo %d (%s)", v.Code, v.Detail)
	}

	h := externalHandler(t, modeEnforce, st, nil)
	o := edgeCall(t, h, "OPTIONS", "/tv3/bind-context", nil, "")
	if o.status != http.StatusOK || o.hdr.Get("Access-Control-Allow-Methods") != "*" {
		t.Errorf("OPTIONS sem preflight na externa: status %d, ACAM %q", o.status, o.hdr.Get("Access-Control-Allow-Methods"))
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })
	rec := serve(externalHandler(t, modeEnforce, st, next), "OPTIONS", "/tv3/bind-context",
		map[string]string{"Origin": "http://x", "Access-Control-Request-Method": "POST"})
	if called || rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Methods") != "*" {
		t.Errorf("preflight POST na externa: chegou ao roteador=%v, status %d, ACAM %q", called, rec.Code, rec.Header().Get("Access-Control-Allow-Methods"))
	}
}

// C.6.7.9 lists every implemented API on both surfaces, and C.6.7.8 answers
// for every one.
func TestExternaAPIInfo(t *testing.T) {
	h := externalHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "externa: lista", apiGet(t, h, "/tv3/api-info", nil), allAPIs)
	for _, id := range []string{"tv3ws-bind-context-register", "tv3ws-bind-context-remove", "tv3ws-bind-context-list"} {
		wantOK(t, "externa: "+id, apiGet(t, h, "/tv3/api-info/"+id, nil),
			`{"receiverApi":{"id":"`+id+`","version":"2.0"}}`)
	}
	hi := newTestHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "interna: lista", apiGet(t, hi, "/tv3/api-info", nil), allAPIs)
}
