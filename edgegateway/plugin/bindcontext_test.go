package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests of the C.6.8 APIs answered by the edge, through the whole ServeHTTP.

const tSvcB = "urn:tv30:service:b"

var (
	rsa512Once sync.Once
	rsa512     *rsa.PrivateKey
)

// A 512-bit key, the size of the example of the standard ("MIIBOgIBAAJB..."),
// valid only for RS256.
func testRSA512(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rsa512Once.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 512)
		if err != nil {
			panic(err)
		}
		rsa512 = k
	})
	return rsa512
}

func pkcs1PrivB64(k *rsa.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(k))
}

func spkiB64(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// edgeStore: current service tSvc, no registered key.
func edgeStore() *fakeStore {
	st := baseStore()
	st.keys = map[string][]string{}
	st.svcHash = map[string]string{"serviceContextId": tSvc, "serviceName": "Canal A", "serviceId": "7"}
	return st
}

type edgeResp struct {
	status int
	hdr    http.Header
	body   string
	json   map[string]interface{}
}

// edgeCall makes the request through ServeHTTP. hdr may carry Content-Type;
// an empty body means no body. The router (next) must not be called.
func edgeCall(t *testing.T, h *authHandler, method, path string, hdr map[string]string, body string) edgeResp {
	t.Helper()
	h.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s chegou ao roteador do KrakenD", method, path)
		w.WriteHeader(599)
	})
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := edgeResp{status: rec.Code, hdr: rec.Header(), body: rec.Body.String()}
	json.Unmarshal(rec.Body.Bytes(), &out.json)
	return out
}

func wantErr(t *testing.T, name string, r edgeResp, code int) {
	t.Helper()
	if r.status != 404 {
		t.Errorf("%s: status %d, esperado 404 (corpo %s)", name, r.status, r.body)
		return
	}
	if got, _ := r.json["error"].(float64); int(got) != code {
		t.Errorf("%s: error %v, esperado %d (%s)", name, r.json["error"], code, r.body)
	}
	if _, ok := r.json["description"].(string); !ok {
		t.Errorf("%s: sem description (%s)", name, r.body)
	}
	if ct := r.hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q", name, ct)
	}
}

func wantOK(t *testing.T, name string, r edgeResp, body string) {
	t.Helper()
	if r.status != 200 || r.body != body {
		t.Errorf("%s: status %d corpo %s, esperado 200 %s", name, r.status, r.body, body)
	}
	for k, want := range map[string]string{"Content-Type": "application/json", "Access-Control-Allow-Origin": "*"} {
		if got := r.hdr.Values(k); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %s=%q, esperado um unico %q", name, k, got, want)
		}
	}
	if r.hdr.Get("API-Version") == "" || r.hdr.Get("Content-Length") == "" {
		t.Errorf("%s: sem API-Version ou Content-Length", name)
	}
}

var (
	assocHdr = map[string]string{"Origin": tAssoc}
	jsonHdr  = map[string]string{"Origin": tAssoc, "Content-Type": "application/json"}
)

func postBind(t *testing.T, h *authHandler, body string) edgeResp {
	return edgeCall(t, h, "POST", "/tv3/bind-context", jsonHdr, body)
}

func regBody(alg, key string) string {
	b, _ := json.Marshal(map[string]string{"alg": alg, "key": key})
	return string(b)
}

// The returned serviceContextId is the globalServiceId of the current
// service.
var okRegister = `{"serviceContextId":"` + tSvc + `"}`

// C.6.8.2 (Table C.49): 101 for a body out of format, an unsupported alg or
// an incompatible key; 105 for a missing alg or key in a JSON object.
// Nothing is written.
func TestRegistroCorpoInvalido(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER}))
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	cases := []struct {
		name string
		hdr  map[string]string
		body string
		code int
	}{
		{"objeto vazio", jsonHdr, `{}`, 105},
		{"sem key", jsonHdr, `{"alg":"HS256"}`, 105},
		{"sem alg", jsonHdr, `{"key":"k"}`, 105},
		{"corpo vazio com Content-Type JSON (vale {})", jsonHdr, "", 105},
		{"alg ES256", jsonHdr, `{"alg":"ES256","key":"x"}`, 101},
		{"alg none", jsonHdr, `{"alg":"none","key":"x"}`, 101},
		{"alg em minusculas", jsonHdr, `{"alg":"hs256","key":"x"}`, 101},
		{"alg null", jsonHdr, `{"alg":null,"key":"x"}`, 101},
		{"key numero", jsonHdr, `{"alg":"HS256","key":123}`, 101},
		{"segredo HS vazio", jsonHdr, `{"alg":"HS256","key":""}`, 101},
		{"RS256 com texto que nao eh chave", jsonHdr, `{"alg":"RS256","key":"segredo"}`, 101},
		{"RS512 com chave EC", jsonHdr, regBody("RS512", ecPEM), 101},
		{"RS512 com modulo de 512 bits", jsonHdr, regBody("RS512", pkcs1PrivB64(testRSA512(t))), 101},
		{"lista", jsonHdr, `[1]`, 101},
		{"texto", jsonHdr, `"texto"`, 101},
		{"numero", jsonHdr, `42`, 101},
		{"null", jsonHdr, `null`, 101},
		{"JSON quebrado", jsonHdr, `{"alg":`, 101},
		{"JSON com lixo depois", jsonHdr, `{"alg":"HS256","key":"k"} x`, 101},
		{"so espacos", jsonHdr, "   ", 101},
		{"sem Content-Type", assocHdr, regBody("HS256", "k"), 101},
		{"Content-Type text/plain", map[string]string{"Origin": tAssoc, "Content-Type": "text/plain"}, regBody("HS256", "k"), 101},
		{"form urlencoded", map[string]string{"Origin": tAssoc, "Content-Type": "application/x-www-form-urlencoded"}, "alg=HS256&key=k", 101},
		{"corpo acima de 100 KiB", jsonHdr, regBody("HS256", strings.Repeat("k", maxRegisterBody)), 101},
	}
	for _, c := range cases {
		wantErr(t, c.name, edgeCall(t, h, "POST", "/tv3/bind-context", c.hdr, c.body), c.code)
	}
	if len(st.keys) != 0 {
		t.Fatalf("corpo invalido gravou algo: %v", st.keys)
	}
	// Detail of the 105.
	r := postBind(t, h, `{}`)
	if d, _ := r.json["description"].(string); d != "Missing argument: alg, key" {
		t.Errorf("description do 105: %q", d)
	}
}

// The four algorithms with a compatible key; the stored element is
// {"alg","key","registeredAt"} (ms), the key unchanged.
func TestRegistroQuatroAlgoritmos(t *testing.T) {
	k2048 := testRSAKey(t)
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	ok := [][2]string{
		{"HS256", "segredo-256"},
		{"HS512", "segredo-512"},
		// A 512-bit private key, like the example of the standard.
		{"RS256", pkcs1PrivB64(testRSA512(t))},
		{"RS512", pubPEM(t, k2048)},
	}
	for _, c := range ok {
		wantOK(t, "POST "+c[0], postBind(t, h, regBody(c[0], c[1])), okRegister)
	}
	// application/json with a parameter is also JSON.
	r := edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Origin": tAssoc, "Content-Type": "application/json; charset=utf-8"}, regBody("HS256", "com-charset"))
	wantOK(t, "Content-Type com charset", r, okRegister)

	list := st.keys[tSvc]
	if len(list) != 5 {
		t.Fatalf("esperadas 5 entradas em bind-context:%s, vieram %d", tSvc, len(list))
	}
	for i, c := range ok {
		var e map[string]interface{}
		if err := json.Unmarshal([]byte(list[i]), &e); err != nil {
			t.Fatalf("entrada %d nao eh JSON: %s", i, list[i])
		}
		if e["alg"] != c[0] || e["key"] != c[1] || e["registeredAt"] != float64(t0.UnixMilli()) || len(e) != 3 {
			t.Errorf("entrada %d: %s", i, list[i])
		}
	}
	// The stored format is the one the bind-token check reads.
	if keys := h.keys.decodeStoredKeys(tSvc, list); len(keys) != 5 {
		t.Errorf("decodeStoredKeys leu %d de 5 entradas", len(keys))
	}
}

// Order: the body comes first (101, 105); a valid body without a current
// service gives 300.
func TestRegistroSemServicoCorrente(t *testing.T) {
	st := edgeStore()
	st.current = ""
	h := newTestHandler(t, modeEnforce, st, nil)
	wantErr(t, "sem servico", postBind(t, h, regBody("HS256", "segredo-a")), 300)
	wantErr(t, "corpo invalido sem servico", postBind(t, h, `{"alg":"HS256"}`), 105)
	if len(st.keys) != 0 {
		t.Fatalf("gravou sem servico corrente: %v", st.keys)
	}
}

// No duplicates: HS only with an identical secret; RSA by the same public
// key in any encoding; the same secret with another alg is another entry.
func TestRegistroSemDuplicata(t *testing.T) {
	k := testRSAKey(t)
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	for _, body := range []string{
		regBody("HS256", "segredo-a"),
		// Identical: not added.
		regBody("HS256", "segredo-a"),
		// Another secret (untrimmed bytes).
		regBody("HS256", "segredo-a "),
		// Another alg.
		regBody("HS512", "segredo-a"),
		regBody("RS256", pubPEM(t, k)),
		// The same public key in base64 of DER.
		regBody("RS256", spkiB64(t, k)),
		// The matching private key.
		regBody("RS256", base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(k))),
	} {
		wantOK(t, "POST", postBind(t, h, body), okRegister)
	}
	if n := len(st.keys[tSvc]); n != 4 {
		t.Fatalf("esperadas 4 entradas, vieram %d: %v", n, st.keys[tSvc])
	}
}

// Class (106) and version (100, 101) before the API logic; warn mode only
// warns.
func TestRegistroClasseEVersao(t *testing.T) {
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	wantErr(t, "nao associado", edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json"}, regBody("HS256", "s")), 106)
	auto := accessFor(t, "cli-1", classStandAlone)
	wantErr(t, "token autonomo", edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json", "Authorization": auto}, regBody("HS256", "s")), 106)
	// API-Version (C.3.6.6): on 100 (a version outside the set), the latest
	// supported; on 101 (malformed), 2.0.
	for _, c := range []struct {
		v    string
		code int
		api  string
	}{{"abc", 101, "2.0"}, {"", 101, "2.0"}, {"2", 101, "2.0"}, {"3.0", 100, "100.0"}, {"1.9", 100, "100.0"}, {"2.1", 100, "100.0"}, {"100.0", 100, "100.0"}} {
		hd := map[string]string{"Origin": tAssoc, "Content-Type": "application/json", "Accept-Version": c.v}
		r := edgeCall(t, h, "POST", "/tv3/bind-context", hd, regBody("HS256", "s"))
		wantErr(t, "Accept-Version "+c.v, r, c.code)
		if v := r.hdr.Get("API-Version"); v != c.api {
			t.Errorf("Accept-Version %q: API-Version %q, esperado %s", c.v, v, c.api)
		}
	}
	if len(st.keys) != 0 {
		t.Fatalf("gravou: %v", st.keys)
	}
	hd := map[string]string{"Origin": tAssoc, "Content-Type": "application/json", "Accept-Version": "2.0"}
	r := edgeCall(t, h, "POST", "/tv3/bind-context", hd, regBody("HS256", "s"))
	wantOK(t, "Accept-Version 2.0", r, okRegister)
	if v := r.hdr.Get("API-Version"); v != "2.0" {
		t.Errorf("API-Version %q, esperado 2.0", v)
	}
	if v := postBind(t, h, regBody("HS256", "s2")).hdr.Get("API-Version"); v != "2.0" {
		t.Errorf("sem Accept-Version: API-Version %q, esperado 2.0", v)
	}

	// 106 is sent in both modes: a client that is not associated does not
	// register a key, even in warn mode.
	st = edgeStore()
	h = newTestHandler(t, modeWarn, st, nil)
	r = edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json"}, regBody("HS256", "s"))
	wantErr(t, "warn nao associado", r, 106)
	if r.hdr.Get(warnHeader) != "" {
		t.Errorf("warn: %s=%q, esperado vazio (o 106 bloqueia)", warnHeader, r.hdr.Get(warnHeader))
	}
	if len(st.keys) != 0 {
		t.Fatalf("warn: o nao associado gravou chave: %v", st.keys)
	}
}

// --- C.6.8.3 (Table C.50) ---------------------------------------------------

func bindGet(t *testing.T, h *authHandler, bindToken string) edgeResp {
	hd := map[string]string{"Authorization": accessFor(t, "cli-1", classStandAlone)}
	if bindToken != "" {
		hd["bind-token"] = bindToken
	}
	return edgeCall(t, h, "GET", "/tv3/bind-context", hd, "")
}

func hsTok(t *testing.T, secret string, claims map[string]interface{}) string {
	if claims == nil {
		claims = timeClaims(t0.Add(-5*time.Second), t0.Add(-5*time.Second), t0.Add(10*time.Minute))
	}
	return signJWT(t, "HS256", []byte(secret), claims)
}

// Two broadcasters: a (HS256 segredo-a) and b (RS256, the current one).
func twoServices(t *testing.T) *fakeStore {
	st := edgeStore()
	st.current = tSvcB
	st.svcHash = map[string]string{"serviceContextId": tSvcB, "serviceName": "Canal B", "serviceId": "9"}
	st.keys[tSvc] = []string{storedJSON("HS256", "segredo-a")}
	st.keys[tSvcB] = []string{storedJSON("RS256", pkcs1PrivB64(testRSA512(t)))}
	return st
}

func TestBindListErros(t *testing.T) {
	h := newTestHandler(t, modeEnforce, twoServices(t), nil)
	future := t0.Add(10 * time.Minute)
	wantErr(t, "sem bind-token", bindGet(t, h, ""), 104)
	wantErr(t, "bind-token so espacos", bindGet(t, h, "   "), 104)
	wantErr(t, "nao-JWT", bindGet(t, h, "nao-e-jwt"), 108)
	wantErr(t, "cabecalho sem alg", bindGet(t, h, b64([]byte(`{"typ":"JWT"}`))+".e30.x"), 108)
	wantErr(t, "assinatura sem chave", bindGet(t, h, hsTok(t, "segredo-errado", nil)), 101)
	wantErr(t, "alg none", bindGet(t, h, signJWT(t, "none", nil, timeClaims(t0, t0, future))), 101)
	wantErr(t, "alg fora dos quatro", bindGet(t, h, signJWTHeader(t, map[string]interface{}{"alg": "ES256"}, "none", nil, timeClaims(t0, t0, future))), 101)
	exp := timeClaims(t0.Add(-time.Hour), t0.Add(-time.Hour), t0.Add(-time.Second))
	wantErr(t, "expirado", bindGet(t, h, hsTok(t, "segredo-a", exp)), 108)
	wantErr(t, "expirado e chave errada: assinatura antes do prazo", bindGet(t, h, hsTok(t, "outra", exp)), 101)
	wantErr(t, "nbf no futuro", bindGet(t, h, hsTok(t, "segredo-a", timeClaims(t0, future, future.Add(time.Hour)))), 108)
	wantErr(t, "iat no futuro", bindGet(t, h, hsTok(t, "segredo-a", timeClaims(future, t0, future.Add(time.Hour)))), 108)
	// Algorithm confusion: HS256 signed with the RSA public key as the secret.
	st := edgeStore()
	k := testRSAKey(t)
	st.keys[tSvc] = []string{storedJSON("RS256", pubPEM(t, k))}
	h = newTestHandler(t, modeEnforce, st, nil)
	wantErr(t, "confusao de algoritmo", bindGet(t, h, hsTok(t, pubPEM(t, k), nil)), 101)
}

func TestBindListServicos(t *testing.T) {
	st := twoServices(t)
	h := newTestHandler(t, modeEnforce, st, nil)
	rsTok := signJWT(t, "RS256", testRSA512(t), timeClaims(t0.Add(-5*time.Second), t0.Add(-5*time.Second), t0.Add(10*time.Minute)))
	// Current = b: name and id (serviceId back to an integer, Table C.8).
	wantOK(t, "token de b", bindGet(t, h, rsTok), `{"boundServices":[{"serviceContextId":"`+tSvcB+`","serviceName":"Canal B","serviceId":9}]}`)
	// a is not the current one: only its serviceContextId, its globalServiceId.
	wantOK(t, "token de a", bindGet(t, h, hsTok(t, "segredo-a", nil)), `{"boundServices":[{"serviceContextId":"`+tSvc+`"}]}`)

	// A non-numeric serviceId and an empty name are omitted.
	st.svcHash = map[string]string{"serviceName": "", "serviceId": "9a"}
	wantOK(t, "sem nome nem id numerico", bindGet(t, h, rsTok), `{"boundServices":[{"serviceContextId":"`+tSvcB+`"}]}`)

	// The same secret in both broadcasters: two items; an unreadable list
	// (WRONGTYPE) and a malformed entry are skipped.
	st = twoServices(t)
	st.keys[tSvcB] = append(st.keys[tSvcB], "nao-json", storedJSON("HS256", "segredo-a"))
	st.wrongType = map[string]bool{"urn:tv30:service:string": true}
	h = newTestHandler(t, modeEnforce, st, nil)
	// BindServices of fakeStore is ordered: ...:b before ...:webmedia.
	wantOK(t, "dois servicos", bindGet(t, h, hsTok(t, "segredo-a", nil)),
		`{"boundServices":[{"serviceContextId":"`+tSvcB+`","serviceName":"Canal B","serviceId":9},{"serviceContextId":"`+tSvc+`"}]}`)
}

// Route policy: access token (107) and class (106) before the API.
func TestBindListPolitica(t *testing.T) {
	h := newTestHandler(t, modeEnforce, twoServices(t), nil)
	tok := hsTok(t, "segredo-a", nil)
	wantErr(t, "sem access token", edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"bind-token": tok}, ""), 107)
	wantErr(t, "associado", edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"Origin": tAssoc, "bind-token": tok}, ""), 106)
	auto := accessFor(t, "cli-1", classStandAlone)
	r := edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"Authorization": auto, "bind-token": tok}, "")
	if r.status != 200 {
		t.Errorf("autonomo: status %d %s", r.status, r.body)
	}
	// Non-local: 106 on the HTTP surface; on the external surface, it passes.
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	wantErr(t, "nao local na interna", edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"Authorization": nonLocal, "bind-token": tok}, ""), 106)
	r = edgeCall(t, externalHandler(t, modeEnforce, twoServices(t), nil), "GET", "/tv3/bind-context", map[string]string{"Authorization": nonLocal, "bind-token": tok}, "")
	if r.status != 200 {
		t.Errorf("nao local na externa: status %d %s", r.status, r.body)
	}
}

// --- C.6.8.4 (Table C.51) ---------------------------------------------------

func bindDelete(t *testing.T, h *authHandler, key string) edgeResp {
	hd := map[string]string{"Origin": tAssoc}
	if key != "" {
		hd["key"] = key
	}
	return edgeCall(t, h, "DELETE", "/tv3/bind-context", hd, "")
}

func TestBindRemove(t *testing.T) {
	st := twoServices(t)
	h := newTestHandler(t, modeEnforce, st, nil)
	rsTok := signJWT(t, "RS256", testRSA512(t), timeClaims(t0, t0, t0.Add(time.Hour)))

	wantErr(t, "sem key", bindDelete(t, h, ""), 105)
	wantErr(t, "key so espacos", bindDelete(t, h, "  "), 105)
	// Registered as a PKCS#1 private key; revoked by the SPKI public key.
	wantOK(t, "revoga RSA pela publica", bindDelete(t, h, spkiB64(t, testRSA512(t))), `{}`)
	if _, ok := st.keys[tSvcB]; ok {
		t.Fatalf("bind-context:%s deveria ter sumido: %v", tSvcB, st.keys[tSvcB])
	}
	wantErr(t, "GET com o token da chave revogada", bindGet(t, h, rsTok), 101)
	wantOK(t, "chave inexistente", bindDelete(t, h, "nunca-registrada"), `{}`)

	// Only the current service: the key of a is still valid.
	if r := bindGet(t, h, hsTok(t, "segredo-a", nil)); r.status != 200 {
		t.Fatalf("token de a: %d %s", r.status, r.body)
	}
	// Current = b.
	bindDelete(t, h, "segredo-a")
	if len(st.keys[tSvc]) != 1 {
		t.Fatalf("revogacao mexeu em outro servico: %v", st.keys[tSvc])
	}

	// Without a current service: success, and nothing changes.
	st.current = ""
	wantOK(t, "sem servico corrente", bindDelete(t, h, "segredo-a"), `{}`)
	if len(st.keys[tSvc]) != 1 {
		t.Fatalf("revogacao sem servico corrente removeu: %v", st.keys[tSvc])
	}

	// Revoking "s" also removes the secret registered as "s " (the header is
	// trimmed), and the duplicates.
	st.current = tSvc
	st.keys[tSvc] = []string{storedJSON("HS256", "s "), storedJSON("HS512", "s"), storedJSON("HS512", "s"), storedJSON("HS256", "outra")}
	wantOK(t, "revoga por texto aparado", bindDelete(t, h, "s"), `{}`)
	if l := st.keys[tSvc]; len(l) != 1 || !strings.Contains(l[0], `"outra"`) {
		t.Fatalf("sobrou %v", l)
	}

	// Class: only the associated client.
	wantErr(t, "nao associado", edgeCall(t, h, "DELETE", "/tv3/bind-context", map[string]string{"key": "outra"}, ""), 106)
}

// Redis down: 200 on the three routes (in both modes), without reaching the
// router.
func TestBindRedisFora(t *testing.T) {
	for _, mode := range []string{modeEnforce, modeWarn} {
		st := twoServices(t)
		h := newTestHandler(t, mode, st, nil)
		tok := hsTok(t, "segredo-a", nil)
		st.err = errorString("dial tcp: connection refused")
		// The associated client recognized by the Origin also looks up Redis.
		wantErr(t, mode+" POST", postBind(t, h, regBody("HS256", "s")), 200)
		wantErr(t, mode+" GET", bindGet(t, h, tok), 200)
		wantErr(t, mode+" DELETE", bindDelete(t, h, "s"), 200)
	}
}

// The Redis function refuses the entry (schema): 404 {error:200} with the
// reason in the description (not "redis indisponivel"), in both modes and on
// both writes.
func TestBindRecusaDoEsquema(t *testing.T) {
	msg := `TV30ESQUEMA tv30_bind_registra: bind-context:` + tSvc + ` entrada: membro "alg": deve ser um de HS256, HS512, RS256, RS512 (veio "none")`
	for _, mode := range []string{modeEnforce, modeWarn} {
		st := twoServices(t)
		st.current = tSvc
		st.writeErr = redisError(msg)
		h := newTestHandler(t, mode, st, nil)
		for _, c := range []struct {
			nome string
			r    edgeResp
		}{{"POST", postBind(t, h, regBody("HS256", "nova"))}, {"DELETE", bindDelete(t, h, "segredo-a")}} {
			wantErr(t, mode+" "+c.nome, c.r, 200)
			d, _ := c.r.json["description"].(string)
			if !strings.Contains(d, "escrita recusada pelo esquema do Redis ("+msg+")") || strings.Contains(d, "indisponivel") {
				t.Errorf("%s %s: description %q", mode, c.nome, d)
			}
		}
		if got := st.keys[tSvc]; len(got) != 1 {
			t.Errorf("%s: lista mudou com a escrita recusada: %q", mode, got)
		}
	}
	if _, ok := schemaRefusal(errorString("dial tcp: connection refused")); ok {
		t.Error("erro de rede nao e recusa do esquema")
	}
	if _, ok := schemaRefusal(redisError("WRONGTYPE Operation against a key holding the wrong kind of value")); ok {
		t.Error("WRONGTYPE nao e recusa do esquema")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// panicStore: Redis "explodes" in the middle of the API; the result is 200,
// not a closed connection.
type panicStore struct{ *fakeStore }

func (p panicStore) CurrentServiceID() (string, error) { panic("falha inesperada") }

func TestEdgePanicVira200(t *testing.T) {
	h := newTestHandler(t, modeEnforce, nil, nil)
	h.store = panicStore{edgeStore()}
	wantErr(t, "panic na API", postBind(t, h, regBody("HS256", "s")), 200)
}

func TestSameKey(t *testing.T) {
	k := testRSAKey(t)
	small := testRSA512(t)
	if !sameKey(pubPEM(t, k), spkiB64(t, k)) {
		t.Error("PEM e base64 da mesma publica")
	}
	if !sameKey(pkcs1PrivB64(small), spkiB64(t, small)) {
		t.Error("privada PKCS#1 e a publica derivada")
	}
	if sameKey(spkiB64(t, small), spkiB64(t, k)) {
		t.Error("chaves RSA diferentes")
	}
	if !sameKey("segredo", " segredo ") || sameKey("segredo", "segredo2") {
		t.Error("HS por texto aparado")
	}
	if sameRegisteredKey("HS256", "segredo", "segredo ") || !sameRegisteredKey("HS512", "s", "s") {
		t.Error("registro HS: so identico")
	}
	if !sameRegisteredKey("RS256", pubPEM(t, k), spkiB64(t, k)) {
		t.Error("registro RS: mesma publica")
	}
}

func TestParseStoredEntry(t *testing.T) {
	if e, ok := parseStoredEntry(`{"alg":"RS256","key":"k","registeredAt":5}`); !ok || e.Alg != "RS256" || e.Key != "k" {
		t.Errorf("entrada valida: %+v %v", e, ok)
	}
	for _, bad := range []string{`{"alg":"ES256","key":"k"}`, `{"alg":"HS256"}`, `{"alg":"HS256","key":1}`, `nao-json`, `[]`} {
		if _, ok := parseStoredEntry(bad); ok {
			t.Errorf("%s deveria ser malformada", bad)
		}
	}
}

// End to end with the real RESP client (fake server in memory): POST, list
// in Redis, the bind-token validates the token+bind route and the GET,
// DELETE removes the list.
func TestC68ComClienteRedis(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() {
		f.strs[keyCurrentService] = tSvc
		f.hashes[keyAssociated] = map[string]string{tAssoc: tSvc}
		f.hashes[keyCurrentSvcHash] = map[string]string{"serviceName": "Canal A", "serviceId": "7"}
		// The clients of the test tokens are in clients:authorized.
		f.sets[keyAuthorized] = map[string]bool{"cli-1": true, "cli-2": true, "cli-3": true}
	})
	h := newTestHandler(t, modeEnforce, nil, nil)
	h.store = &redisStore{c: newRedisClient(f.ln.Addr().String(), time.Second)}

	wantOK(t, "POST", postBind(t, h, regBody("HS512", "segredo-c68")), okRegister)
	wantOK(t, "POST repetido", postBind(t, h, regBody("HS512", "segredo-c68")), okRegister)
	var stored []string
	f.with(func() { stored = append(stored, f.lists[keyBindPrefix+tSvc]...) })
	if len(stored) != 1 || !strings.HasPrefix(stored[0], `{"alg":"HS512","key":"segredo-c68","registeredAt":`) {
		t.Fatalf("bind-context:%s = %q", tSvc, stored)
	}

	bt := signJWT(t, "HS512", []byte("segredo-c68"), timeClaims(t0, t0, t0.Add(time.Hour)))
	req := httptest.NewRequest("GET", "/tv3/current-service/users/current-user", nil)
	req.Header.Set("Authorization", accessFor(t, "cli-1", classStandAlone))
	req.Header.Set("bind-token", bt)
	if v := h.evaluate(req); v.Code != 0 {
		t.Fatalf("rota token+bind com o bind-token registrado: %d %s", v.Code, v.Detail)
	}
	wantOK(t, "GET", bindGet(t, h, bt), `{"boundServices":[{"serviceContextId":"`+tSvc+`","serviceName":"Canal A","serviceId":7}]}`)

	wantOK(t, "DELETE", bindDelete(t, h, "segredo-c68"), `{}`)
	f.with(func() {
		if _, ok := f.lists[keyBindPrefix+tSvc]; ok {
			t.Errorf("lista deveria ter sumido: %q", f.lists[keyBindPrefix+tSvc])
		}
	})
	wantErr(t, "GET depois da revogacao", bindGet(t, h, bt), 101)
}
