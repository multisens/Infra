#!/bin/sh
# Monta a biblioteca "tv30" do Redis (#!lua + esquema.json numa string Lua
# longa + funcoes.lua) e a imprime: sh biblioteca.sh | redis-cli -x FUNCTION LOAD REPLACE
# Uso: biblioteca.sh [esquema.json] [funcoes.lua] (padrao: ao lado deste script)
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
