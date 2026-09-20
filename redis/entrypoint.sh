#!/bin/sh
# Redis consolidado: server (principal) + carga inicial + commander (debug).
# Morte do commander NAO derruba o banco; morte do redis derruba tudo.
set -u

redis-server --appendonly yes --appendfsync everysec &
REDIS_PID=$!

until redis-cli ping >/dev/null 2>&1; do sleep 0.2; done

if [ "$(redis-cli --raw EXISTS seed:done)" = "0" ]; then
  echo "[redis] aplicando carga inicial (redis-cli --pipe)..."
  redis-cli --pipe < /seed.resp
  redis-cli SET seed:done 1 >/dev/null
  echo "[redis] carga concluida"
else
  echo "[redis] carga ja aplicada (seed:done presente) — mantida"
fi

redis-commander --redis-host 127.0.0.1 --redis-port 6379 \
  --port "${COMMANDER_PORT:-18081}" >/dev/null 2>&1 &
echo "[redis] commander (debug) na porta ${COMMANDER_PORT:-18081}"

trap 'kill "$REDIS_PID" 2>/dev/null' TERM INT
wait "$REDIS_PID"
