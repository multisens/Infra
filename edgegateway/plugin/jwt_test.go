package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func bindKeysFor(t *testing.T, pairs ...string) []bindKey {
	t.Helper()
	var raw []string
	for i := 0; i+1 < len(pairs); i += 2 {
		raw = append(raw, storedJSON(pairs[i], pairs[i+1]))
	}
	return (&keyCache{}).decodeStoredKeys("teste", raw)
}

func TestBindTokenQuatroAlgoritmos(t *testing.T) {
	k := testRSAKey(t)
	pem := pubPEM(t, k)
	secret := "segredo-da-emissora"
	claims := timeClaims(t0.Add(-time.Minute), t0.Add(-time.Minute), t0.Add(time.Hour))
	cases := []struct {
		alg     string
		signKey interface{}
		regKey  string
	}{
		{"HS256", []byte(secret), secret},
		{"HS512", []byte(secret), secret},
		{"RS256", k, pem},
		{"RS512", k, pem},
	}
	for _, c := range cases {
		tok := signJWT(t, c.alg, c.signKey, claims)
		if err := verifyBindToken(tok, bindKeysFor(t, c.alg, c.regKey), t0); err != nil {
			t.Errorf("%s: esperado valido, veio %v", c.alg, err)
		}
		// chave registrada com OUTRO alg nao valida (alg do token = alg da chave)
		other := map[string]string{"HS256": "HS512", "HS512": "HS256", "RS256": "RS512", "RS512": "RS256"}[c.alg]
		if err := verifyBindToken(tok, bindKeysFor(t, other, c.regKey), t0); err == nil {
			t.Errorf("%s: chave registrada como %s nao deveria validar", c.alg, other)
		}
	}
}

func TestBindTokenConfusaoDeAlgoritmo(t *testing.T) {
	k := testRSAKey(t)
	pem := pubPEM(t, k)
	claims := timeClaims(t0, t0, t0.Add(time.Hour))
	// ataque classico: HS256 assinado com a chave PUBLICA RSA como segredo
	forged := signJWT(t, "HS256", []byte(pem), claims)
	if err := verifyBindToken(forged, bindKeysFor(t, "RS256", pem), t0); err == nil {
		t.Fatal("HS256 assinado com a chave publica RSA foi aceito")
	}
	// mesmo que a chave tenha sido registrada como RS256 E exista outra HS256 diferente
	if err := verifyBindToken(forged, bindKeysFor(t, "RS256", pem, "HS256", "outro"), t0); err == nil {
		t.Fatal("confusao de algoritmo aceita com lista mista")
	}
	// alg none sempre invalido
	none := signJWT(t, "none", nil, claims)
	if err := verifyBindToken(none, bindKeysFor(t, "HS256", "x"), t0); err == nil {
		t.Fatal("alg none aceito")
	}
	// alg fora dos 4 (ex.: ES256) invalido
	es := signJWTHeader(t, map[string]interface{}{"alg": "ES256"}, "HS256", []byte("x"), claims)
	if err := verifyBindToken(es, bindKeysFor(t, "HS256", "x"), t0); err == nil {
		t.Fatal("alg ES256 aceito")
	}
}

func TestBindTokenListaRotacionavel(t *testing.T) {
	claims := timeClaims(t0, t0, t0.Add(time.Hour))
	tok := signJWT(t, "HS256", []byte("chave-nova"), claims)
	keys := bindKeysFor(t, "HS256", "chave-antiga", "RS256", "lixo-que-nao-parseia", "HS256", "chave-nova")
	if len(keys) != 2 {
		t.Fatalf("esperadas 2 chaves usaveis (a RSA ilegivel eh ignorada), vieram %d", len(keys))
	}
	if err := verifyBindToken(tok, keys, t0); err != nil {
		t.Fatalf("qualquer chave da lista deveria validar: %v", err)
	}
	if err := verifyBindToken(tok, bindKeysFor(t, "HS256", "chave-antiga"), t0); err == nil {
		t.Fatal("chave errada validou")
	}
	if err := verifyBindToken(tok, nil, t0); err == nil {
		t.Fatal("sem chave registrada validou")
	}
}

func TestBindTokenTempo(t *testing.T) {
	key := []byte("k")
	keys := bindKeysFor(t, "HS256", "k")
	cases := []struct {
		name   string
		claims map[string]interface{}
		ok     bool
		want   string
	}{
		{"valido", timeClaims(t0.Add(-time.Hour), t0.Add(-time.Hour), t0.Add(time.Hour)), true, ""},
		{"expirado", timeClaims(t0.Add(-2*time.Hour), t0.Add(-2*time.Hour), t0.Add(-time.Second)), false, "expirado"},
		{"exp == agora", timeClaims(t0.Add(-time.Hour), t0.Add(-time.Hour), t0), false, "expirado"},
		{"nbf no futuro", timeClaims(t0, t0.Add(time.Minute), t0.Add(time.Hour)), false, "nbf"},
		{"iat no futuro", timeClaims(t0.Add(time.Minute), t0, t0.Add(time.Hour)), false, "iat"},
		{"sem claims de tempo (should, nao shall)", map[string]interface{}{"x": 1}, true, ""},
		{"exp nao numerico", map[string]interface{}{"exp": "amanha"}, false, "numerica"},
	}
	for _, c := range cases {
		err := verifyBindToken(signJWT(t, "HS256", key, c.claims), keys, t0)
		if c.ok && err != nil {
			t.Errorf("%s: esperado valido, veio %v", c.name, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: esperado erro contendo %q, veio %v", c.name, c.want, err)
		}
	}
}

// Ordem C.4.1.4: formato -> assinatura -> nbf/exp -> iat. Token expirado E
// com assinatura errada reporta a assinatura.
func TestBindTokenOrdemDasFrentes(t *testing.T) {
	tok := signJWT(t, "HS256", []byte("errada"), timeClaims(t0, t0, t0.Add(-time.Hour)))
	err := verifyBindToken(tok, bindKeysFor(t, "HS256", "certa"), t0)
	if err == nil || !strings.Contains(err.Error(), "assinatura") {
		t.Fatalf("esperado erro de assinatura antes do de tempo, veio %v", err)
	}
}

func TestParseJWTFormato(t *testing.T) {
	good := signJWT(t, "HS256", []byte("k"), map[string]interface{}{"a": 1})
	parts := strings.Split(good, ".")
	bad := []string{
		"",
		"abc",
		parts[0] + "." + parts[1],          // 2 partes
		good + ".x.y",                      // 5 partes (JWE)
		"!!!." + parts[1] + "." + parts[2], // base64 invalido
		b64([]byte("[1,2]")) + "." + parts[1] + "." + parts[2],         // cabecalho nao-objeto
		b64([]byte(`{"typ":"JWT"}`)) + "." + parts[1] + "." + parts[2], // sem alg
		b64([]byte(`{"alg":"HS256","enc":"A128GCM"}`)) + "." + parts[1] + "." + parts[2],
		b64([]byte(`{"alg":"HS256","crit":["exp"]}`)) + "." + parts[1] + "." + parts[2],
		parts[0] + "." + b64([]byte("nao-json")) + "." + parts[2],
	}
	for i, b := range bad {
		if _, err := parseJWT(b); err == nil {
			t.Errorf("caso %d (%q) deveria ser formato invalido", i, b)
		}
	}
	if _, err := parseJWT(good); err != nil {
		t.Fatalf("token bem formado rejeitado: %v", err)
	}
}

func TestAccessToken(t *testing.T) {
	secret := []byte("tv30-dev-secret-nao-usar-em-producao")
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"iat": t0.Unix(), "nbf": t0.Unix(), "exp": t0.Add(24 * time.Hour).Unix(),
			"iss": "GenericIssuer", "sub": "cli-1", "class": "non-local",
		}
	}
	ok := signJWT(t, "HS256", secret, base())
	c, err := verifyAccessToken("Bearer "+ok, secret, "GenericIssuer", t0)
	if err != nil || c.Sub != "cli-1" || c.Class != "non-local" {
		t.Fatalf("token valido rejeitado: %v %+v", err, c)
	}
	if _, err := verifyAccessToken("bearer  "+ok, secret, "GenericIssuer", t0); err != nil {
		t.Errorf("esquema Bearer eh case-insensitive: %v", err)
	}

	mut := func(f func(m map[string]interface{})) map[string]interface{} { m := base(); f(m); return m }
	bad := map[string]string{
		"sem Bearer":       ok,
		"Basic":            "Basic " + ok,
		"segredo errado":   "Bearer " + signJWT(t, "HS256", []byte("outro"), base()),
		"HS512 (alg fixo)": "Bearer " + signJWT(t, "HS512", secret, base()),
		"none":             "Bearer " + signJWT(t, "none", nil, base()),
		"expirado":         "Bearer " + signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { m["exp"] = t0.Add(-time.Second).Unix() })),
		"sem exp":          "Bearer " + signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { delete(m, "exp") })),
		"nbf no futuro":    "Bearer " + signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { m["nbf"] = t0.Add(time.Hour).Unix() })),
		"iss errado":       "Bearer " + signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { m["iss"] = "Outro" })),
		"sem iss":          "Bearer " + signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { delete(m, "iss") })),
		"nao-JWT":          "Bearer abc.def",
	}
	for name, h := range bad {
		if _, err := verifyAccessToken(h, secret, "GenericIssuer", t0); err == nil {
			t.Errorf("%s: deveria ser invalido", name)
		}
	}
	// classe ausente/desconhecida: mesmo padrao do tv3ws (autonomo)
	noClass := signJWT(t, "HS256", secret, mut(func(m map[string]interface{}) { delete(m, "class") }))
	if c, err := verifyAccessToken("Bearer "+noClass, secret, "GenericIssuer", t0); err != nil || c.Class != classAutonomous {
		t.Errorf("sem class: esperado %s, veio %+v %v", classAutonomous, c, err)
	}
}

// Interoperabilidade: tokens gerados pelo jsonwebtoken (a lib do tv3ws),
// em testdata/jsonwebtoken.json.
func TestInteropJsonwebtoken(t *testing.T) {
	raw, err := os.ReadFile("testdata/jsonwebtoken.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Access, HS512, RS256, RS512, PubPEM string
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	c, err := verifyAccessToken("Bearer "+f.Access, []byte("tv30-dev-secret-nao-usar-em-producao"), "GenericIssuer", t0.Add(time.Hour))
	if err != nil || c.Class != classAutonomous || c.Sub == "" {
		t.Fatalf("access token do tv3ws rejeitado: %v %+v", err, c)
	}
	if _, err := verifyAccessToken("Bearer "+f.Access, []byte("tv30-dev-secret-nao-usar-em-producao"), "GenericIssuer", t0.Add(25*time.Hour)); err == nil {
		t.Fatal("access token do tv3ws expirado (24 h) aceito")
	}
	at := t0.Add(time.Minute)
	if err := verifyBindToken(f.HS512, bindKeysFor(t, "HS512", "segredo-da-emissora"), at); err != nil {
		t.Errorf("HS512 do jsonwebtoken: %v", err)
	}
	if err := verifyBindToken(f.RS256, bindKeysFor(t, "RS256", f.PubPEM), at); err != nil {
		t.Errorf("RS256 do jsonwebtoken: %v", err)
	}
	if err := verifyBindToken(f.RS512, bindKeysFor(t, "RS512", f.PubPEM), at); err != nil {
		t.Errorf("RS512 do jsonwebtoken: %v", err)
	}
	// a mesma chave publica em base64 de DER (sem cabecalho PEM)
	body := strings.Join(strings.Split(strings.TrimSpace(f.PubPEM), "\n")[1:], "")
	body = strings.TrimSuffix(body, "-----END PUBLIC KEY-----")
	if err := verifyBindToken(f.RS256, bindKeysFor(t, "RS256", body), at); err != nil {
		t.Errorf("RS256 com chave em base64 de DER: %v", err)
	}
}
