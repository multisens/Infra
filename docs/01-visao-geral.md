# Visão Geral do Sistema

> **Nota (2026-10-02):** diagrama anterior à consolidação. O KrakenD na porta 8090 e o middleware Node `/validate` não existem mais: a borda é o `edgegateway` (44642/44643) com o plugin `tv30-auth` (ver [04](04-pipeline-http.md) e [05](05-autenticacao.md)). O broker não consulta o Redis (ACL e consentimento saíram no item 3; o plugin C só valida esquema). Revisão geral: pendente (backlog).

Fluxo completo de uma interação no receptor TV 3.0, do dispositivo externo ao display.

```mermaid
graph TD
    subgraph Externos["Dispositivos Externos"]
        APP["App / Celular / TV Remota"]
    end

    subgraph Infra["aop_infra (Docker)"]
        KD["KrakenD\nAPI Gateway\n:8090"]
        MW["Middleware Node.js\n/validate\n(em construção)"]
        TV3WS["tv3ws\nTV 3.0 WebServices\n:44642 / :44643"]
        MQ["Mosquitto + Plugin C\n:1883 / :9001"]
        REDIS["Redis\n:6379"]
        AOP["AoP\nUI do Receptor\n:8080"]
    end

    subgraph Display["Display"]
        BROWSER["Browser / Tela do Receptor"]
    end

    APP -->|"HTTP/HTTPS\n(TV 3.0 REST API)"| KD
    KD -->|"POST /validate\n+ headers"| MW
    MW -->|"200 OK / 4xx"| KD
    KD -->|"requisição autorizada"| TV3WS

    TV3WS -->|"publica/assina\nMQTT"| MQ
    MQ -->|"GET acl, consent"| REDIS
    MQ -->|"mensagem validada"| AOP

    TV3WS -->|"lê/escreve\nJWT, sessões"| REDIS

    AOP -->|"HTML renderizado"| BROWSER
    APP -->|"WebSocket\n(remote device)"| TV3WS
```

---

## Responsabilidades por camada

```mermaid
graph LR
    subgraph "Camada de Entrada"
        KD["KrakenD\nRoteamento + Gateway"]
        MW["Middleware Node.js\nValidação de políticas HTTP"]
    end

    subgraph "Camada de Negócio"
        TV3WS["tv3ws\nAPI TV 3.0\nAutenticação JWT\nGestão de usuários/apps"]
        AOP["AoP\nInterface Visual\nGestão de estado"]
    end

    subgraph "Camada de Mensageria"
        MQ["Mosquitto\nBroker MQTT"]
        PLUGIN["Plugin C\nACL + Consentimento\n+ Schema Validation"]
    end

    subgraph "Camada de Dados"
        REDIS["Redis\nACL · Consentimento\nPerfis · Sessões"]
    end

    KD --> MW
    MW --> KD
    KD --> TV3WS
    TV3WS <--> AOP
    TV3WS --> MQ
    AOP --> MQ
    MQ --> PLUGIN
    PLUGIN --> REDIS
    TV3WS --> REDIS
```
