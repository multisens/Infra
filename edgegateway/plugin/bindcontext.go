package main

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// Broadcaster security APIs (C.6.8), respondidas pela borda. As chaves ficam em
// bind-context:{serviceId}, LIST de JSON {"alg","key","registeredAt"(ms)},
// gravada so pelas funcoes do Redis (redis.go). Classe e access token de cada
// rota sao conferidos antes, pela politica da rota (evaluate).

// Menor modulo RSA (bytes) que comporta a assinatura PKCS#1 v1.5: DigestInfo
// (19 bytes de cabecalho + o hash) mais 11 de preenchimento. Com menos, a
// chave registra mas nenhum token valida; a Tabela C.49 preve 101 para "key
// format incompatible with the specified algorithm". A chave de 512 bits do
// exemplo da norma serve para RS256, nao para RS512.
var minRSAModulusBytes = map[string]int{algRS256: 19 + 32 + 11, algRS512: 19 + 64 + 11}

// Teto do corpo do registro: o padrao do express.json (100 KiB).
const maxRegisterBody = 100 << 10

// checkRegistration: corpo do POST /tv3/bind-context (Tabela C.49), nas regras
// do express.json: 101 se o corpo nao for objeto JSON (inclusive Content-Type
// que nao e application/json), se o alg nao for suportado ou se a chave for
// incompativel com ele; 105 se faltar alg ou key. Corpo vazio com Content-Type
// JSON vale {} e da 105.
func checkRegistration(r *http.Request) (alg, key string, code int, detail string) {
	const notObject = "message body must be a JSON object with alg and key, body"
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return "", "", 101, notObject
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRegisterBody+1))
	if err != nil {
		return "", "", 101, "message body could not be read, body"
	}
	if len(raw) > maxRegisterBody {
		return "", "", 101, fmt.Sprintf("message body larger than %d bytes, body", maxRegisterBody)
	}
	obj := map[string]interface{}{}
	if len(raw) > 0 {
		if !json.Valid(raw) {
			return "", "", 101, "message body is not valid JSON, body"
		}
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", "", 101, "message body is not valid JSON, body"
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return "", "", 101, notObject
		}
		obj = m
	}
	algV, hasAlg := obj["alg"]
	keyV, hasKey := obj["key"]
	var missing []string
	if !hasAlg {
		missing = append(missing, "alg")
	}
	if !hasKey {
		missing = append(missing, "key")
	}
	if len(missing) > 0 {
		return "", "", 105, strings.Join(missing, ", ")
	}
	alg, ok := algV.(string)
	if !ok || !supportedAlg(alg) {
		return "", "", 101, "algorithm not supported (HS256, HS512, RS256, RS512), alg"
	}
	key, ok = keyV.(string)
	if !ok {
		return "", "", 101, "key must be a string, key"
	}
	material, err := parseKeyMaterial(alg, key)
	if err != nil {
		return "", "", 101, fmt.Sprintf("key incompatible with %s: %v, key", alg, err)
	}
	if pub, ok := material.(*rsa.PublicKey); ok {
		if min := minRSAModulusBytes[alg]; pub.Size() < min {
			return "", "", 101, fmt.Sprintf("key incompatible with %s: RSA modulus of %d bits is below %d, key", alg, pub.N.BitLen(), min*8)
		}
	}
	return alg, key, 0, ""
}

// storedEntry: o elemento gravado em bind-context:{serviceId}.
type storedEntry struct {
	Alg          string `json:"alg"`
	Key          string `json:"key"`
	RegisteredAt int64  `json:"registeredAt"`
}

// parseStoredEntry: elemento lido do Redis; ok=false se malformado (alg fora
// dos quatro ou key ausente/nao texto).
func parseStoredEntry(raw string) (storedKey, bool) {
	var e struct {
		Alg *string `json:"alg"`
		Key *string `json:"key"`
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil || e.Alg == nil || e.Key == nil || !supportedAlg(*e.Alg) {
		return storedKey{}, false
	}
	return storedKey{Alg: *e.Alg, Key: *e.Key}, true
}

// sameKey: mesma chave? Texto igual sem espacos nas pontas (cabecalho HTTP
// chega aparado) ou, para RSA, a mesma chave publica em outra codificacao.
// Casa o cabecalho "key" da revogacao (C.6.8.4), que nao traz alg: a
// revogacao de "s" remove tambem um segredo HS registrado como "s " (efeito
// aceito).
func sameKey(a, b string) bool {
	if strings.TrimSpace(a) == strings.TrimSpace(b) {
		return true
	}
	pa, err := parseRSAPublicKey(a)
	if err != nil {
		return false
	}
	pb, err := parseRSAPublicKey(b)
	if err != nil {
		return false
	}
	return pa.E == pb.E && pa.N.Cmp(pb.N) == 0
}

// sameRegisteredKey: duplicata no registro (C.6.8.2). HS: igualdade EXATA (a
// verificacao usa os bytes sem aparar, entao "s" e "s " sao segredos
// diferentes); RSA: a mesma chave publica (sameKey).
func sameRegisteredKey(alg, a, b string) bool {
	if alg == algHS256 || alg == algHS512 {
		return a == b
	}
	return sameKey(a, b)
}

// C.6.8.2 (Tabela C.49): registra {alg, key} para o servico corrente. Sem
// servico, 300 ("No DTV service currently in use"): a tabela nao lista erro
// para esse caso.
//
// Leitura e escrita nao sao atomicas: dois registros simultaneos da mesma chave
// podem gravar uma duplicata, inofensiva (qualquer chave da lista valida; a
// revogacao remove todas as iguais). A duplicata e detectada aqui porque chaves
// RSA se comparam pela chave publica, o que a funcao do Redis nao faz.
func (h *authHandler) bindRegister(r *http.Request, _ map[string]string) edgeReply {
	alg, key, code, detail := checkRegistration(r)
	if code != 0 {
		return edgeReply{Code: code, Detail: detail}
	}
	sid, err := h.store.CurrentServiceID()
	if err != nil {
		return redisDown(err)
	}
	if sid == "" {
		return edgeReply{Code: 300, Detail: "no DTV service selected for the bind context"}
	}
	// serviceContextId = globalServiceId do servico corrente (Tabela 6, C.3.5)
	ok := edgeReply{Body: struct {
		ServiceContextID string `json:"serviceContextId"`
	}{sid}}

	raws, err := h.store.BindKeys(sid)
	if err != nil {
		return redisDown(err)
	}
	for _, raw := range raws {
		if e, valid := parseStoredEntry(raw); valid && e.Alg == alg && sameRegisteredKey(alg, e.Key, key) {
			logf("bind-context %s ja registrada para %q; nada a fazer", alg, sid)
			return ok
		}
	}
	entry, err := encodeJSON(storedEntry{Alg: alg, Key: key, RegisteredAt: h.now().UnixMilli()})
	if err != nil {
		return edgeReply{Code: 200, Detail: "falha interna da borda"}
	}
	if err := h.store.AddBindKey(sid, string(entry)); err != nil {
		return redisWriteFailed(err)
	}
	logf("bind-context %s registrada para %q", alg, sid)
	return ok
}

// Item de boundServices: mesma estrutura da resposta da C.6.3.1 (Tabela C.8,
// "serviceId": integer). session:current-service guarda o serviceId em texto;
// aqui ele volta a numero quando eh numerico.
type boundService struct {
	ServiceContextID string  `json:"serviceContextId"`
	ServiceName      string  `json:"serviceName,omitempty"`
	ServiceID        *uint64 `json:"serviceId,omitempty"`
}

// C.6.8.3 (Tabela C.50): servicos, entre todos com chave registrada, cujas
// chaves validam o bind-token. Aqui o bind-token e argumento da API, e nao
// credencial da rota. 104 sem o cabecalho; 108 se nao e JWT; 101 se nenhuma
// chave valida a assinatura; 108 se ela confere mas o token esta fora do prazo
// (a Tabela C.50 nao trata o caso; a assinatura vem antes, C.4.1.4). Lista
// ilegivel ou entrada malformada e ignorada com log.
func (h *authHandler) bindList(r *http.Request, _ map[string]string) edgeReply {
	tok := strings.TrimSpace(r.Header.Get("bind-token"))
	if tok == "" {
		return edgeReply{Code: 104, Detail: "missing header, bind-token"}
	}
	t, err := parseJWT(tok)
	if err != nil {
		return edgeReply{Code: 108, Detail: "bind-token is not a valid JWT (" + err.Error() + ")"}
	}
	sids, err := h.store.BindServices()
	if err != nil {
		return redisDown(err)
	}
	var matched []string
	if supportedAlg(t.alg) {
		for _, sid := range sids {
			raws, err := h.store.BindKeys(sid)
			if err != nil {
				var re redisError
				if errors.As(err, &re) {
					logf("AVISO %s%s ilegivel (%v) — ignorada", keyBindPrefix, sid, err)
					continue
				}
				return redisDown(err)
			}
			for _, k := range h.keys.decodeStoredKeys(sid, raws) {
				if k.Alg == t.alg && t.verifySignature(k.Alg, k.material) {
					matched = append(matched, sid)
					break
				}
			}
		}
	}
	if len(matched) == 0 {
		return edgeReply{Code: 101, Detail: "signature not validated by any registered key, bind-token"}
	}
	if err := t.checkTime(h.now(), false, true); err != nil {
		return edgeReply{Code: 108, Detail: err.Error()}
	}

	// Nome e id so se conhecem para o servico corrente (session:current-
	// service); para os demais nao ha fonte no testbed e os campos sao
	// omitidos (C.3.2.2).
	cur, err := h.store.CurrentServiceID()
	if err != nil {
		return redisDown(err)
	}
	var info map[string]string
	for _, sid := range matched {
		if cur != "" && sid == cur {
			if info, err = h.store.CurrentService(); err != nil {
				return redisDown(err)
			}
			break
		}
	}
	items := make([]boundService, 0, len(matched))
	for _, sid := range matched {
		it := boundService{ServiceContextID: sid}
		if cur != "" && sid == cur && (info["serviceContextId"] == "" || info["serviceContextId"] == sid) {
			it.ServiceName = info["serviceName"]
			if id, ok := numericServiceID(info["serviceId"]); ok {
				it.ServiceID = &id
			}
		}
		items = append(items, it)
	}
	return edgeReply{Body: struct {
		BoundServices []boundService `json:"boundServices"`
	}{items}}
}

func numericServiceID(s string) (uint64, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// C.6.8.4 (Tabela C.51): remove do servico corrente toda entrada com a mesma
// chave (o cabecalho "key" nao traz alg). Sucesso mesmo se a chave nao existe
// ou nao ha servico corrente. Nao libera os recursos compartilhados da
// aplicacao, como pede a C.4.4.
func (h *authHandler) bindRemove(r *http.Request, _ map[string]string) edgeReply {
	key := strings.TrimSpace(r.Header.Get("key"))
	if key == "" {
		return edgeReply{Code: 105, Detail: "key"}
	}
	empty := edgeReply{Body: struct{}{}}
	sid, err := h.store.CurrentServiceID()
	if err != nil {
		return redisDown(err)
	}
	if sid == "" {
		logf("bind-context revogacao sem servico corrente: nada a remover")
		return empty
	}
	raws, err := h.store.BindKeys(sid)
	if err != nil {
		return redisDown(err)
	}
	seen := map[string]bool{}
	var removed int64
	for _, raw := range raws {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		if e, ok := parseStoredEntry(raw); ok && sameKey(e.Key, key) {
			n, err := h.store.RemoveBindKey(sid, raw)
			if err != nil {
				return redisWriteFailed(err)
			}
			removed += n
		}
	}
	logf("bind-context revogacao em %q: %d entrada(s) removida(s)", sid, removed)
	return empty
}
