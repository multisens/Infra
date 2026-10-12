#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <json-c/json.h>
#include <mosquitto.h>
#include <mosquitto_plugin.h>
#include <mosquitto_broker.h>
#include <mqtt_protocol.h>
#include "schema_validator.h"
#include "response_time_tester.h"
#include "error_json.h"
#include "json_payload.h"

mosquitto_plugin_id_t *mosq_pid = NULL;
static json_object *schemas = NULL;

// Load schemas from file
static void load_schemas_from_file(const char* filename) {
    FILE *file = fopen(filename, "r");
    if (!file) {
        mosquitto_log_printf(MOSQ_LOG_WARNING, "Could not open schemas file: %s", filename);
        return;
    }
    
    fseek(file, 0, SEEK_END);
    long length = ftell(file);
    fseek(file, 0, SEEK_SET);
    
    char *content = malloc(length + 1);
    fread(content, 1, length, file);
    content[length] = '\0';
    fclose(file);
    
    json_object *new_schemas = json_tokener_parse(content);
    free(content);
    
    if (new_schemas) {
        if (schemas) {
            json_object_put(schemas);
        }
        schemas = new_schemas;
        json_object_get(schemas);
        
        mosquitto_log_printf(MOSQ_LOG_INFO, "Schemas loaded from file: %d topics configured", json_object_object_length(schemas));
        
        json_object_object_foreach(schemas, key, val) {
            mosquitto_log_printf(MOSQ_LOG_INFO, "Schema loaded for topic: %s", key);
        }
    } else {
        mosquitto_log_printf(MOSQ_LOG_ERR, "Invalid JSON in schemas file: %s", filename);
    }
}

// Publish error to errors/<client_id> topic
// Correcao de 12/10 (defeito achado pela auditoria da documentacao): os
// campos entravam no JSON por snprintf, sem escape, e um payload ou mensagem
// com aspas, barra invertida ou caractere de controle gerava JSON invalido;
// mensagem de 2048 bytes ou mais nao saia. Agora a mensagem e montada por
// error_json_build (error_json.c): escape da RFC 8259, UTF-8 valido, payload
// pelo comprimento (NUL no meio vira \u0000) e corte em fronteira segura, com
// "truncated":true, para caber em ERROR_JSON_CAP - 1 = 2047 bytes.
static void publish_error(const char* topic, const char* error_type, const char* details, const char* payload, size_t payload_len, const char* client_id) {
    char *error_buffer = malloc(ERROR_JSON_CAP);
    if (!error_buffer) return;

    time_t now = time(NULL);
    char timestamp[64];
    strftime(timestamp, sizeof(timestamp), "%Y-%m-%dT%H:%M:%S", gmtime(&now));

    int len = error_json_build(error_buffer, ERROR_JSON_CAP, timestamp, topic, error_type, details,
                               client_id ? client_id : "unknown", payload, payload_len);

    // mosquitto_broker_publish so fica com o buffer (e o libera) quando
    // devolve sucesso; nos outros casos ele e liberado aqui (antes vazava).
    // Topico (correcao de 12/10, revisao do plugin): error_json_topic corta so
    // entre caracteres UTF-8 inteiros e troca +, #, / e controles do client_id
    // por _ (regra em error_json.h); antes era snprintf("errors/%s").
    if (len > 0) {
        char error_topic[ERROR_JSON_TOPIC_CAP];
        if (error_json_topic(error_topic, sizeof(error_topic), client_id) > 0 &&
            mosquitto_broker_publish(NULL, error_topic, len, error_buffer, 0, 0, NULL) == MOSQ_ERR_SUCCESS) {
            return;
        }
    }
    free(error_buffer);
}

// Casamento de topico MQTT (+ e #) — o lookup por igualdade exata deixava
// fora os topicos parametrizados de sinalizacao (tlm/sls/<sid>/esg etc).
static int topic_matches(const char* pattern, const char* topic) {
    while (*pattern && *topic) {
        if (*pattern == '#') return 1;
        if (*pattern == '+') {
            while (*topic && *topic != '/') topic++;
            pattern++;
            if (*pattern == '\0' && *topic == '\0') return 1;
            if (*pattern != '/' || *topic != '/') return 0;
            pattern++; topic++;
            continue;
        }
        if (*pattern != *topic) return 0;
        pattern++; topic++;
    }
    if (*pattern == '\0' && *topic == '\0') return 1;
    // "a/#" casa "a"
    if (pattern[0] == '/' && pattern[1] == '#' && *topic == '\0') return 1;
    if (pattern[0] == '#' && *topic == '\0') return 1;
    return 0;
}

// Get schema for a topic: igualdade exata primeiro, depois padrao (P4 —
// esquema declarado nos topicos REAIS de sinalizacao e estado).
static json_object* get_schema_for_topic(const char* topic) {
    if (!schemas) return NULL;

    json_object *schema;
    if (json_object_object_get_ex(schemas, topic, &schema)) {
        return schema;
    }

    json_object_object_foreach(schemas, key, val) {
        if ((strchr(key, '+') || strchr(key, '#')) && topic_matches(key, topic)) {
            return val;
        }
    }
    return NULL;
}

// Schema declara type=string? (payloads de estado da plataforma sao strings
// puras — uuid, caminho, nome de tela — que nao sao JSON valido)
static int schema_expects_plain_string(json_object* schema) {
    json_object *type;
    if (!json_object_object_get_ex(schema, "type", &type)) return 0;
    const char* t = json_object_get_string(type);
    return t && strcmp(t, "string") == 0;
}

// Validate message against schema
// payload_len (12/10): o comprimento real, para a mensagem de errors/ levar o
// payload inteiro mesmo com NUL no meio.
// Leitura do payload (correcao de 12/10, revisao do plugin): antes era
// json_tokener_parse(payload), que le ate o primeiro NUL e ignora o que vem
// depois do primeiro valor ({"value":1}lixo e {"value":1}\0lixo passavam e
// eram entregues). Agora json_payload_parse le o payload inteiro, pelo
// comprimento, e so aceita exatamente um texto JSON (RFC 8259; regra em
// json_payload.h); o resto vira INVALID_JSON. O literal null continua como
// antes (o json-c o representa por NULL): INVALID_JSON, ou string pura nos
// topicos de esquema type=string.
static int validate_message(const char* topic, const char* payload, size_t payload_len, const char* client_id) {
    json_object *schema = get_schema_for_topic(topic);
    if (!schema) {
        return MOSQ_ERR_SUCCESS;
    }
    
    json_object *message = NULL;
    json_payload_parse(payload, payload_len, &message);
    if (!message && schema_expects_plain_string(schema) && !memchr(payload, '\0', payload_len)) {
        // payload string pura (uuid/caminho/nome de tela): embrulha como
        // string JSON pra validar minLength/maxLength/pattern do schema.
        // 12/10: o payload inteiro, pelo comprimento; com NUL nao embrulha
        // (minLength/maxLength/pattern leem C string e parariam no NUL) e
        // vira INVALID_JSON.
        message = json_object_new_string_len(payload, (int)payload_len);
    }
    if (!message) {
        mosquitto_log_printf(MOSQ_LOG_INFO, "Invalid JSON for topic: %s", topic);
        publish_error(topic, "INVALID_JSON", "Payload is not valid JSON", payload, payload_len, client_id);
        return MOSQ_ERR_ACL_DENIED;
    }
    
    validation_result result = validate_json_schema(message, schema);
    json_object_put(message);
    
    if (!result.is_valid) {
        mosquitto_log_printf(MOSQ_LOG_INFO, "Validation failed for topic %s: %s", topic, result.error_message);
        publish_error(topic, "SCHEMA_VALIDATION_FAILED", result.error_message, payload, payload_len, client_id);
        return MOSQ_ERR_ACL_DENIED;
    }
    
    return MOSQ_ERR_SUCCESS;
}

static int callback_acl_check(int event, void *event_data, void *userdata) {
    struct mosquitto_evt_acl_check *ed = event_data;
    const char *client_id = mosquitto_client_id(ed->client);
    
    // Don't validate the errors topics (avoid loops)
    if (ed->access == MOSQ_ACL_WRITE && strncmp(ed->topic, "errors/", 7) == 0) {
        return MOSQ_ERR_SUCCESS;
    }
    
    // Don't validate PluginResponseTime topics (avoid loops)
    if (ed->access == MOSQ_ACL_WRITE && strncmp(ed->topic, "PluginResponseTime", 18) == 0) {
        return MOSQ_ERR_SUCCESS;
    }
    
    // Check for testResponsetime messages on PublisherResponseTime topics
    if (ed->access == MOSQ_ACL_WRITE && strncmp(ed->topic, "PublisherResponseTime", 21) == 0) {
        if (ed->payload && ed->payloadlen > 0) {
            char *payload_str = malloc(ed->payloadlen + 1);
            memcpy(payload_str, ed->payload, ed->payloadlen);
            payload_str[ed->payloadlen] = '\0';
            
            handle_response_time_test(ed->topic, payload_str);
            free(payload_str);
        }
        return MOSQ_ERR_SUCCESS;
    }
    
    // Controle de acesso a topicos foi removido por decisao de desenho:
    // isolamento de transporte interno nao tem contrapartida normativa
    // (a identidade no barramento e de microsservico; o isolamento que a
    // norma exige e por contexto de servico DTV, na fronteira das APIs).

    // Valida so PUBLISH; o antigo retorno antecipado que restringia a
    // validacao a topicos sensor/* saiu (P4): qualquer topico com esquema
    // declarado e validado — sinalizacao e estado da plataforma inclusive.
    // Topico sem esquema segue passando (lookup devolve NULL).
    if (ed->access != MOSQ_ACL_WRITE || strncmp(ed->topic, "$SYS/", 5) == 0) {
        return MOSQ_ERR_SUCCESS;
    }
    
    if (ed->payload && ed->payloadlen > 0) {
        char *payload_str = malloc(ed->payloadlen + 1);
        memcpy(payload_str, ed->payload, ed->payloadlen);
        payload_str[ed->payloadlen] = '\0';
        
        const char *client_id = mosquitto_client_id(ed->client);
        int result = validate_message(ed->topic, payload_str, ed->payloadlen, client_id);
        free(payload_str);
        
        if (result != MOSQ_ERR_SUCCESS) {
            mosquitto_log_printf(MOSQ_LOG_INFO, "Validation FAILED for topic: %s", ed->topic);
            return MOSQ_ERR_ACL_DENIED;
        }
    }
    
    return MOSQ_ERR_SUCCESS;
}

int mosquitto_plugin_version(int supported_version_count, const int *supported_versions)
{
    return 5;
}

int mosquitto_plugin_init(mosquitto_plugin_id_t *identifier, void **user_data, struct mosquitto_opt *opts, int opt_count)
{
    mosq_pid = identifier;
    
    schemas = json_object_new_object();
    load_schemas_from_file("/mosquitto/config/schemas.json");

    mosquitto_log_printf(MOSQ_LOG_INFO, "Mosquitto plugin loaded (schema validation + response time tester)");
    
    return mosquitto_callback_register(mosq_pid, MOSQ_EVT_ACL_CHECK, callback_acl_check, NULL, NULL);
}

int mosquitto_plugin_cleanup(void *user_data, struct mosquitto_opt *opts, int opt_count)
{
    if (schemas) {
        json_object_put(schemas);
        schemas = NULL;
    }
    
    mosquitto_log_printf(MOSQ_LOG_INFO, "Mosquitto plugin unloaded");
    return mosquitto_callback_unregister(mosq_pid, MOSQ_EVT_ACL_CHECK, callback_acl_check, NULL);
}
