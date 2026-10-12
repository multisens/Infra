# Rede Docker

> **Estado do código em 12/10/2026**, conferido em `docker-compose.yml` e `docker-compose.ssdp.yml` da raiz do TV30, `infra/docker-compose.yml` e `infra/redis/docker-compose.yml`, e com `docker compose config` e `docker network ls` na stack de 12/10. Até 12/10 este documento só trazia os diagramas de antes da consolidação; eles ficaram no fim, na seção [Até 02/10 (histórico)](#até-0210-histórico).

Containers, portas publicadas, redes e ordem de subida. O diagrama de quem fala com quem está em [00](00-diagrama-unificado.md).

## Um projeto, uma subida

A stack sobe de uma vez, com `docker compose up -d` na raiz do TV30. O compose da raiz tem nome fixo de projeto (`name: tv30`) e inclui o `infra/docker-compose.yml`, que por sua vez inclui o `infra/redis/docker-compose.yml`. O compose da `infra/` sozinho não é projeto válido: o `edgegateway` depende do `preflight`, que só existe na raiz ([README da infra](../README.md)).

Os profiles decidem o que sobe. O `.env.example` da raiz liga os dois (`COMPOSE_PROFILES=mqtt,linux`):
- sem profile (sobem sempre): `preflight`, `userfiles-seed`, `edgegateway` e `redis`;
- `linux`: `tv3ws`, `aop`, `bcast` e `sysctl-init`;
- `mqtt`: `mosquitto` (container `mqtt-broker`).

## Containers

| Serviço (compose) | Container | Imagem | Rede | Compose |
|---|---|---|---|---|
| `preflight` | `tv30-preflight` | `alpine:3` (one-shot) | rede e PID do host | raiz |
| `userfiles-seed` | `tv30-userfiles-seed` | `alpine:3` (one-shot) | `tv30_default` (rede padrão do projeto, porque o serviço não declara `networks`) | raiz |
| `sysctl-init` | `tv30-sysctl-init` | `alpine:3` (one-shot, privilegiado) | rede do host | raiz |
| `tv3ws` | `tv3ws` | `tv30-tv3ws` | `ginga_net` | raiz |
| `aop` | `aop` | `tv30-aop` | `ginga_net` | raiz |
| `bcast` | `bcast` | `tv30-bcast` | `ginga_net` | raiz |
| `edgegateway` | `edgegateway` | `tv30-edgegateway` | `ginga_net` (no modo SSDP, rede do host) | `infra/` |
| `mosquitto` | `mqtt-broker` | `tv30-mosquitto` | `ginga_net` | `infra/` |
| `redis` | `redis` | `tv30-redis` | `ginga_net` | `infra/redis/` |

As imagens levam o prefixo `${DOCKERHUB_NS:-tv30}/` e a tag `${IMAGE_TAG}` (padrão `dev` nos serviços da raiz e `latest` nos da `infra/`; o `.env.example` define `IMAGE_TAG=latest`).

A `ginga_net` é uma bridge criada pelo próprio projeto (`name: ginga_net`, `driver: bridge`, declarada em `infra/docker-compose.yml` e `infra/redis/docker-compose.yml`, sem `external`). Dentro dela os serviços se acham pelo nome: `tv3ws`, `redis`, `mosquitto`, `bcast`.

## Portas publicadas no host

| Container | Porta do container | Porta no host | Onde se define |
|---|---|---|---|
| `edgegateway` | 44642 (superfície interna) | 44642, fixa (norma, C.3.4) | `infra/docker-compose.yml` |
| `edgegateway` | 44643 (superfície externa) | 44643, fixa por padrão (decisão do Luís em 12/10) | `EDGE_EXT_PUBLISH` (padrão `44643:44643`; só o número dá porta livre, que o `/manifest` não anuncia) |
| `edgegateway` | 8085 (documentação) | livre, escolhida pelo Docker | `infra/docker-compose.yml` |
| `tv3ws` | 44652, 44653 (API) | não publicadas | a borda chega a elas pela `ginga_net` |
| `tv3ws` | 45000–45199 (WebSocket do remote-device) | as mesmas, fixas | compose da raiz (`WS_PORT_MIN`/`WS_PORT_MAX`) |
| `aop` | 8080 | `AOP_PUBLIC_PORT`, padrão 8080, fixa (C1) | compose da raiz |
| `bcast` | 8081 | livre por padrão (C1) | `BCAST_PUBLISH` (padrão `8081`; `8081:8081` fixa) |
| `mqtt-broker` | 1883 (MQTT) | 1883, fixa | `infra/docker-compose.yml` |
| `mqtt-broker` | 9001 (MQTT por WebSocket) | livre por padrão (C1) | `MQTT_WS_PUBLISH` (padrão `9001`; por exemplo `9003:9001` fixa) |
| `redis` | 6379 | 6379, fixa, sem senha (D-L1) | `infra/redis/docker-compose.yml` |
| `redis` | 18081 (redis-commander, com login) | livre, escolhida pelo Docker | `infra/redis/docker-compose.yml` |

A porta livre muda a cada criação do container: `docker compose port <serviço> <porta do container>` ou `docker port <container> <porta>` mostram qual saiu (por exemplo, `docker port edgegateway 8085`). A página da TV não usa a porta publicada da 9001: ela conecta em `ws://<host da página>/mqtt`, na porta do AoP, e o AoP repassa à 9001 do mosquitto pela `ginga_net` (`aop/src/mqtt-ws.js`).

## Ordem de subida

```mermaid
graph LR
    PF["preflight<br/>(one-shot)"]
    US["userfiles-seed<br/>(one-shot)"]
    RE["redis"]
    MQ["mosquitto"]
    EG["edgegateway"]
    TV["tv3ws"]
    AO["aop"]
    BC["bcast"]
    SY["sysctl-init<br/>(one-shot, ninguém depende)"]

    PF -->|"terminou com 0"| EG
    RE -->|"saudável"| EG
    RE -->|"saudável"| TV
    MQ -->|"iniciado"| TV
    US -->|"terminou com 0"| TV
    MQ -->|"iniciado"| AO
    RE -->|"iniciado"| AO
    US -->|"iniciado"| AO
    MQ -->|"iniciado"| BC
```

- **`redis` saudável** quer dizer biblioteca de funções `tv30` carregada nesta partida e carga inicial aplicada (marca `/tmp/tv30-redis-pronto` e chave `seed:done`; ver [`redis/README.md`](../redis/README.md)).
- **`preflight`** confere DNS e portas e só termina com erro em dois casos: a 44642 ocupada por outro processo, ou o modo SSDP sem `SSDP_ADVERTISE_HOST` (B2). Nos dois, a borda não sobe (`scripts/preflight.sh`).
- **`aop`** usa a forma curta de `depends_on`, que só espera os containers iniciarem; ele mesmo tenta de novo a leitura dos perfis no Redis (`aop/src/core.js`, `loadUserData`).
- **`sysctl-init`** roda em paralelo; nenhum serviço espera por ele.

## Modo SSDP (`docker-compose.ssdp.yml`, só Linux nativo)

Ligado por `COMPOSE_FILE=docker-compose.yml:docker-compose.ssdp.yml` no `.env` da raiz. O override:
- põe o `edgegateway` em `network_mode: host` (sai da `ginga_net`, sem `ports` nem `extra_hosts`), com `EDGE_VARIANT=host`, `REDIS_HOST=127.0.0.1` e `SSDP_ENABLED=true`: a borda fala com o tv3ws em `127.0.0.1:44652/44653` e com o Redis na 6379 publicada;
- publica a 44652/44653 do tv3ws só em `127.0.0.1`;
- liga no `preflight` as checagens da borda em rede do host (`SSDP_EDGE=true`).

As portas da borda (44642, 44643 e 8085) ficam direto no host, e o anunciante usa a UDP 1900. Detalhes em [05](05-autenticacao.md#descoberta-ssdp).

## Desenvolvimento com um serviço no host

Com `EDGE_VARIANT=windows`, as superfícies apontam para `host.docker.internal:44654` (interna) e `:44655` (externa) (`infra/edgegateway/routes.json`), e o `extra_hosts: host.docker.internal:host-gateway` faz o nome resolver também em Linux. O Redis continua sendo o do container. Ver `docs/dev-local.md` da raiz do TV30.

---

## Até 02/10 (histórico)

> **Registro, não estado atual.** Os diagramas abaixo são de antes da consolidação de containers e foram mantidos sem alteração no desenho. O que mudou desde então:
> - não há mais `redis-auth` nem container `rediscommander/redis-commander` na `:8081`. O container `redis` (imagem `tv30-redis`) embute a carga inicial e o redis-commander como processo auxiliar, na porta `18081` do container e em porta de host dinâmica (`docker port redis 18081`), com login (D-L1, Luís, 03/10). A 6379 segue publicada e sem senha;
> - não há mais `krakend-gateway` na `:8090`. A borda é o container `edgegateway`, com as superfícies interna (`44642`, fixa da norma C.3.4) e externa (`44643`) e a documentação em porta dinâmica (`docker port edgegateway 8085`);
> - o `mqtt-broker` não consulta o Redis: o controle de acesso a tópicos foi removido, e o plugin só valida esquema e mede latência (`infra/mqtt-broker/plugin/src/mosquitto_plugin.c`). A seta `hiredis` do primeiro diagrama e o passo "migrate_to_redis.py popula Redis" do segundo não existem mais: o `entrypoint.sh` do broker só sobe o Mosquitto, e o `migrate_to_redis.py` fica na imagem só para depuração manual (`infra/mqtt-broker/infra/entrypoint.sh`);
> - as portas de host mudaram em 11/10 (C1, decisão do Luís): ficaram fixas a 44642, a do AoP (8080), a 1883, a 6379 e a faixa 45000–45199 (e a 44643 no modo SSDP); a 44643 na bridge e a 9001 do Mosquitto passaram a sair numa porta de host escolhida pelo Docker (`EDGE_EXT_PUBLISH` e `MQTT_WS_PUBLISH` em `infra/docker-compose.yml`). Desde 12/10 (decisão do Luís), a 44643 voltou a ser fixa por padrão também na bridge (`EDGE_EXT_PUBLISH=44643:44643`), porque o `/manifest` do tv3ws a anuncia; a 9001 segue em porta livre. A página da TV fala MQTT pelo `/mqtt` do AoP, e não mais pela `:9001` do host. A porta 9001 dos diagramas é a do container;
> - as pastas dos diagramas têm nomes antigos: `mosquitto_plugin/` é hoje `infra/mqtt-broker/`, e `krakenD/` deu lugar a `infra/edgegateway/`. Os três `docker compose up` em sequência também não existem mais: o `infra/docker-compose.yml`, incluído pelo compose da raiz do TV30, sobe tudo de uma vez e cria a `ginga_net`.
>
> O estado atual está nas seções acima.

Containers, portas expostas e comunicação interna via `ginga_net`.

```mermaid
graph TB
    subgraph HOST["Máquina Host"]

        subgraph ginga_net["Rede Docker: ginga_net (bridge)"]

            subgraph redis_svc["redis/ (docker-compose.yml)"]
                REDIS["redis-auth\nredis:7-alpine"]
                REDISCMD["redis-commander\nrediscommander/redis-commander"]
            end

            subgraph krakend_svc["krakenD/ (docker-compose.yml)"]
                KD["krakend-gateway\ndevopsfaith/krakend:2.7"]
            end

            subgraph mosquitto_svc["mosquitto_plugin/infra/ (docker-compose.yml)"]
                MQ["mqtt-broker\n(build local)"]
            end
        end

        P6379(":6379")
        P8081(":8081")
        P8090(":8090")
        P1883(":1883")
        P9001(":9001")
    end

    REDIS --- P6379
    REDISCMD --- P8081
    KD --- P8090
    MQ --- P1883
    MQ --- P9001

    REDISCMD -->|"redis:6379"| REDIS
    KD -->|"redis:6379\n(futuro)"| REDIS
    MQ -->|"redis:6379\nhiredis"| REDIS
```

---

## Ordem de criação e dependência de rede

```mermaid
sequenceDiagram
    participant HOST as Host
    participant REDIS as redis/
    participant KD as krakenD/
    participant MQ as mosquitto_plugin/

    HOST->>REDIS: docker compose up -d
    Note over REDIS: Cria ginga_net
    Note over REDIS: Sobe redis + redis-commander

    HOST->>KD: docker compose up -d
    Note over KD: Conecta à ginga_net (external)
    Note over KD: Sobe krakend

    HOST->>MQ: docker compose up -d
    Note over MQ: Conecta à ginga_net (external)
    Note over MQ: migrate_to_redis.py popula Redis
    Note over MQ: Sobe mosquitto com plugin
```
