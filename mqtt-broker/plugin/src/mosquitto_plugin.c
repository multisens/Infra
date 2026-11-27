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

// Environment variable of the refusal of a topic without a schema: enforce
// (the default) refuses it; warn lets it pass, with the error message.
#define SCHEMA_ENFORCE_ENV "SCHEMA_ENFORCE"
#define SCHEMA_ENFORCE_ON "enforce"
#define SCHEMA_ENFORCE_WARN "warn"
#define NO_SCHEMA_DETAILS "Topic without a declared schema (infra/mqtt-broker/plugin/config/schemas.json)"
#define SCHEMAS_FILE "/mosquitto/config/schemas.json"

// Topics that pass without validation: the error messages and the replies
// of the latency test, published by the plugin itself, the broker topics and
// the requests of the latency test, which the plugin answers.
#define ERRORS_PREFIX "errors/"
#define PLUGIN_RESPONSE_PREFIX "PluginResponseTime"
#define PUBLISHER_RESPONSE_PREFIX "PublisherResponseTime"
#define SYS_PREFIX "$SYS/"

#define PLUGIN_VERSION 5

mosquitto_plugin_id_t *mosq_pid = NULL;
static json_object *schemas = NULL;
static int schema_enforce = 1;

// Loads the schemas; returns 0 on success, -1 if the file cannot be read or
// is not a JSON object.
static int load_schemas_from_file(const char* filename) {
    FILE *file = fopen(filename, "r");
    if (!file) {
        mosquitto_log_printf(MOSQ_LOG_ERR, "Could not open schemas file: %s", filename);
        return -1;
    }

    fseek(file, 0, SEEK_END);
    long length = ftell(file);
    fseek(file, 0, SEEK_SET);

    char *content = malloc(length + 1);
    if (content == NULL) {
        fclose(file);
        mosquitto_log_printf(MOSQ_LOG_ERR, "No memory to read the schemas file: %s", filename);
        return -1;
    }
    fread(content, 1, length, file);
    content[length] = '\0';
    fclose(file);

    json_object *new_schemas = json_tokener_parse(content);
    free(content);

    if (new_schemas == NULL || !json_object_is_type(new_schemas, json_type_object)) {
        if (new_schemas != NULL) {
            json_object_put(new_schemas);
        }
        mosquitto_log_printf(MOSQ_LOG_ERR, "Invalid JSON in schemas file: %s", filename);
        return -1;
    }
    if (schemas) {
        json_object_put(schemas);
    }
    schemas = new_schemas;

    mosquitto_log_printf(MOSQ_LOG_INFO, "Schemas loaded from file: %d topics configured", json_object_object_length(schemas));
    json_object_object_foreach(schemas, key, val) {
        mosquitto_log_printf(MOSQ_LOG_INFO, "Schema loaded for topic: %s", key);
    }
    return 0;
}

// Publishes on errors/<client_id>; message and topic as in error_json.h.
static void publish_error(const char* topic, const char* error_type, const char* details, const char* payload, size_t payload_len, const char* client_id) {
    char *error_buffer = malloc(ERROR_JSON_CAP);
    if (!error_buffer) {
        return;
    }

    time_t now = time(NULL);
    char timestamp[64];
    strftime(timestamp, sizeof(timestamp), "%Y-%m-%dT%H:%M:%S", gmtime(&now));

    int len = error_json_build(error_buffer, ERROR_JSON_CAP, timestamp, topic, error_type, details,
                               client_id ? client_id : "unknown", payload, payload_len);

    // mosquitto_broker_publish takes the buffer, and frees it, only when it
    // returns success; otherwise it is freed here.
    if (len > 0) {
        char error_topic[ERROR_JSON_TOPIC_CAP];
        if (error_json_topic(error_topic, sizeof(error_topic), client_id) > 0 &&
            mosquitto_broker_publish(NULL, error_topic, len, error_buffer, 0, 0, NULL) == MOSQ_ERR_SUCCESS) {
            return;
        }
    }
    free(error_buffer);
}

// MQTT topic matching (+ and #), for the parameterized signaling topics
// (tlm/sls/<sid>/esg and others).
static int topic_matches(const char* pattern, const char* topic) {
    while (*pattern && *topic) {
        if (*pattern == '#') {
            return 1;
        }
        if (*pattern == '+') {
            while (*topic && *topic != '/') {
                topic++;
            }
            pattern++;
            if (*pattern == '\0' && *topic == '\0') {
                return 1;
            }
            if (*pattern != '/' || *topic != '/') {
                return 0;
            }
            pattern++;
            topic++;
            continue;
        }
        if (*pattern != *topic) {
            return 0;
        }
        pattern++;
        topic++;
    }
    if (*pattern == '\0' && *topic == '\0') {
        return 1;
    }
    // "a/#" matches "a".
    if (pattern[0] == '/' && pattern[1] == '#' && *topic == '\0') {
        return 1;
    }
    if (pattern[0] == '#' && *topic == '\0') {
        return 1;
    }
    return 0;
}

static json_object* get_schema_for_topic(const char* topic) {
    if (!schemas) {
        return NULL;
    }

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

// Whether the schema has type string, for a payload that is a plain string
// and not JSON.
static int schema_expects_plain_string(json_object* schema) {
    json_object *type;
    if (!json_object_object_get_ex(schema, "type", &type)) {
        return 0;
    }
    const char* t = json_object_get_string(type);
    return t && strcmp(t, "string") == 0;
}

// Validates the payload against the schema of the topic. A topic without a
// schema is refused, or only reported with SCHEMA_ENFORCE=warn. payload_len
// is the real length: the payload is read whole, even with a NUL inside
// (json_payload.h). The literal null becomes NULL in json-c: INVALID_JSON, or
// a plain string on topics with a type=string schema. A refusal publishes
// the error message and returns MOSQ_ERR_ACL_DENIED.
static int validate_message(const char* topic, const char* payload, size_t payload_len, const char* client_id) {
    json_object *schema = get_schema_for_topic(topic);
    if (!schema) {
        publish_error(topic, "NO_SCHEMA", NO_SCHEMA_DETAILS, payload, payload_len, client_id);
        if (!schema_enforce) {
            mosquitto_log_printf(MOSQ_LOG_WARNING, "Topic without a schema passes (%s=%s): %s", SCHEMA_ENFORCE_ENV, SCHEMA_ENFORCE_WARN, topic);
            return MOSQ_ERR_SUCCESS;
        }
        mosquitto_log_printf(MOSQ_LOG_INFO, "Topic without a schema: %s", topic);
        return MOSQ_ERR_ACL_DENIED;
    }

    json_object *message = NULL;
    json_payload_parse(payload, payload_len, &message);
    if (!message && schema_expects_plain_string(schema) && !memchr(payload, '\0', payload_len)) {
        // A plain string is wrapped as a JSON string for
        // minLength/maxLength/pattern. Not with a NUL, since these rules read a
        // C string and would stop at the NUL.
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

// Whether the topic starts with the prefix.
static int has_prefix(const char* topic, const char* prefix) {
    return strncmp(topic, prefix, strlen(prefix)) == 0;
}

// Copy of the payload with a NUL at the end, for the functions that read a C
// string; NULL without memory.
static char* copy_payload(const void* payload, uint32_t payload_len) {
    char *copy = malloc(payload_len + 1);
    if (copy == NULL) {
        return NULL;
    }
    memcpy(copy, payload, payload_len);
    copy[payload_len] = '\0';
    return copy;
}

// The MOSQ_EVT_ACL_CHECK callback of Mosquitto. It controls no access: it
// grants every access, except a PUBLISH whose payload does not satisfy the
// schema of the topic, or on a topic without a schema. It also answers the
// response time test.
static int callback_acl_check(int event, void *event_data, void *userdata) {
    struct mosquitto_evt_acl_check *ed = event_data;

    // Only PUBLISH is validated.
    if (ed->access != MOSQ_ACL_WRITE) {
        return MOSQ_ERR_SUCCESS;
    }

    // The messages of the plugin itself pass, without a loop.
    if (has_prefix(ed->topic, ERRORS_PREFIX) || has_prefix(ed->topic, PLUGIN_RESPONSE_PREFIX)) {
        return MOSQ_ERR_SUCCESS;
    }

    if (has_prefix(ed->topic, PUBLISHER_RESPONSE_PREFIX)) {
        if (ed->payload && ed->payloadlen > 0) {
            char *payload_str = copy_payload(ed->payload, ed->payloadlen);
            if (payload_str == NULL) {
                return MOSQ_ERR_NOMEM;
            }
            handle_response_time_test(ed->topic, payload_str);
            free(payload_str);
        }
        return MOSQ_ERR_SUCCESS;
    }

    // $SYS/ passes; an empty payload (removal of a retained message) too.
    if (has_prefix(ed->topic, SYS_PREFIX) || !ed->payload || ed->payloadlen == 0) {
        return MOSQ_ERR_SUCCESS;
    }

    char *payload_str = copy_payload(ed->payload, ed->payloadlen);
    if (payload_str == NULL) {
        return MOSQ_ERR_NOMEM;
    }
    const char *client_id = mosquitto_client_id(ed->client);
    int result = validate_message(ed->topic, payload_str, ed->payloadlen, client_id);
    free(payload_str);

    if (result != MOSQ_ERR_SUCCESS) {
        mosquitto_log_printf(MOSQ_LOG_INFO, "Validation FAILED for topic: %s", ed->topic);
        return MOSQ_ERR_ACL_DENIED;
    }
    return MOSQ_ERR_SUCCESS;
}

int mosquitto_plugin_version(int supported_version_count, const int *supported_versions) {
    return PLUGIN_VERSION;
}

int mosquitto_plugin_init(mosquitto_plugin_id_t *identifier, void **user_data, struct mosquitto_opt *opts, int opt_count) {
    mosq_pid = identifier;

    const char *enforce = getenv(SCHEMA_ENFORCE_ENV);
    if (enforce == NULL || enforce[0] == '\0' || strcmp(enforce, SCHEMA_ENFORCE_ON) == 0) {
        schema_enforce = 1;
    } else if (strcmp(enforce, SCHEMA_ENFORCE_WARN) == 0) {
        schema_enforce = 0;
    } else {
        mosquitto_log_printf(MOSQ_LOG_ERR, "%s=%s invalid (%s or %s)", SCHEMA_ENFORCE_ENV, enforce, SCHEMA_ENFORCE_ON, SCHEMA_ENFORCE_WARN);
        return MOSQ_ERR_INVAL;
    }

    if (load_schemas_from_file(SCHEMAS_FILE) != 0) {
        return MOSQ_ERR_INVAL;
    }

    mosquitto_log_printf(MOSQ_LOG_INFO, "Mosquitto plugin loaded (schema validation + response time tester; %s=%s)", SCHEMA_ENFORCE_ENV, schema_enforce ? SCHEMA_ENFORCE_ON : SCHEMA_ENFORCE_WARN);

    return mosquitto_callback_register(mosq_pid, MOSQ_EVT_ACL_CHECK, callback_acl_check, NULL, NULL);
}

int mosquitto_plugin_cleanup(void *user_data, struct mosquitto_opt *opts, int opt_count) {
    if (schemas) {
        json_object_put(schemas);
        schemas = NULL;
    }

    mosquitto_log_printf(MOSQ_LOG_INFO, "Mosquitto plugin unloaded");
    return mosquitto_callback_unregister(mosq_pid, MOSQ_EVT_ACL_CHECK, callback_acl_check, NULL);
}
