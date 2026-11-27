#!/bin/bash
# Integration test of the plugin (errors_json_driver.py): message and topic
# of errors/<client_id>, reading of the whole payload and reply of the
# latency test. An image with its own tag and a disposable broker on its own
# network; the stack is not touched.
#
#   bash infra/mqtt-broker/plugin/tests/test_errors_json.sh
#   IMG=<image> NOBUILD=1 bash ...
#       uses an image already built, and does not remove it
#   KEEP_IMG=1 bash ...
#       does not remove the built image
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
# infra/mqtt-broker
BROKER_DIR=$(cd "$HERE/../.." && pwd)
IMG=${IMG:-tv30-mqtt-broker:teste-errors-json}
P=${PREFIX:-tv30-teste-errors-json}
NET=$P-net
BRK=$P-broker
LOG=$(mktemp)

cleanup() {
  docker rm -f "$BRK" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  if [ -z "${NOBUILD:-}" ] && [ -z "${KEEP_IMG:-}" ]; then docker image rm "$IMG" >/dev/null 2>&1; fi
  rm -f "$LOG" "${SCHEMAS:-}"
}
trap cleanup EXIT
docker rm -f "$BRK" >/dev/null 2>&1
docker network rm "$NET" >/dev/null 2>&1

if [ -z "${NOBUILD:-}" ]; then
  echo "== build $IMG (inclui os testes de unidade test_error_json e test_json_payload)"
  if ! DOCKER_BUILDKIT=1 docker build --progress=plain -t "$IMG" -f "$BROKER_DIR/infra/Dockerfile" "$BROKER_DIR" >"$LOG" 2>&1; then
    tail -40 "$LOG"
    echo "FAIL build da imagem"
    exit 1
  fi
  # Output of the unit tests in the build; without it, the step came from
  # the cache.
  grep -E "^#[0-9]+ [0-9.]+ (test_error_json:|test_json_payload:|python3 json:)" "$LOG" | sed 's/^#[0-9]* [0-9.]* //' | grep . \
    || echo "(passo dos testes de unidade em cache no build: o mesmo codigo ja passou antes)"
fi

# Schemas of the disposable broker: those of the image plus the test topics
# (test-schemas.json), mounted over /mosquitto/config/schemas.json.
SCHEMAS=$(mktemp)
python3 - "$BROKER_DIR/plugin/config/schemas.json" "$HERE/test-schemas.json" >"$SCHEMAS" <<'PY'
import json, sys
merged = json.load(open(sys.argv[1]))
merged.update(json.load(open(sys.argv[2])))
print(json.dumps(merged))
PY
chmod 644 "$SCHEMAS"
docker network create "$NET" >/dev/null
docker run -d --name "$BRK" --network "$NET" -v "$SCHEMAS":/mosquitto/config/schemas.json:ro "$IMG" >/dev/null
for _ in $(seq 1 50); do
  docker logs "$BRK" 2>&1 | grep -q "mosquitto version .* running" && break
  sleep 0.2
done
docker logs "$BRK" 2>&1 | grep -E "Schemas loaded|plugin loaded|running" | head -3

docker run --rm --network "$NET" -v "$HERE":/t:ro --entrypoint python3 "$IMG" /t/errors_json_driver.py "$BRK"
rc=$?
echo "recusas registradas no log do broker: $(docker logs "$BRK" 2>&1 | grep -c 'Validation FAILED for topic')"

# SCHEMA_ENFORCE=warn: the topic without a schema is delivered, with the
# message on errors/<client_id>.
docker rm -f "$BRK" >/dev/null 2>&1
docker run -d --name "$BRK" --network "$NET" -e SCHEMA_ENFORCE=warn "$IMG" >/dev/null
for _ in $(seq 1 50); do
  docker logs "$BRK" 2>&1 | grep -q "mosquitto version .* running" && break
  sleep 0.2
done
WARN_OUT=$(docker run --rm --network "$NET" --entrypoint sh "$IMG" -c "
  mosquitto_sub -h $BRK -t 'zz/sem-esquema/#' -t 'errors/#' -F '%t %p' -W 4 &
  sleep 1
  mosquitto_pub -h $BRK -i tv30-t-warn -t zz/sem-esquema/w -m '{\"a\":1}'
  wait" 2>/dev/null)
if grep -q '^zz/sem-esquema/w {"a":1}$' <<<"$WARN_OUT" && grep -q '^errors/tv30-t-warn .*"error_type":"NO_SCHEMA"' <<<"$WARN_OUT"; then
  echo "PASS warn: topico sem esquema entregue, com a mensagem em errors/tv30-t-warn"
else
  echo "FAIL warn: topico sem esquema  -- $WARN_OUT"
  rc=1
fi

# An invalid value of SCHEMA_ENFORCE does not start the broker.
docker rm -f "$BRK" >/dev/null 2>&1
docker run -d --name "$BRK" --network "$NET" -e SCHEMA_ENFORCE=talvez "$IMG" >/dev/null
for _ in $(seq 1 50); do
  [ "$(docker inspect -f '{{.State.Running}}' "$BRK" 2>/dev/null)" = "false" ] && break
  sleep 0.2
done
if [ "$(docker inspect -f '{{.State.Running}}' "$BRK")" = "false" ] && docker logs "$BRK" 2>&1 | grep -q 'SCHEMA_ENFORCE=talvez invalid'; then
  echo "PASS SCHEMA_ENFORCE invalido: o broker nao sobe"
else
  echo "FAIL SCHEMA_ENFORCE invalido  -- $(docker logs "$BRK" 2>&1 | tail -3)"
  rc=1
fi

# A schemas file that is not a JSON object does not start the broker.
BAD=$(mktemp)
echo '{nao e json' >"$BAD"
chmod 644 "$BAD"
docker rm -f "$BRK" >/dev/null 2>&1
docker run -d --name "$BRK" --network "$NET" -v "$BAD":/mosquitto/config/schemas.json:ro "$IMG" >/dev/null
for _ in $(seq 1 50); do
  [ "$(docker inspect -f '{{.State.Running}}' "$BRK" 2>/dev/null)" = "false" ] && break
  sleep 0.2
done
if [ "$(docker inspect -f '{{.State.Running}}' "$BRK")" = "false" ] && docker logs "$BRK" 2>&1 | grep -q 'Invalid JSON in schemas file'; then
  echo "PASS schemas.json invalido: o broker nao sobe"
else
  echo "FAIL schemas.json invalido  -- $(docker logs "$BRK" 2>&1 | tail -3)"
  rc=1
fi
rm -f "$BAD"
exit $rc
