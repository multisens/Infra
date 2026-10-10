package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// APIs de informacao de API (C.6.7.8, Tabela C.45; C.6.7.9, Tabela C.46),
// RESPONDIDAS pela borda (decisao da reuniao de 05/10 com o Joel, D-0510-3) a
// partir da tabela de rotas: o routes.json marca cada rota com o id da
// Tabela C.2 e a versao, e o generate.js entrega aqui a lista ja sem
// duplicatas e na ordem da Tabela C.2 (campo "apis" do bloco do plugin).
//
//   GET /tv3/api-info/<api-id>               -> {"receiverApi": {id, version}}
//   GET /tv3/api-info[?subsystem=ncl|nclua|tv3ws] -> {"receiverApis": [{id, version}...]}
//
// Erros das tabelas: 101 (id invalido; subsistema desconhecido) e 107 (access
// token do nao local/autonomo, aplicado pela politica da rota, auth=token).
// "Security requirements" e "Restrictions" = "—": todas as classes, sem
// bind-token (p. 300-301 do PDF da norma).
//
// PENDENTE (Joel): a versao de cada API eh a "Current version" da Tabela C.2
// (2.0 em todas, C.3.6.3). O tv3ws tambem aceita Accept-Version 2.1 (proposta
// do Forum, que muda GET /tv3/remote-device/devices/{classId}); se essa API
// deve aparecer aqui como 2.1 nao foi decidido (routes.json, campo api.version).

// Subsistemas da Tabela C.46. O testbed so implementa APIs tv3ws; ncl e
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

// subsystemOf: o prefixo do id ate o primeiro hifen ("tv3ws-...").
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

// C.6.7.8 (Tabela C.45). 101 "If <api-id> does not correspond to a valid API
// identification string".
// PENDENTE (Joel): id que esta na Tabela C.2 mas que o testbed nao implementa
// tambem da 101 (provisorio) — a tabela nao preve outro erro, e nao ha
// "ultima versao suportada" de uma API que o receptor nao tem.
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

// C.6.7.9 (Tabela C.46). Com subsystem: so as daquele subsistema;
// desconhecido (inclusive vazio) => 101; ncl e nclua sao conhecidos e o
// testbed nao implementa nenhuma API deles (lista vazia).
// PENDENTE (Joel): sem subsystem a norma nao diz o que listar (o parametro eh
// opcional na URL); provisorio: todas as APIs implementadas.
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
