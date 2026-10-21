// Ver json_payload.h. Teste: tests/test_json_payload.c, que roda no build da imagem.
#include "json_payload.h"
#include <limits.h>
#include <string.h>

// ws da RFC 8259, secao 2: so estes quatro.
static int rfc_ws(unsigned char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r';
}

// O que o json-c pula como espaco alem dos quatro (isspace no locale C).
static int other_space(unsigned char c) {
    return c == '\f' || c == '\v';
}

int json_payload_parse(const char *payload, size_t len, json_object **out) {
    *out = NULL;
    if (!payload || len > INT_MAX) return 0;
    if (memchr(payload, '\0', len)) return 0;

    const unsigned char *p = (const unsigned char *)payload;
    size_t first = 0, end = len;
    while (first < len && rfc_ws(p[first])) first++;
    while (end > first && rfc_ws(p[end - 1])) end--;
    if (first == end) return 0;
    // O json-c pula \f e \v junto com os ws antes e depois do valor; nenhum
    // valor JSON comeca ou termina com eles.
    if (other_space(p[first]) || other_space(p[end - 1])) return 0;

    json_tokener *tok = json_tokener_new();
    if (!tok) return 0;
    json_tokener_set_flags(tok, JSON_TOKENER_STRICT);
    json_object *o = json_tokener_parse_ex(tok, payload, (int)end);
    enum json_tokener_error err = json_tokener_get_error(tok);
    size_t used = json_tokener_get_parse_end(tok);
    if (err == json_tokener_continue) {
        // Consumiu tudo e ainda espera mais (numero, true, false ou null no
        // fim, ou valor incompleto): o NUL avisa o fim da entrada.
        o = json_tokener_parse_ex(tok, "", 1);
        err = json_tokener_get_error(tok);
        used = end;
    }
    json_tokener_free(tok);

    if (err != json_tokener_success || used != end) {
        if (o) json_object_put(o);
        return 0;
    }
    *out = o;
    return 1;
}
