// Teste de json_payload_parse: so passa payload que, lido inteiro pelo
// comprimento, seja exatamente um texto JSON.
//
//   (em infra/mqtt-broker/plugin)
//   gcc -std=gnu11 -Wall -Wextra -I include tests/test_json_payload.c
//       src/json_payload.c -ljson-c -o /tmp/test_json_payload
//   /tmp/test_json_payload [/tmp/casos.tsv]
//
// Com o argumento, grava cada caso (payload em hexadecimal, veredito, 1 se e
// divergencia conhecida do json-c) para o check_json_payload.py conferir.
#include <json-c/json.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "json_payload.h"

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

static void dump_case(const char *p, size_t n, int verdict, int known) {
    if (!dump) return;
    for (size_t i = 0; i < n; i++) fprintf(dump, "%02x", (unsigned char)p[i]);
    fprintf(dump, "\t%d\t%d\n", verdict, known);
}

// Roda o parse e confere o veredito; devolve o objeto (ou NULL).
static json_object *run(const char *p, size_t n, int want, int known, const char *caso) {
    json_object *o = (json_object *)0x1;  // tem de virar NULL na recusa
    int r = json_payload_parse(p, n, &o);
    CHECK(r == want, "%s: devolveu %d, esperado %d", caso, r, want);
    if (r == 0) CHECK(o == NULL, "%s: recusa sem *out = NULL", caso);
    dump_case(p, n, r, known);
    return r ? o : NULL;
}

// Aceito, com o valor esperado (serializacao do json-c, ou "null").
static void accept(const char *p, size_t n, const char *want, const char *caso) {
    json_object *o = run(p, n, 1, 0, caso);
    const char *got = o ? json_object_to_json_string_ext(o, JSON_C_TO_STRING_PLAIN) : "null";
    CHECK(strcmp(got, want) == 0, "%s: valor %s, esperado %s", caso, got, want);
    json_object_put(o);
}

static void reject(const char *p, size_t n, int known, const char *caso) {
    run(p, n, 0, known, caso);
}

#define ACC(lit, want) accept(lit, sizeof(lit) - 1, want, #lit)
#define REJ(lit) reject(lit, sizeof(lit) - 1, 0, #lit)

// 1. Texto JSON valido: ws valor ws (RFC 8259, secao 2)
static void t_validos(void) {
    ACC("{\"value\":1}", "{\"value\":1}");
    ACC("{\"value\":1} \t\r\n", "{\"value\":1}");
    ACC(" \t\r\n{\"value\":1}", "{\"value\":1}");
    ACC("\n{ \"a\" : [ 1 , 2 ] ,\r\n \"b\" : { } }\n", "{\"a\":[1,2],\"b\":{}}");
    ACC("123", "123");
    ACC("123 ", "123");
    ACC("-0", "0");  // o json-c guarda -0 como o inteiro 0
    ACC("1.5e-3", "1.5e-3");
    ACC("1E+2", "1E+2");
    ACC("0", "0");
    ACC("true", "true");
    ACC("false\n", "false");
    ACC("null", "null");
    ACC(" null ", "null");
    ACC("\"abc\"", "\"abc\"");
    ACC("\"a\\/b\\\\c\\\"d\\n\\u00e7\\u0000x\"", "\"a\\/b\\\\c\\\"d\\n\xc3\xa7\\u0000x\"");
    ACC("\"a\xc3\xa7\xc3\xa3o \xf0\x9f\x98\x80\"", "\"a\xc3\xa7\xc3\xa3o \xf0\x9f\x98\x80\"");
    ACC("[]", "[]");
    ACC("{}", "{}");
    ACC("[{\"a\":[{\"b\":null}]},[]]", "[{\"a\":[{\"b\":null}]},[]]");
    // o que os publicadores da stack mandam (JSON.stringify)
    ACC("{\"top\":\"12%\",\"left\":\"35%\",\"width\":\"60%\",\"height\":\"60%\"}",
        "{\"top\":\"12%\",\"left\":\"35%\",\"width\":\"60%\",\"height\":\"60%\"}");
    ACC("[{\"globalServiceId\":\"g\",\"appVersion\":1,\"appName\":\"n\",\"appIcon\":\"i\",\"bannerIcon\":\"b\","
        "\"backgroundColor\":\"#000000\",\"foregroundColor\":\"#ffffff\"}]",
        "[{\"globalServiceId\":\"g\",\"appVersion\":1,\"appName\":\"n\",\"appIcon\":\"i\",\"bannerIcon\":\"b\","
        "\"backgroundColor\":\"#000000\",\"foregroundColor\":\"#ffffff\"}]");
    ACC("{\"Service\":{\"id\":\"urn:tv30:service:webmedia\"}}", "{\"Service\":{\"id\":\"urn:tv30:service:webmedia\"}}");
    ACC("{\"value\":20}", "{\"value\":20}");
}

// 2. Nao e texto JSON: sobra, NUL, ws fora dos quatro, extensoes do json-c
static void t_invalidos(void) {
    REJ("{\"value\":1}lixo");
    REJ("{\"value\":1}\0lixo");
    REJ("{\"value\":1}\0");
    REJ("\0{\"value\":1}");
    REJ("{\"va\0lue\":1}");
    REJ("{\"value\":1}{\"value\":99}");
    REJ("{\"value\":1} x");
    REJ("[1,2]x");
    REJ("\"abc\"lixo");
    REJ("\"\"qualquer coisa");
    REJ("123x");
    REJ("1e5abc");
    REJ("truex");
    REJ("nullx");
    REJ("3f2a1b9c-0000-4000-8000-000000000000");  // UUID com digito na frente
    REJ("a3f2a1b9-0000-4000-8000-000000000000");
    REJ("/appcat");
    REJ("urn:tv30:service:webmedia");
    REJ("{\"value\":1}\f");
    REJ("{\"value\":1}\v");
    REJ("{\"value\":1} \f ");
    REJ("\f{\"value\":1}");
    REJ(" \v {\"value\":1}");
    REJ("{\"value\":1}//c");
    REJ("{\"value\":1}//x}");
    REJ("{\"value\":1}/*c*/");
    REJ("{\"value\":1} /*c*/ ");
    REJ("/*c*/{\"value\":1}");
    REJ("{\"value\":1,}");
    REJ("[1,]");
    REJ("'abc'");
    REJ("TRUE");
    REJ("01");
    REJ("+1");
    REJ(".5");
    REJ("-");
    REJ("{\"a\":");
    REJ("[");
    REJ("\"abc");
    REJ("{a:1}");
    REJ("");
    REJ("   ");
    REJ(" \t\r\n");
    REJ("\0");
    REJ("abc\0def");
    reject(NULL, 0, 0, "NULL");
}

// 3. Divergencias conhecidas: o json-c 0.15 estrito aceita, a RFC 8259 nao
// (registradas em json_payload.h; o Python confere que sao mesmo invalidas)
static void t_conhecidas(void) {
    const char *k[] = { "NaN", "Infinity", "-Infinity", "1.", "\"a\tb\"", "\"a\x01" "b\"", "\"\xff\"", "\"\xc3(\"" };
    for (size_t i = 0; i < sizeof(k) / sizeof(k[0]); i++) {
        json_object *o = run(k[i], strlen(k[i]), 1, 1, k[i]);
        json_object_put(o);
    }
}

// 4. Todo byte depois e antes do valor: so os quatro ws da RFC passam
static void t_bytes(void) {
    const char *vals[] = { "{\"value\":1}", "[1]", "\"x\"", "true", "null" };
    char p[32], caso[64];
    for (size_t v = 0; v < sizeof(vals) / sizeof(vals[0]); v++) {
        size_t vn = strlen(vals[v]);
        for (int b = 0; b < 256; b++) {
            int ws = b == ' ' || b == '\t' || b == '\n' || b == '\r';
            json_object *o;
            memcpy(p, vals[v], vn);
            p[vn] = (char)b;
            snprintf(caso, sizeof(caso), "%s + 0x%02x", vals[v], b);
            o = run(p, vn + 1, ws, 0, caso);
            json_object_put(o);
            p[0] = (char)b;
            memcpy(p + 1, vals[v], vn);
            snprintf(caso, sizeof(caso), "0x%02x + %s", b, vals[v]);
            o = run(p, vn + 1, ws, 0, caso);
            json_object_put(o);
        }
    }
}

// 5. Pelo comprimento: nada alem de len e lido, e o NUL do fim nao e exigido
static void t_comprimento(void) {
    accept("123", 2, "12", "123 com len 2");
    accept("{\"a\":1}lixo", 7, "{\"a\":1}", "{\"a\":1}lixo com len 7");
    accept("true!", 4, "true", "true! com len 4");
    reject("{\"a\":1}", 6, 0, "{\"a\":1} com len 6");
    // buffer exato, sem NUL no fim
    const char *src = "[1,2,3]";
    size_t n = strlen(src);
    char *b = malloc(n);
    memcpy(b, src, n);
    accept(b, n, "[1,2,3]", "buffer sem NUL");
    free(b);
    // payload grande (1 MiB): array de numeros
    size_t big = 1 << 20;
    char *g = malloc(big);
    size_t k = 0;
    g[k++] = '[';
    while (k + 3 < big) { g[k++] = '7'; g[k++] = ','; }
    g[k++] = '7';
    g[k++] = ']';
    json_object *o = run(g, k, 1, 0, "array de 1 MiB");
    CHECK(o && json_object_is_type(o, json_type_array) && json_object_array_length(o) == (k - 1) / 2,
          "array de 1 MiB: valor errado");
    json_object_put(o);
    g[k++] = 'x';
    reject(g, k, 0, "array de 1 MiB + x");
    free(g);
}

int main(int argc, char **argv) {
    if (argc > 1) {
        dump = fopen(argv[1], "w");
        if (!dump) { perror(argv[1]); return 2; }
    }
    t_validos();
    t_invalidos();
    t_conhecidas();
    t_bytes();
    t_comprimento();
    if (dump) fclose(dump);
    printf("test_json_payload: %d conferencias, %d falha(s)\n", checks, failures);
    return failures ? 1 : 0;
}
