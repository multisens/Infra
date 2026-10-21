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
	"sort"
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

// fakeStore: Redis em memoria para os testes da decisao e das APIs da borda.
type fakeStore struct {
	blocked    map[string]bool
	authorized map[string]bool   // clients:authorized
	associated map[string]string // origins:associated: origem -> servico que lancou a app
	current    string
	keys       map[string][]string // bind-context:{serviceId}
	svcHash    map[string]string   // session:current-service
	wrongType  map[string]bool     // bind-context:{serviceId} de outro tipo (WRONGTYPE)
	err        error
	authzErr   error // so no SISMEMBER clients:authorized
	writeErr   error // so nas escritas (a funcao do Redis recusando o dado)
	calls      int
}

func (f *fakeStore) IsBlocked(id string) (bool, error) {
	f.calls++
	return f.blocked[id], f.err
}
func (f *fakeStore) IsAuthorized(id string) (bool, error) {
	f.calls++
	if f.authzErr != nil {
		return false, f.authzErr
	}
	return f.authorized[id], f.err
}
func (f *fakeStore) AssociatedOrigin(o string) (string, bool, error) {
	f.calls++
	svc, ok := f.associated[o]
	return svc, ok, f.err
}
func (f *fakeStore) CurrentServiceID() (string, error) {
	f.calls++
	return f.current, f.err
}
func (f *fakeStore) BindKeys(id string) ([]string, error) {
	f.calls++
	if f.err == nil && f.wrongType[id] {
		return nil, redisError("WRONGTYPE Operation against a key holding the wrong kind of value")
	}
	return append([]string(nil), f.keys[id]...), f.err
}
func (f *fakeStore) AddBindKey(id, raw string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.keys == nil {
		f.keys = map[string][]string{}
	}
	f.keys[id] = append(f.keys[id], raw)
	return nil
}
func (f *fakeStore) RemoveBindKey(id, raw string) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	var kept []string
	var n int64
	for _, e := range f.keys[id] {
		if e == raw {
			n++
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		delete(f.keys, id) // como no Redis: lista vazia some
	} else {
		f.keys[id] = kept
	}
	return n, nil
}
func (f *fakeStore) BindServices() ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []string
	for id, l := range f.keys {
		if len(l) > 0 {
			out = append(out, id)
		}
	}
	for id := range f.wrongType {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
func (f *fakeStore) CurrentService() (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for k, v := range f.svcHash {
		out[k] = v
	}
	return out, nil
}
