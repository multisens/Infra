# Pipeline HTTP — a borda (`edgegateway`)

> Mensageria, gateways e Redis são decisões do projeto e não aparecem na norma.

Todo cliente acessa a **borda**, nunca a implementação interna. As portas de API do tv3ws (44652 e 44653) não são publicadas no host. O `edgegateway` é um container só, sempre na bridge `ginga_net`, com:
- **superfície interna:** 44642, HTTP, a única porta fixa da norma (C.3.4);
- **superfície externa:** 44643, HTTPS, com o TLS terminado no KrakenD (TLS 1.2 no mínimo). O certificado autoassinado do receptor fica em `/etc/tv30/tls/cert.pem` e `key.pem`, montados de `.tv30/tls` da raiz do TV30; o serviço `tls-init` do compose da raiz o gera na primeira subida (`edgegateway/tls-init.sh`). No host, a 44643 é fixa por padrão (`EDGE_EXT_PUBLISH=44643:44643`), porque o `/manifest` do tv3ws a anuncia;
- **documentação:** 8085, em porta de host livre (`docker port edgegateway 8085`).

Os configs das duas superfícies e os dois OpenAPI são gerados **no build** a partir de `edgegateway/routes.json`, a tabela única de rotas.

**Superfícies.** As duas superfícies têm as 26 rotas da tabela. O `generate.js` (`SURFACE_RULES`) deriva das `classes` e da seção de cada rota o que muda entre elas:
- na externa, as rotas de uso exclusivo do cliente local (`POST` e `DELETE /tv3/bind-context`, a C.6.15.5 e as duas rotas por *handle*) e a C.6.1.2 (`/tv3/authorize`) levam `surface_error: 106` e respondem 106 a qualquer requisição (C.4.1.8);
- na interna, o access token de classe `non-local` fora de `/tv3/authorize` e `/tv3/token` recebe 106 (C.4.1.6).

A norma (C.3.4) só define o `Server-BaseURL` (HTTP, 44642) e o `Server-SecureBaseURL` (HTTPS, porta livre); duas superfícies são decisão do projeto. Detalhes em [05-autenticacao.md](05-autenticacao.md#política-por-rota).

## Fluxo por requisição

```mermaid
flowchart LR
    C["Cliente<br/>(app, celular, TV)"] -->|"HTTP :44642 / HTTPS :44643"| P

    subgraph EDGE["edgegateway (KrakenD 2.7.2)"]
        P["plugin http-server tv30-auth<br/>CORS (OPTIONS e cabecalhos) -><br/>rota (100) -> superficie (106) -> classe (106) -><br/>access token (107) -> servico (300) -><br/>bind-token (104/108) -> versao (100/101)"]
        A["APIs da propria borda (edge.go)<br/>C.6.8 bind-context<br/>C.6.7.8 / C.6.7.9 api-info"]
        G["roteador KrakenD<br/>proxy no-op"]
        P -->|"liberada (ou warn),<br/>rota com edge"| A
        P -->|"liberada (ou warn),<br/>demais rotas"| G
    end

    P -.->|"GET / SISMEMBER /<br/>HGET / LRANGE"| R[("Redis")]
    A -.->|"C.6.8: LRANGE / SCAN / HGETALL / GET<br/>e FCALL tv30_bind_registra / tv30_bind_remove"| R
    A -->|"200 ou erro C.3.2"| C
    P -->|"negada<br/>404 + {error, description}"| C
    G -->|"interna: http://tv3ws:44652<br/>externa: https://tv3ws:44653<br/>+ X-TV30-API-Version, X-TV30-Host"| T["tv3ws"]
    T -->|"status e corpo intactos<br/>(no-op)"| C
```

- **Plugin `tv30-auth`** (`edgegateway/plugin/`, em Go). Valida as credenciais segundo a política por rota de `routes.json`. Com `AUTH_ENFORCE=enforce` (padrão; vazio também vale), bloqueia. `warn` é opção explícita e transitória: nele, o 104, o 107 e o 108 só ficam no log e marcam a resposta com `X-TV30-Auth-Warn`; o 100, o 101, o 106, o 300 e o 200 saem nos dois modos.
  - O CORS é todo dele: responde o `OPTIONS` (o preflight em qualquer caminho) e põe `Access-Control-Allow-Origin` e `Access-Control-Expose-Headers` em toda resposta. A configuração do KrakenD não tem o módulo CORS.
  - Um caminho não declarado recebe 100 sem chegar ao roteador.
  - **Versão (C.3.6).** A borda negocia em toda rota, pelas `versions` do `routes.json` (ausente = 2.0): sem `Accept-Version`, 2.0; fora do formato X.Y, 101; versão que a rota não serve, 100. Repassa a versão negociada no cabeçalho interno `X-TV30-API-Version` e o `Host` original em `X-TV30-Host` (que o `/manifest` usa), apagando os valores do cliente. O tv3ws não interpreta o `Accept-Version`. A C.6.15.5 serve 2.0 e 100.0; `GET` e `DELETE /tv3/remote-device/device/{handle}`, rotas de extensão do testbed, fora da norma, só 100.0.
  - Um panic no roteador vira 404 com `{error: 200}`, e a resposta 5xx do próprio KrakenD (backend lento ou fora do ar, que sairia como 500 sem corpo) também, nos dois modos (`plugin/handler.go`, `serveNext`).
  - É o único ponto que valida credencial: o tv3ws não confere token nem classe.
  - Detalhes em [05-autenticacao.md](05-autenticacao.md) e no [README do plugin](../edgegateway/plugin/README.md).
- **APIs respondidas pela borda.** As rotas com o campo `edge` no `routes.json` são respondidas pelo próprio plugin, depois da mesma decisão de credencial, e não chegam ao tv3ws: a C.6.8 (`POST`, `GET` e `DELETE /tv3/bind-context`, `plugin/bindcontext.go`; o `POST` e o `DELETE` dão 106 na externa) e a C.6.7.8/C.6.7.9 (`GET /tv3/api-info[/<api-id>]`, `plugin/apiinfo.go`). A C.6.8 grava `bind-context:{serviceId}` no Redis: é a única escrita da borda. A lista da C.6.7.8/C.6.7.9 sai do campo `api` (id, seção e versão da Tabela C.2) das rotas no `routes.json`, que o `generate.js` entrega ao plugin: as 20 APIs, nas duas superfícies. A versão informada é a mais recente que a rota serve: 100.0 na C.6.15.5, 2.0 nas demais. O endpoint dessas rotas continua gerado no KrakenD, mas nunca é alcançado; ele só mantém o caminho registrado no roteador (escolha documentada no `generate.js`).
- **Roteador KrakenD.** Só repassa os cabeçalhos e as query strings declarados em cada rota (mais o `bind-token` nas rotas `token+bind` e os dois cabeçalhos internos, que o `generate.js` acrescenta). Toda rota repassada é proxy no-op: o erro do tv3ws (404 com corpo C.3.2) chega intacto ao cliente. A espera pelo tv3ws é o padrão do KrakenD (2 s), menos em `GET /tv3/authorize`, com `timeout` de 15 s no `routes.json`, porque o pop-up espera o espectador por 10 s.
- **HTTPS do tv3ws.** O backend da superfície externa é a 44653 do tv3ws, aberta com o mesmo certificado do receptor: no compose, `.tv30/tls` montado em `/tls` (`TV30_TLS_DIR`); fora dele, `HTTPS_KEY` e `HTTPS_CERT` (`tv3ws/src/tls.ts`). O KrakenD aceita o certificado autoassinado do backend (`allow_insecure_connections`). Sem certificado nenhum, o tv3ws sobe só em HTTP, e as rotas que a 44643 repassa respondem 404 `{error: 200}` (conexão recusada); as respondidas pela borda não mudam.
- **CORS.** `Access-Control-Allow-Origin: *`. No preflight, os cabeçalhos permitidos são `Content-Type, Authorization, bind-token, Accept, Accept-Version` (C.4.1.9.3), mais `key`, usado no `DELETE /tv3/bind-context` (C.6.8.4). Os cabeçalhos expostos são `Content-Length` e `X-TV30-Auth-Warn` (`cors.expose_headers`). **Quem responde o preflight é o plugin `tv30-auth`,** em qualquer caminho e com a lista pedida em qualquer ordem: 200 com `Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: *` (os valores padrão da C.4.1.9.3), o `Access-Control-Allow-Headers` acima e `Access-Control-Max-Age: 43200` (`cors.max_age`, 12 h). O módulo CORS do KrakenD fica fora da configuração porque envolve o plugin e atenderia o preflight antes dele; o módulo do KrakenD 2.7.2 também não concede o preflight com o `Access-Control-Request-Headers` fora de ordem lexicográfica (comentário em `plugin/handler.go`, `ServeHTTP`).
- **Variantes.** `EDGE_VARIANT=linux` (padrão) aponta os backends para o DNS da rede (`tv3ws:44652` e `tv3ws:44653`). `EDGE_VARIANT=windows` aponta para `host.docker.internal:44654` e `:44655`, no cenário dev-host. Nos dois casos o Redis é o do container.
- **Anúncio SSDP (C.3.4).** A imagem da borda não tem anunciante: ele roda nativo no host, subido pelo `./tv30 up` da raiz do TV30. Detalhes em [05-autenticacao.md](05-autenticacao.md#descoberta-ssdp).
- **Morre-inteiro.** Os dois processos KrakenD e o httpd da documentação são vigiados pelo `entrypoint.sh`: se um cair, o container todo cai. Configuração inválida do plugin também derruba o processo.

## Build

```mermaid
flowchart LR
    RJ["routes.json"] --> GEN["node --test generate.test.js<br/>+ generate.js build<br/>(node:20-alpine; surface_error e<br/>nonlocal_http derivados de classes)"]
    GEN --> CFG["krakend-{internal,external}.{linux,windows}.json<br/>(com a politica, as rotas edge, as versoes e a lista apis<br/>do plugin; a externa com o bloco tls)<br/>+ openapi-{internal,external}.json"]
    SRC["plugin/*.go"] --> B["Go por TARGETARCH<br/>amd64: krakend/builder:2.7.2<br/>arm64: golang:1.22.7-alpine3.19 + binutils-gold<br/>go vet + go test + -buildmode=plugin"]
    B --> SO["tv30-auth.so"]
    CFG --> IMG["devopsfaith/krakend:2.7.2<br/>krakend check (4 configs)<br/>check-plugin + test-plugin -s<br/>+ tls-init.sh"]
    SO --> IMG
```

O `.so` só carrega num binário com o mesmo Go, a mesma libc e a mesma arquitetura. Por isso:
- o Go do build segue a arquitetura (`TARGETARCH`): em amd64, o builder oficial `krakend/builder:2.7.2`; em arm64, `golang:1.22.7-alpine3.19`, o mesmo Go e a mesma libc da `devopsfaith/krakend:2.7.2` arm64, com o `binutils-gold`, que o Go usa para ligar plugin em arm64. Outra arquitetura falha no build;
- as versões são **fixas**;
- o build falha se o plugin não carregar (`check-plugin` confere Go e libc contra o KrakenD).

O anunciante SSDP não entra na imagem: o `./tv30 up` o compila e testa no host ([05](05-autenticacao.md#descoberta-ssdp)).
