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

// Broadcaster security APIs (C.6.8), answered by the edge. The keys are in
// bind-context:{serviceId}, a LIST of JSON {"alg","key","registeredAt"},
// registeredAt in ms.

// Smallest RSA modulus, in bytes, that holds a PKCS#1 v1.5 signature:
// DigestInfo (19 header bytes plus the hash) plus 11 bytes of padding.
var minRSAModulusBytes = map[string]int{algRS256: 19 + 32 + 11, algRS512: 19 + 64 + 11}

// Maximum size of the registration body, 100 KiB.
const maxRegisterBody = 100 << 10

// checkRegistration checks the body of POST /tv3/bind-context (Table C.49):
// 101 if the body is not a JSON object, or the Content-Type is not
// application/json, if the alg is not supported or if the key is
// incompatible with it; 105 if alg or key is missing. An empty body with a
// JSON Content-Type is read as {} and gives 105.
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

// storedEntry is the element written to bind-context:{serviceId}.
type storedEntry struct {
	Alg          string `json:"alg"`
	Key          string `json:"key"`
	RegisteredAt int64  `json:"registeredAt"`
}

// parseStoredEntry decodes an element read from Redis. Returns false if it
// is malformed: alg not supported, or key missing or not a string.
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

// sameKey reports whether two keys are the same: equal text after trimming
// the spaces at the ends or, for RSA, the same public key in any encoding.
// No alg is compared.
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

// sameRegisteredKey reports whether a registered key duplicates another
// (C.6.8.2): for HS, exact equality, since the signature check uses the
// untrimmed bytes; for RSA, the same public key (sameKey).
func sameRegisteredKey(alg, a, b string) bool {
	if alg == algHS256 || alg == algHS512 {
		return a == b
	}
	return sameKey(a, b)
}

// C.6.8.2, Table C.49: registers {alg, key} for the current service; an
// already registered key is not added again. Without a current service,
// 300.
//
// Reading and writing are not atomic: two concurrent registrations of the
// same key may store it twice.
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
	// serviceContextId is the globalServiceId of the current service (Table 6,
	// C.3.5).
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

// boundService is an item of boundServices, with the structure of the
// response of C.6.3.1 (Table C.8, "serviceId": integer). The serviceId,
// stored as text, is set only when it is numeric.
type boundService struct {
	ServiceContextID string  `json:"serviceContextId"`
	ServiceName      string  `json:"serviceName,omitempty"`
	ServiceID        *uint64 `json:"serviceId,omitempty"`
}

// C.6.8.3, Table C.50: lists the services, among all with a registered
// key, whose keys validate
// the bind-token, here an argument of the API and not the credential of the
// route. 104 without the header; 108 if it is not a JWT; 101 if no key
// validates the signature; 108 if the signature is valid but the token is
// out of its validity period. An unreadable list or a malformed entry is
// logged and skipped.
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

	// Name and id are set only for the current service
	// (session:current-service), and omitted for the others (C.3.2.2).
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

// C.6.8.4, Table C.51: removes from the current service every entry with
// the same key, whatever its alg.
// Succeeds even if the key does not exist or there is no current service.
// TODO: release the shared resources of the application (C.4.4).
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
