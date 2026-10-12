#ifndef ERROR_JSON_H
#define ERROR_JSON_H

#include <stddef.h>

// Mensagem publicada em errors/<client_id> quando o plugin recusa uma
// publicacao (correcao de 12/10, defeito achado pela auditoria da
// documentacao: publish_error colava os campos com snprintf, sem escape).
//
// Capacidade do buffer, com o NUL: a mensagem tem no maximo
// ERROR_JSON_CAP - 1 = 2047 bytes, o mesmo limite que publish_error ja tinha
// (la, uma mensagem de 2048 bytes ou mais nao saia; agora o conteudo e
// cortado para caber, e a mensagem sai).
#define ERROR_JSON_CAP 2048

// Teto, em bytes JA escapados, de cada campo de texto alem do payload. O que
// sobra do limite fica para o payload, o campo que costuma ser grande. Os
// tetos so cortam casos extremos: topico e client_id do MQTT vao a 65535
// bytes; "details" vem do schema_validator (error_message[512], ate 511
// bytes antes do escape). Decisao de implementacao (12/10), sem base na norma.
#define ERROR_JSON_TIMESTAMP_MAX 32
#define ERROR_JSON_TOPIC_MAX 512
#define ERROR_JSON_TYPE_MAX 64
#define ERROR_JSON_DETAILS_MAX 768
#define ERROR_JSON_CLIENT_MAX 256

// Menor capacidade com que error_json_build garante a mensagem inteira: os
// literais da estrutura (101 bytes: chaves, aspas, nomes dos campos e o
// marcador de corte), os tetos acima e o NUL. Abaixo disso a funcao recusa
// (-1) em vez de montar JSON pela metade.
#define ERROR_JSON_MIN_CAP (101 + ERROR_JSON_TIMESTAMP_MAX + ERROR_JSON_TOPIC_MAX + \
    ERROR_JSON_TYPE_MAX + ERROR_JSON_DETAILS_MAX + ERROR_JSON_CLIENT_MAX + 1)

// Monta em out (cap bytes) o objeto
//   {"timestamp":"..","topic":"..","error_type":"..","details":"..",
//    "client_id":".."[,"payload":".."][,"truncated":true]}
// sempre como JSON valido (RFC 8259) e UTF-8 valido:
// - aspas, barra invertida e U+0000 a U+001F saem escapados (secao 7);
// - byte que nao forma UTF-8 valido vira U+FFFD, escrito \ufffd (o texto JSON
//   tem de ser UTF-8, secao 8.1; o payload do MQTT e binario);
// - o payload entra com o comprimento dado (payload_len), entao um NUL no meio
//   dele sai como \u0000 em vez de encerrar o campo; payload NULL omite o campo;
// - campo que nao cabe e cortado so entre unidades inteiras (um caractere
//   UTF-8 ou um escape completo), nunca no meio, e a mensagem ganha
//   "truncated":true.
// Os demais textos sao C strings; NULL vale "". Devolve o comprimento da
// mensagem (sem o NUL, sempre menor que cap) ou -1 se out e NULL ou cap e
// menor que ERROR_JSON_MIN_CAP.
int error_json_build(char *out, size_t cap,
                     const char *timestamp, const char *topic,
                     const char *error_type, const char *details,
                     const char *client_id,
                     const char *payload, size_t payload_len);

// Capacidade do buffer do topico errors/<client_id>, com o NUL: o topico tem
// no maximo 255 bytes, o mesmo limite do char error_topic[256] de antes.
#define ERROR_JSON_TOPIC_CAP 256

// Monta em out (cap bytes) o topico errors/<nome> (correcao de 12/10, defeito
// achado na revisao do plugin: o snprintf em 256 bytes podia cortar um
// caractere UTF-8 no meio, e o topico saia com UTF-8 invalido, que o cliente
// libmosquitto recusa derrubando a conexao; client_id com + ou # dava nome de
// topico com curinga, que nenhum assinante consegue pedir; com /, niveis a
// mais). Regra do <nome>, a partir do client_id:
// - cada caractere +, #, / e cada controle (U+0000 a U+001F, U+007F a U+009F,
//   os que a secao 1.5.3 do MQTT 3.1.1 e o mosquitto recusam) vira um _;
// - cada byte que nao forma UTF-8 valido vira um _;
// - o resto do UTF-8 sai como esta;
// - o corte, se o topico passar de cap - 1 bytes, e so entre caracteres
//   inteiros, nunca no meio de um;
// - client_id NULL vale "unknown" (como antes); client_id vazio vira "_"
//   (o nivel nunca fica vazio).
// O mosquitto ja recusa no CONNECT client_id com UTF-8 invalido ou controle,
// entao, na pratica, so +, # e / sao trocados; o resto e defesa. A troca pode
// juntar dois clientes no mesmo topico (a+b e a_b): o campo client_id da
// mensagem leva o client_id original. Decisao de implementacao (12/10), sem
// base na norma. Devolve o comprimento do topico (sem o NUL) ou -1 se out e
// NULL ou cap e menor que 9 ("errors/" + 1 caractere + NUL).
int error_json_topic(char *out, size_t cap, const char *client_id);

// Escreve em out (cap bytes, com o NUL) s[0..n) como string JSON, com as
// aspas, pelas mesmas regras de escape de error_json_build (RFC 8259, secao 7;
// byte que nao forma UTF-8 valido vira \ufffd), sem corte: devolve o
// comprimento (sem o NUL) ou -1, com out = "", se nao couber inteiro ou se out
// e NULL. s NULL vale "". Usado pelo response_time_tester.c (correcao de
// 12/10: o campo id saia sem escape).
int error_json_string(char *out, size_t cap, const char *s, size_t n);

#endif
