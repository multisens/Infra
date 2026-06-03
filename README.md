# TV30 — `infra/` (submodule)

Submodule do monorepo [TV30](https://github.com/multisens/TV30) que reúne os componentes de **infraestrutura compartilhada** do testbed TV 3.0 (ABNT NBR 25608): Redis, broker MQTT (Mosquitto + plugin C de ACL/consentimento), gateways KrakenD (externo e interno) e middlewares de validação JWT/OpenAPI.

> **Não suba este `docker-compose.yml` isolado.** Ele é incluído via `include:` pelo compose da raiz do TV30 — toda a stack sobe de uma vez com `docker compose up -d` **na raiz do monorepo**. Subir aqui dentro sem o compose raiz não traz os serviços `aop`, `ccws` e `bcast`, e a rede `ginga_net` fica órfã.

---

## Serviços expostos

Portas listadas são as do **host** quando a stack sobe pela raiz do TV30.

| Serviço                | Porta host        | Função                                                                |
|------------------------|-------------------|-----------------------------------------------------------------------|
| `redis`                | `6379`            | Cache/estado compartilhado (ACL, consentimento, perfis de usuário).   |
| `redis-commander`      | `18081`           | UI web do Redis (8081 do host fica reservada pro `bcast`).            |
| `redis-seed`           | —                 | One-shot. Popula Redis a partir de `acl.json` + `userData.json`.      |
| `mosquitto`            | `1883`, `9001`    | Broker MQTT + WebSocket. Plugin C valida ACL/consent/schema via Redis. Profile `mqtt`. |
| `krakend-external`     | `44643` (HTTPS)   | API Gateway externo. Plugin Go `consent-validator` em todas as rotas. |
| `krakend-internal`     | `44642`           | API Gateway interno (serviço-a-serviço).                              |
| `validation-middleware`| `3000`            | Validação JWT + geração do spec OpenAPI a partir do `krakend.json`.   |
| `swagger-ui`           | `8085`            | Swagger UI do gateway externo.                                        |
| `middleware-internal`  | `3001`            | Validação + OpenAPI do gateway interno.                               |
| `swagger-ui-internal`  | `8086`            | Swagger UI do gateway interno.                                        |

O serviço `sysctl-init` mencionado em alguns docs **não vive aqui** — está no `docker-compose.yml` da raiz do TV30. Ele é um one-shot privilegiado (`alpine:3`, `network_mode: host`, `profiles: [linux]`) que executa `sysctl -w net.bridge.bridge-nf-call-iptables=0` no host Linux antes do resto da stack subir. Sem isso, em alguns kernels o tráfego entre containers pela bridge é interceptado por regras `iptables` do host e MQTT/Redis ficam intermitentes. Em hosts onde o `sysctl` é read-only (ex.: Docker Desktop), o comando falha silenciosamente — o `|| echo 'sysctl skipped...'` cobre esse caso.

---

## Build local das imagens (opcional)

Em deploy normal as imagens vêm prontas do Docker Hub. Se quiser buildar localmente sem subir nada:

```bash
docker compose -f infra/docker-compose.yml build
```

Isso **apenas builda** as imagens definidas neste compose (mosquitto, krakend-external, krakend-internal, validation-middleware, middleware-internal). Para de fato rodar a stack, use `docker compose up -d` na raiz do TV30.

---

## Estrutura

```
infra/
  docker-compose.yml         # incluido via `include:` pelo compose raiz
  redis/                     # Redis + Redis Commander
  mosquitto_plugin/          # Broker MQTT + plugin C (ACL/consent/schema)
  krakenD_external/          # Gateway HTTPS publico + plugin Go
  krakenD_internal/          # Gateway HTTP interno (serv-a-serv)
  middleware/                # Validacao JWT + OpenAPI (externo) + Swagger UI
  middleware_internal/       # Validacao + OpenAPI (interno) + Swagger UI
  dockerfiles/               # Dockerfiles dos containers que vivem na raiz (aop, ccws, bcast, ...)
  user-files-template/       # Seed embarcado em CCWS/AoP — userData.json baseline
  docs/                      # Documentacao tecnica (fonte de verdade)
```

---

## Documentação

A fonte de verdade técnica está em [`docs/`](./docs/README.md):

- [`01-visao-geral.md`](./docs/01-visao-geral.md) — Visão geral da stack
- [`02-rede-docker.md`](./docs/02-rede-docker.md) — Rede `ginga_net` e portas
- [`03-pipeline-mqtt.md`](./docs/03-pipeline-mqtt.md) — Plugin MQTT (ACL/consent/schema)
- [`04-pipeline-http.md`](./docs/04-pipeline-http.md) — KrakenD + middleware Node
- [`05-autenticacao.md`](./docs/05-autenticacao.md) — Fluxos TV 3.0
- [`06-modelo-redis.md`](./docs/06-modelo-redis.md) — Modelo de dados Redis
- [`08-mqtt-map.md`](./docs/08-mqtt-map.md) — Mapa completo de tópicos MQTT

Para subir a stack completa (incluindo `aop`, `ccws`, `bcast`) e configurar `.env`, ver o [README do TV30](../README.md).
