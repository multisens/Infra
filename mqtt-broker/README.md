# Mosquitto Plugin

Plugin modular para Mosquitto MQTT Broker com duas funcionalidades: validação de schema JSON na publicação e teste de tempo de resposta.

> **Revisto em 2026-10-10 contra o código.** Este README descrevia também um controle de acesso em duas camadas (ACL por usuário e consentimento por `serviceId`, com `authorize.c`, `acl.json` e `userData.json` no plugin). Ele foi removido por decisão de desenho (comentário em `plugin/src/mosquitto_plugin.c`): o plugin não consulta o Redis e não decide acesso por cliente.

## Estrutura do Projeto

```
mqtt-broker/
├── plugin/
│   ├── src/
│   │   ├── mosquitto_plugin.c            # Orquestrador principal
│   │   ├── schema_validator.c            # Validador de JSON Schema
│   │   └── response_time_tester.c        # Teste de latência
│   ├── include/
│   │   ├── schema_validator.h
│   │   └── response_time_tester.h
│   ├── config/
│   │   ├── mosquitto.conf                # Config do broker
│   │   └── schemas.json                  # Schemas de validação
│   ├── tests/
│   │   └── test_all_validations.sh
│   └── docs/
│       └── mosquitto_plugin_context.md
├── infra/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── entrypoint.sh                     # Só sobe o Mosquitto
│   └── migrate_to_redis.py               # Fora da partida; fica na imagem para depuração manual
├── brokertimetest/                       # App web de testes
│   ├── server.js
│   ├── public/
│   └── package.json
└── README.md
```

## Funcionalidades

### 1. Validação de JSON Schema
- Valida o payload de cada PUBLISH contra o schema do tópico em `plugin/config/schemas.json` (chave igual ao tópico ou com os curingas `+` e `#`); tópico sem schema passa sem validação
- Payload fora do schema é recusado, e o erro é publicado em `errors/<client_id>`
- Os schemas declarados cobrem tópicos de estado e de sinalização da plataforma, além dos de teste `sensor/*`

### 2. Teste de Tempo de Resposta
- Mede latência do broker
- Tópicos: `PublisherResponseTime<id>/iteration<N>`
- Responde em: `PluginResponseTime<id>/iteration<N>`

## Instalação

O broker sobe com a stack da raiz do TV30 (serviço `mosquitto`, perfil `mqtt`, no `infra/docker-compose.yml`, que o compose da raiz inclui). Sozinho, a partir desta pasta e com a rede `ginga_net` já criada:

```bash
docker compose -f infra/docker-compose.yml up --build -d
```

## Uso

### Validação de Schema
```bash
mosquitto_pub -h localhost -t "sensor/room1/temperature" -m '{"value": 25}'
```

### Teste de Latência
```bash
# Interface web
cd brokertimetest
npm start
# Abra http://localhost:3000
```

## Desenvolvimento

### Adicionar Nova Funcionalidade

1. Criar `src/nova_funcionalidade.c` e `include/nova_funcionalidade.h`
2. Implementar a lógica
3. Importar no `src/mosquitto_plugin.c`
4. Atualizar `Dockerfile` para compilar
5. Rebuild: `docker compose -f infra/docker-compose.yml up --build -d` (ou, na raiz do TV30, `docker compose --profile mqtt up --build -d mosquitto`)

### Estrutura Modular

Cada funcionalidade é independente:
- **schema_validator**: Validação de dados JSON Schema
- **response_time_tester**: Métricas de performance
- **mosquitto_plugin**: Orquestração e callbacks

## Arquitetura

```
mosquitto_plugin.c (orquestrador)
├── Registra o callback MOSQ_EVT_ACL_CHECK (usado só para validar o PUBLISH)
├── Roteia mensagens para módulos
│   ├── schema_validator → Valida schemas
│   └── response_time_tester → Mede latência
└── Gerencia ciclo de vida do plugin
```

## Documentação

- **Contexto do Plugin**: `plugin/docs/mosquitto_plugin_context.md`
- **Pipeline e validação de schema**: `../docs/03-pipeline-mqtt.md` e `../docs/09-schema-validation.md`
