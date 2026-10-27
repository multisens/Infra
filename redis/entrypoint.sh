#!/bin/sh
# redis-server, the tv30 function library, the initial load and the commander
# (debug). If redis-server or the commander exits, the container exits.
set -u

# Healthcheck mark: it exists only after the library loaded in this start
# and the load is applied. /tmp survives docker restart, so it is removed
# before anything else.
READY=/tmp/tv30-redis-ready
rm -f "$READY"

redis-server --appendonly yes --appendfsync everysec &
REDIS_PID=$!
CMD_PID=""

# docker stop (dumb-init forwards SIGTERM): waits for redis-server to finish
# its shutdown (AOF) instead of exiting with the database still writing.
shutdown() {
  [ -n "$CMD_PID" ] && kill "$CMD_PID" 2>/dev/null
  kill "$REDIS_PID" 2>/dev/null
  wait "$REDIS_PID" 2>/dev/null
  exit 0
}
trap shutdown TERM INT

until redis-cli ping >/dev/null 2>&1; do
  kill -0 "$REDIS_PID" 2>/dev/null || { echo "[redis] ERROR: redis-server exited during the start"; exit 1; }
  sleep 0.2
done

# The library loads on every start, before the initial load: Redis keeps the
# functions in the AOF, and REPLACE makes the version of this image prevail
# over the one of the volume.
OUTPUT=$(sh /opt/tv30-redis/library.sh /opt/tv30-redis/schema.json /opt/tv30-redis/functions.lua \
  | redis-cli -x FUNCTION LOAD REPLACE 2>&1)
if [ "$OUTPUT" != tv30 ]; then
  echo "[redis] ERROR: the tv30 function library did NOT load: $OUTPUT"
  echo "[redis] without it every write of the services fails; container stopped"
  kill "$REDIS_PID" 2>/dev/null
  wait "$REDIS_PID"
  exit 1
fi
echo "[redis] tv30 function library loaded (schema version $(redis-cli --raw FCALL_RO tv30_version 0))"

# Initial load only on an empty database. A refusal here happens only with a
# library and a seed of different origins: without seed:done, the container
# stays unhealthy.
if [ "$(redis-cli --raw EXISTS seed:done)" = "0" ]; then
  echo "[redis] applying the initial load through the functions (redis-cli --pipe)..."
  RESULT=$(redis-cli --pipe < /seed.resp 2>&1)
  if echo "$RESULT" | grep -q '^errors: 0,'; then
    redis-cli SET seed:done 1 >/dev/null
    echo "[redis] load done ($(echo "$RESULT" | sed -n 's/^errors: 0, replies: \([0-9]*\).*/\1/p') profile(s))"
  else
    echo "[redis] ERROR: the initial load was refused by the schema:"
    echo "$RESULT" | grep -v '^All data transferred\|^Last reply received' | sed 's/^/[redis]   /'
    echo "[redis] seed:done NOT written: the container stays unhealthy (fix the userData.json of infra/user-files-template and rebuild the image)"
  fi
else
  echo "[redis] load already applied (seed:done present) — kept"
fi
[ "$(redis-cli --raw EXISTS seed:done)" = "1" ] && touch "$READY"

# Login only on the commander UI; 6379 has no password. In 0.9.0 it is not
# HTTP basic: GET / and the static files open without a credential, POST
# /signin exchanges user and password for a token, and the data routes
# answer 401 without it. HTTP_USER/HTTP_PASSWORD keep the password out of ps
# and of the log, but it is in the environment of the container (docker
# inspect). An empty password would disable the login: empty falls back to
# the default. A write through the commander does not go through the
# functions: FCALL_RO tv30_audit 0 shows it.
CMD_USER="${REDIS_COMMANDER_USER:-admin}"
CMD_PASS="${REDIS_COMMANDER_PASSWORD:-tv30-redis-admin}"
HTTP_USER="$CMD_USER" HTTP_PASSWORD="$CMD_PASS" \
  redis-commander --redis-host 127.0.0.1 --redis-port 6379 \
  --port "${COMMANDER_PORT:-18081}" >/dev/null 2>&1 &
CMD_PID=$!
echo "[redis] commander (debug) on port ${COMMANDER_PORT:-18081}, with login (user: ${CMD_USER})"

# ash has no `wait -n`: polls (the foreground `sleep` reaps the process that
# exited, and `kill -0` starts to fail). If one exits, the other is stopped;
# the watcher waits for the AOF of redis-server and exits with 1.
while :; do
  for p in "$REDIS_PID" "$CMD_PID"; do
    if ! kill -0 "$p" 2>/dev/null; then
      if [ "$p" = "$CMD_PID" ]; then WHO=commander; else WHO=redis-server; fi
      echo "[redis] $WHO (pid $p) died — bringing the whole container down"
      kill "$CMD_PID" "$REDIS_PID" 2>/dev/null
      wait "$REDIS_PID" 2>/dev/null
      exit 1
    fi
  done
  sleep 1
done
