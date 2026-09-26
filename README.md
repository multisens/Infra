# TV30 — `infra/` (submodule)

Submodule do monorepo [TV30](https://github.com/multisens/TV30) que reúne os componentes de **infraestrutura compartilhada** do testbed TV 3.0 (ABNT NBR 25608): armazenamento (Redis, com seed e interface de inspeção embutidos), broker MQTT (Mosquitto + plugin C de validação de esquema) e a **borda** (`edgegateway`: as duas superfícies KrakenD + documentação, geradas de uma tabela única de rotas).

> **Não suba este `docker-compose.yml` isolado.** Ele é incluído via `include:` pelo compose da raiz do TV30 — toda a stack sobe de uma vez com `docker compose up -d` **na raiz do monorepo**. Subir aqui dentro sem o compose raiz não traz os serviços `aop`, `tv3ws` e `bcast`, e a rede `ginga_net` fica órfã.

---

## Serviços expostos

Portas listadas são as do **host** quando a stack sobe pela raiz do TV30.

| Serviço       | Porta host                     | Função                                                                 |
|---------------|--------------------------------|------------------------------------------------------------------------|
| `redis`       | `6379`; UI em porta dinâmica   | Armazenamento (perfis, sessão, credenciais, registro de dispositivos). O container embute o **seed** (carga RESP em tempo de build via `redis-cli --pipe`, roda uma vez — healthcheck só fica saudável após a carga) e o **Redis Commander** (processo auxiliar; a morte dele NÃO derruba o banco). |
| `mqtt-broker` | `1883`, `9001` (WS)            | Broker MQTT + plugin C de **validação de esquema** na publicação (tópicos reais de sinalização e estado + `sensor/*`; ver `mqtt-broker/plugin/config/schemas.json`). Profile `mqtt`. |
| `edgegateway` | `44642` (interna, fixa da norma C.3.4), `44643` (externa), docs em porta dinâmica | A borda num container só: superfícies interna e externa do KrakenD (conjuntos de rota distintos) + Swagger UI com os dois specs. Tudo gerado **no build** a partir de `edgegateway/routes.json` (tabela única — M4). Proxy puro: status e corpo do backend passam intactos. Morre-inteiro: qualquer processo interno caindo derruba o container. |

Descobrir as portas dinâmicas: `docker compose ps` (linhas `edgegateway` e `redis`).

O serviço `sysctl-init` mencionado em alguns docs **não vive aqui** — está no `docker-compose.yml` da raiz do TV30. Ele é um one-shot privilegiado (`alpine:3`, `network_mode: host`, `profiles: [linux]`) que executa `sysctl -w net.bridge.bridge-nf-call-iptables=0` no host Linux antes do resto da stack subir. Sem isso, em alguns kernels o tráfego entre containers pela bridge é interceptado por regras `iptables` do host e MQTT/Redis ficam intermitentes. Em hosts onde o `sysctl` é read-only (ex.: Docker Desktop), o comando falha silenciosamente — o `|| echo 'sysctl skipped...'` cobre esse caso.

---

## Build local das imagens (opcional)

Em deploy normal as imagens vêm prontas do Docker Hub. Se quiser buildar localmente sem subir nada:

```bash
docker compose -f infra/docker-compose.yml build
```

Isso **apenas builda** as imagens definidas neste compose (mosquitto, edgegateway, redis). Para de fato rodar a stack, use `docker compose up -d` na raiz do TV30.

---

## Estrutura

```
infra/
  docker-compose.yml         # incluido via `include:` pelo compose raiz
  redis/                     # Redis consolidado (seed em build + commander embutido)
  mqtt-broker/               # Broker MQTT + plugin C (validacao de schema)
  edgegateway/               # A borda: routes.json (fonte unica) + generate.js
                             #   -> gera os configs KrakenD e os OpenAPI no build
  dockerfiles/               # Dockerfiles dos containers que vivem na raiz (aop, tv3ws, bcast, ...)
  user-files-template/       # Seed do armazenamento — userData.json com o perfil padrao ("Viewer 1")
  docs/                      # Documentacao tecnica (fonte de verdade)
```

Toda rota nova das APIs entra **somente** em `edgegateway/routes.json` — uma
edição gera as duas superfícies (variantes linux/windows) e os dois specs.

---

## Documentação

A fonte de verdade técnica está em [`docs/`](./docs/README.md):

- [`01-visao-geral.md`](./docs/01-visao-geral.md) — Visão geral da stack
- [`02-rede-docker.md`](./docs/02-rede-docker.md) — Rede `ginga_net` e portas
- [`03-pipeline-mqtt.md`](./docs/03-pipeline-mqtt.md) — Plugin MQTT (validação de esquema)
- [`04-pipeline-http.md`](./docs/04-pipeline-http.md) — A borda (edgegateway/KrakenD)
- [`05-autenticacao.md`](./docs/05-autenticacao.md) — Fluxos TV 3.0
- [`06-modelo-redis.md`](./docs/06-modelo-redis.md) — Modelo de dados Redis
- [`08-mqtt-map.md`](./docs/08-mqtt-map.md) — Mapa completo de tópicos MQTT

Para subir a stack completa (incluindo `aop`, `tv3ws`, `bcast`) e configurar `.env`, ver o [README do TV30](../README.md).
