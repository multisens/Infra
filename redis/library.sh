#!/bin/sh
# Builds the "tv30" Redis library (#!lua, schema.json in a long Lua string,
# functions.lua) and prints it:
#   sh library.sh | redis-cli -x FUNCTION LOAD REPLACE
# Usage: library.sh [schema.json] [functions.lua] (default: next to this
# script)
set -eu
DIR=$(dirname "$0")
SCHEMA=${1:-$DIR/schema.json}
LUA=${2:-$DIR/functions.lua}
for f in "$SCHEMA" "$LUA"; do
  [ -r "$f" ] || { echo "library.sh: cannot read $f" >&2; exit 1; }
done
# The long Lua string ends with ]==]: the schema cannot contain that
# sequence.
if grep -q ']==]' "$SCHEMA"; then
  echo "library.sh: $SCHEMA contains ']==]', which would end the Lua string" >&2
  exit 1
fi
printf '#!lua name=tv30\n'
printf 'local SCHEMA_JSON = [==[\n'
cat "$SCHEMA"
printf '\n]==]\n'
cat "$LUA"
