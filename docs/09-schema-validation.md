# 09 · Schema Validation no Plugin Mosquitto

Validação de payload MQTT por schema JSON, executada dentro do plugin C do broker. Documento focado no dev que precisa **adicionar / alterar regras** sem precisar mexer em C.

---

## Quando o validador roda

Configurado em `plugin/src/mosquitto_plugin.c` (callback `callback_acl_check`):

```c
// ... Valida so PUBLISH; topico sem esquema passa.
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

No build do container, é copiado para `/mosquitto/config/schemas.json` (`COPY` do `infra/mqtt-broker/infra/Dockerfile`). O plugin lê esse caminho na inicialização (`mosquitto_plugin_init`, em `mosquitto_plugin.c`):

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
> **Leitura do payload** (`json_payload_parse`, em `plugin/src/json_payload.c`, regra em `plugin/include/json_payload.h`): o payload é lido **inteiro, pelo comprimento**, e só vale como JSON se for exatamente um texto JSON da RFC 8259 (seção 2): espaço em branco, um valor, espaço em branco, com espaço em branco = espaço, tab, LF ou CR. O json-c roda no modo estrito. Viram `INVALID_JSON`: sobra depois do valor (`{"value":1}lixo`, dois valores seguidos), NUL em qualquer posição (`{"value":1}\0lixo`), comentário (`{"value":1}/*x*/`, `//x`), aspas simples, vírgula sobrando, `TRUE`, zero à esquerda e `\f` ou `\v` em volta do valor.
>
> **Payload texto puro:** se o payload não é um texto JSON e o schema declara `"type": "string"`, o payload **inteiro** é validado como string (uuid, caminho, nome de tela, os casos de `aop/currentUser` e `aop/currentService`), desde que não tenha NUL (com NUL, `INVALID_JSON`). O literal `null` também cai aqui (o json-c o representa por `NULL`). Payload que é um texto JSON vale como o valor dele: `"abc"`, com aspas, é a string `abc`, e `42` é número, recusado por `type: string`.

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

Pelo compose da raiz do TV30, que inclui o `infra/docker-compose.yml` (subir o da `infra/` isolado cria outro projeto, com o mesmo nome de container). Na raiz do TV30:

```bash
docker compose --profile mqtt build mosquitto
docker compose --profile mqtt up -d mosquitto
```

> O plugin só lê o arquivo na própria inicialização (`mosquitto_plugin_init`, em `mosquitto_plugin.c`), ou seja, na partida do broker. Copiar o arquivo para dentro do container (`docker cp`) e reiniciá-lo funciona só até o container ser recriado, porque a imagem continua com a cópia antiga. Use o build.

### 3 · Verificar nos logs

```bash
docker logs mqtt-broker 2>&1 | grep -E "Schemas? loaded"
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

Saída esperada na subscrição em `errors/` (uma linha só; quebrada aqui para leitura):
```
errors/my-test-client {"timestamp":"2026-05-05T10:00:00","topic":"sensor/garage/co2",
  "error_type":"SCHEMA_VALIDATION_FAILED",
  "details":"Property 'ppm': Value 99999.00 is greater than maximum 5000.00",
  "client_id":"my-test-client","payload":"{\"ppm\":99999,\"timestamp\":\"now\",\"sensor_id\":\"abc\"}"}
```

O `details` vem de `schema_validator.c` (`validate_properties` prefixa `Property '<nome>': `; `validate_maximum` formata os números com `%.2f`). O `payload` sai escapado, como string JSON (ver *Comportamento de erro*).

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
  "payload": "payload recusado, como string JSON",
  "truncated": true
}
```

O `timestamp` sai em UTC no formato `AAAA-MM-DDTHH:MM:SS`, sem fuso. A mensagem é montada por `error_json_build` (`plugin/src/error_json.c`, regra em `plugin/include/error_json.h`; chamada em `publish_error`, em `mosquitto_plugin.c`) e é **sempre JSON válido e UTF-8 válido**:
- aspas, barra invertida e U+0000 a U+001F saem escapados (RFC 8259, seção 7);
- byte que não forma UTF-8 válido vira U+FFFD, escrito `�` (o texto JSON tem de ser UTF-8, seção 8.1; o payload MQTT é binário);
- o payload entra pelo comprimento, então um NUL no meio dele sai como `\u0000`;
- a mensagem tem no máximo 2047 bytes (`ERROR_JSON_CAP - 1`). Cada campo de texto além do payload tem um teto, em bytes já escapados (`timestamp` 32, `topic` 512, `error_type` 64, `details` 768, `client_id` 256), e o payload fica com o resto. O campo que não cabe é cortado só entre unidades inteiras (um caractere UTF-8 ou um escape completo), e a mensagem ganha `"truncated":true`, que só aparece quando houve corte. Os tetos são decisão de implementação, sem base na norma.

**Tópico** (`error_json_topic`, no mesmo arquivo): `errors/<nome>`, com até 255 bytes. No `<nome>`, tirado do `client_id`, cada `+`, `#`, `/` e cada controle (U+0000 a U+001F, U+007F a U+009F) vira `_`, e o byte que não forma UTF-8 válido também; o corte, se o tópico passar de 255 bytes, é só entre caracteres inteiros. `client_id` vazio vira `_`. O mosquitto já recusa no CONNECT o `client_id` com UTF-8 inválido ou controle, então, na prática, só `+`, `#` e `/` são trocados. A troca pode juntar dois clientes no mesmo tópico (`a+b` e `a_b`); o campo `client_id` da mensagem leva o original.

> Tópicos `errors/*` e `PluginResponseTime*` são **ignorados pelo callback** (retorno antecipado em `callback_acl_check`, `mosquitto_plugin.c`) pra evitar loop infinito.

---

## Limitações conhecidas

- **Curingas sem prioridade por especificidade.** Com mais de uma chave com `+`/`#` casando o mesmo tópico, vale a primeira na ordem do arquivo, e não a mais específica.
- **Sem `$ref`** — não dá pra reusar sub-schemas via referência. Copia/cola.
- **`pattern` é regex POSIX simples**, sem flags. Sem suporte a `^...$` multi-linha, lookahead/lookbehind, etc.
- **Erros de schema malformado são silenciosos no carregamento** — só loga `Invalid JSON in schemas file`, e o plugin segue funcionando com schemas vazios. Sempre validar o JSON com `jq`/`json5` antes do rebuild.
- **`format` (date-time, email, uri, etc.) é ignorado**. Pra validar formatos, usa `pattern` com regex.
- **Combinadores `oneOf`/`anyOf`/`allOf`/`not` não existem.** Cada propriedade tem um único schema.
- **Mudanças no schema exigem rebuild da imagem**, não só restart. O JSON é copiado em build-time pelo Dockerfile.
- **O que o json-c estrito ainda aceita** (`plugin/include/json_payload.h`): `NaN`, `Infinity`, número terminado em ponto (`1.`), caractere de controle cru dentro de string e UTF-8 inválido dentro de string.
- **Payload texto puro com NUL** num tópico `type: string` vira `INVALID_JSON`: `minLength`, `maxLength` e `pattern` leem C string e parariam no NUL.
- **`errors/<client_id>` pode juntar clientes:** a troca de `+`, `#` e `/` por `_` dá o mesmo tópico para `a+b` e `a_b`; o campo `client_id` da mensagem leva o original.

---

## Fluxo resumido

```
Cliente publica em sensor/X/Y
    ↓
callback_acl_check (mosquitto_plugin.c)
    ↓ (é WRITE, fora de errors/, PluginResponseTime*, PublisherResponseTime* e $SYS/, com payload?)
validate_message (mosquitto_plugin.c)
    ↓ get_schema_for_topic("sensor/X/Y") (exato, depois + e #)
    ↓ não tem schema → MOSQ_ERR_SUCCESS (passa)
    ↓ tem schema → json_payload_parse (json_payload.c: payload inteiro, exatamente um texto JSON, modo estrito)
        ↓ não é texto JSON e schema type=string, sem NUL → o payload inteiro vira string
        ↓ JSON inválido → publish_error("INVALID_JSON") + DENIED
        ↓ JSON válido → validate_json_schema (schema_validator.c)
            ↓ falha → publish_error("SCHEMA_VALIDATION_FAILED") + DENIED
            ↓ passa → MOSQ_ERR_SUCCESS
publish_error → error_json_build (mensagem) + error_json_topic (tópico), em error_json.c
```

---

## Onde mexer no código (se schemas.json não dá conta)

| O que mudar | Arquivo |
|---|---|
| Adicionar nova keyword (ex.: `format`) | `plugin/src/schema_validator.c` — criar `validate_<keyword>()` e chamar em `validate_with_schema` |
| Validar um tópico novo | só `plugin/config/schemas.json` (sem filtro de prefixo no código) |
| Mudar os tópicos que nunca são validados (`errors/`, teste de latência, `$SYS/`) | `plugin/src/mosquitto_plugin.c` (`callback_acl_check`) |
| Mudar a leitura do payload (o que conta como texto JSON) | `plugin/src/json_payload.c` (`json_payload_parse`), regra em `plugin/include/json_payload.h`; o texto puro dos tópicos `type: string`, em `validate_message` (`mosquitto_plugin.c`) |
| Mudar formato do JSON publicado em `errors/*` ou o tópico | `plugin/src/error_json.c` (`error_json_build`, `error_json_topic`), tetos e regras em `plugin/include/error_json.h`; a chamada, em `plugin/src/mosquitto_plugin.c` (`publish_error`) |
| Mudar o casamento de tópico (exato, depois `+`/`#`) | `plugin/src/mosquitto_plugin.c` (`topic_matches`, `get_schema_for_topic`) |

Qualquer mudança em C exige rebuild da imagem (mesma sequência da seção "Rebuild e restart" acima).

---

## Testes do plugin

Dois testes de unidade em C rodam **no build da imagem**, antes de compilar o plugin (`infra/mqtt-broker/infra/Dockerfile`): se um deles falha, a imagem não sai.
- `plugin/tests/test_error_json.c`: a mensagem e o tópico de `errors/` e o escape do `error_json_string`; cada mensagem que ele gera é conferida de novo pelo `json` do Python 3 da imagem.
- `plugin/tests/test_json_payload.c`: o veredito de `json_payload_parse` em cada caso, conferido de novo pelo `json` do Python (`plugin/tests/check_json_payload.py`).

Dois testes de integração sobem um broker **descartável** (imagem com tag própria, rede própria, sem portas publicadas) e não tocam a stack; precisam de Docker:

```bash
bash infra/mqtt-broker/plugin/tests/test_errors_json.sh       # errors/: JSON, tamanho, tópico; payload inteiro; teste de latência
bash infra/mqtt-broker/plugin/tests/test_all_validations.sh   # uma publicação válida e 19 violações do sensor/comprehensive/test
```

O `test_errors_json.sh` roda o `plugin/tests/errors_json_driver.py` num segundo container da mesma imagem. Os dois aceitam `IMG=<imagem> NOBUILD=1` (usa uma imagem já construída) e `KEEP_IMG=1` (não remove a imagem construída).
