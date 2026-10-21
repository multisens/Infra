#!/bin/bash
# Teste de integracao do plugin (errors_json_driver.py): mensagem e topico de
# errors/<client_id>, leitura do payload inteiro e resposta do teste de latencia.
# Imagem com tag propria e broker descartavel numa rede propria; nao toca a stack.
#
#   bash infra/mqtt-broker/plugin/tests/test_errors_json.sh
#   IMG=<imagem> NOBUILD=1 bash ...   usa uma imagem ja construida (e nao a remove)
#   KEEP_IMG=1 bash ...               nao remove a imagem construida
set -u
AQUI=$(cd "$(dirname "$0")" && pwd)
BROKER_DIR=$(cd "$AQUI/../.." && pwd)   # infra/mqtt-broker
IMG=${IMG:-tv30-mosquitto:teste-errors-json}
P=${PREFIXO:-tv30-teste-errors-json}
NET=$P-net
BRK=$P-broker
LOG=$(mktemp)

cleanup() {
  docker rm -f "$BRK" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  if [ -z "${NOBUILD:-}" ] && [ -z "${KEEP_IMG:-}" ]; then docker image rm "$IMG" >/dev/null 2>&1; fi
  rm -f "$LOG"
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
  # saida do teste de unidade no build; sem ela, o passo veio do cache (o
  # mesmo codigo ja passou num build anterior)
  grep -E "^#[0-9]+ [0-9.]+ (test_error_json:|test_json_payload:|python3 json:)" "$LOG" | sed 's/^#[0-9]* [0-9.]* //' | grep . \
    || echo "(passo dos testes de unidade em cache no build: o mesmo codigo ja passou antes)"
fi

docker network create "$NET" >/dev/null
docker run -d --name "$BRK" --network "$NET" "$IMG" >/dev/null
for _ in $(seq 1 50); do
  docker logs "$BRK" 2>&1 | grep -q "mosquitto version .* running" && break
  sleep 0.2
done
docker logs "$BRK" 2>&1 | grep -E "Schemas loaded|plugin loaded|running" | head -3

docker run --rm --network "$NET" -v "$AQUI":/t:ro --entrypoint python3 "$IMG" /t/errors_json_driver.py "$BRK"
rc=$?
echo "recusas registradas no log do broker: $(docker logs "$BRK" 2>&1 | grep -c 'Validation FAILED for topic')"
exit $rc
