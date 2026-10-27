package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// APIs answered by the edge itself (the "edge" field of the route), after
// the same decision as the other routes and the version negotiation.

const (
	// POST /tv3/bind-context (C.6.8.2)
	edgeBindRegister = "bind-context-register"
	// GET /tv3/bind-context (C.6.8.3)
	edgeBindList = "bind-context-list"
	// DELETE /tv3/bind-context (C.6.8.4)
	edgeBindRemove = "bind-context-remove"
	// GET /tv3/api-info/{apiId} (C.6.7.8)
	edgeAPIInfo = "api-info"
	// GET /tv3/api-info (C.6.7.9)
	edgeAPIList = "api-list"
)

// edgeReply is the result of an edge API: Code 0 is success, status 200 with
// Body as JSON; any other Code is an error in the format of C.3.2.
type edgeReply struct {
	Code   int
	Detail string
	Body   interface{}
}

type edgeFunc func(h *authHandler, r *http.Request, params map[string]string) edgeReply

var edgeHandlers = map[string]edgeFunc{
	edgeBindRegister: (*authHandler).bindRegister,
	edgeBindList:     (*authHandler).bindList,
	edgeBindRemove:   (*authHandler).bindRemove,
	edgeAPIInfo:      (*authHandler).apiInfo,
	edgeAPIList:      (*authHandler).apiList,
}

func edgeHandlerNames() string {
	names := make([]string, 0, len(edgeHandlers))
	for n := range edgeHandlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Version negotiation (C.3.6.5), on every route, against the versions field
// of routes.json, 2.0 when absent (C.3.6.3): without Accept-Version, 2.0;
// outside the X.Y format, 101; a version the route does not serve, 100.
// Versions from 100 on are testbed extensions. The negotiated version goes
// to the service in X-TV30-API-Version and, for an extension, also in the
// path (internalPath).
const (
	defaultVersion        = "2.0"
	extensionMajor        = 100
	internalVersionHeader = "X-TV30-API-Version"
	// Original Host header of the client request.
	internalHostHeader = "X-TV30-Host"
	// Class of the local client (associated or standAlone) and, for the
	// stand-alone one, its clientid; absent for any other client.
	internalClientClassHeader = "X-TV30-Client-Class"
	internalClientIDHeader    = "X-TV30-Client-Id"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// Prefix of the internal path (internalPath); no declared route starts with
// it.
var internalPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(/|$)`)

// compareVersions compares two "X.Y" versions numerically (C.3.6.2): 2.10 is
// newer than 2.9.
func compareVersions(a, b string) int {
	am, an := splitVersion(a)
	bm, bn := splitVersion(b)
	if am != bm {
		return am - bm
	}
	return an - bn
}

func splitVersion(v string) (int, int) {
	maj, min, _ := strings.Cut(v, ".")
	x, _ := strconv.Atoi(maj)
	y, _ := strconv.Atoi(min)
	return x, y
}

func negotiateVersion(r *http.Request, served []string) (version string, code int, detail string) {
	vals := r.Header.Values("Accept-Version")
	v := defaultVersion
	if len(vals) > 0 {
		// Several headers are joined with ", ", which is malformed.
		v = strings.Join(vals, ", ")
		if !versionPattern.MatchString(v) {
			return "", 101, "malformed Accept-Version '" + v + "'"
		}
	}
	for _, s := range served {
		if v == s {
			return v, 0, ""
		}
	}
	return "", 100, "unsupported version '" + v + "' for this API (served: " + strings.Join(served, ", ") + ")"
}

func isExtensionVersion(v string) bool {
	major, _ := splitVersion(v)
	return major >= extensionMajor
}

// internalPath returns the path from the edge to the service: for a testbed
// extension, /v<X.Y> followed by the public path; otherwise, the public path.
func internalPath(path, version string) string {
	if !isExtensionVersion(version) {
		return path
	}
	// No trailing slash, so that a redirect by the router does not show the
	// internal path to the client.
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return "/v" + version + path
}

// toInternalPath replaces the request path with the internal path. The URL
// is copied, so the URL it replaces is not changed.
func toInternalPath(r *http.Request, version string) {
	if !isExtensionVersion(version) {
		return
	}
	u := *r.URL
	u.Path = internalPath(u.Path, version)
	if u.RawPath != "" {
		u.RawPath = internalPath(u.RawPath, version)
	}
	r.URL = &u
}

// versionInfo holds the versions served by the requested route and the
// latest version of the server, kept in the request context.
type versionInfo struct {
	served []string
	latest string
}

type versionCtxKey struct{}

func withVersionInfo(r *http.Request, vi versionInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), versionCtxKey{}, vi))
}

func versionInfoOf(r *http.Request) versionInfo {
	if vi, ok := r.Context().Value(versionCtxKey{}).(versionInfo); ok {
		return vi
	}
	return versionInfo{served: []string{defaultVersion}, latest: defaultVersion}
}

// serveEdge answers a route of the edge. A warn mode header already set is
// kept.
func (h *authHandler) serveEdge(w http.ResponseWriter, r *http.Request, v verdict, version string) {
	reply := h.runEdge(edgeHandlers[v.rt.Edge], r, v.params)
	if reply.Code != 0 {
		logf("API surface=%s code=%d %s %q detalhe=%q", h.cfg.Surface, reply.Code, r.Method, r.URL.Path, reply.Detail)
		writeC32(w, r, reply.Code, reply.Detail)
		return
	}
	body, err := encodeJSON(reply.Body)
	if err != nil {
		logf("API surface=%s code=200 %s %q resposta nao serializa: %v", h.cfg.Surface, r.Method, r.URL.Path, err)
		writeC32(w, r, 200, "falha interna da borda")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Access-Control-Allow-Origin", "*")
	hd.Set("API-Version", version)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// runEdge runs the handler, and returns error 200 if it panics.
func (h *authHandler) runEdge(fn edgeFunc, r *http.Request, params map[string]string) (reply edgeReply) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler {
			panic(p)
		}
		logf("PANIC recuperado na API da borda surface=%s %s %q: %v", h.cfg.Surface, r.Method, r.URL.Path, p)
		reply = edgeReply{Code: 200, Detail: "falha interna da borda"}
	}()
	return fn(h, r, params)
}

// redisDown returns error 200, "Platform resource unavailable" (Table C.1),
// for a Redis failure in an edge API.
func redisDown(err error) edgeReply {
	return edgeReply{Code: 200, Detail: "redis indisponivel (" + err.Error() + ")"}
}

// redisWriteFailed returns error 200 for a failed write (C.6.8.2, C.6.8.4);
// for a refusal by the Redis schema, with the refusal in the detail.
func redisWriteFailed(err error) edgeReply {
	if msg, ok := schemaRefusal(err); ok {
		return edgeReply{Code: 200, Detail: "escrita recusada pelo esquema do Redis (" + msg + ")"}
	}
	return redisDown(err)
}

// encodeJSON encodes v as JSON without escaping <, > and &, and without a
// trailing newline.
func encodeJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
