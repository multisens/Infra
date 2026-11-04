#!/bin/sh
# Check at image build time: on a temporary redis-server, loads the library,
# applies seed.resp and requires a load without errors and an empty audit.
# Nothing stays in the image (no RDB or AOF).
set -eu
PORT=6390
LIB=/opt/tv30-redis
redis-server --port "$PORT" --save '' --appendonly no --dir /tmp --daemonize yes --logfile /tmp/check-image.log
i=0
until redis-cli -p "$PORT" ping >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 100 ] || { echo "check-image: the temporary redis did not start"; cat /tmp/check-image.log; exit 1; }
  sleep 0.1
done
stop() { redis-cli -p "$PORT" shutdown nosave >/dev/null 2>&1 || true; rm -f /tmp/check-image.log; }

out=$(sh "$LIB/library.sh" "$LIB/schema.json" "$LIB/functions.lua" | redis-cli -p "$PORT" -x FUNCTION LOAD REPLACE)
[ "$out" = tv30 ] || { echo "check-image: the library did not load: $out"; stop; exit 1; }

res=$(redis-cli -p "$PORT" --pipe < /seed.resp 2>&1) || true
if ! echo "$res" | grep -q '^errors: 0,'; then
  echo "check-image: the initial load (userData.json) was refused by the schema:"
  echo "$res"
  stop; exit 1
fi
audit=$(redis-cli -p "$PORT" FCALL_RO tv30_audit 0)
if [ -n "$audit" ]; then
  echo "check-image: the audit after the load is not empty:"
  echo "$audit"
  stop; exit 1
fi
n=$(redis-cli -p "$PORT" --raw SCARD users:index)
stop
echo "check-image: tv30 library loaded, initial load with $n profile(s) through the functions, empty audit"
