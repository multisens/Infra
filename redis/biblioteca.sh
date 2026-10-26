#!/bin/sh
# Builds the "tv30" Redis library (#!lua, esquema.json in a long Lua string,
# funcoes.lua) and prints it:
#   sh biblioteca.sh | redis-cli -x FUNCTION LOAD REPLACE
# Usage: biblioteca.sh [esquema.json] [funcoes.lua] (default: next to this
# script)
set -eu
DIR=$(dirname "$0")
ESQ=${1:-$DIR/esquema.json}
LUA=${2:-$DIR/funcoes.lua}
for f in "$ESQ" "$LUA"; do
  [ -r "$f" ] || { echo "biblioteca.sh: nao consigo ler $f" >&2; exit 1; }
done
# The long Lua string ends with ]==]: the schema cannot contain that
# sequence.
if grep -q ']==]' "$ESQ"; then
  echo "biblioteca.sh: $ESQ contem ']==]', que encerraria a string Lua" >&2
  exit 1
fi
printf '#!lua name=tv30\n'
printf 'local ESQUEMA_JSON = [==[\n'
cat "$ESQ"
printf '\n]==]\n'
cat "$LUA"
