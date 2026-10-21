#ifndef JSON_PAYLOAD_H
#define JSON_PAYLOAD_H

#include <stddef.h>
#include <json-c/json.h>

// Aceita o payload so se, lido inteiro pelo comprimento, for exatamente um texto
// JSON (RFC 8259, secao 2: ws valor ws, ws = espaco, tab, LF, CR). Recusa NUL,
// sobra depois do valor, \f ou \v nas bordas e o que o json-c so aceita fora do
// modo estrito (comentario, aspas simples, virgula sobrando, TRUE, zero a esquerda);
// sem ele, comentario depois do valor passaria por espaco. O json-c 0.15 estrito
// aceita NaN, Infinity, "1." e controle cru ou UTF-8 invalido em string.
// Devolve 1 com o valor em *out (NULL para o literal null) ou 0 com *out = NULL;
// o chamador libera *out com json_object_put.
int json_payload_parse(const char *payload, size_t len, json_object **out);

#endif
