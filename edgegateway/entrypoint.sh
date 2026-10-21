#!/bin/sh
# Supervisor do edgegateway (dumb-init como PID 1 encaminha sinais): se
# qualquer processo cair, o container encerra por completo.
set -u

VARIANT="${EDGE_VARIANT:-linux}"

krakend run -c "/etc/krakend/krakend-external.${VARIANT}.json" &
PID_EXT=$!
krakend run -c "/etc/krakend/krakend-internal.${VARIANT}.json" &
PID_INT=$!
httpd -f -p 8085 -h /docs &
PID_DOCS=$!

PIDS="$PID_EXT $PID_INT $PID_DOCS"

echo "[edgegateway] externa=44643 interna=44642 docs=8085 (variant=${VARIANT})"

shutdown() {
  kill $PIDS 2>/dev/null
  exit 0
}
trap shutdown TERM INT

# ash nao tem `wait -n`: vigia por polling e derruba tudo se um cair.
while :; do
  for p in $PIDS; do
    if ! kill -0 "$p" 2>/dev/null; then
      echo "[edgegateway] processo $p morreu — derrubando o container inteiro"
      kill $PIDS 2>/dev/null
      exit 1
    fi
  done
  sleep 1
done
