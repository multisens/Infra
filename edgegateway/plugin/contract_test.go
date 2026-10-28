package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Contract between redisStore and the real Redis functions. Runs only with
// TV30_REDIS_TEST=host:port of a Redis with the tv30 library loaded;
// skipped without it.
func TestContractFunctions(t *testing.T) {
	addr := os.Getenv("TV30_REDIS_TEST")
	if addr == "" {
		t.Skip("sem TV30_REDIS_TEST (rode bash infra/redis/test/run.sh contract)")
	}
	s := &redisStore{c: newRedisClient(addr, 2*time.Second)}
	const sid = "urn:tv30:test:contrato-go"

	// The entry bindRegister writes (encodeJSON of storedEntry), with a
	// multi-line PEM.
	entry, err := encodeJSON(storedEntry{Alg: "RS256", Key: "-----BEGIN PUBLIC KEY-----\nMFww\n-----END PUBLIC KEY-----", RegisteredAt: 1760000000000})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddBindKey(sid, string(entry)); err != nil {
		t.Fatalf("entrada da borda recusada pelo esquema: %v", err)
	}
	for _, bad := range []string{`{"alg":"none","key":"k","registeredAt":1}`, `nao-json`, `{"alg":"HS256","key":"","registeredAt":1}`, `{"alg":"HS256","key":"k"}`} {
		msg, ok := schemaRefusal(s.AddBindKey(sid, bad))
		if !ok || !strings.HasPrefix(msg, "TV30SCHEMA tv30_bind_register: bind-context:"+sid+" entry: ") {
			t.Errorf("entrada %s: esperada recusa do esquema, veio %q", bad, msg)
		}
	}
	if _, ok := schemaRefusal(s.AddBindKey("", string(entry))); !ok {
		t.Error("serviceId vazio deveria ser recusado")
	}
	if l, err := s.BindKeys(sid); err != nil || len(l) != 1 || l[0] != string(entry) {
		t.Fatalf("depois das recusas: %q %v", l, err)
	}
	if n, err := s.RemoveBindKey(sid, string(entry)); err != nil || n != 1 {
		t.Fatalf("remocao: %d %v", n, err)
	}
	if n, err := s.RemoveBindKey(sid, string(entry)); err != nil || n != 0 {
		t.Fatalf("remocao repetida: %d %v", n, err)
	}

	// clients:authorized and clients:blocked as written by the functions
	// (authorize, block, add back on boot).
	const cid = "contrato-go-cliente"
	must0 := func(nome string, args ...string) {
		t.Helper()
		if _, err := s.c.fcall(true, nome, args...); err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
	}
	authz := func() (bool, bool) {
		t.Helper()
		a, err1 := s.IsAuthorized(cid)
		b, err2 := s.IsBlocked(cid)
		if err1 != nil || err2 != nil {
			t.Fatalf("SISMEMBER: %v %v", err1, err2)
		}
		return a, b
	}
	if a, b := authz(); a || b {
		t.Fatalf("antes: autorizado=%v bloqueado=%v", a, b)
	}
	must0("tv30_client_authorize", cid, "class", "standAlone", "refreshToken", "rt")
	if a, b := authz(); !a || b {
		t.Fatalf("autorizado: autorizado=%v bloqueado=%v", a, b)
	}
	must0("tv30_client_block", cid)
	if a, b := authz(); a || !b {
		t.Fatalf("bloqueado: autorizado=%v bloqueado=%v", a, b)
	}
	// Manual unblock.
	if _, err := s.c.do("SREM", keyBlocked, cid); err != nil {
		t.Fatal(err)
	}
	if a, b := authz(); a || b {
		t.Fatalf("desbloqueado sem nova autorizacao: autorizado=%v bloqueado=%v", a, b)
	}
	must0("tv30_client_add_authorized", cid)
	if a, b := authz(); !a || b {
		t.Fatalf("incluido de novo: autorizado=%v bloqueado=%v", a, b)
	}
	defer func() {
		s.c.do("SREM", keyAuthorized, cid)
		s.c.do("DEL", "client:"+cid)
	}()

	// C.6.8 API on the real Redis: current service and associated origin
	// written by the functions.
	must := func(nome string, args ...string) {
		t.Helper()
		if _, err := s.c.fcall(true, nome, args...); err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
	}
	must("tv30_session_service_id", sid)
	must("tv30_origin_register", tAssoc, sid)
	// The GET of C.6.8.3 requires an access token: its client (cli-1, bindGet)
	// must be in clients:authorized.
	must("tv30_client_authorize", "cli-1", "class", "standAlone", "refreshToken", "rt")
	defer func() {
		s.c.fcall(true, "tv30_origin_remove", tAssoc)
		s.c.fcall(true, "tv30_session_service_id", "")
		s.c.do("SREM", keyAuthorized, "cli-1")
		s.c.do("DEL", "client:cli-1")
	}()
	h := newTestHandler(t, modeEnforce, nil, nil)
	h.store = s
	// The current globalServiceId.
	okReg := `{"serviceContextId":"` + sid + `"}`
	wantOK(t, "POST", postBind(t, h, regBody("HS512", "segredo-contrato")), okReg)
	wantOK(t, "POST repetido", postBind(t, h, regBody("HS512", "segredo-contrato")), okReg)
	if l, _ := s.BindKeys(sid); len(l) != 1 || !strings.HasPrefix(l[0], `{"alg":"HS512","key":"segredo-contrato","registeredAt":`) {
		t.Fatalf("bind-context:%s = %q", sid, l)
	}
	bt := signJWT(t, "HS512", []byte("segredo-contrato"), timeClaims(t0, t0, t0.Add(time.Hour)))
	r := bindGet(t, h, bt)
	if r.status != 200 || !strings.Contains(r.body, `"boundServices":[{"serviceContextId":"`+sid+`"`) {
		t.Fatalf("GET bind-context: %d %s", r.status, r.body)
	}
	wantOK(t, "DELETE", bindDelete(t, h, "segredo-contrato"), `{}`)
	if l, _ := s.BindKeys(sid); len(l) != 0 {
		t.Fatalf("lista depois do DELETE: %q", l)
	}

	// 304 on services:announced written by its function.
	const fora = "urn:tv30:test:contrato-go-fora"
	must("tv30_announced_services", sid, "urn:tv30:test:contrato-go-outro")
	defer s.c.fcall(true, "tv30_announced_services")
	if in, err := s.ServiceInCatalog(sid); err != nil || !in {
		t.Fatalf("servico anunciado: %v %v", in, err)
	}
	if in, err := s.ServiceInCatalog(fora); err != nil || in {
		t.Fatalf("servico fora: %v %v", in, err)
	}
	cred := map[string]string{"Authorization": accessFor(t, "cli-1", classStandAlone), "bind-token": "abc"}
	checkC32(t, serve(h, "GET", "/tv3/"+fora+"/users/u1", cred), 304)
	if v := h.evaluate(withHeaders(httptest.NewRequest("GET", "/tv3/"+sid+"/users/u1", nil), cred)); v.Code != 108 {
		t.Fatalf("servico do catalogo segue para o bind-token: %d %q", v.Code, v.Detail)
	}
	t.Logf("contrato: entrada da borda aceita, %d entradas invalidas recusadas, API C.6.8 completa sobre as funcoes, 304 sobre services:announced", 5)
}
