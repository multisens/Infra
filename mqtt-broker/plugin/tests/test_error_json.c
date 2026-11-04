// Test of error_json.c: message of errors/<client_id> (1 to 7), topic (8)
// and error_json_string (9). Each message goes through strict json-c, an own
// scan (no raw control character, valid UTF-8) and a field by field
// comparison.
//
//   (in infra/mqtt-broker/plugin)
//   gcc -std=gnu11 -Wall -Wextra -I include tests/test_error_json.c
//       src/error_json.c -ljson-c -o /tmp/test_error_json
//   /tmp/test_error_json [/tmp/mensagens.jsonl]
//   (one message per line, for the Python json module)
#include <json-c/json.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "error_json.h"

static int checks = 0, failures = 0;
static FILE *dump = NULL;

#define CHECK(cond, ...) do { \
    checks++; \
    if (!(cond)) { \
        failures++; \
        fprintf(stderr, "FALHA %s:%d: ", __FILE__, __LINE__); \
        fprintf(stderr, __VA_ARGS__); \
        fputc('\n', stderr); \
    } \
} while (0)

#define TS "2026-10-12T00:00:00"

// Valid UTF-8 by RFC 3629, written here again, on purpose without the
// tested code.
static int valid_utf8(const unsigned char *s, size_t n) {
    size_t i = 0;
    while (i < n) {
        unsigned c = s[i];
        size_t k;
        unsigned cp;
        if (c < 0x80) { i++; continue; }
        else if ((c & 0xE0) == 0xC0) { k = 2; cp = c & 0x1F; }
        else if ((c & 0xF0) == 0xE0) { k = 3; cp = c & 0x0F; }
        else if ((c & 0xF8) == 0xF0) { k = 4; cp = c & 0x07; }
        else return 0;
        if (i + k > n) return 0;
        for (size_t j = 1; j < k; j++) {
            if ((s[i + j] & 0xC0) != 0x80) return 0;
            cp = (cp << 6) | (s[i + j] & 0x3F);
        }
        if ((k == 2 && cp < 0x80) || (k == 3 && cp < 0x800) || (k == 4 && cp < 0x10000)) return 0;
        if (cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) return 0;
        i += k;
    }
    return 1;
}

// Own scan and the json-c reader. Returns the object, or NULL.
static json_object *parse_checked(const char *msg, int len, size_t cap, const char *caso) {
    CHECK(len >= 0, "%s: error_json_build devolveu %d", caso, len);
    if (len < 0) return NULL;
    CHECK((size_t)len < cap, "%s: comprimento %d nao cabe em %zu", caso, len, cap);
    CHECK(strlen(msg) == (size_t)len, "%s: NUL no meio ou comprimento errado (%zu != %d)", caso, strlen(msg), len);
    int ctl = 0;
    for (int i = 0; i < len; i++) if ((unsigned char)msg[i] < 0x20) ctl++;
    CHECK(ctl == 0, "%s: %d byte(s) de controle cru(s) na mensagem", caso, ctl);
    CHECK(valid_utf8((const unsigned char *)msg, (size_t)len), "%s: mensagem nao e UTF-8 valido", caso);
    CHECK(len > 1 && msg[0] == '{' && msg[len - 1] == '}', "%s: nao comeca com { e termina com }", caso);
    json_tokener *tok = json_tokener_new();
    json_tokener_set_flags(tok, JSON_TOKENER_STRICT | JSON_TOKENER_VALIDATE_UTF8);
    json_object *o = json_tokener_parse_ex(tok, msg, len);
    enum json_tokener_error e = json_tokener_get_error(tok);
    CHECK(o && e == json_tokener_success && json_object_is_type(o, json_type_object),
          "%s: json-c recusou (%s): %.300s", caso, json_tokener_error_desc(e), msg);
    if (o && e == json_tokener_success) {
        CHECK(json_tokener_get_parse_end(tok) == (size_t)len, "%s: sobrou texto depois do objeto", caso);
    }
    json_tokener_free(tok);
    if (dump && len > 0) fprintf(dump, "%s\n", msg);
    return o;
}

static int field(json_object *o, const char *k, const char **v, size_t *n) {
    json_object *f;
    if (!o || !json_object_object_get_ex(o, k, &f) || !json_object_is_type(f, json_type_string)) return 0;
    *v = json_object_get_string(f);
    *n = (size_t)json_object_get_string_len(f);
    return 1;
}

static int has(json_object *o, const char *k) {
    return o && json_object_object_get_ex(o, k, NULL);
}

static int is_truncated(json_object *o) {
    json_object *f;
    return o && json_object_object_get_ex(o, "truncated", &f) &&
           json_object_is_type(f, json_type_boolean) && json_object_get_boolean(f);
}

static void expect_field(json_object *o, const char *k, const char *want, size_t wn, const char *caso) {
    const char *v;
    size_t n;
    int ok = field(o, k, &v, &n);
    CHECK(ok, "%s: campo %s ausente ou nao texto", caso, k);
    if (ok) CHECK(n == wn && memcmp(v, want, wn) == 0, "%s: campo %s decodificado diferente (%zu bytes, esperado %zu)", caso, k, n, wn);
}

static char buf[ERROR_JSON_CAP];

static int build(size_t cap, const char *topic, const char *type, const char *details,
                 const char *client, const char *payload, size_t plen) {
    memset(buf, 'Z', sizeof(buf));
    return error_json_build(buf, cap, TS, topic, type, details, client, payload, plen);
}

// 1. Format with plain text.
static void t_formato(void) {
    int len = build(ERROR_JSON_CAP, "sensor/room1/temperature", "INVALID_JSON",
                    "Payload is not valid JSON", "cli", "abc", 3);
    const char *want = "{\"timestamp\":\"" TS "\",\"topic\":\"sensor/room1/temperature\","
                       "\"error_type\":\"INVALID_JSON\",\"details\":\"Payload is not valid JSON\","
                       "\"client_id\":\"cli\",\"payload\":\"abc\"}";
    CHECK(len == (int)strlen(want) && strcmp(buf, want) == 0, "formato: %s", buf);
    json_object_put(parse_checked(buf, len, ERROR_JSON_CAP, "formato"));

    len = build(ERROR_JSON_CAP, "t", "X", "d", "c", NULL, 0);
    want = "{\"timestamp\":\"" TS "\",\"topic\":\"t\",\"error_type\":\"X\",\"details\":\"d\",\"client_id\":\"c\"}";
    CHECK(strcmp(buf, want) == 0, "sem payload: %s", buf);
    json_object_put(parse_checked(buf, len, ERROR_JSON_CAP, "sem payload"));

    // An empty payload (length 0) is still a field.
    len = build(ERROR_JSON_CAP, "t", "X", "d", "c", "", 0);
    json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, "payload vazio");
    expect_field(o, "payload", "", 0, "payload vazio");
    json_object_put(o);
}

// 2. Quotes, backslash and every control character, NUL included.
static void t_escapes(void) {
    char p[64];
    size_t n = 0;
    for (int c = 0; c < 0x20; c++) p[n++] = (char)c;
    p[n++] = '"'; p[n++] = '\\'; p[n++] = '/'; p[n++] = 0x7F;
    memcpy(p + n, "fim", 3); n += 3;
    int len = build(ERROR_JSON_CAP, "t", "X", "d", "c", p, n);
    json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, "escapes");
    expect_field(o, "payload", p, n, "escapes");
    CHECK(!is_truncated(o), "escapes: marcado como cortado");
    json_object_put(o);
    // Exact form of the escapes (RFC 8259, section 7).
    const char *raw = "\"\\u0000\\u0001\\u0002\\u0003\\u0004\\u0005\\u0006\\u0007\\b\\t\\n\\u000b\\f\\r\\u000e";
    CHECK(strstr(buf, raw) != NULL, "escapes: forma exata ausente em %s", buf);
    CHECK(strstr(buf, "\\u001f\\\"\\\\/\x7f" "fim\"") != NULL, "escapes: fim da forma exata ausente em %s", buf);

    // NUL inside: the field goes to the end (payload_len), not to the NUL.
    len = build(ERROR_JSON_CAP, "t", "X", "d", "c", "abc\0def", 7);
    o = parse_checked(buf, len, ERROR_JSON_CAP, "NUL no meio");
    expect_field(o, "payload", "abc\0def", 7, "NUL no meio");
    json_object_put(o);
}

// 3. Quotes and backslash in topic, details and client_id, not only in the
// payload.
static void t_campos(void) {
    const char *topic = "tlm/sls/x\"y\\z/esg";
    const char *details = "Item at index 0: Property 'backgroundColor': String 'x\"y\\z\n' does not match pattern '^#[0-9A-Fa-f]{6}$'";
    const char *client = "cli\"q\\b\t";
    int len = build(ERROR_JSON_CAP, topic, "SCHEMA_VALIDATION_FAILED", details, client, "{\"a\":1}", 7);
    json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, "campos");
    expect_field(o, "topic", topic, strlen(topic), "campos");
    expect_field(o, "details", details, strlen(details), "campos");
    expect_field(o, "client_id", client, strlen(client), "campos");
    expect_field(o, "error_type", "SCHEMA_VALIDATION_FAILED", 24, "campos");
    expect_field(o, "timestamp", TS, strlen(TS), "campos");
    expect_field(o, "payload", "{\"a\":1}", 7, "campos");
    json_object_put(o);

    // NULL is "".
    len = error_json_build(buf, ERROR_JSON_CAP, NULL, NULL, NULL, NULL, NULL, NULL, 0);
    o = parse_checked(buf, len, ERROR_JSON_CAP, "NULL");
    expect_field(o, "topic", "", 0, "NULL");
    expect_field(o, "client_id", "", 0, "NULL");
    CHECK(!has(o, "payload"), "NULL: payload presente");
    json_object_put(o);
    CHECK(error_json_build(NULL, ERROR_JSON_CAP, TS, "t", "X", "d", "c", "p", 1) == -1, "out NULL aceito");
    CHECK(error_json_build(buf, ERROR_JSON_MIN_CAP - 1, TS, "t", "X", "d", "c", "p", 1) == -1, "cap abaixo do minimo aceita");
}

// 4. UTF-8: valid passes as is; each invalid byte becomes U+FFFD.
static void t_utf8(void) {
    const char *ok = "a\xc3\xa7\xc3\xa3o \xe2\x82\xac \xf0\x9f\x98\x80 \xf4\x8f\xbf\xbf";
    int len = build(ERROR_JSON_CAP, "t", "X", "d", "c", ok, strlen(ok));
    json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, "utf8 valido");
    expect_field(o, "payload", ok, strlen(ok), "utf8 valido");
    json_object_put(o);
    CHECK(strstr(buf, ok) != NULL, "utf8 valido: nao saiu como esta");

    struct { const char *in; size_t n; int fffd; } bad[] = {
        // A byte that never appears in UTF-8.
        { "\xff", 1, 1 },
        // A stray continuation byte.
        { "\x80", 1, 1 },
        // Overlong form of '/'.
        { "\xc0\xaf", 2, 2 },
        // Overlong 3-byte form.
        { "\xe0\x80\xaf", 3, 3 },
        // UTF-16 surrogate (U+D800).
        { "\xed\xa0\x80", 3, 3 },
        // Above U+10FFFF.
        { "\xf4\x90\x80\x80", 4, 4 },
        // Overlong 4-byte form.
        { "\xf0\x8f\xbf\xbf", 4, 4 },
        // A sequence cut at the end.
        { "\xe2\x82", 2, 2 },
        // A cut 2-byte start.
        { "\xc3", 1, 1 },
        // A start followed by a non-continuation byte.
        { "\xc3(", 2, 1 },
    };
    for (size_t i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        char caso[64];
        snprintf(caso, sizeof(caso), "utf8 invalido %zu", i);
        len = build(ERROR_JSON_CAP, "t", "X", "d", "c", bad[i].in, bad[i].n);
        o = parse_checked(buf, len, ERROR_JSON_CAP, caso);
        // Expected: each invalid byte becomes EF BF BD; the rest as is.
        char want[64];
        size_t wn = 0;
        for (int k = 0; k < bad[i].fffd; k++) { memcpy(want + wn, "\xef\xbf\xbd", 3); wn += 3; }
        if (bad[i].n == 2 && bad[i].in[1] == '(') want[wn++] = '(';
        expect_field(o, "payload", want, wn, caso);
        json_object_put(o);
    }
    // Invalid also in details, where the schema validator copies payload
    // text.
    len = build(ERROR_JSON_CAP, "t", "X", "String '\xff' does not match", "c", NULL, 0);
    o = parse_checked(buf, len, ERROR_JSON_CAP, "utf8 invalido em details");
    const char *dwant = "String '\xef\xbf\xbd' does not match";
    expect_field(o, "details", dwant, strlen(dwant), "utf8 invalido em details");
    json_object_put(o);
}

// 5. Cut: for each kind of unit, every size until past the limit. The cut
// is always between whole units, the message fits, uses the room (does not
// cut early) and carries "truncated" only when it cut.
static void t_corte(void) {
    struct { const char *u; size_t un; const char *dec; size_t dn; size_t esc; } units[] = {
        { "a", 1, "a", 1, 1 },
        { "\"", 1, "\"", 1, 2 },
        { "\\", 1, "\\", 1, 2 },
        { "\n", 1, "\n", 1, 2 },
        { "\x01", 1, "\x01", 1, 6 },
        { "\xe2\x82\xac", 3, "\xe2\x82\xac", 3, 3 },
        { "\xf0\x9f\x98\x80", 4, "\xf0\x9f\x98\x80", 4, 4 },
        { "\xff", 1, "\xef\xbf\xbd", 3, 6 },
    };
    static char p[12000];
    static char full[40000];
    for (size_t u = 0; u < sizeof(units) / sizeof(units[0]); u++) {
        int saw_cut = 0, saw_exact = 0;
        size_t max_count = 2200 / units[u].esc + 2;
        for (size_t count = 0; count <= max_count; count++) {
            char caso[64];
            snprintf(caso, sizeof(caso), "corte unidade %zu x %zu", u, count);
            size_t n = 0, fn = 0;
            for (size_t k = 0; k < count; k++) {
                memcpy(p + n, units[u].u, units[u].un); n += units[u].un;
                memcpy(full + fn, units[u].dec, units[u].dn); fn += units[u].dn;
            }
            int len = build(ERROR_JSON_CAP, "sensor/room1/temperature", "INVALID_JSON",
                            "Payload is not valid JSON", "cli", p, n);
            json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, caso);
            const char *v;
            size_t vn;
            if (!field(o, "payload", &v, &vn)) { CHECK(0, "%s: sem payload", caso); json_object_put(o); continue; }
            CHECK(vn <= fn && memcmp(v, full, vn) == 0, "%s: payload decodificado nao e prefixo do original", caso);
            CHECK(vn % units[u].dn == 0, "%s: corte no meio de uma unidade (%zu bytes)", caso, vn);
            int cut = vn < fn;
            CHECK(cut == is_truncated(o), "%s: truncated=%d mas cortou=%d", caso, is_truncated(o), cut);
            if (cut) {
                saw_cut = 1;
                // Uses the room: less than one escaped unit is left.
                CHECK(len > ERROR_JSON_CAP - 1 - (int)units[u].esc, "%s: cortou cedo demais (len %d)", caso, len);
            }
            if (!cut && len == ERROR_JSON_CAP - 1) saw_exact = 1;
            json_object_put(o);
        }
        CHECK(saw_cut, "unidade %zu: nenhum caso cortado", u);
        if (units[u].esc == 1) CHECK(saw_exact, "unidade %zu: nenhum caso de 2047 bytes exatos sem corte", u);
    }
}

// 6. Fields other than the payload at extreme sizes: each stops at its
// maximum, and the message stays valid and with the payload.
static void t_tetos(void) {
    static char big[70000];
    memset(big, '"', 1000); big[1000] = '\0';
    int len = build(ERROR_JSON_CAP, big, "X", "d", "c", "x", 1);
    json_object *o = parse_checked(buf, len, ERROR_JSON_CAP, "topico grande");
    const char *v;
    size_t vn;
    CHECK(field(o, "topic", &v, &vn) && vn == ERROR_JSON_TOPIC_MAX / 2, "topico grande: %zu bytes", vn);
    CHECK(is_truncated(o), "topico grande: sem truncated");
    expect_field(o, "payload", "x", 1, "topico grande");
    json_object_put(o);

    memset(big, 0x01, 511); big[511] = '\0';
    len = build(ERROR_JSON_CAP, "t", "X", big, "c", NULL, 0);
    o = parse_checked(buf, len, ERROR_JSON_CAP, "details grande");
    CHECK(field(o, "details", &v, &vn) && vn == ERROR_JSON_DETAILS_MAX / 6, "details grande: %zu bytes", vn);
    CHECK(is_truncated(o), "details grande: sem truncated");
    json_object_put(o);

    memset(big, 'c', 65535); big[65535] = '\0';
    len = build(ERROR_JSON_CAP, big, big, big, big, big, 65535);
    o = parse_checked(buf, len, ERROR_JSON_CAP, "tudo grande");
    CHECK(field(o, "client_id", &v, &vn) && vn == ERROR_JSON_CLIENT_MAX, "tudo grande: client_id %zu", vn);
    CHECK(field(o, "error_type", &v, &vn) && vn == ERROR_JSON_TYPE_MAX, "tudo grande: error_type %zu", vn);
    CHECK(field(o, "payload", &v, &vn) && vn > 0, "tudo grande: payload vazio");
    CHECK(is_truncated(o), "tudo grande: sem truncated");
    json_object_put(o);
}

// 7. Every capacity between the minimum and 2048, with the worst case in
// every field: always valid JSON and within the capacity.
static void t_capacidades(void) {
    static char mix[6000];
    size_t n = 0;
    const char *pat = "\"\\\x01\n\xe2\x82\xac\xff\xf0\x9f\x98\x80" "a";
    size_t pn = strlen(pat);
    while (n + pn < sizeof(mix) - 1) { memcpy(mix + n, pat, pn); n += pn; }
    mix[n] = '\0';
    for (size_t cap = ERROR_JSON_MIN_CAP; cap <= ERROR_JSON_CAP; cap++) {
        char caso[64];
        snprintf(caso, sizeof(caso), "capacidade %zu", cap);
        int len = build(cap, mix, mix, mix, mix, mix, n);
        json_object *o = parse_checked(buf, len, cap, caso);
        CHECK(is_truncated(o), "%s: sem truncated", caso);
        CHECK(has(o, "payload"), "%s: sem payload", caso);
        CHECK(buf[len] == '\0', "%s: sem NUL no fim", caso);
        CHECK(cap == ERROR_JSON_CAP || buf[cap] == 'Z', "%s: escreveu alem da capacidade", caso);
        json_object_put(o);
    }
}

// 8. Topic errors/<client_id>: +, #, / and control characters become _, an
// invalid byte becomes _, cut only between whole characters, at most 255
// bytes.
static char tbuf[ERROR_JSON_TOPIC_CAP + 64];

// Valid MQTT topic name, checked here again as mosquitto does
// (mosquitto_pub_topic_check and mosquitto_validate_utf8): not empty, no +
// or #, valid UTF-8, no control character (U+0000 to U+001F, U+007F to
// U+009F).
static int valid_topic(const char *t, size_t n) {
    const unsigned char *s = (const unsigned char *)t;
    if (n == 0 || !valid_utf8(s, n)) return 0;
    for (size_t i = 0; i < n; i++) {
        if (s[i] == '+' || s[i] == '#' || s[i] < 0x20 || s[i] == 0x7F) return 0;
        if (s[i] == 0xC2 && i + 1 < n && s[i + 1] >= 0x80 && s[i + 1] <= 0x9F) return 0;
    }
    return 1;
}

static int topic(size_t cap, const char *client) {
    memset(tbuf, 'Z', sizeof(tbuf));
    return error_json_topic(tbuf, cap, client);
}

static void expect_topic(const char *client, const char *want, const char *caso) {
    int len = topic(ERROR_JSON_TOPIC_CAP, client);
    CHECK(len == (int)strlen(want) && strcmp(tbuf, want) == 0, "%s: topico '%s', esperado '%s'", caso, tbuf, want);
    CHECK(valid_topic(tbuf, strlen(tbuf)), "%s: topico invalido", caso);
}

static void t_topico(void) {
    expect_topic("cli", "errors/cli", "simples");
    expect_topic(NULL, "errors/unknown", "NULL");
    expect_topic("", "errors/_", "vazio");
    expect_topic("a+b#c/d", "errors/a_b_c_d", "curingas e barra");
    expect_topic("+", "errors/_", "so +");
    expect_topic("#", "errors/_", "so #");
    expect_topic("/", "errors/_", "so /");
    expect_topic("//", "errors/__", "duas barras");
    expect_topic("a\x01\x1f\x7f" "b", "errors/a___b", "controles C0 e DEL");
    expect_topic("a\xc2\x80\xc2\x9f" "b", "errors/a__b", "controles C1");
    expect_topic("\xc2\xa0\xc3\xa7\xe2\x82\xac\xf0\x9f\x98\x80", "errors/\xc2\xa0\xc3\xa7\xe2\x82\xac\xf0\x9f\x98\x80",
                 "UTF-8 valido como esta");
    expect_topic("\xff" "a\xc3(" "\xed\xa0\x80", "errors/_a_(___", "UTF-8 invalido, byte a byte");
    expect_topic("cli\"q\\b", "errors/cli\"q\\b", "aspas e barra invertida ficam");
    expect_topic("$SYS", "errors/$SYS", "cifrao fica");
    CHECK(error_json_topic(NULL, ERROR_JSON_TOPIC_CAP, "c") == -1, "topico: out NULL aceito");
    CHECK(error_json_topic(tbuf, 8, "c") == -1, "topico: cap 8 aceita");
    CHECK(topic(9, "cc") == 8 && strcmp(tbuf, "errors/c") == 0 && tbuf[9] == 'Z', "topico: cap 9 deu '%s'", tbuf);

    // Cut: for each character size, with 0 to 3 padding bytes in front, every
    // length until past the limit. The topic fits in 255 bytes, is valid, is a
    // prefix of the whole topic, cuts only between characters and uses the
    // room.
    struct { const char *u; size_t un; const char *out; size_t on; } units[] = {
        { "c", 1, "c", 1 },
        { "+", 1, "_", 1 },
        { "\xc3\xa9", 2, "\xc3\xa9", 2 },
        { "\xc2\x85", 2, "_", 1 },
        { "\xe2\x82\xac", 3, "\xe2\x82\xac", 3 },
        { "\xf0\x9f\x98\x80", 4, "\xf0\x9f\x98\x80", 4 },
    };
    static char id[2000], full[2000];
    for (size_t u = 0; u < sizeof(units) / sizeof(units[0]); u++) {
        for (size_t pad = 0; pad < 4; pad++) {
            int saw_cut = 0, saw_exact = 0;
            for (size_t count = 0; count * units[u].un <= 600; count++) {
                char caso[80];
                snprintf(caso, sizeof(caso), "topico unidade %zu, %zu + %zu", u, pad, count);
                size_t n = 0, fn = 7;
                memcpy(full, "errors/", 7);
                for (size_t k = 0; k < pad; k++) { id[n++] = 'c'; full[fn++] = 'c'; }
                for (size_t k = 0; k < count; k++) {
                    memcpy(id + n, units[u].u, units[u].un); n += units[u].un;
                    memcpy(full + fn, units[u].out, units[u].on); fn += units[u].on;
                }
                id[n] = '\0';
                if (n == 0) full[fn++] = '_';
                int len = topic(ERROR_JSON_TOPIC_CAP, id);
                CHECK(len > 7 && (size_t)len == strlen(tbuf), "%s: comprimento %d", caso, len);
                if (len <= 7) continue;
                CHECK(len <= ERROR_JSON_TOPIC_CAP - 1, "%s: %d bytes", caso, len);
                CHECK(tbuf[ERROR_JSON_TOPIC_CAP] == 'Z', "%s: escreveu alem da capacidade", caso);
                CHECK(valid_topic(tbuf, (size_t)len), "%s: topico invalido", caso);
                CHECK((size_t)len <= fn && memcmp(tbuf, full, (size_t)len) == 0, "%s: nao e prefixo do topico inteiro", caso);
                if (n > 0) CHECK(((size_t)len - 7 - pad) % units[u].on == 0, "%s: corte no meio de um caractere", caso);
                if ((size_t)len < fn) {
                    saw_cut = 1;
                    CHECK(len > ERROR_JSON_TOPIC_CAP - 1 - (int)units[u].on, "%s: cortou cedo demais (%d)", caso, len);
                } else if (len == ERROR_JSON_TOPIC_CAP - 1) {
                    saw_exact = 1;
                }
            }
            CHECK(saw_cut, "topico unidade %zu, enchimento %zu: nenhum caso cortado", u, pad);
            if (units[u].on == 1) CHECK(saw_exact, "topico unidade %zu, enchimento %zu: nenhum caso de 255 bytes exatos", u, pad);
        }
    }
}

// 9. error_json_string: JSON string escaping, without cutting.
static void expect_string(const char *s, size_t n, const char *want, const char *caso) {
    char out[256];
    memset(out, 'Z', sizeof(out));
    int len = error_json_string(out, sizeof(out), s, n);
    CHECK(len == (int)strlen(want) && strcmp(out, want) == 0, "string %s: '%s', esperado '%s'", caso, out, want);
}

static void t_string(void) {
    expect_string("abc", 3, "\"abc\"", "simples");
    expect_string("", 0, "\"\"", "vazia");
    expect_string(NULL, 0, "\"\"", "NULL");
    expect_string("a\"b\\c\n\x01", 7, "\"a\\\"b\\\\c\\n\\u0001\"", "escapes");
    expect_string("a\0b", 3, "\"a\\u0000b\"", "NUL no meio");
    expect_string("\xff", 1, "\"\\ufffd\"", "byte invalido");

    // Strict json-c, with UTF-8 validation, reads back the same text.
    const char *s = "id \"x\" \\ \t\r\n\x1f \xc3\xa7 \xf0\x9f\x98\x80 /";
    size_t sn = strlen(s);
    char out[256];
    int len = error_json_string(out, sizeof(out), s, sn);
    CHECK(len > 0 && (size_t)len == strlen(out), "string: comprimento %d", len);
    int ctl = 0;
    for (int i = 0; i < len; i++) if ((unsigned char)out[i] < 0x20) ctl++;
    CHECK(ctl == 0, "string: %d byte(s) de controle cru(s)", ctl);
    json_tokener *tok = json_tokener_new();
    json_tokener_set_flags(tok, JSON_TOKENER_STRICT | JSON_TOKENER_VALIDATE_UTF8);
    json_object *o = json_tokener_parse_ex(tok, out, len);
    CHECK(o && json_object_is_type(o, json_type_string) && (size_t)json_object_get_string_len(o) == sn &&
          memcmp(json_object_get_string(o), s, sn) == 0 && json_tokener_get_parse_end(tok) == (size_t)len,
          "string: json-c leu diferente: %s", out);
    json_object_put(o);
    json_tokener_free(tok);
    if (dump && len > 0) fprintf(dump, "%s\n", out);

    // No cut: it fits whole or returns -1 with an empty out.
    char small[8];
    CHECK(error_json_string(small, 8, "abcde", 5) == 7 && strcmp(small, "\"abcde\"") == 0, "string: cabe exato deu '%s'", small);
    memset(small, 'Z', sizeof(small));
    CHECK(error_json_string(small, 8, "abcdef", 6) == -1 && small[0] == '\0', "string: nao cabe e nao devolveu -1 vazio");
    memset(small, 'Z', sizeof(small));
    CHECK(error_json_string(small, 8, "abcd\"", 5) == -1 && small[0] == '\0', "string: escape que nao cabe");
    CHECK(error_json_string(small, 2, "", 0) == -1 && small[0] == '\0', "string: cap 2 aceita");
    CHECK(error_json_string(NULL, 8, "a", 1) == -1, "string: out NULL aceito");
    for (size_t cap = 3; cap < 20; cap++) {
        memset(out, 'Z', sizeof(out));
        int r = error_json_string(out, cap, "\"\"\"\"\"\"", 6);
        CHECK(out[cap] == 'Z', "string cap %zu: escreveu alem da capacidade", cap);
        CHECK(r == (cap >= 15 ? 14 : -1), "string cap %zu: devolveu %d", cap, r);
        CHECK(r == -1 ? out[0] == '\0' : strlen(out) == (size_t)r, "string cap %zu: texto errado", cap);
    }
}

int main(int argc, char **argv) {
    if (argc > 1) {
        dump = fopen(argv[1], "w");
        if (!dump) { perror(argv[1]); return 2; }
    }
    t_formato();
    t_escapes();
    t_campos();
    t_utf8();
    t_corte();
    t_tetos();
    t_capacidades();
    t_topico();
    t_string();
    if (dump) fclose(dump);
    printf("test_error_json: %d conferencias, %d falha(s)\n", checks, failures);
    return failures ? 1 : 0;
}
