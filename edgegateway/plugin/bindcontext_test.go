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

// Testes das APIs C.6.8 respondidas pela borda. Os casos vem dos testes do
// tv3ws que implementava a API antes da reuniao de 05/10
// (test/broadcaster-security.test.ts e a parte de registro de
// test/bind-token.test.ts), passando pelo ServeHTTP inteiro do plugin.

const tSvcB = "urn:tv30:service:b"

var (
	rsa512Once sync.Once
	rsa512     *rsa.PrivateKey
)

// chave de 512 bits: o tamanho do exemplo da norma ("MIIBOgIBAAJB...");
// so serve para RS256.
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

// edgeStore: servico corrente tSvc, nenhuma chave registrada.
func edgeStore() *fakeStore {
	st := baseStore()
	st.keys = map[string][]string{}
	st.svcHash = map[string]string{"serviceContextId": tSCID, "serviceName": "Canal A", "serviceId": "7"}
	return st
}

type edgeResp struct {
	status int
	hdr    http.Header
	body   string
	json   map[string]interface{}
}

// edgeCall faz a requisicao pelo ServeHTTP. hdr pode trazer Content-Type;
// corpo "" = sem corpo. O roteador (next) nao pode ser chamado.
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

var okRegister = `{"serviceContextId":"` + tSCID + `"}`

// C.6.8.2 (Tabela C.47): 101 corpo fora do formato / alg nao suportado /
// chave incompativel; 105 falta alg ou key num objeto JSON. Nada eh gravado.
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
	// detalhe do 105, como no tv3ws
	r := postBind(t, h, `{}`)
	if d, _ := r.json["description"].(string); d != "Missing argument: alg, key" {
		t.Errorf("description do 105: %q", d)
	}
}

// Os quatro algoritmos com chave compativel; o elemento gravado eh
// {"alg","key","registeredAt"} (ms), a key sem transformacao.
func TestRegistroQuatroAlgoritmos(t *testing.T) {
	k2048 := testRSAKey(t)
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	ok := [][2]string{
		{"HS256", "segredo-256"},
		{"HS512", "segredo-512"},
		{"RS256", pkcs1PrivB64(testRSA512(t))}, // privada de 512 bits, como o exemplo da norma
		{"RS512", pubPEM(t, k2048)},
	}
	for _, c := range ok {
		wantOK(t, "POST "+c[0], postBind(t, h, regBody(c[0], c[1])), okRegister)
	}
	// application/json com parametro tambem eh JSON
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
	// o formato gravado eh o que a validacao do bind-token le
	if keys := h.keys.decodeStoredKeys(tSvc, list); len(keys) != 5 {
		t.Errorf("decodeStoredKeys leu %d de 5 entradas", len(keys))
	}
}

// Ordem: o corpo vem antes (101/105); corpo valido sem servico corrente => 300.
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

// Sem duplicata: HS so com segredo identico; RSA pela mesma chave publica em
// qualquer codificacao; mesmo segredo com outro alg eh outra entrada.
func TestRegistroSemDuplicata(t *testing.T) {
	k := testRSAKey(t)
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	for _, body := range []string{
		regBody("HS256", "segredo-a"),
		regBody("HS256", "segredo-a"),  // identico: nao entra
		regBody("HS256", "segredo-a "), // outro segredo (bytes sem aparar)
		regBody("HS512", "segredo-a"),  // outro alg
		regBody("RS256", pubPEM(t, k)),
		regBody("RS256", spkiB64(t, k)), // mesma publica em base64 de DER
		regBody("RS256", base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(k))), // a privada correspondente
	} {
		wantOK(t, "POST", postBind(t, h, body), okRegister)
	}
	if n := len(st.keys[tSvc]); n != 4 {
		t.Fatalf("esperadas 4 entradas, vieram %d: %v", n, st.keys[tSvc])
	}
}

// Classe (106) e versao (100/101) antes da logica da API; warn so avisa.
func TestRegistroClasseEVersao(t *testing.T) {
	st := edgeStore()
	h := newTestHandler(t, modeEnforce, st, nil)
	wantErr(t, "nao associado", edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json"}, regBody("HS256", "s")), 106)
	auto := accessFor(t, "cli-1", classAutonomous)
	wantErr(t, "token autonomo", edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json", "Authorization": auto}, regBody("HS256", "s")), 106)
	for _, c := range []struct {
		v    string
		code int
	}{{"abc", 101}, {"", 101}, {"2", 101}, {"3.0", 100}, {"1.9", 100}} {
		hd := map[string]string{"Origin": tAssoc, "Content-Type": "application/json", "Accept-Version": c.v}
		wantErr(t, "Accept-Version "+c.v, edgeCall(t, h, "POST", "/tv3/bind-context", hd, regBody("HS256", "s")), c.code)
	}
	if len(st.keys) != 0 {
		t.Fatalf("gravou: %v", st.keys)
	}
	hd := map[string]string{"Origin": tAssoc, "Content-Type": "application/json", "Accept-Version": "2.1"}
	r := edgeCall(t, h, "POST", "/tv3/bind-context", hd, regBody("HS256", "s"))
	wantOK(t, "Accept-Version 2.1", r, okRegister)
	if v := r.hdr.Get("API-Version"); v != "2.1" {
		t.Errorf("API-Version %q, esperado 2.1", v)
	}
	if v := postBind(t, h, regBody("HS256", "s2")).hdr.Get("API-Version"); v != "2.0" {
		t.Errorf("sem Accept-Version: API-Version %q, esperado 2.0", v)
	}

	// warn: o nao associado so leva o aviso, e a borda responde a API
	st = edgeStore()
	h = newTestHandler(t, modeWarn, st, nil)
	r = edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json"}, regBody("HS256", "s"))
	wantOK(t, "warn nao associado", r, okRegister)
	if r.hdr.Get(warnHeader) != "106" {
		t.Errorf("warn: %s=%q, esperado 106", warnHeader, r.hdr.Get(warnHeader))
	}
	r = edgeCall(t, h, "POST", "/tv3/bind-context", map[string]string{"Content-Type": "application/json"}, `{}`)
	wantErr(t, "warn + erro da API", r, 105)
	if r.hdr.Get(warnHeader) != "106" {
		t.Errorf("warn + erro da API: %s=%q, esperado 106", warnHeader, r.hdr.Get(warnHeader))
	}
}

// --- C.6.8.3 (Tabela C.48) --------------------------------------------------

func bindGet(t *testing.T, h *authHandler, bindToken string) edgeResp {
	hd := map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)}
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

// Duas emissoras: a (HS256 segredo-a) e b (RS256, a corrente).
func twoServices(t *testing.T) *fakeStore {
	st := edgeStore()
	st.current = tSvcB
	st.svcHash = map[string]string{"serviceContextId": tSCID, "serviceName": "Canal B", "serviceId": "9"}
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
	// confusao de algoritmo: HS256 assinado com a chave publica RSA como segredo
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
	// corrente = b: nome e id (serviceId volta a inteiro, Tabela C.8)
	wantOK(t, "token de b", bindGet(t, h, rsTok), `{"boundServices":[{"serviceContextId":"`+tSCID+`","serviceName":"Canal B","serviceId":9}]}`)
	// a nao eh o corrente: so o serviceContextId
	wantOK(t, "token de a", bindGet(t, h, hsTok(t, "segredo-a", nil)), `{"boundServices":[{"serviceContextId":"`+tSCID+`"}]}`)

	// serviceId nao numerico e nome vazio sao omitidos
	st.svcHash = map[string]string{"serviceName": "", "serviceId": "9a"}
	wantOK(t, "sem nome nem id numerico", bindGet(t, h, rsTok), `{"boundServices":[{"serviceContextId":"`+tSCID+`"}]}`)

	// o mesmo segredo nas duas emissoras: dois itens; lista ilegivel
	// (WRONGTYPE) e entrada malformada sao ignoradas
	st = twoServices(t)
	st.keys[tSvcB] = append(st.keys[tSvcB], "nao-json", storedJSON("HS256", "segredo-a"))
	st.wrongType = map[string]bool{"urn:tv30:service:string": true}
	h = newTestHandler(t, modeEnforce, st, nil)
	// (BindServices do fakeStore vem em ordem: ...:b antes de ...:webmedia)
	wantOK(t, "dois servicos", bindGet(t, h, hsTok(t, "segredo-a", nil)),
		`{"boundServices":[{"serviceContextId":"`+tSCID+`","serviceName":"Canal B","serviceId":9},{"serviceContextId":"`+tSCID+`"}]}`)
}

// Politica da rota: access token (107) e classe (106) antes da API.
func TestBindListPolitica(t *testing.T) {
	h := newTestHandler(t, modeEnforce, twoServices(t), nil)
	tok := hsTok(t, "segredo-a", nil)
	wantErr(t, "sem access token", edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"bind-token": tok}, ""), 107)
	wantErr(t, "associado", edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"Origin": tAssoc, "bind-token": tok}, ""), 106)
	nonLocal := accessFor(t, "cli-2", classNonLocal)
	r := edgeCall(t, h, "GET", "/tv3/bind-context", map[string]string{"Authorization": nonLocal, "bind-token": tok}, "")
	if r.status != 200 {
		t.Errorf("nao local: status %d %s", r.status, r.body)
	}
}

// --- C.6.8.4 (Tabela C.49) --------------------------------------------------

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
	// registrada como privada PKCS#1; revogada pela publica SPKI
	wantOK(t, "revoga RSA pela publica", bindDelete(t, h, spkiB64(t, testRSA512(t))), `{}`)
	if _, ok := st.keys[tSvcB]; ok {
		t.Fatalf("bind-context:%s deveria ter sumido: %v", tSvcB, st.keys[tSvcB])
	}
	wantErr(t, "GET com o token da chave revogada", bindGet(t, h, rsTok), 101)
	wantOK(t, "chave inexistente", bindDelete(t, h, "nunca-registrada"), `{}`)

	// so o servico corrente: a chave de a continua valendo
	if r := bindGet(t, h, hsTok(t, "segredo-a", nil)); r.status != 200 {
		t.Fatalf("token de a: %d %s", r.status, r.body)
	}
	bindDelete(t, h, "segredo-a") // corrente = b
	if len(st.keys[tSvc]) != 1 {
		t.Fatalf("revogacao mexeu em outro servico: %v", st.keys[tSvc])
	}

	// sem servico corrente: sucesso e nada muda
	st.current = ""
	wantOK(t, "sem servico corrente", bindDelete(t, h, "segredo-a"), `{}`)
	if len(st.keys[tSvc]) != 1 {
		t.Fatalf("revogacao sem servico corrente removeu: %v", st.keys[tSvc])
	}

	// revogar "s" remove tambem o segredo registrado como "s " (o cabecalho
	// chega aparado), e as duplicatas
	st.current = tSvc
	st.keys[tSvc] = []string{storedJSON("HS256", "s "), storedJSON("HS512", "s"), storedJSON("HS512", "s"), storedJSON("HS256", "outra")}
	wantOK(t, "revoga por texto aparado", bindDelete(t, h, "s"), `{}`)
	if l := st.keys[tSvc]; len(l) != 1 || !strings.Contains(l[0], `"outra"`) {
		t.Fatalf("sobrou %v", l)
	}

	// classe: so o associado
	wantErr(t, "nao associado", edgeCall(t, h, "DELETE", "/tv3/bind-context", map[string]string{"key": "outra"}, ""), 106)
}

// Redis fora: 200 nas tres rotas (nos dois modos), sem chegar ao roteador.
func TestBindRedisFora(t *testing.T) {
	for _, mode := range []string{modeEnforce, modeWarn} {
		st := twoServices(t)
		h := newTestHandler(t, mode, st, nil)
		tok := hsTok(t, "segredo-a", nil)
		st.err = errorString("dial tcp: connection refused")
		// associado reconhecido pelo Origin tambem consulta o Redis: em
		// enforce o 200 sai da decisao; em warn, da API
		wantErr(t, mode+" POST", postBind(t, h, regBody("HS256", "s")), 200)
		wantErr(t, mode+" GET", bindGet(t, h, tok), 200)
		wantErr(t, mode+" DELETE", bindDelete(t, h, "s"), 200)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// panicStore: o Redis "explode" no meio da API — vira 200, nao conexao
// fechada.
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

// Ponta a ponta com o cliente RESP de verdade (servidor falso em memoria):
// POST -> lista no Redis -> bind-token valida a rota token+bind e o GET ->
// DELETE some com a lista.
func TestC68ComClienteRedis(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() {
		f.strs[keyCurrentService] = tSvc
		f.hashes[keyAssociated] = map[string]string{tAssoc: tSCID}
		f.hashes[keyCurrentSvcHash] = map[string]string{"serviceName": "Canal A", "serviceId": "7"}
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
	req.Header.Set("Authorization", accessFor(t, "cli-1", classAutonomous))
	req.Header.Set("bind-token", bt)
	if v := h.evaluate(req); v.Code != 0 {
		t.Fatalf("rota token+bind com o bind-token registrado: %d %s", v.Code, v.Detail)
	}
	wantOK(t, "GET", bindGet(t, h, bt), `{"boundServices":[{"serviceContextId":"`+tSCID+`","serviceName":"Canal A","serviceId":7}]}`)

	wantOK(t, "DELETE", bindDelete(t, h, "segredo-c68"), `{}`)
	f.with(func() {
		if _, ok := f.lists[keyBindPrefix+tSvc]; ok {
			t.Errorf("lista deveria ter sumido: %q", f.lists[keyBindPrefix+tSvc])
		}
	})
	wantErr(t, "GET depois da revogacao", bindGet(t, h, bt), 101)
}
