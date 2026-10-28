// Message and topic of errors/<client_id>, and JSON string escaping
// (error_json.h). Tests: tests/test_error_json.c.
#include "error_json.h"
#include <stdio.h>
#include <string.h>

typedef struct {
    char *buf;
    // Bytes of buf, with the NUL.
    size_t cap;
    // Bytes written, without the NUL.
    size_t len;
    // Some field was cut.
    int truncated;
} json_writer;

static const char MARKER[] = ",\"truncated\":true";
#define MARKER_LEN (sizeof(MARKER) - 1)

// Length (1 to 4) of the well-formed UTF-8 character that starts at s, with
// n bytes available; 0 if the sequence is not valid UTF-8: a stray
// continuation byte, an overlong form (C0, C1, E0 80..9F, F0 80..8F), a
// UTF-16 surrogate (ED A0..BF), above U+10FFFF (F4 90.., F5..FF) or a cut
// sequence. Ranges of RFC 3629, section 4.
static size_t utf8_char_len(const unsigned char *s, size_t n) {
    unsigned char c = s[0];
    if (c < 0x80) return 1;
    if (c < 0xC2) return 0;
    if (c < 0xE0) {
        if (n < 2 || (s[1] & 0xC0) != 0x80) return 0;
        return 2;
    }
    if (c < 0xF0) {
        if (n < 3) return 0;
        if (c == 0xE0 && s[1] < 0xA0) return 0;
        if (c == 0xED && s[1] > 0x9F) return 0;
        if ((s[1] & 0xC0) != 0x80 || (s[2] & 0xC0) != 0x80) return 0;
        return 3;
    }
    if (c < 0xF5) {
        if (n < 4) return 0;
        if (c == 0xF0 && s[1] < 0x90) return 0;
        if (c == 0xF4 && s[1] > 0x8F) return 0;
        if ((s[1] & 0xC0) != 0x80 || (s[2] & 0xC0) != 0x80 || (s[3] & 0xC0) != 0x80) return 0;
        return 4;
    }
    return 0;
}

// Appends a structure literal (braces, field names). The room for the
// literals is reserved by ERROR_JSON_MIN_CAP; if it still does not fit,
// writes nothing and marks the cut, which does not happen with
// cap >= ERROR_JSON_MIN_CAP.
static void put_literal(json_writer *w, const char *t) {
    size_t n = strlen(t);
    if (w->len + n >= w->cap) {
        w->truncated = 1;
        return;
    }
    memcpy(w->buf + w->len, t, n);
    w->len += n;
    w->buf[w->len] = '\0';
}

// Writes s[0..n) as the content of a JSON string, without quotes (RFC 8259,
// section 7), in at most max bytes; each unit (UTF-8 character or escape)
// goes in whole or not at all. Returns 1 if everything fit, 0 if it cut.
static int put_string(json_writer *w, const char *s, size_t n, size_t max) {
    const unsigned char *p = (const unsigned char *)s;
    size_t used = 0, i = 0;
    int whole = 1;
    while (i < n) {
        char esc[8];
        const char *unit = esc;
        size_t ulen, adv = 1;
        unsigned char c = p[i];
        if (c == '"' || c == '\\') {
            esc[0] = '\\';
            esc[1] = (char)c;
            ulen = 2;
        } else if (c < 0x20) {
            const char *named = NULL;
            switch (c) {
            case '\b': named = "\\b"; break;
            case '\f': named = "\\f"; break;
            case '\n': named = "\\n"; break;
            case '\r': named = "\\r"; break;
            case '\t': named = "\\t"; break;
            }
            if (named) {
                unit = named;
                ulen = 2;
            } else {
                snprintf(esc, sizeof(esc), "\\u%04x", c);
                ulen = 6;
            }
        } else {
            size_t k = utf8_char_len(p + i, n - i);
            if (k == 0) {
                // Invalid byte: U+FFFD.
                unit = "\\ufffd";
                ulen = 6;
            } else {
                unit = (const char *)p + i;
                ulen = adv = k;
            }
        }
        if (used + ulen > max || w->len + ulen >= w->cap) {
            whole = 0;
            w->truncated = 1;
            break;
        }
        memcpy(w->buf + w->len, unit, ulen);
        w->len += ulen;
        used += ulen;
        i += adv;
    }
    w->buf[w->len] = '\0';
    return whole;
}

static void put_field(json_writer *w, const char *literal, const char *s, size_t max) {
    put_literal(w, literal);
    if (s) put_string(w, s, strlen(s), max);
}

int error_json_build(char *out, size_t cap,
                     const char *timestamp, const char *topic,
                     const char *error_type, const char *details,
                     const char *client_id,
                     const char *payload, size_t payload_len) {
    if (!out || cap < ERROR_JSON_MIN_CAP) return -1;
    json_writer w = { out, cap, 0, 0 };
    out[0] = '\0';

    put_field(&w, "{\"timestamp\":\"", timestamp, ERROR_JSON_TIMESTAMP_MAX);
    put_field(&w, "\",\"topic\":\"", topic, ERROR_JSON_TOPIC_MAX);
    put_field(&w, "\",\"error_type\":\"", error_type, ERROR_JSON_TYPE_MAX);
    put_field(&w, "\",\"details\":\"", details, ERROR_JSON_DETAILS_MAX);
    put_field(&w, "\",\"client_id\":\"", client_id, ERROR_JSON_CLIENT_MAX);
    put_literal(&w, "\"");

    if (payload) {
        put_literal(&w, ",\"payload\":\"");
        // The payload gets what is left, minus the closing quote and brace (2
        // bytes) and, if a field was already cut, the marker. If the payload
        // does not fit whole, it goes back to the start of the field and writes
        // it again, leaving room for the marker.
        size_t start = w.len;
        size_t reserve = 2 + (w.truncated ? MARKER_LEN : 0);
        size_t room = (start + reserve < cap - 1) ? cap - 1 - start - reserve : 0;
        int had_cut = w.truncated;
        if (!put_string(&w, payload, payload_len, room) && !had_cut) {
            w.len = start;
            w.buf[w.len] = '\0';
            room = (room > MARKER_LEN) ? room - MARKER_LEN : 0;
            put_string(&w, payload, payload_len, room);
        }
        put_literal(&w, "\"");
    }
    if (w.truncated) put_literal(&w, MARKER);
    put_literal(&w, "}");
    return (int)w.len;
}

int error_json_topic(char *out, size_t cap, const char *client_id) {
    static const char PREFIX[] = "errors/";
    const size_t plen = sizeof(PREFIX) - 1;
    if (!out || cap < plen + 2) return -1;
    memcpy(out, PREFIX, plen);
    size_t len = plen;
    const unsigned char *p = (const unsigned char *)(client_id ? client_id : "unknown");
    size_t n = strlen((const char *)p), i = 0;
    if (n == 0) out[len++] = '_';
    while (i < n) {
        unsigned char c = p[i];
        size_t k = utf8_char_len(p + i, n - i);
        int swap;
        if (k == 0) {
            // Invalid byte.
            swap = 1;
            k = 1;
        } else if (k == 1) {
            swap = c == '+' || c == '#' || c == '/' || c < 0x20 || c == 0x7F;
        } else {
            // U+0080 to U+009F.
            swap = k == 2 && c == 0xC2 && p[i + 1] <= 0x9F;
        }
        size_t ulen = swap ? 1 : k;
        if (len + ulen > cap - 1) break;
        if (swap) out[len] = '_';
        else memcpy(out + len, p + i, k);
        len += ulen;
        i += k;
    }
    out[len] = '\0';
    return (int)len;
}

int error_json_string(char *out, size_t cap, const char *s, size_t n) {
    if (!out || cap < 3) {
        if (out && cap > 0) out[0] = '\0';
        return -1;
    }
    json_writer w = { out, cap, 0, 0 };
    out[0] = '\0';
    put_literal(&w, "\"");
    // cap - 3: the two quotes and the NUL.
    if (s && !put_string(&w, s, n, cap - 3)) {
        out[0] = '\0';
        return -1;
    }
    put_literal(&w, "\"");
    return (int)w.len;
}
