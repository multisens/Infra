#!/bin/bash
# Teste de integracao: a mensagem que o plugin publica em errors/<client_id>
# e sempre JSON valido (correcao de 12/10, defeito achado pela auditoria da
# documentacao: publish_error colava os campos sem escape, e uma mensagem de
# 2048 bytes ou mais nao saia). Desde as correcoes de 12/10 da revisao do
# plugin, tambem a leitura do payload inteiro na validacao, o topico
# errors/<client_id> e a resposta do teste de latencia (ver o driver).
#
# Constroi a imagem do broker com tag PROPRIA (o build ja roda os testes de
# unidade tests/test_error_json.c e tests/test_json_payload.c), sobe um
# broker DESCARTAVEL numa rede propria, sem portas publicadas, e roda
# errors_json_driver.py num segundo container da mesma imagem. Nao toca a
# stack (nem o container mqtt-broker).
# No fim remove o container, a rede e a imagem que construiu.
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
