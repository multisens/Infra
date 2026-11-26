#!/bin/sh
# Supervisor of the edgegateway (dumb-init as PID 1 forwards signals): if any
# process exits, the whole container exits.
set -u

VARIANT="${EDGE_VARIANT:-linux}"
TLS_DIR=/etc/tv30/tls

# Receiver certificate: generated only if missing; a failure stops the
# container before the surfaces start.
TLS_DIR="$TLS_DIR" tv30-tls-init || { echo "[edgegateway] ERROR: the receiver certificate was not generated in $TLS_DIR"; exit 1; }

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

# ash has no `wait -n`: polls, and brings everything down if one exits.
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
