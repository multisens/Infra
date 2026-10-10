# Rede Docker

> **Nota (2026-10-04): documento histórico, anterior à consolidação de containers.** Os diagramas abaixo não correspondem ao código atual:
> - não há mais `redis-auth` nem container `rediscommander/redis-commander` na `:8081`. O container `redis` (imagem `tv30-redis`) embute a carga inicial e o redis-commander como processo auxiliar, na porta `18081` do container e em porta de host dinâmica (`docker port redis 18081`), com login (D-L1, Luís, 03/10). A 6379 segue publicada e sem senha;
> - não há mais `krakend-gateway` na `:8090`. A borda é o container `edgegateway`, com as superfícies interna (`44642`, fixa da norma C.3.4) e externa (`44643`) e a documentação em porta dinâmica (`docker port edgegateway 8085`);
> - o `mqtt-broker` não consulta o Redis: o controle de acesso a tópicos foi removido, e o plugin só valida esquema e mede latência (`infra/mqtt-broker/plugin/src/mosquitto_plugin.c`). A seta `hiredis` do primeiro diagrama e o passo "migrate_to_redis.py popula Redis" do segundo não existem mais: o `entrypoint.sh` do broker só sobe o Mosquitto, e o `migrate_to_redis.py` fica na imagem só para depuração manual (`infra/mqtt-broker/infra/entrypoint.sh`);
> - as pastas dos diagramas têm nomes antigos: `mosquitto_plugin/` é hoje `infra/mqtt-broker/`, e `krakenD/` deu lugar a `infra/edgegateway/`. Os três `docker compose up` em sequência também não existem mais: o `infra/docker-compose.yml`, incluído pelo compose da raiz do TV30, sobe tudo de uma vez e cria a `ginga_net`.
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
