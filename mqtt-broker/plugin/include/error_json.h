#ifndef ERROR_JSON_H
#define ERROR_JSON_H

#include <stddef.h>

// Mensagem publicada em errors/<client_id> quando o plugin recusa uma publicacao.

// Capacidade do buffer, com o NUL: a mensagem tem no maximo 2047 bytes.
#define ERROR_JSON_CAP 2048

// Teto, em bytes ja escapados, de cada campo alem do payload, que fica com o
// resto. So cortam casos extremos: topico e client_id do MQTT vao a 65535 bytes;
// details vem de error_message[512]. Sem base na norma.
#define ERROR_JSON_TIMESTAMP_MAX 32
#define ERROR_JSON_TOPIC_MAX 512
#define ERROR_JSON_TYPE_MAX 64
#define ERROR_JSON_DETAILS_MAX 768
#define ERROR_JSON_CLIENT_MAX 256

// Menor cap com que error_json_build garante a mensagem inteira: 101 bytes de
// literais da estrutura, os tetos acima e o NUL. Abaixo disso devolve -1.
#define ERROR_JSON_MIN_CAP (101 + ERROR_JSON_TIMESTAMP_MAX + ERROR_JSON_TOPIC_MAX + \
    ERROR_JSON_TYPE_MAX + ERROR_JSON_DETAILS_MAX + ERROR_JSON_CLIENT_MAX + 1)

// Monta em out (cap bytes)
//   {"timestamp":"..","topic":"..","error_type":"..","details":"..",
//    "client_id":".."[,"payload":".."][,"truncated":true]}
// sempre como JSON e UTF-8 validos (RFC 8259): aspas, barra e U+0000 a U+001F
// escapados; byte fora de UTF-8 vira \ufffd (o payload do MQTT e binario). O
// payload vai pelo comprimento (NUL no meio sai \u0000; NULL omite o campo).
// Corte so entre unidades inteiras, com "truncated":true. Demais textos sao C
// strings (NULL vale ""). Devolve o comprimento (sem o NUL) ou -1 (out NULL ou
// cap < ERROR_JSON_MIN_CAP).
int error_json_build(char *out, size_t cap,
                     const char *timestamp, const char *topic,
                     const char *error_type, const char *details,
                     const char *client_id,
                     const char *payload, size_t payload_len);

// Capacidade do buffer do topico errors/<client_id>, com o NUL (255 bytes).
#define ERROR_JSON_TOPIC_CAP 256

// Monta em out o topico errors/<nome>: +, #, / e controles (U+0000 a U+001F,
// U+007F a U+009F) do client_id viram _, como cada byte fora de UTF-8; corte so
// entre caracteres inteiros. Curinga ou UTF-8 invalido no topico o tornam
// inassinavel ou derrubam o cliente libmosquitto. NULL vale "unknown"; vazio, "_".
// a+b e a_b caem no mesmo topico: o campo client_id leva o original. Sem base na
// norma. Devolve o comprimento ou -1 (out NULL ou cap < 9).
int error_json_topic(char *out, size_t cap, const char *client_id);

// s[0..n) como string JSON com aspas, pelo escape de error_json_build, sem
// corte: devolve o comprimento ou -1 (out = "") se nao couber. s NULL vale "".
int error_json_string(char *out, size_t cap, const char *s, size_t n);

#endif
