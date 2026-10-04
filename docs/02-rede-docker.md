# Rede Docker

> **Nota (2026-10-04): documento histórico, anterior à consolidação de containers.** Os diagramas abaixo não correspondem ao código atual:
> - não há mais `redis-auth` nem container `rediscommander/redis-commander` na `:8081`. O container `redis` (imagem `tv30-redis`) embute a carga inicial e o redis-commander como processo auxiliar, na porta `18081` do container e em porta de host dinâmica (`docker port redis 18081`), com login (D-L1, Luís, 03/10). A 6379 segue publicada e sem senha;
> - não há mais `krakend-gateway` na `:8090`. A borda é o container `edgegateway`, com as superfícies interna (`44642`, fixa da norma C.3.4) e externa (`44643`) e a documentação em porta dinâmica (`docker port edgegateway 8085`);
> - o `mqtt-broker` não consulta o Redis: o controle de acesso a tópicos foi removido (`infra/docker-compose.yml`).
>
> O estado atual está em `infra/README.md` e `infra/ARCHITECTURE.md`. Revisão deste documento: pendente.

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
