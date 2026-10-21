package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// APIs de informacao de API (C.6.7.8, Tabela C.47; C.6.7.9, Tabela C.48), a
// partir do campo "apis" do bloco do plugin (ids da Tabela C.2, na ordem dela).
// Cada API informa uma versao so, a "latest supported version": a mais recente
// que as rotas dela servem.

// Subsistemas da Tabela C.48. O testbed so implementa APIs tv3ws; ncl e
// nclua sao conhecidos (lista vazia, nao 101).
var apiSubsystems = []string{"ncl", "nclua", "tv3ws"}

// Formato dos ids da Tabela C.2 ("tv3ws-current-service"): <subsistema>-<nome>.
var apiIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// Versao no formato "X.Y" (C.3.6.2): X inteiro > 0, Y inteiro >= 0.
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

// loadAPICatalog le o campo "apis" do bloco do plugin. Qualquer valor
// invalido eh erro (morre-inteiro, como o resto da config).
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

// C.6.7.8: 101 "If <api-id> does not correspond to a valid API identification
// string", tambem para id da Tabela C.2 nao implementado (a Tabela C.47 nao
// preve outro erro, e nao ha versao de uma API que o receptor nao tem).
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

// C.6.7.9: com subsystem, so as daquele subsistema (desconhecido ou vazio =>
// 101). Sem subsystem, que a Tabela C.48 deixa opcional sem dizer o que
// listar, lista todas.
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
		out = []apiEntry{} // "receiverApis": [] e nao null
	}
	return edgeReply{Body: struct {
		ReceiverAPIs []apiEntry `json:"receiverApis"`
	}{out}}
}
