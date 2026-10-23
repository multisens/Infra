#ifndef JSON_PAYLOAD_H
#define JSON_PAYLOAD_H

#include <stddef.h>
#include <json-c/json.h>

// Accepts the payload only if, read whole by its length, it is exactly one
// JSON text (RFC 8259, section 2: ws value ws, ws = space, tab, LF, CR).
// Refuses NUL, anything after the value, \f or \v at the ends and what
// json-c accepts only outside strict mode (comments, single quotes, trailing
// commas, TRUE, leading zeros). Accepted, though not JSON, as by json-c 0.15
// in strict mode: NaN, Infinity, "1." and raw control characters or invalid
// UTF-8 in a string. Returns 1 with the value in *out, a new reference (NULL
// for the literal null), or 0 with *out = NULL.
int json_payload_parse(const char *payload, size_t len, json_object **out);

#endif
