# TV30 — `infra/` (submodule)

Submodule do monorepo [TV30](https://github.com/multisens/TV30) que reúne os componentes de **infraestrutura compartilhada** do testbed TV 3.0 (ABNT NBR 25608): armazenamento (Redis, com seed e interface de inspeção embutidos), broker MQTT (Mosquitto + plugin C de validação de esquema) e a **borda** (`edgegateway`: as duas superfícies KrakenD + documentação, geradas de uma tabela única de rotas).

> **Não suba este `docker-compose.yml` isolado.** Ele é incluído via `include:` pelo compose da raiz do TV30 — toda a stack sobe de uma vez com `docker compose up -d` **na raiz do monorepo**. Sozinho, ele nem forma um projeto válido: o `edgegateway` depende do `preflight`, que só existe no compose da raiz, e o `docker compose -f infra/docker-compose.yml ...` para com `service "edgegateway" depends on undefined service "preflight"` (conferido com `docker compose config` em 12/10). Os serviços `aop`, `tv3ws` e `bcast` também só existem lá.

---

## Serviços expostos

Portas listadas são as do **host** quando a stack sobe pela raiz do TV30.

| Serviço       | Porta host                     | Função                                                                 |
|---------------|--------------------------------|------------------------------------------------------------------------|
| `redis`       | `6379`, só em `127.0.0.1` (A1, Joel 15/10); UI em porta dinâmica | Armazenamento (perfis, sessão, credenciais, registro de dispositivos). Quem escreve cada família de chave está em `docs/modelo-redis.md` da raiz do TV30. O container embute o **seed** (carga inicial gerada no build, em RESP, pelo `redis/emit_seed.py` a partir de `user-files-template/userData.json`, e aplicada pelo entrypoint com `redis-cli --pipe` só em banco vazio, sem a chave `seed:done`; o healthcheck só fica saudável depois dela) e o **Redis Commander** (processo auxiliar de inspeção). Morre-inteiro desde 11/10 (C2, decisão do Luís): se o `redis-server` ou o commander cair, o container cai e o `restart` o traz de volta; até 11/10, a morte do commander não derrubava o banco. A UI do commander exige login, com usuário e senha em `REDIS_COMMANDER_USER`/`REDIS_COMMANDER_PASSWORD` (padrão `admin`/`tv30-redis-admin`, definido em `redis/docker-compose.yml` e repetido no `redis/entrypoint.sh`; para trocar, defina as variáveis no `.env` da raiz do TV30). Já a conexão com o banco (6379) segue sem senha e sem ACL, a lista de controle de acesso do Redis (D-L1, decidida pelo Luís em 03/10). Desde o P4 (decisão do Luís em 10/10), toda escrita do código nas famílias de chave do testbed passa por funções do Redis que conferem o esquema (`redis/esquema.json`), carregadas a cada partida, antes do seed. A auditoria é `redis-cli FCALL_RO tv30_auditoria 0`. Ver [`redis/README.md`](./redis/README.md). |
| `mqtt-broker` | `1883` (fixa); WebSocket (`9001` do container) em porta dinâmica (C1, 11/10; `MQTT_WS_PUBLISH`) | Broker MQTT + plugin C de **validação de esquema** na publicação (tópicos reais de sinalização e estado + `sensor/*`; ver `mqtt-broker/plugin/config/schemas.json`). Desde 12/10, o payload é lido inteiro e só passa como JSON se for exatamente um texto JSON, e a recusa sai em `errors/<client_id>` sempre como JSON válido ([docs/09](docs/09-schema-validation.md)). O plugin não consulta o Redis nem decide acesso por cliente. Serviço `mosquitto` do compose, profile `mqtt`. A página da TV não usa a porta publicada da 9001: fala MQTT por WebSocket no `/mqtt` do próprio AoP, que repassa à 9001 do container pela `ginga_net`. |
| `edgegateway` | `44642` (interna, HTTP, fixa da norma C.3.4) e `44643` (externa, HTTPS; fixa por padrão desde 12/10, decisão do Luís; `EDGE_EXT_PUBLISH`); docs em porta dinâmica | A borda num container só, sempre na bridge `ginga_net`: superfícies interna e externa do KrakenD + Swagger UI com os dois specs. Tudo gerado **no build** a partir de `edgegateway/routes.json` (tabela única — M4). As duas superfícies têm as 26 rotas da tabela (F-1, Joel 15/10; até 15/10, a externa só tinha as que admitem o cliente não local). O que muda entre elas sai das `classes` e da seção de cada rota (`generate.js`, `SURFACE_RULES`): na externa, as rotas de uso exclusivo do cliente local e a C.6.1.2 (`/tv3/authorize`) respondem 106 a qualquer requisição (C.4.1.8); na interna, o access token de classe não local fora de `/tv3/authorize` e `/tv3/token` recebe 106 (C.4.1.6; E7). A 44643 tem TLS (Transport Layer Security) terminado no KrakenD (A7, Joel 15/10), com o certificado autoassinado do receptor que o serviço `tls-init` do compose da raiz gera na primeira subida em `.tv30/tls` (`edgegateway/tls-init.sh`), montado em `/etc/tv30/tls`. Backends: interna em `http://tv3ws:44652`, externa em `https://tv3ws:44653`. Desde a A4 (decisão do Luís em 11/10), `POST /tv3/{serviceContextId}/users`, fora da norma, não está na tabela e dá 100. **Validação de credenciais** (access token, bind-token, classe de cliente; também o `sub` do access token em `clients:authorized`, E6; erros 100/104/106/107/108/300 no formato C.3.2) pelo plugin Go `tv30-auth`, carregado nas duas superfícies. `AUTH_ENFORCE=enforce` é o padrão desde 15/10 (Joel; vazio também vale) e bloqueia; `warn`, opção explícita e transitória, só avisa no 104, no 107 e no 108, e os demais códigos saem nos dois modos. O plugin também **negocia a versão** em toda rota, pelas `versions` do `routes.json` (ausente = 2.0; D1, D2 e D4, Joel 15/10), e a repassa no cabeçalho interno `X-TV30-API-Version`, com o `Host` original em `X-TV30-Host`. Desde a reunião de 05/10 com o Joel, a borda é o único ponto que valida credencial (o tv3ws ficou "anônimo", D-0510-1) e **não é mais só proxy**: o plugin responde ele mesmo a C.6.8 (`/tv3/bind-context`, D-0510-2), gravando `bind-context:*` no Redis, e a C.6.7.8/C.6.7.9 (`/tv3/api-info`, D-0510-3), com a lista de APIs tirada do campo `api` do `routes.json`. Nas demais rotas, repassadas ao tv3ws, é proxy puro: status e corpo do backend passam intactos, menos o 5xx do próprio KrakenD, trocado por 404 `{error: 200}`. Ver [`edgegateway/plugin/README.md`](./edgegateway/plugin/README.md). **Anúncio SSDP** (Simple Service Discovery Protocol, C.3.4): desde 15/10 (complemento do Joel), o anunciante em Go `edgegateway/ssdp/` não está na imagem. Ele roda nativo no host, fora do Docker, em Linux, macOS e Windows, compilado e subido pelo `./tv30 up` da raiz (no Windows, `.\tv30.ps1 up`). O host do anúncio é detectado, e o `SSDP_ADVERTISE_HOST` é sobreposição opcional (B2 revista). O `docker-compose.ssdp.yml` e a variante `host` saíram. Ver [docs/05](docs/05-autenticacao.md#descoberta-ssdp). |

Descobrir as portas dinâmicas: `docker compose ps` (linhas `edgegateway`, `mqtt-broker` e `redis`), ou direto `docker port redis 18081` (UI do commander), `docker port edgegateway 8085` (docs) e `docker port mqtt-broker 9001` (MQTT por WebSocket). `EDGE_EXT_PUBLISH` e `MQTT_WS_PUBLISH` recebem o valor que vai em `ports`: só o número = porta do container com porta de host aleatória; `host:container` fixa. Os padrões são diferentes:
- `MQTT_WS_PUBLISH`: padrão `9001`, porta de host aleatória (C1, decisão do Luís em 11/10). Para fixar, por exemplo, `MQTT_WS_PUBLISH=9003:9001`. Ele substitui o `MQTT_WS_PORT` no publish do mosquitto.
- `EDGE_EXT_PUBLISH`: padrão `44643:44643`, porta de host fixa (decisão do Luís em 12/10). O motivo é o `/manifest` do tv3ws, que anuncia o `Server-SecureBaseURL` em `<host>:44643` (`EDGE_HTTPS_PORT`, padrão 44643) e não sabe a porta que o Docker escolhe. Com só o número (`EDGE_EXT_PUBLISH=44643`), a porta de host fica livre, como foi o padrão de 11/10 (C1) a 12/10: ela se descobre com `docker port edgegateway 44643`, e o `/manifest` não a anuncia. Para outra porta de host fixa, o `EDGE_HTTPS_PORT` do tv3ws tem de levar o mesmo número.

O serviço `sysctl-init` mencionado em alguns docs **não vive aqui** — está no `docker-compose.yml` da raiz do TV30. Ele é um one-shot privilegiado (`alpine:3`, `network_mode: host`, `profiles: [linux]`) que executa `sysctl -w net.bridge.bridge-nf-call-iptables=0` no host Linux. Nenhum serviço depende dele (`depends_on`), então ele roda junto com os demais, sem ordem garantida. O motivo, segundo o comentário do compose da raiz (não reproduzido em bancada): em alguns kernels o tráfego entre containers pela bridge é interceptado por regras `iptables` do host e MQTT/Redis ficam intermitentes. Em hosts onde o `sysctl` é read-only (ex.: Docker Desktop), o comando falha silenciosamente — o `|| echo 'sysctl skipped...'` cobre esse caso.

---

## Build local das imagens (opcional)

Em deploy normal as imagens vêm prontas do Docker Hub. Para buildar localmente as imagens definidas neste compose sem subir nada, rode na raiz do TV30 (o compose daqui, sozinho, não é projeto válido; ver o aviso no topo):

```bash
docker compose --profile mqtt build edgegateway redis mosquitto
```

O `--profile mqtt` é para o `mosquitto`, que só existe nesse profile (com `COMPOSE_PROFILES=mqtt,linux` no `.env` da raiz, ele já está ligado). O build de cada imagem roda os testes dela: o do `edgegateway` roda `node --test generate.test.js`, `go vet` e `go test` do plugin (com o Go escolhido pela arquitetura, `TARGETARCH`: `krakend/builder:2.7.2` em amd64, `golang:1.22.7-alpine3.19` com `binutils-gold` em arm64) e o `krakend check` das quatro configs (o anunciante SSDP é compilado e testado pelo `./tv30 up`, fora da imagem); o do `redis` roda o `redis/confere-imagem.sh`; e, desde 12/10, o do `mosquitto` roda os testes de unidade do plugin C (`mqtt-broker/plugin/tests/test_error_json.c` e `test_json_payload.c`) antes de compilá-lo. Para de fato rodar a stack, use `docker compose up -d` na raiz do TV30.

---

## Estrutura

```
infra/
  docker-compose.yml         # incluido via `include:` pelo compose raiz
  redis/                     # Redis consolidado (seed em build + commander embutido + funcoes do esquema, P4; ver redis/README.md)
  mqtt-broker/               # Broker MQTT + plugin C (validacao de schema)
  edgegateway/               # A borda: routes.json (fonte unica) + generate.js
                             #   + plugin/ (Go, tv30-auth: validacao de credenciais
                             #     e as APIs C.6.8 e C.6.7.8/C.6.7.9 respondidas na borda)
                             #   + ssdp/ (Go, anunciante SSDP nativo do host, compilado pelo ./tv30 up)
                             #   + tls-init.sh (certificado do receptor, A7)
                             #   -> gera os configs KrakenD e os OpenAPI no build
  dockerfiles/               # Dockerfiles dos containers que vivem na raiz (aop, tv3ws, bcast, ...)
  user-files-template/       # Modelo de ./user-files: userData.json (seed do Redis, perfil "Viewer 1")
                             #   + thumbs/ (avatar do perfil da carga, G8), copiados pelo userfiles-seed da raiz
  docs/                      # Documentacao tecnica (o codigo e o arbitro)
```

Toda rota nova das APIs entra **somente** em `edgegateway/routes.json` — uma
edição gera as duas superfícies (variantes linux/windows) e os dois specs.

---

## Documentação

A documentação técnica da infra está em [`docs/`](./docs/README.md). O código é o árbitro: onde um documento divergir dele, vale o código, e a divergência é defeito do documento.

- [`00-diagrama-unificado.md`](./docs/00-diagrama-unificado.md) — Diagrama unificado da stack (containers, portas e quem fala com quem)
- [`01-visao-geral.md`](./docs/01-visao-geral.md) — Visão geral da stack
- [`02-rede-docker.md`](./docs/02-rede-docker.md) — Rede `ginga_net`, portas e ordem de subida
- [`03-pipeline-mqtt.md`](./docs/03-pipeline-mqtt.md) — Plugin MQTT (validação de esquema)
- [`04-pipeline-http.md`](./docs/04-pipeline-http.md) — A borda (edgegateway/KrakenD)
- [`05-autenticacao.md`](./docs/05-autenticacao.md) — Classes de cliente, emissão do token, validação na borda e SSDP
- [`06-modelo-redis.md`](./docs/06-modelo-redis.md) — Modelo de dados Redis
- [`07-usuarios-perfis.md`](./docs/07-usuarios-perfis.md) — Usuários e perfis (Tabela 7 da norma e o que o testbed grava)
- [`08-mqtt-map.md`](./docs/08-mqtt-map.md) — Mapa de tópicos MQTT
- [`09-schema-validation.md`](./docs/09-schema-validation.md) — Como adicionar ou alterar regras de esquema no plugin do Mosquitto

Para subir a stack completa (incluindo `aop`, `tv3ws`, `bcast`) e configurar `.env`, ver o [README do TV30](../README.md).
