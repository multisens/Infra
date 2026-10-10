# Pipeline MQTT: validação de esquema

> **Revisto em 2026-10-10 contra o código.** Até esta revisão, este documento descrevia um pipeline de segurança com três camadas (ACL por `client_id` no formato `user_<userId>`, consentimento por `serviceId` e esquema), um módulo `authorize.c` e a carga de ACL e consentimento no Redis pelo `migrate_to_redis.py` na partida. Nada disso existe mais: o controle de acesso a tópicos foi removido por decisão de desenho (comentário em `infra/mqtt-broker/plugin/src/mosquitto_plugin.c`), o plugin não consulta o Redis e só valida esquema e mede latência.

O plugin C intercepta toda mensagem publicada no broker antes de entregá-la aos assinantes. Ele usa o evento `MOSQ_EVT_ACL_CHECK` do Mosquitto só para isso: valida o PUBLISH (`MOSQ_ACL_WRITE`) e deixa passar todo o resto (assinatura e entrega), sem decidir acesso por cliente.

## Fluxo de validação por mensagem

```mermaid
flowchart TD
    PUB["Cliente publica mensagem\ntopic: <tópico>\npayload"]

    PUB --> CHK_SKIP{Tópico especial?}

    CHK_SKIP -->|"errors/*"| ALLOW_SKIP["Entrega direta\n(evita loop)"]
    CHK_SKIP -->|"PluginResponseTime*"| ALLOW_SKIP
    CHK_SKIP -->|"PublisherResponseTime*"| LATENCY["Mede latência\nPublica em PluginResponseTime<id>/iteration<n>\n(a mensagem original é entregue)"]
    CHK_SKIP -->|"$SYS/*"| ALLOW
    CHK_SKIP -->|"outros"| CHK_EMPTY

    CHK_EMPTY{"Payload vazio?"}
    CHK_EMPTY -->|"Sim"| ALLOW
    CHK_EMPTY -->|"Não"| LOOKUP

    LOOKUP["Procura o esquema do tópico em schemas.json\n(igualdade exata; depois padrões com + e #)"]
    LOOKUP -->|"Sem esquema"| ALLOW
    LOOKUP -->|"Com esquema"| PARSE

    PARSE{"Payload é JSON?\n(esquema type=string aceita texto puro)"}
    PARSE -->|"Não"| DENY["MOSQ_ERR_ACL_DENIED\n+ publica em errors/<client_id>\n(INVALID_JSON)"]
    PARSE -->|"Sim"| SCHEMA

    SCHEMA["Valida o payload contra o esquema\n(JSON Schema Draft-07 em C)"]
    SCHEMA -->|"Payload inválido"| DENY_SCHEMA["MOSQ_ERR_ACL_DENIED\n+ publica em errors/<client_id>\n(SCHEMA_VALIDATION_FAILED)"]
    SCHEMA -->|"Payload válido"| ALLOW

    ALLOW["Mensagem entregue\naos assinantes"]
```

Os esquemas declarados (`infra/mqtt-broker/plugin/config/schemas.json`) cobrem tópicos de estado e de sinalização da plataforma, além dos de teste `sensor/*`. O mapa dos tópicos está em [`08-mqtt-map.md`](./08-mqtt-map.md), e a validação em detalhe, em [`09-schema-validation.md`](./09-schema-validation.md).

---

## Estrutura do Plugin C

```mermaid
graph TD
    subgraph mosquitto_plugin.so
        MAIN["mosquitto_plugin.c\nOrquestrador\ncallback_acl_check()\nvalidate_message()\npublish_error()"]
        SCHEMA["schema_validator.c\nValidação JSON Schema\nvalidate_json_schema()"]
        LATENCY["response_time_tester.c\nTeste de latência\nhandle_response_time_test()"]
    end

    SCHEMAS_FILE["/mosquitto/config/schemas.json"]

    MAIN --> SCHEMA
    MAIN --> LATENCY
    MAIN -->|"lê schemas na inicialização"| SCHEMAS_FILE
```

---

## Validações de Schema suportadas

```mermaid
mindmap
  root((JSON Schema\nDraft-07))
    Tipos
      string
      number
      integer
      boolean
      object
      array
      null
    String
      minLength
      maxLength
      pattern (POSIX ERE)
    Número
      minimum
      maximum
      exclusiveMinimum
      exclusiveMaximum
      multipleOf
    Array
      minItems
      maxItems
      uniqueItems
      items (por elemento)
    Objeto
      required
      properties
      minProperties
      maxProperties
      additionalProperties
    Enumeração
      enum
      const
```

---

## Inicialização do container Mosquitto

```mermaid
sequenceDiagram
    participant DC as Docker
    participant EP as entrypoint.sh
    participant MQ as Mosquitto + Plugin

    DC->>EP: entrypoint.sh
    EP->>MQ: exec mosquitto -c /mosquitto/config/mosquitto.conf
    MQ->>MQ: carrega mosquitto_plugin.so
    MQ->>MQ: carrega /mosquitto/config/schemas.json
    Note over MQ: Pronto para receber conexões (1883; WebSocket na 9001)
```

O broker não depende do Redis. O `migrate_to_redis.py` continua na imagem (`/usr/local/bin`), só para depuração manual; a partida não o executa (`infra/mqtt-broker/infra/entrypoint.sh`).
