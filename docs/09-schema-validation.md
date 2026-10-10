# 09 · Schema Validation no Plugin Mosquitto

Validação de payload MQTT por schema JSON, executada dentro do plugin C do broker. Documento focado no dev que precisa **adicionar / alterar regras** sem precisar mexer em C.

---

## Quando o validador roda

Configurado em `plugin/src/mosquitto_plugin.c` (callback `callback_acl_check`). O antigo filtro que restringia a validação a `sensor/*` saiu (P4); o que vale hoje:

```c
// Valida so PUBLISH; ... qualquer topico com esquema declarado e validado
if (ed->access != MOSQ_ACL_WRITE || strncmp(ed->topic, "$SYS/", 5) == 0) {
    return MOSQ_ERR_SUCCESS;
}
```

| Operação | Tópico | Schema validation? |
|---|---|---|
| PUBLISH | qualquer tópico com schema declarado em `schemas.json` (`sensor/`, `aop/`, `tlm/`, `video/event`...) | **Sim** |
| PUBLISH | sem schema declarado | Não (passa direto) |
| PUBLISH | `errors/*`, `PluginResponseTime*`, `PublisherResponseTime*` (teste de latência) e `$SYS/*` | Não |
| PUBLISH | payload vazio | Não |
| SUBSCRIBE | qualquer | Não |

Ou seja: **todo PUBLISH com payload num tópico com schema declarado é validado**. O callback é o de checagem de acesso do Mosquitto, mas o plugin não faz controle de acesso: o que ele recusa é payload fora do schema.

---

## Onde ficam os schemas

Arquivo único: `infra/mqtt-broker/plugin/config/schemas.json`.

No build do container, é copiado para `/mosquitto/config/schemas.json` (`infra/mqtt-broker/infra/Dockerfile`, linha 42). O plugin lê esse caminho na inicialização (`mosquitto_plugin.c:227`):

```c
load_schemas_from_file("/mosquitto/config/schemas.json");
```

Estrutura do arquivo: **objeto raiz** onde cada chave é um **tópico** (exato ou com os curingas MQTT `+` e `#`) e o valor é o schema JSON Draft-07.

```json
{
  "sensor/room1/temperature": { "...schema..." },
  "tlm/sls/+/esg":            { "...schema..." }
}
```

> **Casamento** (`get_schema_for_topic` e `topic_matches`, em `mosquitto_plugin.c`): primeiro a chave igual ao tópico; senão, a primeira chave com `+` ou `#` que casa, na ordem do arquivo. `tlm/sls/+/esg` vale para `tlm/sls/<serviceId>/esg`.
>
> **Payload texto puro:** se o payload não é JSON e o schema declara `"type": "string"`, ele é validado como string (uuid, caminho, nome de tela, os casos de `aop/currentUser` e `aop/currentService`).

---

## Keywords suportadas

Subset do JSON Schema Draft-07. Implementação em `plugin/src/schema_validator.c`.

### Tipos (`type`)
`string` · `number` · `integer` · `boolean` · `array` · `object` · `null`

### Por tipo aplicável

| Keyword | Tipos | Descrição |
|---|---|---|
| `minimum` / `maximum` | number, integer | Valor inclusivo |
| `exclusiveMinimum` / `exclusiveMaximum` | number, integer | Valor exclusivo |
| `multipleOf` | number, integer | Múltiplo do valor |
| `minLength` / `maxLength` | string | Comprimento da string |
| `pattern` | string | Regex (POSIX, sem flags) |
| `minItems` / `maxItems` | array | Tamanho do array |
| `uniqueItems` | array | Sem duplicatas |
| `items` | array | Schema aplicado a cada elemento (ou array de schemas posicional) |
| `minProperties` / `maxProperties` | object | Quantidade de chaves |
| `required` | object | Lista de propriedades obrigatórias |
| `properties` | object | Schema por propriedade |
| `additionalProperties` | object | `false` proíbe extras; ou schema para validar extras |

### Genéricos (qualquer tipo)
| Keyword | Descrição |
|---|---|
| `enum` | Valor deve ser um da lista |
| `const` | Valor deve ser exatamente este |
| `description` | Apenas documentação (ignorado pelo validador) |
| `$schema` | Apenas documentação (ignorado) |

> **Não suportados** (silenciosamente ignorados): `oneOf`, `anyOf`, `allOf`, `not`, `if/then/else`, `format`, `contains`, `propertyNames`, `dependencies`, `definitions`/`$ref`, `$id`. Se precisar de algum deles, vai precisar implementar em `schema_validator.c`.

---

## Adicionar uma regra nova — passo a passo

### 1 · Editar `plugin/config/schemas.json`

Acrescenta uma chave nova com o tópico exato e seu schema. Mantém vírgulas e brackets corretos.

```json
{
  "sensor/room1/temperature": { ... },
  "sensor/garage/co2": {
    "$schema": "http://json-schema.org/draft-07/schema#",
    "type": "object",
    "properties": {
      "ppm":       { "type": "integer", "minimum": 0, "maximum": 5000 },
      "timestamp": { "type": "string",  "minLength": 1 },
      "sensor_id": { "type": "string",  "pattern": "^CO2-[0-9]{3}$" }
    },
    "required": ["ppm", "timestamp"],
    "additionalProperties": false
  }
}
```

### 2 · Rebuild e restart do container

O schema é copiado pra dentro da imagem em build-time, então `docker compose restart` **não basta** — precisa rebuild:

Pelo compose da raiz do TV30, que inclui o `infra/docker-compose.yml` (subir o da `infra/` isolado cria outro projeto, com o mesmo nome de container):

```bash
wsl -- bash -c "cd /mnt/d/Proj_CEFET/TV30 && \
    docker compose --profile mqtt build mosquitto && \
    docker compose --profile mqtt up -d mosquitto"
```

> Alternativa pra dev rápido (não persiste em commits subsequentes): copiar o arquivo para o container ao vivo e mandar SIGHUP. Mas o plugin não recarrega sem restart, então vai precisar reiniciar de qualquer jeito. Use o build.

### 3 · Verificar nos logs

```bash
wsl -- docker logs mqtt-broker 2>&1 | grep "Schema loaded"
```

Saída esperada (uma linha por tópico do arquivo; abreviada aqui):
```
Schemas loaded from file: <n> topics configured
Schema loaded for topic: sensor/room1/temperature
...
Schema loaded for topic: sensor/garage/co2
```

Se não aparecer `Schema loaded for topic: sensor/garage/co2`, ou JSON está malformado (procurar `Invalid JSON in schemas file`), ou o build não pegou o arquivo atualizado (rebuild com `--no-cache`).

---

## Como testar a regra

**Payload válido** (não deve aparecer erro):
```bash
mosquitto_pub -h localhost -p 1883 -t sensor/garage/co2 \
    -m '{"ppm":420,"timestamp":"2026-05-05T10:00:00Z","sensor_id":"CO2-007"}'
```

**Payload inválido** (deve ser bloqueado e gerar mensagem em `errors/<clientId>`):
```bash
# subscreve em errors antes
mosquitto_sub -h localhost -p 1883 -t 'errors/#' -v &

# publish com violacao (ppm fora do range, sensor_id pattern errado)
mosquitto_pub -h localhost -p 1883 -i my-test-client -t sensor/garage/co2 \
    -m '{"ppm":99999,"timestamp":"now","sensor_id":"abc"}'
```

Saída esperada na subscrição em `errors/`:
```json
errors/my-test-client {
  "timestamp":"2026-05-05T10:00:00",
  "topic":"sensor/garage/co2",
  "error_type":"SCHEMA_VALIDATION_FAILED",
  "details":"Value 99999 exceeds maximum 5000",
  "client_id":"my-test-client",
  "payload":"..."
}
```

---

## Comportamento de erro

Quando a validação falha, o plugin:

1. Retorna `MOSQ_ERR_ACL_DENIED` ao broker → mensagem **não é entregue** aos subscribers
2. Loga em `MOSQ_LOG_INFO`: `Validation failed for topic <X>: <reason>`
3. Publica um JSON de erro em `errors/<clientId>` (sem retain, QoS 0). Estrutura:

```json
{
  "timestamp": "ISO 8601",
  "topic": "tópico que falhou",
  "error_type": "INVALID_JSON" | "SCHEMA_VALIDATION_FAILED",
  "details": "razão legível",
  "client_id": "...",
  "payload": "payload bruto recusado"
}
```

> Tópicos `errors/*` e `PluginResponseTime*` são **ignorados pelo callback** (early-return em `mosquitto_plugin.c:163-171`) pra evitar loop infinito.

---

## Limitações conhecidas

- **Curingas sem prioridade por especificidade.** Com mais de uma chave com `+`/`#` casando o mesmo tópico, vale a primeira na ordem do arquivo, e não a mais específica.
- **Sem `$ref`** — não dá pra reusar sub-schemas via referência. Copia/cola.
- **`pattern` é regex POSIX simples**, sem flags. Sem suporte a `^...$` multi-linha, lookahead/lookbehind, etc.
- **Erros de schema malformado são silenciosos no carregamento** — só loga `Invalid JSON in schemas file`, e o plugin segue funcionando com schemas vazios. Sempre validar o JSON com `jq`/`json5` antes do rebuild.
- **`format` (date-time, email, uri, etc.) é ignorado**. Pra validar formatos, usa `pattern` com regex.
- **Combinadores `oneOf`/`anyOf`/`allOf`/`not` não existem.** Cada propriedade tem um único schema.
- **Mudanças no schema exigem rebuild da imagem**, não só restart. O JSON é copiado em build-time pelo Dockerfile.

---

## Fluxo resumido

```
Cliente publica em sensor/X/Y
    ↓
callback_acl_check (mosquitto_plugin.c:159)
    ↓ (é WRITE, fora de errors/, PluginResponseTime*, PublisherResponseTime* e $SYS/, com payload?)
validate_message (mosquitto_plugin.c:129)
    ↓ get_schema_for_topic("sensor/X/Y") (exato, depois + e #)
    ↓ não tem schema → MOSQ_ERR_SUCCESS (passa)
    ↓ tem schema → tokenize JSON (schema type=string aceita texto puro)
        ↓ JSON inválido → publish_error("INVALID_JSON") + DENIED
        ↓ JSON válido → validate_with_schema (schema_validator.c:396)
            ↓ falha → publish_error("SCHEMA_VALIDATION_FAILED") + DENIED
            ↓ passa → MOSQ_ERR_SUCCESS
```

---

## Onde mexer no código (se schemas.json não dá conta)

| O que mudar | Arquivo |
|---|---|
| Adicionar nova keyword (ex.: `format`) | `plugin/src/schema_validator.c` — criar `validate_<keyword>()` e chamar em `validate_with_schema` |
| Validar um tópico novo | só `plugin/config/schemas.json` (sem filtro de prefixo no código) |
| Mudar os tópicos que nunca são validados (`errors/`, teste de latência, `$SYS/`) | `plugin/src/mosquitto_plugin.c:163-197` (`callback_acl_check`) |
| Mudar formato do JSON publicado em `errors/*` | `plugin/src/mosquitto_plugin.c:54-76` (`publish_error`) |
| Mudar o casamento de tópico (exato, depois `+`/`#`) | `plugin/src/mosquitto_plugin.c:80-117` (`topic_matches`, `get_schema_for_topic`) |

Qualquer mudança em C exige rebuild da imagem (mesma sequência da seção "Rebuild e restart" acima).
