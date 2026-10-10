# Pipeline HTTP — a borda (`edgegateway`)

> **Estado do código em 2026-10-10** (revisto para as decisões da reunião de 05/10 com o Joel).
>
> A versão anterior deste documento descrevia um desenho que nunca chegou a validar nada:
> - o KrakenD na porta 8090 delegava a validação a um middleware Node (`POST /validate`), que ninguém chamava;
> - o plugin `consent-validator` era só um proxy de qualquer caminho até o serviço.
>
> As duas peças saíram (`infra@f2eb652`). Mensageria, gateways e Redis são decisões do projeto e não aparecem na norma.

Todo cliente acessa a **borda**, nunca a implementação interna (D10). As portas de API do tv3ws (44652 e 44653) não são publicadas no host; com o override `docker-compose.ssdp.yml` da raiz do TV30, são publicadas só em `127.0.0.1`, para a borda em rede do host. O `edgegateway` é um container só, com:
- **superfície interna:** 44642, a única porta fixa da norma (C.3.4);
- **superfície externa:** 44643;
- **documentação:** 8085.

Os configs das duas superfícies e os dois OpenAPI são gerados **no build** a partir de `edgegateway/routes.json`, a tabela única de rotas (M4).

## Fluxo por requisição

```mermaid
flowchart LR
    C["Cliente<br/>(app, celular, TV)"] -->|"HTTP :44642 / :44643"| P

    subgraph EDGE["edgegateway (KrakenD 2.7.2)"]
        P["plugin http-server tv30-auth<br/>rota (100) -> classe (106) -><br/>access token (107) -> bind-token (104/108)"]
        A["APIs da propria borda (edge.go)<br/>C.6.8 bind-context<br/>C.6.7.8 / C.6.7.9 api-info"]
        G["roteador KrakenD<br/>+ CORS<br/>proxy no-op"]
        P -->|"liberada (ou warn),<br/>rota com edge"| A
        P -->|"liberada (ou warn),<br/>demais rotas"| G
    end

    P -.->|"GET / SISMEMBER /<br/>HEXISTS / LRANGE"| R[("Redis")]
    A -.->|"C.6.8: LRANGE / RPUSH / LREM /<br/>SCAN / HGETALL / GET"| R
    A -->|"200 ou erro C.3.2"| C
    P -->|"negada (enforce)<br/>404 + {error, description}"| C
    G -->|"interna: http://tv3ws:44652<br/>externa: https://tv3ws:44653"| T["tv3ws"]
    T -->|"status e corpo intactos<br/>(no-op)"| C
```

- **Plugin `tv30-auth`** (`edgegateway/plugin/`, em Go). Valida as credenciais segundo a política por rota de `routes.json`. Com `AUTH_ENFORCE=warn` (padrão), só registra no log e marca a resposta com `X-TV30-Auth-Warn`; com `enforce`, bloqueia.
  - Um caminho não declarado recebe 100 sem chegar ao roteador.
  - Um panic no roteador vira 200.
  - Desde a reunião de 05/10 com o Joel, é o único ponto que valida credencial: o tv3ws ficou "anônimo" (D-0510-1).
  - Detalhes em [05-autenticacao.md](05-autenticacao.md) e no [README do plugin](../edgegateway/plugin/README.md).
- **APIs respondidas pela borda** (reunião de 05/10 com o Joel). As rotas com o campo `edge` no `routes.json` são respondidas pelo próprio plugin, depois da mesma decisão de credencial, e não chegam ao tv3ws: a C.6.8 (`POST`, `GET` e `DELETE /tv3/bind-context`, D-0510-2, `plugin/bindcontext.go`) e a C.6.7.8/C.6.7.9 (`GET /tv3/api-info[/<api-id>]`, D-0510-3, `plugin/apiinfo.go`). A C.6.8 grava `bind-context:{serviceId}` no Redis: é a única escrita da borda. A lista da C.6.7.8/C.6.7.9 sai do campo `api` (id, seção e versão da Tabela C.2) das rotas do `routes.json`, que o `generate.js` entrega ao plugin. O endpoint dessas rotas continua gerado no KrakenD, mas nunca é alcançado; ele só mantém o caminho registrado no roteador (escolha documentada no `generate.js`).
- **Roteador KrakenD.** Só repassa os cabeçalhos e as query strings declarados em cada rota. Toda rota repassada é proxy no-op: o erro do tv3ws (404 com corpo C.3.2) chega intacto ao cliente.
- **CORS.** `Access-Control-Allow-Origin: *`. No preflight, os cabeçalhos permitidos são `Content-Type, Authorization, bind-token, Accept, Accept-Version` (C.4.1.9.3), mais `key`, usado no `DELETE /tv3/bind-context` (C.6.8.4). O cabeçalho exposto é `X-TV30-Auth-Warn`. **Achado de 09/10:** o módulo CORS do KrakenD 2.7.2 não concede o preflight quando o `Access-Control-Request-Headers` vem fora de ordem lexicográfica (`content-type,bind-token`), em qualquer rota; em ordem (`bind-token,content-type`), como os navegadores mandam, passa. Ver `KNOWN-ISSUES.md` da raiz do TV30.
- **Variantes.** `EDGE_VARIANT=linux` aponta os backends para o DNS da rede (`tv3ws`). `EDGE_VARIANT=windows` aponta para `host.docker.internal`, no cenário dev-host. `EDGE_VARIANT=host` aponta para `127.0.0.1:44652/44653`, com a borda em rede do host; quem a define é o override `docker-compose.ssdp.yml` da raiz do TV30, que também faz o plugin ler o Redis em `127.0.0.1` (a 6379 publicada). Nos três casos o Redis é o do container.
- **Anúncio SSDP (C.3.4; L6, opção A, decidido pelo Luís em 09/10).** Com `SSDP_ENABLED=true`, que o mesmo override liga, o `entrypoint.sh` sobe também o anunciante `ssdp-announcer` (`edgegateway/ssdp/`, em Go). Detalhes em [05-autenticacao.md](05-autenticacao.md#descoberta-ssdp).
- **Morre-inteiro.** Os dois processos KrakenD, o httpd da documentação e, quando ligado, o anunciante SSDP são vigiados pelo `entrypoint.sh`: se um cair, o container todo cai (para o anunciante, decisão do Luís em 09/10). Configuração inválida do plugin também derruba o processo.

## Build

```mermaid
flowchart LR
    RJ["routes.json"] --> GEN["generate.js build<br/>(node:20-alpine)"]
    GEN --> CFG["krakend-{internal,external}.{linux,windows,host}.json<br/>(com a politica, as rotas edge e a lista apis do plugin)<br/>+ openapi-{internal,external}.json"]
    SRC["plugin/*.go"] --> B["krakend/builder:2.7.2<br/>go vet + go test + -buildmode=plugin"]
    B --> SO["tv30-auth.so"]
    SSRC["ssdp/*.go"] --> SB["krakend/builder:2.7.2<br/>go vet + go test + CGO_ENABLED=0"]
    SB --> SBIN["ssdp-announcer"]
    CFG --> IMG["devopsfaith/krakend:2.7.2<br/>krakend check (6 configs)<br/>check-plugin + test-plugin -s"]
    SO --> IMG
    SBIN --> IMG
```

O `.so` só carrega num binário com o mesmo Go, a mesma libc e a mesma arquitetura. Por isso:
- o builder e a imagem final usam a **mesma versão fixa**;
- o build falha se o plugin não carregar.
