package main

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"sync"
	"testing"
	"time"
)

// Instante fixo dos testes (o mesmo iat das fixtures do jsonwebtoken).
var t0 = time.Unix(1790000000, 0)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signJWT monta um JWS compacto. key: []byte (HS*) ou *rsa.PrivateKey (RS*).
// alg "none" gera assinatura vazia.
func signJWT(t *testing.T, alg string, key interface{}, claims map[string]interface{}) string {
	t.Helper()
	return signJWTHeader(t, map[string]interface{}{"alg": alg, "typ": "JWT"}, alg, key, claims)
}

func signJWTHeader(t *testing.T, hdr map[string]interface{}, alg string, key interface{}, claims map[string]interface{}) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(pb)
	var sig []byte
	switch alg {
	case "HS256":
		m := hmac.New(sha256.New, key.([]byte))
		m.Write([]byte(in))
		sig = m.Sum(nil)
	case "HS512":
		m := hmac.New(sha512.New, key.([]byte))
		m.Write([]byte(in))
		sig = m.Sum(nil)
	case "RS256":
		d := sha256.Sum256([]byte(in))
		s, err := rsa.SignPKCS1v15(rand.Reader, key.(*rsa.PrivateKey), crypto.SHA256, d[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = s
	case "RS512":
		d := sha512.Sum512([]byte(in))
		s, err := rsa.SignPKCS1v15(rand.Reader, key.(*rsa.PrivateKey), crypto.SHA512, d[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = s
	case "none":
	default:
		t.Fatalf("alg de teste desconhecido %s", alg)
	}
	return in + "." + b64(sig)
}

func timeClaims(iat, nbf, exp time.Time) map[string]interface{} {
	return map[string]interface{}{"iat": iat.Unix(), "nbf": nbf.Unix(), "exp": exp.Unix()}
}

var (
	rsaOnce sync.Once
	rsaKey  *rsa.PrivateKey
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rsaOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		rsaKey = k
	})
	return rsaKey
}

func pubPEM(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func storedJSON(alg, key string) string {
	b, _ := json.Marshal(map[string]interface{}{"alg": alg, "key": key, "registeredAt": t0.UnixMilli()})
	return string(b)
}

// fakeStore: Redis em memoria para os testes da decisao.
type fakeStore struct {
	blocked    map[string]bool
	associated map[string]bool
	current    string
	keys       map[string][]string
	err        error
	calls      int
}

func (f *fakeStore) IsBlocked(id string) (bool, error) {
	f.calls++
	return f.blocked[id], f.err
}
func (f *fakeStore) IsAssociatedOrigin(o string) (bool, error) {
	f.calls++
	return f.associated[o], f.err
}
func (f *fakeStore) CurrentServiceID() (string, error) {
	f.calls++
	return f.current, f.err
}
func (f *fakeStore) BindKeys(id string) ([]string, error) {
	f.calls++
	return f.keys[id], f.err
}
