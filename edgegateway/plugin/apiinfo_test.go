package main

import "testing"

// C.6.7.8 (Tabela C.45) e C.6.7.9 (Tabela C.46), respondidas pela borda a
// partir do catalogo "apis" (testExtra: 6 APIs, todas tv3ws, versao 2.0).

func apiGet(t *testing.T, h *authHandler, path string, hdr map[string]string) edgeResp {
	if hdr == nil {
		hdr = map[string]string{"Authorization": accessFor(t, "cli-1", classAutonomous)}
	}
	return edgeCall(t, h, "GET", path, hdr, "")
}

const allAPIs = `{"receiverApis":[` +
	`{"id":"tv3ws-application-authorization","version":"2.0"},` +
	`{"id":"tv3ws-current-service","version":"2.0"},` +
	`{"id":"tv3ws-api-info","version":"2.0"},` +
	`{"id":"tv3ws-bind-context-register","version":"2.0"},` +
	`{"id":"tv3ws-bind-context-list","version":"2.0"},` +
	`{"id":"tv3ws-bind-context-remove","version":"2.0"}]}`

func TestAPIInfo(t *testing.T) {
	h := newTestHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "api conhecida", apiGet(t, h, "/tv3/api-info/tv3ws-current-service", nil),
		`{"receiverApi":{"id":"tv3ws-current-service","version":"2.0"}}`)
	wantOK(t, "a propria api-info", apiGet(t, h, "/tv3/api-info/tv3ws-api-info", nil),
		`{"receiverApi":{"id":"tv3ws-api-info","version":"2.0"}}`)
	// id da Tabela C.2 que o testbed nao implementa: 101 (provisorio)
	wantErr(t, "id da Tabela C.2 nao implementado", apiGet(t, h, "/tv3/api-info/tv3ws-geolocation-info", nil), 101)
	wantErr(t, "id inventado", apiGet(t, h, "/tv3/api-info/nao-existe", nil), 101)
	wantErr(t, "id com caixa trocada", apiGet(t, h, "/tv3/api-info/TV3WS-CURRENT-SERVICE", nil), 101)
	// barra codificada vira segmento a mais: rota nao declarada
	wantErr(t, "id com barra", apiGet(t, h, "/tv3/api-info/a%2Fb", nil), 100)
}

func TestAPIList(t *testing.T) {
	h := newTestHandler(t, modeEnforce, baseStore(), nil)
	wantOK(t, "sem subsystem", apiGet(t, h, "/tv3/api-info", nil), allAPIs)
	wantOK(t, "subsystem=tv3ws", apiGet(t, h, "/tv3/api-info?subsystem=tv3ws", nil), allAPIs)
	wantOK(t, "subsystem=ncl (conhecido, nada implementado)", apiGet(t, h, "/tv3/api-info?subsystem=ncl", nil), `{"receiverApis":[]}`)
	wantOK(t, "subsystem=nclua", apiGet(t, h, "/tv3/api-info?subsystem=nclua", nil), `{"receiverApis":[]}`)
	wantOK(t, "outro parametro eh ignorado", apiGet(t, h, "/tv3/api-info?x=1", nil), allAPIs)
	wantErr(t, "subsystem desconhecido", apiGet(t, h, "/tv3/api-info?subsystem=dtv", nil), 101)
	wantErr(t, "subsystem vazio", apiGet(t, h, "/tv3/api-info?subsystem=", nil), 101)
	wantErr(t, "subsystem com caixa trocada", apiGet(t, h, "/tv3/api-info?subsystem=TV3WS", nil), 101)
	wantErr(t, "query malformada", apiGet(t, h, "/tv3/api-info?subsystem=%zz", nil), 101)

	// catalogo vazio: lista vazia, nao null
	cfg := testConfig(t, modeEnforce)
	cfg.APIs = &apiCatalog{byID: map[string]apiEntry{}}
	h2 := newHandler(cfg, baseStore(), nil)
	wantOK(t, "catalogo vazio", apiGet(t, h2, "/tv3/api-info", map[string]string{"Origin": tAssoc}), `{"receiverApis":[]}`)
}

// 107 ao nao local/autonomo sem access token valido; o associado dispensa
// (C.4.1.1); todas as classes podem chamar.
func TestAPIInfoPolitica(t *testing.T) {
	h := newTestHandler(t, modeEnforce, baseStore(), nil)
	for _, p := range []string{"/tv3/api-info", "/tv3/api-info/tv3ws-api-info"} {
		wantErr(t, p+" sem token", apiGet(t, h, p, map[string]string{}), 107)
		wantErr(t, p+" token lixo", apiGet(t, h, p, map[string]string{"Authorization": "Bearer lixo"}), 107)
		for name, hd := range map[string]map[string]string{
			"associado pelo Origin": {"Origin": tAssoc},
			"token de nao local":    {"Authorization": accessFor(t, "cli-2", classNonLocal)},
			"token de associado":    {"Authorization": accessFor(t, "cli-3", classAssociated)},
		} {
			if r := apiGet(t, h, p, hd); r.status != 200 {
				t.Errorf("%s %s: status %d %s", p, name, r.status, r.body)
			}
		}
		wantErr(t, p+" Accept-Version 9.9", apiGet(t, h, p, map[string]string{"Origin": tAssoc, "Accept-Version": "9.9"}), 100)
	}
	// warn: sem token so avisa, e a borda responde
	h = newTestHandler(t, modeWarn, baseStore(), nil)
	r := apiGet(t, h, "/tv3/api-info", map[string]string{})
	wantOK(t, "warn sem token", r, allAPIs)
	if r.hdr.Get(warnHeader) != "107" {
		t.Errorf("warn: %s=%q", warnHeader, r.hdr.Get(warnHeader))
	}
}
