# Mapa de tópicos MQTT — TV 3.0

> **Estado do código em 12/10/2026**, conferido por busca de `publish`, `subscribe` e `addTopicHandler` em `aop/src`, `aop/public/js`, `tv3ws/src`, `bcast/src` e `infra/mqtt-broker`; as linhas são as desta data, conferidas de novo depois das correções da tarde de 12/10 (fechamento do pop-up de pareamento, mensagem e tópico de `errors/<client_id>`, leitura do payload no plugin). Até 12/10 este mapa citava módulos do AoP com os nomes antigos (`disp-lyr`, hoje `graphic-overlays`), linhas que tinham mudado e tópicos de nó com formato que o código não usa, não listava o bcast nem o plugin do broker e dizia que só três tópicos eram retidos.
>
> O barramento MQTT e os nomes de tópico são decisões deste projeto; a ABNT NBR 25608 não define a comunicação interna do receptor. O mapa com a descrição de cada tópico também está em `docs/mqtt-topicos.md` da raiz do TV30.

## Clientes MQTT

| Client ID | Processo | Arquivo | Conexão |
|---|---|---|---|
| `aop-core` | AoP (servidor) | `aop/src/core.js`, linha 42 | `mqtt://${MQTT_HOST}` (no compose, `mosquitto:1883`) |
| `rp-display` | página da TV, no navegador | `aop/src/modules/graphic-overlays/view.ejs`, linha 148 | `ws://<host da página>/mqtt`, que o AoP repassa à 9001 do mosquitto (`aop/src/mqtt-ws.js`) |
| `tv3ws-client` | tv3ws | `tv3ws/src/mqtt-client.ts`, linha 7 | `mqtt://${MQTT_HOST}`, MQTT 5 |
| `bcast_svc` | bcast | `bcast/src/index.ts`, linha 52 | `mqtt://${MQTT_HOST}`, MQTT 5 |
| (sem cliente) | plugin C do broker | `infra/mqtt-broker/plugin/src/` | publica de dentro do broker (`mosquitto_broker_publish`) |

Todas as assinaturas dos três primeiros usam *no local* (`noLocal` no AoP e na página, `nl: true` no tv3ws): ninguém recebe a própria publicação. O tv3ws registra os tratadores por tópico exato (`addTopicHandler`, `tv3ws/src/mqtt-client.ts`, linhas 57–67), com QoS 1.

A coluna **Esquema** diz se o tópico tem esquema em `infra/mqtt-broker/plugin/config/schemas.json`, que o plugin confere na publicação ([09](09-schema-validation.md)).

---

## Sessão e usuário

### `aop/currentUser` — retido; esquema: string de até 64 caracteres

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `aop/src/core.js` | 246 | `setCurrentUser()`: grava antes o `lastAccess` do perfil; payload: id do perfil |
| **PUB** | `tv3ws/src/api/user/service.ts` | 113 | `setCurrentUser()` (C.6.14.4): grava antes `session:current-user` |
| **SUB** | `aop/src/core.js` | 93 (tratador na 233) | `loadCurrentUser()`: grava o `lastAccess` do perfil (`touchLastAccess`, D-0510-5) |
| **SUB** | `tv3ws/src/api/user/service.ts` | 72 | `updateCurrentUser()`: `session:current-user` (`tv30_sessao_usuario`) |

O payload é o id do perfil em texto puro (UUID desde a F-3, 11/10). De 11/10 a 12/10, o plugin do broker recusava o UUID que começa com dígito: lia o começo dele como número (`json_tokener_parse`) e o esquema `type: string` falhava (`Expected type 'string', got 'int'`), e a publicação não chegava aos assinantes. Desde a correção de 12/10, o payload que não é um texto JSON inteiro vale como string ([09](09-schema-validation.md)).

### `aop/currentService` — retido; esquema: string de até 256 caracteres

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `aop/src/core.js` | 273 | `setCurrentService()`: payload: id do serviço |
| **PUB** | `aop/src/core.js` | 279 | `unsetCurrentService()`: payload `''` |
| **SUB** | `aop/src/core.js` | 93 (tratador na 267) | `loadCurrentService()` |
| **SUB** | `tv3ws/src/api/user/service.ts` | 73 | `updateCurrentService()`: `session:current-service-id` (`tv30_sessao_servico_id`) |
| **SUB** | `tv3ws/src/core.ts` | 190 (tratador na 141) | `currentService()`: `session:current-service` (`tv30_sessao_servico`) e assinatura de `aop/{serviceId}/currentApp` |

### `aop/services` — sem publicador neste repositório

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **SUB** | `tv3ws/src/core.ts` | 198 (tratador na 192) | `loadServiceData()`: lista de serviços em JSON |

### `aop/users` — fora de uso

Desde 10/10 ninguém publica nem assina o tópico, e o esquema dele saiu do `schemas.json`. O tv3ws deixou de sincronizar perfis a partir do `userData.json` na reunião de 05/10 com o Joel (D-0510-5), e a plataforma (AoP) é a dona dos perfis.

---

## Dispositivos remotos

### `aop/devices/{devclass}` — retido; sem assinante neste repositório

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `tv3ws/src/modules/remotedevice-manager/manager.ts` | 87 | `addRemoteDevice()`: array JSON de handles, publicado depois de gravar o espelho no Redis (D-0510-6) |
| **PUB** | `tv3ws/src/modules/remotedevice-manager/manager.ts` | 110 | `removeRemoteDevice()`: array JSON de handles, ou `''` se não sobrou nenhum; também depois da remoção no Redis |

---

## Aplicação e nós — o tv3ws assina; sem publicador neste repositório

### `aop/{serviceId}/currentApp`

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **SUB** | `tv3ws/src/core.ts` | 166 (tratador na 88) | `setAppId()`: assinado na troca de serviço (`currentService()`). Desde 12/10, trocar de aplicação zera o diretório da aplicação (`core.app.url`) até chegar o `.../path` da nova, e a mensagem vazia encerra a aplicação corrente (tira os tratadores de `.../path` e `.../doc/nodes` e zera o diretório); antes, a mensagem vazia era ignorada, e a nova aplicação servia o diretório da anterior na C.6.4.7. `PENDENTE (Joel)`: o tópico não tem publicador neste repositório, e o contrato dele (como se sinaliza o fim da aplicação) não está escrito |

### `aop/{serviceId}/{appId}/path` e `aop/{serviceId}/{appId}/doc/nodes`

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **SUB** | `tv3ws/src/core.ts` | 113 | `.../path`: `setAppBaseURL()`, URL base da app (de onde a C.6.4.7 lê os arquivos) |
| **SUB** | `tv3ws/src/core.ts` | 119 | `.../doc/nodes`: `setAppNodes()`, array de nós; associa os nós aos dispositivos remotos |

### Tópicos de nó: `aop/{serviceId}/{appId}/doc/{nó}/...`

`{nó}` é o id do nó com `.` trocado por `/` (`tv3ws/src/modules/remotedevice-manager/remote-device.ts`, `setNode`). Os tópicos só existem enquanto um nó está associado a um dispositivo remoto.

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **SUB** | `.../remote-device.ts` | 355 | `{nó}/interfaces`: `setNodeInterfaces()`; a assinatura sai depois da primeira mensagem |
| **SUB** | `.../remote-device.ts` | 357–358 | `{nó}/preparationEvent/actionNotification` e `{nó}/presentationEvent/actionNotification`: `onMqttMessage()`, repassado ao dispositivo pelo WebSocket |
| **SUB** | `.../remote-device.ts` | 388 | `{nó}/{interface}/attributionEvent/value`: `setPropertyValue()`, uma por interface de propriedade; sai depois do primeiro valor |
| **PUB** | `.../remote-device.ts` | 220 | `{nó}[/{label}]/{eventType}Event[/OK]/eventNotification`: a transição vinda do dispositivo (`publishTransitionMetadata()`, linha 208); não retido |
| **PUB** | `.../remote-device.ts` | 223 | o mesmo prefixo + `/user`: usuário, só em `selection`; retido |
| **PUB** | `.../remote-device.ts` | 226 | o mesmo prefixo + `/value`: valor, só em `attribution`; retido |

O `/user` e o `/value` saem retidos porque o `publish` do tv3ws tem `retain = true` por padrão (`tv3ws/src/mqtt-client.ts`, linha 95), e as duas chamadas não passam o terceiro argumento; o `eventNotification` passa `false`.

---

## Camadas da tela

Publicadas pelo AoP sem retenção e assinadas pela página da TV (`aop/src/modules/graphic-overlays/view.ejs`).

| Tópico | Esquema | PUB (`aop/src/core.js`) | SUB (`view.ejs`) |
|---|---|---|---|
| `aop/display/layers/rxgui` | sim | 133, `setDisplayGui()`: caminho da tela do AoP | 169, `aop/display/layers/+` |
| `aop/display/layers/graphics` | sim | 161, `setDisplayGraphics()`: URL da app no host próprio, `http://<rótulo>.<APPS_DOMAIN>:<porta>/graphicsAppProxy/...` (P1.3), ou `''` | 169, `aop/display/layers/+` |
| `aop/display/layers/video/url` | sim | 169 e 172, `setVideoURL()`: `/videoStreamProxy/<arquivo>` para `.m3u8` e `.mpd`; senão a URL recebida | 170, `aop/display/layers/video/+` |
| `aop/display/layers/video/size` | sim | 177, `setVideoSize()`: JSON `{top, left, width, height}` | 170, `aop/display/layers/video/+` |

---

## Pop-ups

| Tópico | Esquema | PUB | SUB |
|---|---|---|---|
| `aop/display/layers/popup/yesno/message` | não | `tv3ws/src/core.ts`, 288, `showYesNoPopUpAsync()`: JSON `{value, timeout}`; não retido | `view.ejs`, 171, `aop/display/layers/popup/+/message` |
| `aop/display/layers/popup/yesno/response` | não | `aop/public/js/popup.js`, 87, 93 e 98: `"false"` (fim do tempo do pop-up), `"true"` (Sim) ou `"false"` (Não) | `tv3ws/src/core.ts`, 286: assinado em `showYesNoPopUpAsync()` e tirado na resposta ou no fim do tempo (269); só `"true"` autoriza |
| `aop/display/layers/popup/qrcode` | não | `tv3ws/src/core.ts`: 295, `showQRCodePopUp()`, abertura, JSON `{value, timeout}`, com prazo de 120 s desde 12/10; 316, `closeQRCodePopUp()`, fechamento, `{"close":true}`, no fim do pareamento (desde 12/10). Não retido | `view.ejs`, 176, `aop/display/layers/popup/+` |
| `aop/display/layers/popup/pin` | não | `tv3ws/src/core.ts`: 301, `showPINPopUp()`, abertura, JSON `{value, timeout}`, com prazo de 120 s desde 12/10; 321, `closePINPopUp()`, fechamento, `{"close":true}`, no fim do pareamento (desde 12/10). Não retido | `view.ejs`, 176, `aop/display/layers/popup/+` |

A página trata as mensagens de pop-up em `handlePopupMessage` (`aop/public/js/popup.js`, linha 28), chamada por `mqtt_message` (`view.ejs`, linha 261). Desde 12/10 (correção do pareamento, C.4.3.2 e C.4.3.3):
- **fechamento:** `{"close":true}` no tópico do QR code ou do PIN fecha o pop-up aberto daquele tipo; com pop-up de outro tipo na tela, ou sem pop-up, nada muda. No sim/não, o `{"close":true}` é ignorado. Quem publica é o tv3ws, no fim do pareamento (o `/tv3/token` com o desafio resolvido), e só se o pop-up daquele tipo ainda for do cliente que pareou (`endPairing`, em `tv3ws/src/api/client-identification/service.ts`). Abertura e fechamento saem do mesmo cliente MQTT, no mesmo tópico e com a mesma QoS, e chegam nessa ordem;
- **um pop-up só:** o pop-up novo substitui o da tela, com o prazo e os botões dele (`presentPopup`), e o prazo do anterior deixa de valer. Um sim/não substituído não publica resposta (o tv3ws o dá por expirado no fim dos 10 s dele);
- **mensagem inválida** (JSON quebrado, não objeto, sem `value` de texto, tipo desconhecido) vai para o console e é ignorada. Desde 12/10 o `client.on('message')` da página também captura exceções (`view.ejs`, linhas 158–164): antes, um JSON inválido no tópico do QR code fazia a página parar de tratar todas as mensagens seguintes (medido no Chrome 154);
- **escape:** o texto do sim/não (que leva o `display-name` do cliente) e o PIN entram no HTML escapados (`escapeHtml`); o QR code é desenhado pela biblioteca a partir do texto.

---

## Sinalização (LLS e SLS) e evento de vídeo — publicados pelo bcast

### `tlm/lls/{bsid}/bamt` — retido; esquema `tlm/lls/+/bamt`

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `bcast/src/index.ts` | 105 | BAMT em JSON; `''` na limpeza (linha 137) |
| **SUB** | `aop/src/core.js` | 93 (tratador na 288) | `tlm/lls/#`: `loadLLSMetadata()`, que só trata o último nível `bamt`; com payload vazio, limpa a lista de serviços e assina de novo |

`{bsid}` é o `BSID` do bcast (no compose da raiz, `tv30-default`).

### `tlm/sls/{serviceId}/esg` e `tlm/sls/{serviceId}/bald` — retidos; esquemas `tlm/sls/+/esg` e `tlm/sls/+/bald`

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `bcast/src/modules/eduplay/index.ts` | 37–38 (`''` em 43–44) | ESG e BALD do serviço; vazio ao descartar |
| **PUB** | `bcast/src/modules/uff/index.ts` | 33–34 (`''` em 38–39) | idem |
| **PUB** | `bcast/src/modules/users-test/index.ts` | 33–34 (`''` em 38–39) | idem |
| **PUB** | `bcast/src/modules/webmedia/index.ts` | 44 e 47 (`''` em 53–54) | idem |
| **SUB** | `aop/src/core.js` | 274 (tratador na 314) | `tlm/sls/{serviceId}/#`: `loadSLSMetadata()`; assinado em `setCurrentService()` e tirado em `unsetCurrentService()` (278) |

### `video/event` — não retido; esquema: sim

| Direção | Arquivo | Linha | Detalhe |
|---|---|---|---|
| **PUB** | `bcast/src/modules/live_stream/index.ts` | 114 | ação do segmento HLS em JSON |
| **SUB** | `aop/src/modules/graphic-overlays/view.ejs` | 177 | dispara `stream_event` na app da camada gráfica (por `postMessage` à origem dela, desde o P1.3) |

---

## Tópicos do plugin do broker e do teste de latência

| Tópico | Quem publica | Detalhe |
|---|---|---|
| `errors/{client_id}` | plugin C (`mosquitto_plugin.c`, linha 82, em `publish_error()`, linha 63) | JSON da recusa de esquema (`INVALID_JSON` ou `SCHEMA_VALIDATION_FAILED`); QoS 0, não retido. Nenhum serviço assina. Desde 12/10, sempre JSON válido (RFC 8259) e UTF-8 válido, com até 2047 bytes e `"truncated":true` quando algum campo foi cortado (`error_json_build`, em `src/error_json.c`); no `{client_id}` do tópico, `+`, `#`, `/` e controles viram `_`, e o tópico tem até 255 bytes, cortado entre caracteres UTF-8 inteiros (`error_json_topic`). Formato em [09](09-schema-validation.md) |
| `PublisherResponseTime{id}/iteration{n}` | `infra/mqtt-broker/brokertimetest/` (`server.js`, `headless-run.js`) | pedido do teste de latência; o plugin o intercepta e o deixa passar |
| `PluginResponseTime{id}/iteration{n}` | plugin C (`response_time_tester.c`, linha 105) | resposta ao teste, `{localtime, id, iteration, diffTime}`; QoS 0, não retido; o testador assina `PluginResponseTime{id}/#`. Desde 12/10, o `id` sai escapado (`error_json_string`), e o buffer da resposta não vaza quando ela não cabe em 511 bytes ou o publish falha |

---

## Visão geral

```mermaid
graph TD
    BC["bcast (bcast_svc)<br/>PUB tlm/lls/{bsid}/bamt, tlm/sls/{sid}/esg|bald,<br/>video/event"]
    AOP["AoP (aop-core)<br/>PUB aop/currentUser, aop/currentService, aop/display/layers/*<br/>SUB aop/currentUser, aop/currentService, tlm/lls/#, tlm/sls/{sid}/#"]
    TV3["tv3ws (tv3ws-client)<br/>PUB aop/currentUser, aop/devices/{class}, popup/yesno|qrcode|pin, tópicos de nó<br/>SUB aop/currentUser, aop/currentService, aop/services,<br/>aop/{sid}/currentApp, .../path, .../doc/*, popup/yesno/response"]
    PG["página da TV (rp-display)<br/>PUB popup/yesno/response<br/>SUB aop/display/layers/+, video/+, popup/+/message, popup/+, video/event"]
    RE[("Redis")]

    BC -->|"sinalização"| AOP
    BC -->|"video/event"| PG
    AOP -->|"usuário e serviço correntes"| TV3
    TV3 -->|"usuário corrente (C.6.14.4)"| AOP
    AOP -->|"camadas da tela"| PG
    TV3 -->|"pop-ups"| PG
    PG -->|"resposta do pop-up"| TV3
    TV3 -.->|"session:* (espelho)"| RE
    AOP -.->|"lastAccess do perfil"| RE
```

## Retenção

| Tópico | retain | Quem publica |
|---|---|---|
| `aop/currentUser` | `true` | AoP e tv3ws |
| `aop/currentService` | `true` | AoP |
| `aop/devices/{devclass}` | `true` | tv3ws |
| `tlm/lls/{bsid}/bamt`, `tlm/sls/{serviceId}/esg`, `tlm/sls/{serviceId}/bald` | `true` | bcast |
| tópicos de nó `.../user` e `.../value` | `true` (padrão do `publish` do tv3ws) | tv3ws |
| camadas da tela, pop-ups e resposta, `eventNotification`, `video/event`, `errors/*`, `PluginResponseTime*` | `false` | AoP, tv3ws, página, bcast e plugin |
