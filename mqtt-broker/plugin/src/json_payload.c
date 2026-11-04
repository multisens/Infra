// See json_payload.h. Tests: tests/test_json_payload.c.
#include "json_payload.h"
#include <limits.h>
#include <string.h>

// The ws of RFC 8259, section 2: only these four.
static int rfc_ws(unsigned char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r';
}

// What json-c skips as space besides the four (isspace in the C locale).
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
    // json-c skips \f and \v with the ws before and after the value; no JSON
    // value starts or ends with them.
    if (other_space(p[first]) || other_space(p[end - 1])) return 0;

    json_tokener *tok = json_tokener_new();
    if (!tok) return 0;
    json_tokener_set_flags(tok, JSON_TOKENER_STRICT);
    json_object *o = json_tokener_parse_ex(tok, payload, (int)end);
    enum json_tokener_error err = json_tokener_get_error(tok);
    size_t used = json_tokener_get_parse_end(tok);
    if (err == json_tokener_continue) {
        // All input consumed and more expected (a number, true, false or null
        // at the end, or an incomplete value): the NUL marks the end of the
        // input.
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
