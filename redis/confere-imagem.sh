#!/bin/sh
# Conferencia no build da imagem: num redis-server temporario, carrega a
# biblioteca, aplica o seed.resp e exige carga sem erro e auditoria vazia.
# Nada fica na imagem (sem RDB nem AOF).
set -eu
P=6390
LIB=/opt/tv30-redis
redis-server --port "$P" --save '' --appendonly no --dir /tmp --daemonize yes --logfile /tmp/confere.log
i=0
until redis-cli -p "$P" ping >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 100 ] || { echo "confere-imagem: o redis temporario nao subiu"; cat /tmp/confere.log; exit 1; }
  sleep 0.1
done
para() { redis-cli -p "$P" shutdown nosave >/dev/null 2>&1 || true; rm -f /tmp/confere.log; }

out=$(sh "$LIB/biblioteca.sh" "$LIB/esquema.json" "$LIB/funcoes.lua" | redis-cli -p "$P" -x FUNCTION LOAD REPLACE)
[ "$out" = tv30 ] || { echo "confere-imagem: a biblioteca nao carregou: $out"; para; exit 1; }

res=$(redis-cli -p "$P" --pipe < /seed.resp 2>&1) || true
if ! echo "$res" | grep -q '^errors: 0,'; then
  echo "confere-imagem: a carga inicial (userData.json) foi recusada pelo esquema:"
  echo "$res"
  para; exit 1
fi
aud=$(redis-cli -p "$P" FCALL_RO tv30_auditoria 0)
if [ -n "$aud" ]; then
  echo "confere-imagem: a auditoria depois da carga nao esta vazia:"
  echo "$aud"
  para; exit 1
fi
n=$(redis-cli -p "$P" --raw SCARD users:index)
para
echo "confere-imagem: biblioteca tv30 carregada, carga inicial com $n perfil(is) pelas funcoes, auditoria vazia"
