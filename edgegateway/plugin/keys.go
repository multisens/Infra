package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
)

// parseKeyMaterial decodes a key registered by the broadcaster (C.6.8.2):
//   - HS256/HS512: the key is the secret, the UTF-8 bytes of the string;
//   - RS256/RS512: PEM (PUBLIC KEY, RSA PUBLIC KEY, RSA PRIVATE KEY, PRIVATE
//     KEY) or base64 of DER (SPKI or PKCS#1 public). A private key (PKCS#1
//     or PKCS#8) is accepted, and its public key is returned.
//
// Returns an error for an empty secret, an unsupported alg or an unreadable
// RSA key.
func parseKeyMaterial(alg, key string) (interface{}, error) {
	switch alg {
	case algHS256, algHS512:
		if key == "" {
			return nil, errors.New("segredo vazio")
		}
		return []byte(key), nil
	case algRS256, algRS512:
		return parseRSAPublicKey(key)
	}
	return nil, fmt.Errorf("alg %q nao suportado", alg)
}

// PEM block types accepted for an RSA key.
var pemLabels = map[string]bool{"PUBLIC KEY": true, "RSA PUBLIC KEY": true, "RSA PRIVATE KEY": true, "PRIVATE KEY": true}

func parseRSAPublicKey(s string) (*rsa.PublicKey, error) {
	s = strings.TrimSpace(s)
	var der []byte
	if strings.HasPrefix(s, "-----BEGIN") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, errors.New("PEM invalido")
		}
		if !pemLabels[block.Type] {
			return nil, fmt.Errorf("PEM com rotulo %q nao aceito", block.Type)
		}
		der = block.Bytes
	} else {
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
		var err error
		if der, err = decodeBase64Any(compact); err != nil {
			return nil, errors.New("chave nem PEM nem base64")
		}
	}
	return rsaPublicFromDER(der)
}

func decodeBase64Any(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("base64 invalido")
}

func rsaPublicFromDER(der []byte) (*rsa.PublicKey, error) {
	notRSA := false
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		if pub, ok := k.(*rsa.PublicKey); ok {
			return pub, nil
		}
		notRSA = true
	}
	if pub, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return pub, nil
	}
	if priv, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return &priv.PublicKey, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if priv, ok := k.(*rsa.PrivateKey); ok {
			return &priv.PublicKey, nil
		}
		notRSA = true
	}
	if notRSA {
		return nil, errors.New("chave nao eh RSA")
	}
	return nil, errors.New("chave nao parseia como RSA (SPKI, PKCS#1 ou PKCS#8)")
}

// storedKey is the JSON of each element of bind-context:{serviceId},
// {"alg","key","registeredAt"}; registeredAt is not read.
type storedKey struct {
	Alg string `json:"alg"`
	Key string `json:"key"`
}

// keyCache holds parsed keys by alg and key string. When it reaches
// keyCacheMax entries, it is emptied before the next insertion.
type keyCache struct {
	mu sync.Mutex
	m  map[string]interface{}
}

const keyCacheMax = 512

func (c *keyCache) get(alg, key string) (interface{}, error) {
	id := alg + "\n" + key
	c.mu.Lock()
	if v, ok := c.m[id]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	v, err := parseKeyMaterial(alg, key)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= keyCacheMax {
		c.m = map[string]interface{}{}
	}
	c.m[id] = v
	c.mu.Unlock()
	return v, nil
}

// decodeStoredKeys converts the raw list elements into usable keys. An
// element that is malformed, has an unsupported alg or a key that does not
// parse is logged and skipped; the other elements are still returned.
func (c *keyCache) decodeStoredKeys(serviceID string, raw []string) []bindKey {
	out := make([]bindKey, 0, len(raw))
	for i, r := range raw {
		var sk storedKey
		if err := json.Unmarshal([]byte(r), &sk); err != nil || sk.Alg == "" {
			logf("AVISO bind-context:%s[%d] nao eh {alg,key} valido — ignorado", serviceID, i)
			continue
		}
		if !supportedAlg(sk.Alg) {
			logf("AVISO bind-context:%s[%d] alg %q nao suportado — ignorado", serviceID, i, sk.Alg)
			continue
		}
		m, err := c.get(sk.Alg, sk.Key)
		if err != nil {
			logf("AVISO bind-context:%s[%d] chave %s ilegivel (%v) — ignorada", serviceID, i, sk.Alg, err)
			continue
		}
		out = append(out, bindKey{Alg: sk.Alg, material: m})
	}
	return out
}
