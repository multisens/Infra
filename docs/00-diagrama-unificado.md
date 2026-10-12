# Diagrama unificado — estado atual

> **Estado do código em 12/10/2026**, conferido nos composes (`docker-compose.yml` e `docker-compose.ssdp.yml` da raiz do TV30, `infra/docker-compose.yml` e `infra/redis/docker-compose.yml`) e no código de cada serviço. Até 12/10 este arquivo trazia só o diagrama de antes da consolidação, que ficou no fim, na seção [Até 02/10 (histórico)](#até-0210-histórico).
>
> Mensageria (MQTT), gateways (KrakenD) e Redis são decisões deste projeto e não aparecem na ABNT NBR 25608. Da norma vêm a porta 44642 do `Server-BaseURL` e a descoberta SSDP (C.3.4), as APIs de `/tv3` (Anexo C) e as classes de cliente (C.4.1).

## Stack padrão (bridge `ginga_net`)

```mermaid
graph LR
    subgraph FORA["Fora do Docker"]
        TV["Navegador da TV<br/>(página do AoP)"]
        APPE["App de emissora no iframe da TV<br/>origem própria http://{app}.apps.localhost:8080<br/>(P1.3)"]
        CLI["Cliente local autônomo<br/>ou não local"]
        RD["Dispositivo remoto<br/>(WebSocket do remote-device)"]
    end

    subgraph NET["Rede Docker ginga_net (bridge)"]
        subgraph EDGE["edgegateway"]
            KI["KrakenD interna :44642<br/>26 rotas"]
            KE["KrakenD externa :44643<br/>23 rotas (só as que admitem o não local)"]
            PL["plugin tv30-auth<br/>(nas duas superfícies)"]
            DOC["httpd :8085<br/>Swagger UI + 2 OpenAPI"]
        end
        TV3["tv3ws<br/>HTTP :44652 e HTTPS :44653 (não publicadas)<br/>WebSocket :45000-45199"]
        AOP["aop :8080<br/>páginas da TV, host de cada app,<br/>/videoStreamProxy, /mqtt"]
        BC["bcast :8081<br/>pacotes das apps e mídia"]
        MQ["mqtt-broker (serviço mosquitto)<br/>MQTT :1883, WebSocket :9001<br/>plugin C: esquema e teste de latência"]
        RE["redis :6379 (sem senha)<br/>biblioteca de funções tv30 (P4)<br/>commander :18081 (com login)"]
    end

    CLI -->|"HTTP 44642"| KI
    CLI -->|"HTTP 44643"| KE
    APPE -->|"HTTP 44642<br/>(associado pelo Origin, L1)"| KI
    APPE -->|"HTTP no host da app"| AOP
    TV -->|"HTTP 8080"| AOP
    TV -->|"WebSocket /mqtt"| AOP
    RD -->|"WebSocket 45000-45199"| TV3

    KI -->|"HTTP tv3ws:44652"| TV3
    KE -->|"HTTPS tv3ws:44653"| TV3
    PL -.->|"lê clients:*, origins:associated, session:*<br/>e bind-context:*; grava bind-context:* (FCALL)"| RE

    AOP -->|"proxy HTTP bcast:8081<br/>(apps e vídeo)"| BC
    AOP -->|"repassa o WebSocket<br/>a mosquitto:9001"| MQ
    AOP <-->|"MQTT 1883"| MQ
    TV3 <-->|"MQTT 1883"| MQ
    BC -->|"MQTT 1883: tlm/lls, tlm/sls,<br/>video/event"| MQ

    AOP -.->|"lê e grava perfis e<br/>origins:associated (FCALL)"| RE
    TV3 -.->|"lê perfis, origens e clientes; grava session:*,<br/>client(s):*, atributos de emissora (FCALL);<br/>espelho remote-devices:* (direto)"| RE
```

Setas cheias: HTTP, WebSocket ou MQTT. Setas tracejadas: Redis. O plugin `tv30-auth` roda dentro dos dois processos KrakenD e responde ele mesmo a C.6.8 (`/tv3/bind-context`) e a C.6.7.8/C.6.7.9 (`/tv3/api-info`); as demais rotas vão ao tv3ws ([04](04-pipeline-http.md)). O `PL` está separado no desenho só para mostrar o acesso ao Redis.

Pontos que o desenho não mostra:
- **HTTPS do tv3ws.** O tv3ws só abre a 44653 com `HTTPS_KEY` e `HTTPS_CERT` no `tv3ws/.env` (`tv3ws/src/server.ts`). Sem elas, as rotas que a 44643 repassa ao tv3ws respondem 404 `{"error":200}` (o KrakenD não alcança o backend), e as que a própria borda responde continuam.
- **Quem fala com o broker sem estar no desenho.** Os tópicos `aop/services`, `aop/{serviceId}/currentApp`, `aop/{serviceId}/{appId}/path` e `.../doc/*`, que o tv3ws assina, não têm publicador neste repositório ([08](08-mqtt-map.md)).
- **One-shots da raiz.** `preflight` (rede e PID do host; confere portas e, no modo SSDP, o `SSDP_ADVERTISE_HOST`; a borda só sobe depois dele), `userfiles-seed` (semeia `./user-files`) e `sysctl-init` (profile `linux`). Ver [02](02-rede-docker.md).
- **Ninguém fala HTTP com o tv3ws por dentro.** O AoP lê e grava o Redis direto e não usa a porta HTTP do tv3ws (comentário do serviço `aop` no compose da raiz); clientes só chegam ao tv3ws pela borda.

### Portas no host

| Container | Porta do container | Porta no host |
|---|---|---|
| `edgegateway` | 44642 (interna) | 44642, fixa (norma, C.3.4) |
| `edgegateway` | 44643 (externa) | 44643, fixa por padrão (`EDGE_EXT_PUBLISH=44643:44643`, decisão do Luís em 12/10); só o número dá porta livre |
| `edgegateway` | 8085 (documentação) | livre, escolhida pelo Docker (`docker port edgegateway 8085`) |
| `tv3ws` | 44652, 44653 | não publicadas |
| `tv3ws` | 45000–45199 | as mesmas, fixas |
| `aop` | 8080 | `AOP_PUBLIC_PORT`, padrão 8080, fixa (C1) |
| `bcast` | 8081 | livre por padrão (`BCAST_PUBLISH`, C1) |
| `mqtt-broker` | 1883 | 1883, fixa |
| `mqtt-broker` | 9001 (WebSocket) | livre por padrão (`MQTT_WS_PUBLISH`, C1) |
| `redis` | 6379 | 6379, fixa, sem senha (D-L1) |
| `redis` | 18081 (commander) | livre (`docker port redis 18081`) |

## Modo SSDP (override `docker-compose.ssdp.yml`, só Linux nativo)

```mermaid
graph LR
    LAN["Dispositivo na LAN"]
    subgraph HOST["Rede do host"]
        subgraph EDGE2["edgegateway (network_mode: host)"]
            SS["ssdp-announcer<br/>UDP 1900"]
            K2["KrakenD :44642 e :44643"]
        end
    end
    subgraph NET2["ginga_net"]
        T2["tv3ws<br/>44652/44653 publicadas só em 127.0.0.1"]
        R2["redis :6379"]
    end
    LAN -->|"M-SEARCH"| SS
    SS -->|"LOCATION http://{SSDP_ADVERTISE_HOST}:44642/manifest"| LAN
    LAN -->|"GET /manifest e APIs"| K2
    K2 -->|"127.0.0.1:44652 / :44653<br/>(variante host)"| T2
    K2 -.->|"127.0.0.1:6379"| R2
```

O override põe só a borda em rede do host (`EDGE_VARIANT=host`, `REDIS_HOST=127.0.0.1`, `SSDP_ENABLED=true`); o resto da stack continua na `ginga_net`. Detalhes em [05](05-autenticacao.md#descoberta-ssdp).

---

## Até 02/10 (histórico)

> **Registro, não estado atual.** Este é o diagrama de antes da consolidação dos containers, mantido sem alteração no desenho. Não existem mais no código: o KrakenD na porta 8090 com o middleware Node `/validate`; o controle de acesso (ACL) e de consentimento no plugin do broker, que hoje só valida esquema; o `migrate_to_redis.py` no boot do Mosquitto; as chaves `acl:*` e `user:{id}:profile`. As portas 44642/44643 são da borda, e o tv3ws escuta em 44652/44653 sem publicá-las. O estado atual está nas seções acima.

Visão completa do sistema: dispositivos, serviços, segurança, mensageria e dados.

```mermaid
graph TB
    %% ── Externos ──────────────────────────────────────────────────────────────
    subgraph EXT["Dispositivos Externos"]
        DEV_LOCAL["Dispositivo Local\n(mesma rede)"]
        DEV_REMOTE["Dispositivo Remoto\n(rede externa)"]
    end

    %% ── Descoberta ────────────────────────────────────────────────────────────
    subgraph DISC["Descoberta (SSDP :1900)"]
        SSDP["SSDP Server\nM-SEARCH → anuncia receptor"]
    end

    %% ── Gateway HTTP ──────────────────────────────────────────────────────────
    subgraph GW["Gateway HTTP (ginga_net)"]
        KD["KrakenD\n:8090\nRoteamento"]
        MW["Middleware Node.js\n/validate\n① Valida JWT\n② ... futuras políticas"]
    end

    %% ── Aplicações ────────────────────────────────────────────────────────────
    subgraph APPS["Aplicações (GingaDistrib/)"]
        subgraph TV3WS_BOX["tv3ws — TV 3.0 WebServices\n:44642 (HTTP) / :44643 (HTTPS)"]
            AUTH_API["POST /tv3/authorize\nPOST /tv3/token"]
            USER_API["GET/POST /tv3/current-service/users"]
            APP_API["GET /tv3/current-service/apps"]
            DEV_API["WS /tv3/remote-device"]
        end

        subgraph AOP_BOX["AoP — Interface do Receptor\n:8080"]
            DISP["disp-lyr\nDisplay"]
            CAT["app-cat\nCatálogo"]
            PRF["prf-chs / prf-mngr\nPerfis"]
            BTP["btp-app\nMídia"]
        end
    end

    %% ── MQTT ──────────────────────────────────────────────────────────────────
    subgraph MQTT_BOX["Broker MQTT (ginga_net)"]
        subgraph MQ_SVC["Mosquitto :1883 / :9001"]
            MQ["Broker"]
            subgraph PLUGIN["Plugin C — mosquitto_plugin.so"]
                P_ACL["① ACL\nSMEMBERS acl:<user_id>\nwildcards + e #"]
                P_CON["② Consentimento\naop/* → SISMEMBER\nuser:<id>:consent <serviceId>"]
                P_SCH["③ Schema\nsensor/* → JSON Schema\nDraft-07 (C)"]
            end
        end
    end

    %% ── Dados ─────────────────────────────────────────────────────────────────
    subgraph DATA["Dados (ginga_net)"]
        subgraph REDIS_BOX["Redis :6379"]
            R_ACL["acl:<user_id>\nSET de padrões MQTT"]
            R_CON["user:<user_id>:consent\nSET de serviceIds"]
            R_PRF["user:<user_id>:profile\nHASH de atributos"]
            R_SES["sessões / tokens\ngerenciado pelo tv3ws"]
        end
        REDIS_UI["Redis Commander\n:18081 (porta de host dinamica, com login)"]
    end

    %% ── Boot ──────────────────────────────────────────────────────────────────
    subgraph BOOT["Boot do Mosquitto"]
        PY["migrate_to_redis.py\nacl.json + userData.json → Redis"]
    end

    %% ── Display ───────────────────────────────────────────────────────────────
    BROWSER["Browser / Display\ndo Receptor"]

    %% ── Fluxo HTTP ────────────────────────────────────────────────────────────
    DEV_LOCAL  -->|"SSDP M-SEARCH"| SSDP
    DEV_REMOTE -->|"SSDP M-SEARCH"| SSDP
    SSDP       -->|"anuncia :44642"| DEV_LOCAL

    DEV_LOCAL  -->|"HTTP/HTTPS"| KD
    DEV_REMOTE -->|"HTTP/HTTPS"| KD
    KD         -->|"POST /validate + headers"| MW
    MW         -->|"200 OK / 4xx"| KD
    KD         -->|"autorizado"| TV3WS_BOX

    %% ── Autenticação ──────────────────────────────────────────────────────────
    AUTH_API   -->|"lê/escreve tokens"| R_SES

    %% ── tv3ws → MQTT ───────────────────────────────────────────────────────────
    TV3WS_BOX   -->|"publica/assina\nMQTT"| MQ

    %% ── AoP → MQTT ────────────────────────────────────────────────────────────
    AOP_BOX    -->|"publica/assina\nMQTT"| MQ

    %% ── Pipeline do Plugin ────────────────────────────────────────────────────
    MQ         --> P_ACL --> P_CON --> P_SCH
    P_ACL      -->|"SMEMBERS"| R_ACL
    P_CON      -->|"SISMEMBER"| R_CON
    P_SCH      -->|"aprovado"| AOP_BOX

    %% ── Boot ──────────────────────────────────────────────────────────────────
    PY         -->|"SADD / HSET"| R_ACL
    PY         -->|"SADD"| R_CON
    PY         -->|"HSET"| R_PRF

    %% ── Middleware futuro ─────────────────────────────────────────────────────
    MW         -.->|"consultará (futuro)"| R_CON

    %% ── Display ───────────────────────────────────────────────────────────────
    AOP_BOX    -->|"HTML/EJS"| BROWSER
    REDIS_UI   -->|"inspeciona"| REDIS_BOX

    %% ── Estilos ───────────────────────────────────────────────────────────────
    classDef gateway  fill:#f4a261,stroke:#e76f51,color:#000
    classDef app      fill:#457b9d,stroke:#1d3557,color:#fff
    classDef mqtt     fill:#2a9d8f,stroke:#264653,color:#fff
    classDef data     fill:#e9c46a,stroke:#f4a261,color:#000
    classDef external fill:#a8dadc,stroke:#457b9d,color:#000
    classDef boot     fill:#cdb4db,stroke:#9c89b8,color:#000

    class KD,MW gateway
    class TV3WS_BOX,AOP_BOX,AUTH_API,USER_API,APP_API,DEV_API,DISP,CAT,PRF,BTP app
    class MQ,PLUGIN,P_ACL,P_CON,P_SCH mqtt
    class REDIS_BOX,R_ACL,R_CON,R_PRF,R_SES,REDIS_UI data
    class DEV_LOCAL,DEV_REMOTE,SSDP,BROWSER external
    class PY boot
```
