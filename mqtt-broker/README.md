# Mosquitto Plugin

Plugin modular para Mosquitto MQTT Broker com duas funcionalidades: validação de schema JSON na publicação e teste de tempo de resposta.

> **Revisto em 2026-10-10 contra o código e conferido de novo em 2026-10-12, também depois das correções da tarde** (leitura do payload inteiro, mensagem e tópico de `errors/<client_id>` sempre válidos, escape do `id` no teste de latência e testes novos). Este README descrevia também um controle de acesso em duas camadas (ACL por usuário e consentimento por `serviceId`, com `authorize.c`, `acl.json` e `userData.json` no plugin). Ele foi removido por decisão de desenho (comentário em `plugin/src/mosquitto_plugin.c`): o plugin não consulta o Redis e não decide acesso por cliente.

## Estrutura do Projeto

```
mqtt-broker/
├── plugin/
│   ├── src/
│   │   ├── mosquitto_plugin.c            # Orquestrador principal
│   │   ├── schema_validator.c            # Validador de JSON Schema
│   │   ├── json_payload.c                # Leitura do payload inteiro como texto JSON (desde 12/10)
│   │   ├── error_json.c                  # Mensagem e tópico de errors/<client_id>, escape JSON (desde 12/10)
│   │   └── response_time_tester.c        # Teste de latência
│   ├── include/
│   │   ├── schema_validator.h
│   │   ├── json_payload.h                # Regra da leitura do payload
│   │   ├── error_json.h                  # Formato, tetos e regra do tópico de errors/
│   │   └── response_time_tester.h
│   ├── config/
│   │   ├── mosquitto.conf                # Config do broker
│   │   └── schemas.json                  # Schemas de validação
│   ├── tests/
│   │   ├── test_error_json.c             # Unidade: mensagem e tópico de errors/ (roda no build da imagem)
│   │   ├── test_json_payload.c           # Unidade: leitura do payload (roda no build da imagem)
│   │   ├── check_json_payload.py         # Confere o veredito do test_json_payload com o json do Python
│   │   ├── test_errors_json.sh           # Integração num broker descartável (errors/, payload, latência)
│   │   ├── errors_json_driver.py         # Driver do test_errors_json.sh
│   │   └── test_all_validations.sh       # Integração num broker descartável (keywords do schema)
│   └── docs/
│       └── mosquitto_plugin_context.md
├── infra/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── entrypoint.sh                     # Só sobe o Mosquitto
│   └── migrate_to_redis.py               # Fora da partida; fica na imagem para depuração manual
├── brokertimetest/                       # Teste de latência (interface web e execução sem interface)
│   ├── server.js                         # interface web na porta 3000
│   ├── public/
│   ├── headless-run.js                   # rodadas de 10, 20, 50 e 100 clientes, sem interface
│   ├── headless-compare.js               # comparação com e sem plugin
│   ├── *.sh, *.jsonl, vanilla.conf       # baterias e dados da medição de 21/08
│   ├── RESULTADOS_2026-08-21.md          # registro da medição de 21/08
│   └── package.json
├── SESSAO_07_02_2026.md                  # registro da sessão de 07/02 (controle de acesso, removido depois)
└── README.md
```

## Funcionalidades

### 1. Validação de JSON Schema
- Valida o payload de cada PUBLISH contra o schema do tópico em `plugin/config/schemas.json` (chave igual ao tópico ou com os curingas `+` e `#`); tópico sem schema passa sem validação
- Desde 12/10, o payload é lido inteiro, pelo comprimento, e só vale como JSON se for exatamente um texto JSON (RFC 8259, json-c no modo estrito): sobra depois do valor, NUL, comentário, aspas simples e vírgula sobrando dão `INVALID_JSON`. Nos tópicos de schema `"type": "string"`, o payload que não é texto JSON vale inteiro como string (UUID, caminho, nome de tela)
- Payload fora do schema é recusado, e o erro é publicado em `errors/<client_id>`. Desde 12/10, a mensagem é sempre JSON válido e UTF-8 válido, com até 2047 bytes e `"truncated":true` quando algum campo foi cortado; no tópico, `+`, `#`, `/` e controles do `client_id` viram `_`
- Os schemas declarados cobrem tópicos de estado e de sinalização da plataforma, além dos de teste `sensor/*`
- Detalhes: `../docs/09-schema-validation.md`

### 2. Teste de Tempo de Resposta
- Mede latência do broker
- Tópicos: `PublisherResponseTime<id>/iteration<N>`
- Responde em: `PluginResponseTime<id>/iteration<N>`, com `{localtime, id, iteration, diffTime}` (desde 12/10, o `id` sai escapado, e o buffer da resposta não vaza quando ela não sai)

## Instalação

O broker sobe com a stack da raiz do TV30 (serviço `mosquitto`, perfil `mqtt`, no `infra/docker-compose.yml`, que o compose da raiz inclui). Sozinho, a partir desta pasta e com a rede `ginga_net` já criada:

```bash
docker compose -f infra/docker-compose.yml up --build -d
```

Esse compose isolado (`infra/docker-compose.yml` desta pasta) é outro: publica a 1883 e a 9001 em portas de host fixas (`1883:1883` e `9001:9001`, enquanto a stack da raiz deixa a 9001 em porta livre), usa o mesmo nome de container da stack (`mqtt-broker`, então os dois não sobem juntos) e define `REDIS_HOST`/`REDIS_PORT`, que o broker não usa.

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

## Testes

**No build da imagem** (desde 12/10): o `infra/Dockerfile` compila e roda `plugin/tests/test_error_json.c` e `plugin/tests/test_json_payload.c` antes de compilar o plugin, e o `json` do Python 3 da imagem confere de novo o que cada um gerou. Se um deles falha, a imagem não sai.

**Integração, num broker descartável** (imagem com tag própria, rede própria, sem portas publicadas; não toca a stack nem o container `mqtt-broker`). Precisam de Docker e rodam a partir da raiz do TV30:

```bash
bash infra/mqtt-broker/plugin/tests/test_errors_json.sh       # errors/<client_id>, leitura do payload, teste de latência
bash infra/mqtt-broker/plugin/tests/test_all_validations.sh   # 1 publicação válida e 19 violações do sensor/comprehensive/test
```

Os dois constroem a imagem, sobem o broker, rodam o driver num segundo container da mesma imagem e, no fim, removem container, rede e imagem. `IMG=<imagem> NOBUILD=1` usa uma imagem já construída (e não a remove); `KEEP_IMG=1` guarda a imagem construída. Até 12/10, o `test_all_validations.sh` chamava o container `mosquitto-plugin` (nome antigo) e esperava o erro em `errors/validation`.

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
- **json_payload**: Leitura do payload inteiro como texto JSON
- **error_json**: Mensagem e tópico de `errors/<client_id>` e escape de string JSON (usado também pelo teste de latência)
- **response_time_tester**: Métricas de performance
- **mosquitto_plugin**: Orquestração e callbacks

## Arquitetura

```
mosquitto_plugin.c (orquestrador)
├── Registra o callback MOSQ_EVT_ACL_CHECK (usado só para validar o PUBLISH)
├── Roteia mensagens para módulos
│   ├── json_payload → Lê o payload inteiro (exatamente um texto JSON)
│   ├── schema_validator → Valida schemas
│   ├── error_json → Monta a mensagem e o tópico de errors/<client_id>
│   └── response_time_tester → Mede latência (escape do id por error_json)
└── Gerencia ciclo de vida do plugin
```

## Documentação

- **Contexto do Plugin** (registro do início do desenvolvimento do plugin, com trecho de 17/01/2026; não é estado atual): `plugin/docs/mosquitto_plugin_context.md`
- **Pipeline e validação de schema**: `../docs/03-pipeline-mqtt.md` e `../docs/09-schema-validation.md`
