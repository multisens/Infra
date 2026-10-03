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
	"os"
	"testing"
	"time"
)

// Todas as codificacoes de chave RSA aceitas validam o mesmo token.
func TestFormatosDeChaveRSA(t *testing.T) {
	k := testRSAKey(t)
	spki, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pkcs1pub := x509.MarshalPKCS1PublicKey(&k.PublicKey)
	pkcs1priv := x509.MarshalPKCS1PrivateKey(k)
	pkcs8priv, _ := x509.MarshalPKCS8PrivateKey(k)
	std := base64.StdEncoding.EncodeToString
	pemOf := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}

	formats := map[string]string{
		"PEM PUBLIC KEY":             pemOf("PUBLIC KEY", spki),
		"PEM RSA PUBLIC KEY":         pemOf("RSA PUBLIC KEY", pkcs1pub),
		"base64 DER SPKI":            std(spki),
		"base64 DER PKCS#1 publico":  std(pkcs1pub),
		"base64 DER PKCS#1 privado":  std(pkcs1priv), // forma do exemplo MIIBOgIBAAJB... da norma
		"base64 DER PKCS#8 privado":  std(pkcs8priv),
		"PEM RSA PRIVATE KEY":        pemOf("RSA PRIVATE KEY", pkcs1priv),
		"PEM PRIVATE KEY (PKCS#8)":   pemOf("PRIVATE KEY", pkcs8priv),
		"base64 com quebra de linha": std(spki)[:40] + "\n" + std(spki)[40:],
	}
	tok := signJWT(t, "RS256", k, timeClaims(t0, t0, t0.Add(time.Hour)))
	for name, key := range formats {
		m, err := parseKeyMaterial("RS256", key)
		if err != nil {
			t.Errorf("%s: nao parseou: %v", name, err)
			continue
		}
		if err := verifyBindToken(tok, []bindKey{{Alg: "RS256", material: m}}, t0); err != nil {
			t.Errorf("%s: nao validou: %v", name, err)
		}
	}
}

// Chave de 512 bits (tamanho do exemplo da norma, {"alg":"RS256","key":
// "MIIBOgIBAAJB..."}: PKCS#1 privada em base64 de DER). Observacao: com 512
// bits so da para RS256 — o DigestInfo do SHA-512 (83 bytes + 11 de padding)
// nao cabe num modulo de 64 bytes, entao RS512 exige chave maior.
func TestChaveRSA512BitsDaNorma(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 512)
	if err != nil {
		t.Skipf("GenerateKey(512) indisponivel: %v", err)
	}
	b := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(k))
	t.Logf("prefixo gerado: %s (o do exemplo da norma: MIIBOgIBAAJB)", b[:12])
	tok := signJWT(t, "RS256", k, timeClaims(t0, t0, t0.Add(time.Hour)))
	if err := verifyBindToken(tok, bindKeysFor(t, "RS256", b), t0); err != nil {
		t.Fatalf("chave PKCS#1 privada de 512 bits: %v", err)
	}
}

func TestChavesInvalidas(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	cases := map[string][2]string{
		"lixo":             {"RS256", "isto nao eh chave"},
		"base64 nao-DER":   {"RS256", base64.StdEncoding.EncodeToString([]byte("abc"))},
		"PEM quebrado":     {"RS256", "-----BEGIN PUBLIC KEY-----\nxx\n-----END PUBLIC KEY-----"},
		"chave EC":         {"RS256", base64.StdEncoding.EncodeToString(ecDER)},
		"segredo HS vazio": {"HS256", ""},
		"alg desconhecido": {"ES256", "x"},
	}
	for name, c := range cases {
		if _, err := parseKeyMaterial(c[0], c[1]); err == nil {
			t.Errorf("%s: deveria falhar", name)
		}
	}
}

// Teste cruzado com o tv3ws: o mesmo testdata/keyformats.json esta em
// tv3ws/test/fixtures/ e eh lido por test/bind-token.test.ts. Os dois
// parsers (este e o do registro C.6.8.2) tem de concordar em todos os casos.
func TestFormatosCruzadosComOTv3ws(t *testing.T) {
	raw, err := os.ReadFile("testdata/keyformats.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name string `json:"name"`
			Alg  string `json:"alg"`
			OK   bool   `json:"ok"`
			Key  string `json:"key"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil || len(fx.Cases) == 0 {
		t.Fatalf("fixture ilegivel: %v", err)
	}
	for _, c := range fx.Cases {
		_, err := parseKeyMaterial(c.Alg, c.Key)
		if c.OK && err != nil {
			t.Errorf("%s: deveria aceitar, recusou: %v", c.Name, err)
		}
		if !c.OK && err == nil {
			t.Errorf("%s: deveria recusar, aceitou", c.Name)
		}
	}
}

func TestEntradasMalformadasSaoIgnoradas(t *testing.T) {
	raw := []string{"nao-json", `{"key":"sem-alg"}`, `{"alg":"none","key":"x"}`, storedJSON("HS256", "ok")}
	keys := (&keyCache{}).decodeStoredKeys("svc", raw)
	if len(keys) != 1 || keys[0].Alg != "HS256" {
		t.Fatalf("esperada so a entrada valida, veio %+v", keys)
	}
}

func TestKeyCache(t *testing.T) {
	c := &keyCache{}
	a, err := c.get("HS256", "s")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.get("HS256", "s")
	if string(a.([]byte)) != string(b.([]byte)) || len(c.m) != 1 {
		t.Fatal("cache nao reaproveitou a entrada")
	}
	for i := 0; i < keyCacheMax+5; i++ {
		c.get("HS256", string(rune('a'+i%26))+string(rune(i)))
	}
	if len(c.m) > keyCacheMax {
		t.Fatalf("cache passou do teto: %d", len(c.m))
	}
}
