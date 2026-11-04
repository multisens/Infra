#ifndef ERROR_JSON_H
#define ERROR_JSON_H

#include <stddef.h>

// Message published on errors/<client_id> when the plugin refuses a
// publication.

// Buffer capacity, with the NUL: the message has at most 2047 bytes.
#define ERROR_JSON_CAP 2048

// Maximum, in escaped bytes, of each field other than the payload, which
// gets the rest.
#define ERROR_JSON_TIMESTAMP_MAX 32
#define ERROR_JSON_TOPIC_MAX 512
#define ERROR_JSON_TYPE_MAX 64
#define ERROR_JSON_DETAILS_MAX 768
#define ERROR_JSON_CLIENT_MAX 256

// Smallest cap with which error_json_build guarantees the whole message: 101
// bytes of structure literals, the maximums above and the NUL. Below it,
// returns -1.
#define ERROR_JSON_MIN_CAP (101 + ERROR_JSON_TIMESTAMP_MAX + ERROR_JSON_TOPIC_MAX + \
    ERROR_JSON_TYPE_MAX + ERROR_JSON_DETAILS_MAX + ERROR_JSON_CLIENT_MAX + 1)

// Builds in out (cap bytes)
//   {"timestamp":"..","topic":"..","error_type":"..","details":"..",
//    "client_id":".."[,"payload":".."][,"truncated":true]}
// always as valid JSON and UTF-8 (RFC 8259): quotes, backslash and U+0000 to
// U+001F escaped; a byte outside UTF-8 becomes �. The payload is read
// by its length (a NUL inside becomes \u0000; NULL omits the field). Cuts
// only between whole units, with "truncated":true. The other texts are C
// strings (NULL is ""). Returns the length, without the NUL, or -1 (out NULL
// or cap < ERROR_JSON_MIN_CAP).
int error_json_build(char *out, size_t cap,
                     const char *timestamp, const char *topic,
                     const char *error_type, const char *details,
                     const char *client_id,
                     const char *payload, size_t payload_len);

// Capacity of the buffer of the errors/<client_id> topic, with the NUL (255
// bytes).
#define ERROR_JSON_TOPIC_CAP 256

// Builds in out the topic errors/<name>: +, #, / and control characters
// (U+0000 to U+001F, U+007F to U+009F) of the client_id become _, as does
// each byte outside UTF-8; cuts only between whole characters. NULL is
// "unknown"; empty, "_". a+b and a_b give the same topic: the client_id
// field carries the original. Returns the length or -1 (out NULL or cap < 9).
int error_json_topic(char *out, size_t cap, const char *client_id);

// Writes s[0..n) as a quoted JSON string, with the escaping of
// error_json_build, without cutting: returns the length, or -1 (out = "") if
// it does not fit. s NULL is "".
int error_json_string(char *out, size_t cap, const char *s, size_t n);

#endif
