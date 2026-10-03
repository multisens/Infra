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

// Algoritmos de assinatura do bind-token que a norma manda suportar
// (C.4.1.4.8): HS256, HS512, RS256, RS512. Qualquer outro (inclusive
// "none") eh invalido.
const (
	algHS256 = "HS256"
	algHS512 = "HS512"
	algRS256 = "RS256"
	algRS512 = "RS512"
)

func supportedAlg(a string) bool {
	return a == algHS256 || a == algHS512 || a == algRS256 || a == algRS512
}

// jwtToken eh um JWS compacto ja decodificado (ainda NAO verificado).
type jwtToken struct {
	alg          string
	claims       map[string]interface{}
	signingInput string
	signature    []byte
}

// parseJWT: primeira frente da validacao (C.4.1.4) — formato. JWS compacto
// com 3 partes base64url, cabecalho e payload JSON, `alg` presente, sem
// cifragem (JWE tem 5 partes; o bind-token "shall not be encrypted").
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
		// RFC 7515 4.1.11: extensao critica desconhecida => rejeitar.
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

// b64url aceita base64url sem padding (RFC 7515) e tolera padding.
func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// verifySignature confere a assinatura com a chave dada. key eh []byte para
// HS* e *rsa.PublicKey para RS*; tipo errado = assinatura invalida (isso
// impede a confusao de algoritmo: chave RSA publica usada como segredo HMAC).
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

// numericClaim le uma NumericDate (segundos, inteiro ou fracionario).
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

// checkTime: nbf/exp (e iat quando pedido). Mesma regra do jsonwebtoken
// usado pelo tv3ws: expirado se agora >= exp; inativo se nbf > agora.
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

// accessClaims: o que a borda usa do access token emitido pelo tv3ws
// (tv3ws/src/modules/auth-manager/manager.ts: iat, nbf, exp, iss, sub, class).
type accessClaims struct {
	Sub   string
	Class string
}

// verifyAccessToken (D2: assinatura + expiracao, sem ignoreExpiration).
// O access token eh emitido pelo PROPRIO tv3ws em HS256 com JWT_SECRET; o
// alg do cabecalho tem que ser HS256 (o tv3ws hoje confia no alg do proprio
// token — aqui ele eh fixo).
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
	// exp obrigatorio: sem ele o token nunca expiraria (D2). O tv3ws
	// sempre emite exp.
	if err := t.checkTime(now, true, false); err != nil {
		return nil, err
	}
	if iss := t.stringClaim("iss"); iss != issuer {
		return nil, fmt.Errorf("iss %q diferente do esperado", iss)
	}
	c := &accessClaims{Sub: t.stringClaim("sub"), Class: t.stringClaim("class")}
	if !validClass(c.Class) {
		// mesmo padrao do tv3ws (getRequestClass): sem classe => autonomo.
		c.Class = classAutonomous
	}
	return c, nil
}

// bindKey eh uma entrada de bind-context:{serviceId} ja com a chave
// decodificada (material = []byte para HS*, *rsa.PublicKey para RS*).
type bindKey struct {
	Alg      string
	material interface{}
}

// verifyBindToken aplica as 4 frentes da C.4.1.4, nesta ordem: formato JWT,
// assinatura (qualquer chave registrada do servico corrente com o MESMO alg
// valida — C.4.1.3, lista rotacionavel), nbf/exp, iat.
//
// PENDENTE (Joel): lacuna L5 — a norma compara nbf/exp/iat com o relogio do
// System Time Fragment da radiodifusao (C.4.1.3); o testbed nao tem STF e
// usa o relogio do host, sem tolerancia de defasagem.
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
