#ifndef JSON_PAYLOAD_H
#define JSON_PAYLOAD_H

#include <stddef.h>
#include <json-c/json.h>

// Leitura do payload MQTT como texto JSON, para a validacao de esquema
// (correcao de 12/10, defeito achado na revisao do plugin: o validador lia o
// payload com json_tokener_parse, que para no primeiro NUL e ignora o que vem
// depois do primeiro valor; {"value":1}lixo e {"value":1}\0lixo passavam e
// eram entregues inteiros).
//
// O payload e lido inteiro, pelo comprimento, e so e aceito se for
// exatamente um texto JSON da RFC 8259, secao 2: ws valor ws, com ws = espaco,
// tab, LF ou CR. Recusa:
// - payload com NUL: U+0000 cru nao cabe em texto JSON (fora de string nao e
//   ws nem valor; dentro de string, controle so entra escapado, secao 7);
// - qualquer byte depois do valor que nao seja um dos quatro ws;
// - \f ou \v antes ou depois do valor (o json-c os pula como espaco);
// - o que o json-c so aceita fora do modo estrito: comentario, aspas simples,
//   virgula sobrando, TRUE, zero a esquerda. O modo estrito e necessario para
//   a conferencia do fim: sem ele o json-c consome comentario depois do valor
//   como se fosse espaco, e {"value":1}/*lixo*/ ou {"value":1}//lixo passariam.
// Continua aceito (o json-c 0.15 estrito deixa passar, fora do escopo desta
// correcao): NaN, Infinity, numero terminado em ponto (1.), controle cru
// dentro de string e UTF-8 invalido dentro de string.
//
// Devolve 1 se o payload e um texto JSON, com o valor em *out (NULL para o
// literal null, que e como o json-c o representa; o chamador decide o que
// fazer com ele); 0 se nao e, com *out = NULL. O chamador libera *out com
// json_object_put.
int json_payload_parse(const char *payload, size_t len, json_object **out);

#endif
