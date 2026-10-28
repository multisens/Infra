package main

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Bind-token signature algorithms (C.4.1.4.8). Any other alg, "none"
// included, is invalid.
const (
	algHS256 = "HS256"
	algHS512 = "HS512"
	algRS256 = "RS256"
	algRS512 = "RS512"
)

func supportedAlg(a string) bool {
	return a == algHS256 || a == algHS512 || a == algRS256 || a == algRS512
}

// jwtToken is a decoded compact JWS whose signature is not yet verified.
type jwtToken struct {
	alg          string
	claims       map[string]interface{}
	signingInput string
	signature    []byte
}

// parseJWT checks the JWT format, the first front of C.4.1.4.1: a compact
// JWS of 3 base64url parts, header and payload as JSON objects, `alg`
// present, no `enc` and no `crit` in the header (C.4.1.3: not encrypted).
func parseJWT(tok string) (*jwtToken, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("nao eh JWT (esperadas 3 partes, vieram %d)", len(parts))
	}
	hb, err := b64url(parts[0])
	if err != nil {
		return nil, fmt.Errorf("cabecalho JWT nao eh base64url")
	}
	pb, err := b64url(parts[1])
	if err != nil {
		return nil, fmt.Errorf("payload JWT nao eh base64url")
	}
	sig, err := b64url(parts[2])
	if err != nil {
		return nil, fmt.Errorf("assinatura JWT nao eh base64url")
	}

	var hdr map[string]interface{}
	if err := json.Unmarshal(hb, &hdr); err != nil || hdr == nil {
		return nil, fmt.Errorf("cabecalho JWT nao eh objeto JSON")
	}
	alg, _ := hdr["alg"].(string)
	if alg == "" {
		return nil, fmt.Errorf("cabecalho JWT sem alg")
	}
	if _, enc := hdr["enc"]; enc {
		return nil, fmt.Errorf("JWT cifrado (enc) nao eh aceito")
	}
	if _, crit := hdr["crit"]; crit {
		// RFC 7515 4.1.11: an unknown critical extension is rejected.
		return nil, fmt.Errorf("cabecalho JWT com crit nao suportado")
	}

	dec := json.NewDecoder(bytes.NewReader(pb))
	dec.UseNumber()
	var claims map[string]interface{}
	if err := dec.Decode(&claims); err != nil || claims == nil {
		return nil, fmt.Errorf("payload JWT nao eh objeto JSON")
	}
	return &jwtToken{alg: alg, claims: claims, signingInput: parts[0] + "." + parts[1], signature: sig}, nil
}

// b64url decodes base64url with or without padding.
func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// verifySignature checks the signature with the given key: []byte for HS*,
// *rsa.PublicKey for RS*. Returns false if alg differs from the token's, the
// signature is empty or the key has the wrong type.
func (t *jwtToken) verifySignature(alg string, key interface{}) bool {
	if t.alg != alg || len(t.signature) == 0 {
		return false
	}
	in := []byte(t.signingInput)
	switch alg {
	case algHS256, algHS512:
		secret, ok := key.([]byte)
		if !ok {
			return false
		}
		h := hmac.New(sha256.New, secret)
		if alg == algHS512 {
			h = hmac.New(sha512.New, secret)
		}
		h.Write(in)
		return hmac.Equal(h.Sum(nil), t.signature)
	case algRS256, algRS512:
		pub, ok := key.(*rsa.PublicKey)
		if !ok || pub == nil {
			return false
		}
		var digest []byte
		hash := crypto.SHA256
		if alg == algRS512 {
			hash = crypto.SHA512
			d := sha512.Sum512(in)
			digest = d[:]
		} else {
			d := sha256.Sum256(in)
			digest = d[:]
		}
		return rsa.VerifyPKCS1v15(pub, hash, digest, t.signature) == nil
	}
	return false
}

// numericClaim reads a NumericDate, integer or fractional seconds. Returns
// whether the claim is present, and an error if it is not a number.
func (t *jwtToken) numericClaim(name string) (float64, bool, error) {
	v, present := t.claims[name]
	if !present {
		return 0, false, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, true, fmt.Errorf("claim %s nao numerica", name)
	}
	f, err := n.Float64()
	if err != nil {
		return 0, true, fmt.Errorf("claim %s nao numerica", name)
	}
	return f, true, nil
}

func (t *jwtToken) stringClaim(name string) string {
	s, _ := t.claims[name].(string)
	return s
}

// checkTime checks nbf and exp, and iat when checkIat is set: invalid if
// now >= exp, nbf > now or iat > now; with requireExp, also if exp is absent.
func (t *jwtToken) checkTime(now time.Time, requireExp, checkIat bool) error {
	sec := float64(now.Unix())
	nbf, hasNbf, err := t.numericClaim("nbf")
	if err != nil {
		return err
	}
	exp, hasExp, err := t.numericClaim("exp")
	if err != nil {
		return err
	}
	if requireExp && !hasExp {
		return errors.New("token sem exp")
	}
	if hasNbf && nbf > sec {
		return errors.New("token ainda nao valido (nbf no futuro)")
	}
	if hasExp && sec >= exp {
		return errors.New("token expirado")
	}
	if checkIat {
		iat, hasIat, err := t.numericClaim("iat")
		if err != nil {
			return err
		}
		if hasIat && iat > sec {
			return errors.New("token emitido no futuro (iat)")
		}
	}
	return nil
}

// accessClaims holds the access token claims read by the edge.
type accessClaims struct {
	Sub   string
	Class string
}

// verifyAccessToken checks a "Bearer <accessToken>" header: HS256 with
// secret, whatever alg the header declares; exp required; nbf; iss equal to
// issuer; class claim required, one of the client classes.
func verifyAccessToken(authorization string, secret []byte, issuer string, now time.Time) (*accessClaims, error) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(authorization), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, errors.New("Authorization fora do formato 'Bearer <accessToken>'")
	}
	t, err := parseJWT(strings.TrimSpace(tok))
	if err != nil {
		return nil, err
	}
	if t.alg != algHS256 {
		return nil, fmt.Errorf("alg %q no access token (esperado HS256)", t.alg)
	}
	if !t.verifySignature(algHS256, secret) {
		return nil, errors.New("assinatura do access token invalida")
	}
	if err := t.checkTime(now, true, false); err != nil {
		return nil, err
	}
	if iss := t.stringClaim("iss"); iss != issuer {
		return nil, fmt.Errorf("iss %q diferente do esperado", iss)
	}
	c := &accessClaims{Sub: t.stringClaim("sub"), Class: t.stringClaim("class")}
	if !validClass(c.Class) {
		return nil, fmt.Errorf("class %q do access token invalida", c.Class)
	}
	return c, nil
}

// bindKey is an entry of bind-context:{serviceId} with the decoded key:
// []byte for HS*, *rsa.PublicKey for RS*.
type bindKey struct {
	Alg      string
	material interface{}
}

// verifyBindToken applies the four fronts of C.4.1.4.1 in order: JWT format,
// signature (any key of the service with the same alg validates it,
// C.4.1.3), nbf and exp, iat. Times are compared with the host clock, with
// no leeway.
func verifyBindToken(tok string, keys []bindKey, now time.Time) error {
	t, err := parseJWT(strings.TrimSpace(tok))
	if err != nil {
		return err
	}
	if !supportedAlg(t.alg) {
		return fmt.Errorf("alg %q nao suportado no bind-token (HS256, HS512, RS256, RS512)", t.alg)
	}
	valid := false
	for _, k := range keys {
		if k.Alg == t.alg && t.verifySignature(k.Alg, k.material) {
			valid = true
			break
		}
	}
	if !valid {
		return errors.New("assinatura do bind-token nao confere com nenhuma chave registrada para o servico corrente")
	}
	return t.checkTime(now, false, true)
}
