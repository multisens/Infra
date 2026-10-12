# Visão geral do sistema

> **Estado do código em 12/10/2026**, conferido nos composes da raiz e da `infra/` e no código de cada serviço. Até 12/10, os dois diagramas deste arquivo eram de antes da consolidação; eles ficaram no fim, na seção [Até 02/10 (histórico)](#até-0210-histórico). Containers e portas estão em [00](00-diagrama-unificado.md) e [02](02-rede-docker.md).
>
> Mensageria, gateways e Redis são decisões deste projeto; a ABNT NBR 25608 define as APIs e as classes de cliente, não onde elas rodam.

## O caminho de uma requisição de cliente

```mermaid
graph TD
    CLI["Cliente<br/>(app local, celular, app de emissora)"]
    subgraph EDGE["edgegateway (a borda)"]
        PL["plugin tv30-auth<br/>rota (100) → classe (106) →<br/>access token (107) → bind-token (104/108)"]
        EA["APIs respondidas na borda<br/>C.6.8 bind-context<br/>C.6.7.8 / C.6.7.9 api-info"]
        KR["roteador KrakenD<br/>(proxy no-op)"]
    end
    TV3["tv3ws<br/>demais APIs de /tv3<br/>(negocia Accept-Version, emite o token)"]
    MQ["mqtt-broker<br/>(plugin C: esquema)"]
    AOP["AoP<br/>(plataforma: telas, perfis, apps)"]
    RE[("Redis")]
    TVB["Navegador da TV"]
    RD["Dispositivo remoto"]

    CLI -->|"HTTP 44642 / 44643"| PL
    PL -->|"rota com edge"| EA
    PL -->|"demais rotas"| KR
    KR -->|"HTTP 44652 / HTTPS 44653"| TV3
    PL -.->|"credenciais"| RE
    EA -.->|"bind-context:*"| RE
    TV3 -.->|"lê perfis, origens e clientes;<br/>grava sessão e clientes"| RE
    TV3 <-->|"MQTT"| MQ
    AOP <-->|"MQTT"| MQ
    AOP -.->|"perfis e origens associadas"| RE
    AOP -->|"páginas HTTP; camadas e pop-ups<br/>por MQTT no WebSocket /mqtt"| TVB
    RD -->|"WebSocket do remote-device"| TV3
```

A borda é o único ponto que valida credencial (D1, de 28/09; D-0510-1, de 05/10): o tv3ws não confere token nem classe. Com `AUTH_ENFORCE=warn` (padrão), as falhas de credencial só são registradas e marcadas com `X-TV30-Auth-Warn`. Detalhes em [04](04-pipeline-http.md) e [05](05-autenticacao.md).

Dois exemplos de interação que passam pelo broker:
- **Autorização (`GET /tv3/authorize`, C.6.1.2).** O tv3ws publica o pop-up sim/não em `aop/display/layers/popup/yesno/message`; a página da TV o mostra e publica a resposta em `.../yesno/response`; só `"true"` autoriza ([05](05-autenticacao.md)).
- **Troca de usuário corrente (`POST /tv3/current-service/users/current-user`, C.6.14.4).** O tv3ws grava `session:current-user` pela função `tv30_sessao_usuario` e publica `aop/currentUser` retido; o AoP, dono dos perfis, grava o `lastAccess` ([07](07-usuarios-perfis.md)).

## Responsabilidades por serviço

| Serviço | O que faz | Com quem fala |
|---|---|---|
| `edgegateway` | Validação de credenciais, C.6.8 e C.6.7.8/C.6.7.9, roteamento ao tv3ws, CORS, documentação (8085) e, no modo SSDP, o anúncio (C.3.4) | tv3ws (HTTP/HTTPS), Redis |
| `tv3ws` | Demais APIs de `/tv3`; emissão do access token; WebSocket do remote-device | broker (MQTT), Redis |
| `aop` | Telas da TV, gestor de perfis (dono de `users:index`, `user:{id}` e `user:{id}:consent`), lançamento das apps de emissora no host próprio de cada uma (`origins:associated`), proxy das apps e do vídeo, `/mqtt` | broker (MQTT e repasse do WebSocket), Redis, bcast (HTTP) |
| `bcast` | Simula a transmissão: publica LLS e SLS (`tlm/lls/...`, `tlm/sls/...`) e `video/event`, e serve os pacotes das apps e a mídia | broker (MQTT) |
| `mqtt-broker` | Barramento entre os serviços; o plugin C valida o esquema da publicação e responde ao teste de latência | ninguém (não consulta o Redis) |
| `redis` | Estado compartilhado; a biblioteca de funções `tv30` confere o esquema de toda escrita do código (P4) | ninguém |

---

## Até 02/10 (histórico)

> **Registro, não estado atual.** Os dois diagramas abaixo são de antes da consolidação e foram mantidos sem alteração no desenho. Não existem mais no código: o KrakenD na porta 8090 e o middleware Node `/validate`; o tv3ws nas portas 44642/44643 (hoje são da borda; o tv3ws escuta em 44652/44653 sem publicá-las); a consulta do broker ao Redis (ACL e consentimento saíram no item 3; o plugin C só valida esquema). O estado atual está nas seções acima.

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
