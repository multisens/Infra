# 09 · Schema Validation no Plugin Mosquitto

Validação de payload MQTT por schema JSON, executada dentro do plugin C do broker. Documento focado no dev que precisa **adicionar / alterar regras** sem precisar mexer em C.

---

## Quando o validador roda

Configurado em `plugin/src/mosquitto_plugin.c` (callback `callback_acl_check`):

```c
// Only validate PUBLISH operations on sensor topics
if (ed->access != MOSQ_ACL_WRITE || strncmp(ed->topic, "sensor/", 7) != 0) {
    return MOSQ_ERR_SUCCESS;
}
```

| Operação | Tópico | Schema validation? |
|---|---|---|
| PUBLISH | `sensor/...` | **Sim** |
| PUBLISH | qualquer outro (`aop/`, `tlm/`, `errors/`, custom) | Não |
| SUBSCRIBE | qualquer | Não |
| PUBLISH em `sensor/X` sem schema definido | — | Não (passa direto) |

Ou seja: **só PUBLISH em `sensor/*` com schema declarado é validado**.

---

## Onde ficam os schemas

Arquivo único: `infra/mosquitto_plugin/plugin/config/schemas.json`.

No build do container, é copiado para `/mosquitto/config/schemas.json` (ver Dockerfile linha 44). O plugin lê esse caminho na inicialização (`mosquitto_plugin.c:182`):

```c
load_schemas_from_file("/mosquitto/config/schemas.json");
```

Estrutura do arquivo: **objeto raiz** onde cada chave é o **tópico exato** e o valor é o schema JSON Draft-07.

```json
{
  "sensor/room1/temperature": { "...schema..." },
  "sensor/room2/humidity":    { "...schema..." }
}
```

> **Match é exato.** Não há suporte a wildcard (`+`/`#`). `sensor/room1/+` não vira regra para `sensor/room1/temperature`.

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

```bash
wsl -- bash -c "cd /mnt/d/ProjCEFET/TV30/infra && \
    docker compose --profile mqtt build mosquitto && \
    docker compose --profile mqtt up -d mosquitto"
```

> Alternativa pra dev rápido (não persiste em commits subsequentes): copiar o arquivo para o container ao vivo e mandar SIGHUP. Mas o plugin não recarrega sem restart, então vai precisar reiniciar de qualquer jeito. Use o build.

### 3 · Verificar nos logs

```bash
wsl -- docker logs mqtt-broker 2>&1 | grep "Schema loaded"
```

Saída esperada:
```
Schemas loaded from file: 4 topics configured
Schema loaded for topic: sensor/room1/temperature
Schema loaded for topic: sensor/room2/humidity
Schema loaded for topic: sensor/comprehensive/test
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

> Tópicos `errors/*` e `PluginResponseTime*` são **ignorados pelo callback** (early-return em `mosquitto_plugin.c:121-128`) pra evitar loop infinito.

---

## Limitações conhecidas

- **Sem wildcards** em chaves de schema. `sensor/+/temperature` não funciona como regra. Cada tópico precisa estar listado explicitamente.
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
callback_acl_check (mosquitto_plugin.c:116)
    ↓ (é WRITE em sensor/?)
validate_message (mosquitto_plugin.c:91)
    ↓ get_schema_for_topic("sensor/X/Y")
    ↓ não tem schema → MOSQ_ERR_SUCCESS (passa)
    ↓ tem schema → tokenize JSON
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
| Liberar/restringir tópicos validados (ex.: validar `aop/*`) | `plugin/src/mosquitto_plugin.c:149-152` (filtro `strncmp("sensor/")`) |
| Mudar formato do JSON publicado em `errors/*` | `plugin/src/mosquitto_plugin.c:55-77` (`publish_error`) |
| Suportar wildcard em chaves do `schemas.json` | `plugin/src/mosquitto_plugin.c:80-88` (`get_schema_for_topic`) |

Qualquer mudança em C exige rebuild da imagem (mesma sequência da seção "Rebuild e restart" acima).
