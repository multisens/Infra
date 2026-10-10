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

// Broadcaster security APIs (C.6.8), RESPONDIDAS pela borda (decisao da
// reuniao de 05/10 com o Joel, D-0510-2). Mesmo contrato que o tv3ws
// implementava em src/api/broadcaster-security (removido de la na mesma
// rodada), inclusive o armazenamento:
//
//	bind-context:{serviceId}  LIST de JSON {"alg","key","registeredAt"(ms)}
//
// serviceId = session:current-service-id no momento da chamada. A classe que
// pode chamar cada rota (106: POST/DELETE so associado; GET so autonomo e
// nao local) e o access token do GET (107) ficam na politica da rota
// (routes.json), aplicada antes por evaluate.

// Menor modulo RSA (bytes) que comporta a assinatura PKCS#1 v1.5: DigestInfo
// (19 bytes de cabecalho + o hash) mais 11 de preenchimento. Com menos, a
// chave registra mas nenhum token valida — a Tabela C.47 preve 101 para "key
// format incompatible with the specified algorithm". A chave de 512 bits do
// exemplo da norma (64 bytes) serve para RS256, nao para RS512.
var minRSAModulusBytes = map[string]int{algRS256: 19 + 32 + 11, algRS512: 19 + 64 + 11}

// Teto do corpo do registro: o mesmo padrao do express.json do tv3ws.
const maxRegisterBody = 100 << 10

// checkRegistration: corpo do POST /tv3/bind-context (Tabela C.47), nas
// mesmas regras do tv3ws (checkRegistration + express.json): 101 se o corpo
// nao for objeto JSON (inclusive Content-Type que nao eh application/json, o
// que o express.json nao le), se o alg nao for suportado ou se a chave for
// incompativel com o alg; 105 se faltar alg ou key num objeto JSON. Corpo
// vazio com Content-Type JSON vale {} (o express.json faz isso) e da 105.
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
// dos quatro ou key ausente/nao texto), como o parseStoredEntry do tv3ws.
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
// aceito, igual ao tv3ws).
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

// C.6.8.2 (Tabela C.47): registra {alg, key} para o servico corrente.
// Ordem: corpo (101/105) -> servico corrente (300) -> grava. A norma nao lista
// erro para "sem servico" na C.47; 300 eh o codigo do catalogo para "No DTV
// service currently in use" (mesma escolha do tv3ws).
//
// Leitura e escrita nao sao atomicas (LRANGE e depois RPUSH, como no tv3ws):
// dois registros simultaneos da mesma chave podem gravar uma duplicata, que
// nao muda o resultado (qualquer chave da lista valida; a revogacao remove
// todas as iguais).
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
	ok := edgeReply{Body: struct {
		ServiceContextID string `json:"serviceContextId"`
	}{h.cfg.ServiceContextID}}

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
		return redisDown(err)
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

// C.6.8.3 (Tabela C.48): servicos cujas chaves validam o bind-token, de
// TODOS os servicos com chave registrada (SCAN bind-context:*). Codigos: 104
// sem o cabecalho; 108 se nao eh JWT; 101 se nenhuma chave registrada valida
// a assinatura; 108 se a assinatura confere mas o token esta fora do prazo
// (nbf/exp/iat — a C.48 eh silente; Tabela C.1: "invalid"). A assinatura vem
// antes do prazo (C.4.1.4). Lista ilegivel (outro tipo no Redis) ou entrada
// malformada eh ignorada com log.
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
	// PENDENTE (Joel): serviceContextId eh a constante do tv3ws, igual para
	// todo servico (L2) — itens de servicos diferentes saem indistinguiveis.
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
		it := boundService{ServiceContextID: h.cfg.ServiceContextID}
		if cur != "" && sid == cur {
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

// numericServiceID: o serviceId so vira inteiro quando eh so digitos (o
// tv3ws usava /^[0-9]+$/ e Number()).
func numericServiceID(s string) (uint64, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// C.6.8.4 (Tabela C.49): remove do servico corrente toda entrada com a mesma
// chave (o cabecalho "key" nao traz alg). Sucesso mesmo se a chave nao existe
// — inclusive sem servico corrente, quando nao ha lista de que remover.
// PENDENTE (Joel): ao revogar, a C.4.4 manda liberar os recursos
// compartilhados que a aplicacao usava — nao implementado (L7).
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
				return redisDown(err)
			}
			removed += n
		}
	}
	logf("bind-context revogacao em %q: %d entrada(s) removida(s)", sid, removed)
	return empty
}
