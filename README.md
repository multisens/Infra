# TV30 — `infra/` (submodule)

Submodule do monorepo [TV30](https://github.com/multisens/TV30) que reúne os componentes de **infraestrutura compartilhada** do testbed TV 3.0 (ABNT NBR 25608): armazenamento (Redis, com seed e interface de inspeção embutidos), broker MQTT (Mosquitto + plugin C de validação de esquema) e a **borda** (`edgegateway`: as duas superfícies KrakenD + documentação, geradas de uma tabela única de rotas).

> **Não suba este `docker-compose.yml` isolado.** Ele é incluído via `include:` pelo compose da raiz do TV30 — toda a stack sobe de uma vez com `docker compose up -d` **na raiz do monorepo**. Subir aqui dentro sem o compose raiz não traz os serviços `aop`, `tv3ws` e `bcast`, e a rede `ginga_net` fica órfã.

---

## Serviços expostos

Portas listadas são as do **host** quando a stack sobe pela raiz do TV30.

| Serviço       | Porta host                     | Função                                                                 |
|---------------|--------------------------------|------------------------------------------------------------------------|
| `redis`       | `6379`; UI em porta dinâmica   | Armazenamento (perfis, sessão, credenciais, registro de dispositivos). O container embute o **seed** (carga RESP em tempo de build via `redis-cli --pipe`, roda uma vez — healthcheck só fica saudável após a carga) e o **Redis Commander** (processo auxiliar; a morte dele NÃO derruba o banco). A UI do commander exige login, com usuário e senha em `REDIS_COMMANDER_USER`/`REDIS_COMMANDER_PASSWORD` (padrão `admin`/`tv30-redis-admin`, definido em `redis/docker-compose.yml` e repetido no `redis/entrypoint.sh`; para trocar, defina as variáveis no `.env` da raiz do TV30). Já a conexão com o banco (6379) segue sem senha (D-L1, decidida pelo Luís em 03/10). |
| `mqtt-broker` | `1883`, `9001` (WS)            | Broker MQTT + plugin C de **validação de esquema** na publicação (tópicos reais de sinalização e estado + `sensor/*`; ver `mqtt-broker/plugin/config/schemas.json`). Profile `mqtt`. |
| `edgegateway` | `44642` (interna, fixa da norma C.3.4), `44643` (externa), docs em porta dinâmica. Com o `docker-compose.ssdp.yml` da raiz: rede do host, com `44642`, `44643` e `8085` direto no host e UDP `1900` | A borda num container só: superfícies interna e externa do KrakenD (a mesma tabela de rotas nas duas; cada rota declara em que superfícies existe) + Swagger UI com os dois specs. Tudo gerado **no build** a partir de `edgegateway/routes.json` (tabela única — M4). Proxy puro: status e corpo do backend passam intactos. **Validação de credenciais** (access token, bind-token, classe de cliente; erros 100/104/106/107/108 no formato C.3.2) pelo plugin Go `tv30-auth`, carregado nas duas superfícies — `AUTH_ENFORCE=warn` (padrão) só avisa nas falhas de credencial (o 100 de rota não declarada vale nos dois modos); `enforce` bloqueia. Ver [`edgegateway/plugin/README.md`](./edgegateway/plugin/README.md). **Anúncio SSDP** (C.3.4; L6, opção A, decidido pelo Luís em 09/10): o anunciante em Go `edgegateway/ssdp/` (binário `ssdp-announcer`) só sobe com `SSDP_ENABLED=true`, que o override `docker-compose.ssdp.yml` da raiz do TV30 liga junto com a rede do host e a variante `host` (só Linux nativo). Morre-inteiro: qualquer processo interno caindo derruba o container, inclusive o anunciante (decisão do Luís, 09/10). |

Descobrir as portas dinâmicas: `docker compose ps` (linhas `edgegateway` e `redis`), ou direto `docker port redis 18081` (UI do commander) e `docker port edgegateway 8085` (docs).

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
                             #   + plugin/ (Go, validacao de credenciais tv30-auth)
                             #   + ssdp/ (Go, anunciante SSDP, so com SSDP_ENABLED=true)
                             #   -> gera os configs KrakenD e os OpenAPI no build
  dockerfiles/               # Dockerfiles dos containers que vivem na raiz (aop, tv3ws, bcast, ...)
  user-files-template/       # Seed do armazenamento — userData.json com o perfil padrao ("Viewer 1")
  docs/                      # Documentacao tecnica (fonte de verdade)
```

Toda rota nova das APIs entra **somente** em `edgegateway/routes.json` — uma
edição gera as duas superfícies (variantes linux/windows/host) e os dois specs.

---

## Documentação

A fonte de verdade técnica está em [`docs/`](./docs/README.md):

- [`01-visao-geral.md`](./docs/01-visao-geral.md) — Visão geral da stack
- [`02-rede-docker.md`](./docs/02-rede-docker.md) — Rede `ginga_net` e portas
- [`03-pipeline-mqtt.md`](./docs/03-pipeline-mqtt.md) — Plugin MQTT (validação de esquema)
- [`04-pipeline-http.md`](./docs/04-pipeline-http.md) — A borda (edgegateway/KrakenD)
- [`05-autenticacao.md`](./docs/05-autenticacao.md) — Classes de cliente, emissão do token, validação na borda e SSDP
- [`06-modelo-redis.md`](./docs/06-modelo-redis.md) — Modelo de dados Redis
- [`08-mqtt-map.md`](./docs/08-mqtt-map.md) — Mapa completo de tópicos MQTT

Para subir a stack completa (incluindo `aop`, `tv3ws`, `bcast`) e configurar `.env`, ver o [README do TV30](../README.md).
