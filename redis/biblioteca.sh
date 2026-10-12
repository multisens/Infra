#!/bin/sh
# Monta a biblioteca de funcoes "tv30" do Redis (P4, decisao do Luis em
# 10/10) e a imprime na saida padrao, pronta para
#   sh biblioteca.sh | redis-cli -x FUNCTION LOAD REPLACE
#
# A biblioteca = cabecalho #!lua + o esquema.json embutido numa string Lua
# longa (ESQUEMA_JSON) + o corpo funcoes.lua. Assim as regras ficam
# declaradas num lugar so, o esquema.json: a funcao aplica exatamente o que
# esta nele, e os testes (infra/redis/test) leem o mesmo arquivo.
#
# Uso: biblioteca.sh [esquema.json] [funcoes.lua]
#      (padrao: os dois ao lado deste script)
# Quem chama: infra/redis/entrypoint.sh (a cada partida), o Dockerfile
# (conferencia no build) e infra/redis/test/run.sh.
set -eu
DIR=$(dirname "$0")
ESQ=${1:-$DIR/esquema.json}
LUA=${2:-$DIR/funcoes.lua}
for f in "$ESQ" "$LUA"; do
  [ -r "$f" ] || { echo "biblioteca.sh: nao consigo ler $f" >&2; exit 1; }
done
# a string Lua longa termina em ]==]: o esquema nao pode conter essa sequencia
if grep -q ']==]' "$ESQ"; then
  echo "biblioteca.sh: $ESQ contem ']==]', que encerraria a string Lua" >&2
  exit 1
fi
printf '#!lua name=tv30\n'
printf 'local ESQUEMA_JSON = [==[\n'
cat "$ESQ"
printf '\n]==]\n'
cat "$LUA"
