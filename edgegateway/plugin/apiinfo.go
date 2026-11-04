package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// API information APIs (C.6.7.8, Table C.47; C.6.7.9, Table C.48), answered
// from the "apis" field of the plugin block: ids of Table C.2, in its order,
// one version per API.

// Subsystems of Table C.48. A subsystem with no implemented API gives an
// empty list.
var apiSubsystems = []string{"ncl", "nclua", "tv3ws"}

// Format of the ids of Table C.2, <subsystem>-<name>, as in
// "tv3ws-current-service".
var apiIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// Version format "X.Y" (C.3.6.2): integer X > 0, integer Y >= 0.
var apiVersionPattern = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]+$`)

type apiEntry struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type apiCatalog struct {
	list []apiEntry
	byID map[string]apiEntry
}

func knownSubsystem(s string) bool {
	for _, k := range apiSubsystems {
		if s == k {
			return true
		}
	}
	return false
}

func subsystemOf(id string) string {
	s, _, _ := strings.Cut(id, "-")
	return s
}

// loadAPICatalog reads the "apis" field of the plugin block. Returns an
// error for any invalid value.
func loadAPICatalog(raw interface{}) (*apiCatalog, error) {
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("apis nao eh lista: %v", raw)
	}
	cat := &apiCatalog{list: []apiEntry{}, byID: map[string]apiEntry{}}
	for i, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("apis[%d] nao eh objeto", i)
		}
		id, _ := m["id"].(string)
		version, _ := m["version"].(string)
		if !apiIDPattern.MatchString(id) || !knownSubsystem(subsystemOf(id)) {
			return nil, fmt.Errorf("apis[%d].id %q invalido (<subsistema>-<nome>, subsistemas %s)", i, id, strings.Join(apiSubsystems, ", "))
		}
		if !apiVersionPattern.MatchString(version) {
			return nil, fmt.Errorf("apis[%d].version %q invalida (X.Y, C.3.6.2)", i, version)
		}
		if _, dup := cat.byID[id]; dup {
			return nil, fmt.Errorf("apis[%d].id %q duplicado", i, id)
		}
		e := apiEntry{ID: id, Version: version}
		cat.list = append(cat.list, e)
		cat.byID[id] = e
	}
	return cat, nil
}

// C.6.7.8 - Querying individual API information. Answers 101 for an id that
// is not an implemented API, including an id of Table C.2.
func (h *authHandler) apiInfo(_ *http.Request, params map[string]string) edgeReply {
	id := params["apiId"]
	e, ok := h.cfg.APIs.byID[id]
	if !ok {
		return edgeReply{Code: 101, Detail: fmt.Sprintf("'%s' is not the id of an API implemented by this receiver (Table C.2), api-id", id)}
	}
	return edgeReply{Body: struct {
		ReceiverAPI apiEntry `json:"receiverApi"`
	}{e}}
}

// C.6.7.9 - Querying the list of APIs in a subsystem. With subsystem, lists
// the APIs of that subsystem, and answers 101 if it is unknown or empty;
// without it, lists all APIs.
func (h *authHandler) apiList(r *http.Request, _ map[string]string) edgeReply {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return edgeReply{Code: 101, Detail: "malformed query string"}
	}
	out := h.cfg.APIs.list
	if q.Has("subsystem") {
		sub := q.Get("subsystem")
		if !knownSubsystem(sub) {
			return edgeReply{Code: 101, Detail: fmt.Sprintf("unknown subsystem '%s' (%s), subsystem", sub, strings.Join(apiSubsystems, ", "))}
		}
		out = []apiEntry{}
		for _, e := range h.cfg.APIs.list {
			if subsystemOf(e.ID) == sub {
				out = append(out, e)
			}
		}
	}
	if out == nil {
		// Encodes "receiverApis" as [], not null.
		out = []apiEntry{}
	}
	return edgeReply{Body: struct {
		ReceiverAPIs []apiEntry `json:"receiverApis"`
	}{out}}
}
