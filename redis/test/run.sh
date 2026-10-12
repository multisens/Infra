#!/usr/bin/env bash
# Testes do esquema do Redis (P4, decisao do Luis em 10/10: "Funcoes +
# auditoria") em containers DESCARTAVEIS, com nome proprio (nao tocam a
# stack: nada de container "redis", nem rede ginga_net, nem o volume dela).
#
# Uso (Linux/WSL, com docker):  bash infra/redis/test/run.sh [modo...]
#   funcoes   (padrao) biblioteca num redis:7-alpine + infra/redis/test/
#             funcoes.test.js num node:20-alpine: aceitacao, recusa de cada
#             regra do esquema.json, atomicidade, invariantes e auditoria
#   estatico  infra/redis/test/escritas.test.js: nenhum servico (tv3ws, aop,
#             plugin da borda, carga inicial) grava direto numa familia do
#             alcance (precisa do TV30 inteiro: tv3ws/ e aop/ ao lado de infra/)
#   contrato  os ADAPTADORES de verdade (tv3ws/src/redis-funcoes.ts,
#             aop/src/redis-funcoes.js, o gestor de perfis do AoP, o
#             auth-manager do tv3ws e o redisStore do plugin Go) contra as
#             funcoes num Redis real; a auditoria tem de terminar vazia. O
#             gestor de perfis grava o avatar em thumbs/ de um USER_DATA_PATH
#             temporario do container do teste (G8, Luis 12/10)
#   imagem    constroi a imagem do redis (infra/redis/Dockerfile) com tag de
#             teste, sobe um container dela e confere: funcoes carregadas,
#             versao do esquema.json, carga inicial pelas funcoes (id do
#             perfil em UUID, F-3; avatar = nome do arquivo que o template
#             traz em thumbs/, G8), auditoria vazia, healthcheck, que uma
#             nova partida recarrega a biblioteca e nao refaz a carga, e que
#             a morte do commander derruba o container (morre-inteiro, C2)
#   tudo      os quatro
# Sai com codigo != 0 se algum falhar. No fim (trap) remove containers, rede
# e a imagem de teste.
set -u
INFRA=$(cd "$(dirname "$0")/../.." && pwd)
ROOT=$(cd "$INFRA/.." && pwd)
TAG="tv30-redis-teste-$$"
NET="$TAG"
RDS="$TAG-redis"
IMG="tv30-redis:teste-$$"
NODE_IMG=${NODE_IMG:-node:20-alpine}
REDIS_IMG=${REDIS_IMG:-redis:7-alpine}
GO_IMG=${GO_IMG:-krakend/builder:2.7.2}
FALHAS=()

cleanup() {
  docker rm -f "$RDS" "$TAG-img" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  docker image rm "$IMG" >/dev/null 2>&1
  return 0
}
trap cleanup EXIT
trap 'exit 130' INT TERM

espera_ping() {
  local c=$1
  for _ in $(seq 1 50); do
    [ "$(docker exec "$c" redis-cli ping 2>/dev/null)" = PONG ] && return 0
    sleep 0.2
  done
  echo "ERRO: $c nao respondeu ao PING"; return 1
}

# redis:7-alpine limpo com a biblioteca carregada por biblioteca.sh
sobe_redis() {
  docker rm -f "$RDS" >/dev/null 2>&1
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  docker run -d --name "$RDS" --network "$NET" "$REDIS_IMG" >/dev/null || return 1
  espera_ping "$RDS" || return 1
  local out
  out=$(sh "$INFRA/redis/biblioteca.sh" | docker exec -i "$RDS" redis-cli -x FUNCTION LOAD REPLACE)
  [ "$out" = tv30 ] || { echo "ERRO: FUNCTION LOAD respondeu: $out"; return 1; }
  echo "biblioteca tv30 carregada em $RDS ($(docker exec "$RDS" redis-server --version | cut -d' ' -f3))"
}

modo_funcoes() {
  echo; echo "== funcoes: infra/redis/test/funcoes.test.js =="
  sobe_redis || return 1
  docker run --rm --network "$NET" -e REDIS_HOST="$RDS" \
    -v "$INFRA/redis:/redis:ro" -w /redis/test "$NODE_IMG" node --test funcoes.test.js
}

modo_estatico() {
  echo; echo "== estatico: infra/redis/test/escritas.test.js =="
  [ -d "$ROOT/tv3ws/src" ] && [ -d "$ROOT/aop/src" ] || { echo "ERRO: precisa do TV30 inteiro (tv3ws/ e aop/ ao lado de infra/)"; return 1; }
  docker run --rm -v "$ROOT:/tv30:ro" -w /tv30/infra/redis/test "$NODE_IMG" node --test escritas.test.js
}

modo_contrato() {
  echo; echo "== contrato: adaptadores de verdade x funcoes =="
  [ -d "$ROOT/tv3ws/node_modules" ] && [ -d "$ROOT/aop/node_modules" ] \
    || { echo "ERRO: precisa de tv3ws/node_modules e aop/node_modules (npm install nos dois)"; return 1; }
  sobe_redis || return 1
  docker run --rm --network "$NET" -e REDIS_HOST="$RDS" -e REDIS_PORT=6379 \
    -v "$ROOT:/tv30:ro" -w /tv30/infra/redis/test "$NODE_IMG" node --test contrato.test.js || return 1
  echo; echo "-- plugin da borda (Go): redisStore x funcoes --"
  docker run --rm --network "$NET" -e TV30_REDIS_TESTE="$RDS:6379" -e GOCACHE=/tmp/gocache \
    -v "$INFRA/edgegateway/plugin:/src:ro" -w /src "$GO_IMG" \
    sh -c 'go test -count=1 -run TestContratoFuncoes -v . >/tmp/go.out 2>&1; rc=$?; grep -E "^(=== RUN|--- |PASS|FAIL|ok|panic)|contrato_test" /tmp/go.out; exit $rc' || return 1
  local achados
  achados=$(docker exec "$RDS" redis-cli FCALL_RO tv30_auditoria 0)
  [ -z "$achados" ] || { echo "FALHA: auditoria depois do contrato do plugin: $achados"; return 1; }
  echo "auditoria vazia depois do contrato do plugin"
}

modo_imagem() {
  echo; echo "== imagem: build de infra/redis/Dockerfile e partida =="
  docker build -q -t "$IMG" -f "$INFRA/redis/Dockerfile" "$INFRA" >/dev/null || { echo "FALHA: build da imagem"; return 1; }
  echo "imagem $IMG construida (o build ja confere biblioteca, carga e auditoria)"
  local C="$TAG-img" s ok=1 hc
  # o MESMO healthcheck do infra/redis/docker-compose.yml (lido de la)
  hc=$(sed -n 's/^ *test: \["CMD-SHELL", "\(.*\)"\]$/\1/p' "$INFRA/redis/docker-compose.yml" | sed 's/\$\$/$/g; s/\\"/"/g')
  [ -n "$hc" ] || { echo "FALHA: healthcheck nao encontrado no docker-compose.yml"; return 1; }
  echo "healthcheck do compose: $hc"
  docker rm -f "$C" >/dev/null 2>&1
  docker run -d --name "$C" --health-cmd "$hc" --health-interval 1s --health-retries 60 "$IMG" >/dev/null || return 1
  for _ in $(seq 1 60); do
    s=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$C" 2>/dev/null)
    [ "$s" = healthy ] && break
    sleep 1
  done
  [ "$s" = healthy ] && echo "PASS  healthcheck saudavel depois da carga" || { echo "FALHA healthcheck: '$s'"; docker logs "$C" | tail -20; ok=0; }
  local logs; logs=$(docker logs "$C" 2>&1)
  grep -q 'biblioteca de funcoes tv30 carregada' <<<"$logs" && echo "PASS  log: biblioteca carregada" || { echo "FALHA log sem 'biblioteca de funcoes tv30 carregada'"; ok=0; }
  grep -q 'carga concluida' <<<"$logs" && echo "PASS  log: carga concluida" || { echo "FALHA log sem 'carga concluida'"; ok=0; }
  local n; n=$(docker exec "$C" redis-cli --raw SCARD users:index)
  [ "${n:-0}" -ge 1 ] && echo "PASS  carga inicial gravou $n perfil(is) pelas funcoes" || { echo "FALHA users:index vazio"; ok=0; }
  # id do primeiro perfil da carga, lido do template (F-3, Luis 11/10: UUID;
  # UUID estrito desde 12/10: versao 1 a 8 e variante 8, 9, a ou b)
  local P; P=$(sed -n 's/^[[:space:]]*"id": "\([^"]*\)".*/\1/p' "$INFRA/user-files-template/userData.json" | head -1)
  [[ "$P" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] && [ "$(docker exec "$C" redis-cli --raw SISMEMBER users:index "$P")" = 1 ] \
    && echo "PASS  perfil da carga com id UUID estrito em minusculas ($P), em users:index (F-3; 12/10)" || { echo "FALHA id da carga '$P' (UUID estrito em users:index?)"; ok=0; }
  local b; b=$(docker exec "$C" redis-cli --raw HGET "user:$P" closedCaptioning)
  [ "$b" = false ] && echo "PASS  booleano da carga gravado como 'false' (antes: 'False')" || { echo "FALHA closedCaptioning da carga = '$b'"; ok=0; }
  # G8 (decisao do Luis em 12/10, esquema versao 4): o avatar da carga e o nome
  # de um arquivo do diretorio de miniaturas, e o template traz o arquivo (o
  # userfiles-seed o copia para ./user-files/thumbs); ate 12/10, SVG em linha
  local av f; av=$(docker exec "$C" redis-cli --raw HGET "user:$P" avatar)
  f="$INFRA/user-files-template/thumbs/$av"
  [ "$av" = "$P.svg" ] && [ -f "$f" ] && [ "$(head -c 4 "$f")" = '<svg' ] \
    && echo "PASS  avatar da carga = nome do arquivo ($av), com o SVG em user-files-template/thumbs (G8)" || { echo "FALHA avatar da carga '$av' (esperado $P.svg, com o SVG em user-files-template/thumbs)"; ok=0; }
  local v esq; v=$(docker exec "$C" redis-cli --raw FCALL_RO tv30_versao 0)
  esq=$(sed -n 's/^ *"versao": "\([^"]*\)".*/\1/p' "$INFRA/redis/esquema.json")
  [ -n "$esq" ] && [ "$v" = "$esq" ] && echo "PASS  imagem com o esquema versao $v (o do esquema.json)" || { echo "FALHA versao do esquema na imagem '$v', no esquema.json '$esq'"; ok=0; }
  local a; a=$(docker exec "$C" redis-cli FCALL_RO tv30_auditoria 0)
  [ -z "$a" ] && echo "PASS  auditoria vazia depois da carga" || { echo "FALHA auditoria: $a"; ok=0; }
  # nova partida: biblioteca recarregada (REPLACE), carga mantida
  docker exec "$C" redis-cli FCALL tv30_perfil_ultimo_acesso 0 "$P" 4242 >/dev/null
  docker restart "$C" >/dev/null
  for _ in $(seq 1 60); do
    s=$(docker inspect -f '{{.State.Health.Status}}' "$C" 2>/dev/null)
    [ "$s" = healthy ] && break
    sleep 1
  done
  logs=$(docker logs --since 0 "$C" 2>&1)
  [ "$(grep -c 'biblioteca de funcoes tv30 carregada' <<<"$logs")" -ge 2 ] && echo "PASS  nova partida recarregou a biblioteca" || { echo "FALHA biblioteca nao recarregada na nova partida"; ok=0; }
  grep -q 'carga ja aplicada' <<<"$logs" && echo "PASS  nova partida manteve a carga (seed:done)" || { echo "FALHA nova partida sem 'carga ja aplicada'"; ok=0; }
  [ "$(docker exec "$C" redis-cli --raw HGET "user:$P" lastAccess)" = 4242 ] && echo "PASS  dado gravado antes da nova partida continua (AOF)" || { echo "FALHA lastAccess perdido"; ok=0; }
  [ "$s" = healthy ] && echo "PASS  saudavel de novo" || { echo "FALHA healthcheck depois da nova partida: $s"; ok=0; }
  # C2 (decisao do Luis em 11/10): morre-inteiro tambem para o commander — a
  # morte dele derruba o container (exit 1), com a mensagem no log
  local cpid est
  cpid=$(docker exec "$C" sh -c "ps -o pid,args | grep '[r]edis-commander' | awk '{print \$1}' | head -1")
  if [ -n "$cpid" ]; then
    docker exec "$C" kill -9 "$cpid"
    for _ in $(seq 1 15); do
      [ "$(docker inspect -f '{{.State.Running}}' "$C")" = false ] && break
      sleep 1
    done
    est=$(docker inspect -f '{{.State.Running}} exit={{.State.ExitCode}}' "$C")
    [ "$est" = "false exit=1" ] && echo "PASS  morte do commander derrubou o container (exit 1, C2)" || { echo "FALHA morte do commander: estado '$est' (esperado 'false exit=1')"; ok=0; }
    grep -q "commander (pid $cpid) morreu" <<<"$(docker logs "$C" 2>&1)" && echo "PASS  log: commander morreu — derrubando o container inteiro" || { echo "FALHA log sem a morte do commander"; ok=0; }
  else
    echo "FALHA processo do commander nao encontrado no container"; ok=0
  fi
  [ "$ok" = 1 ]
}

MODOS=("$@")
[ ${#MODOS[@]} -eq 0 ] && MODOS=(funcoes)
[ "${MODOS[0]}" = tudo ] && MODOS=(estatico funcoes contrato imagem)
for m in "${MODOS[@]}"; do
  case "$m" in
    funcoes|estatico|contrato|imagem) "modo_$m" || FALHAS+=("$m") ;;
    *) echo "modo desconhecido: $m (funcoes, estatico, contrato, imagem, tudo)"; exit 2 ;;
  esac
done
echo
if [ ${#FALHAS[@]} -eq 0 ]; then echo "== OK: ${MODOS[*]} =="; exit 0; fi
echo "== FALHOU: ${FALHAS[*]} =="; exit 1
