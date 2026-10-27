#!/usr/bin/env bash
# Tests of the Redis schema in disposable containers; the stack is not
# touched.
# Usage: bash infra/redis/test/run.sh [mode...]
#   (default: functions; exits != 0 if any fails)
#   functions  functions.test.js against the library on a redis:7-alpine
#   static     writes.test.js: no service writes directly to a family in
#              scope (needs the whole TV30: tv3ws/ and aop/ next to infra/)
#   contract   the real adapters of tv3ws, aop and the Go plugin against the
#              functions
#   image      image build and start: load, schema version, healthcheck, a
#              new start, and the commander exiting bringing the container
#              down
#   all        the four
set -u
INFRA=$(cd "$(dirname "$0")/../.." && pwd)
ROOT=$(cd "$INFRA/.." && pwd)
TAG="tv30-redis-test-$$"
NET="$TAG"
RDS="$TAG-redis"
IMG="tv30-redis:test-$$"
NODE_IMG=${NODE_IMG:-node:20-alpine}
REDIS_IMG=${REDIS_IMG:-redis:7-alpine}
# Go of the edge image (edgegateway/Dockerfile); krakend/builder exists only
# for amd64 and would run emulated on an ARM host, and the test does not use
# cgo, so the same Go is enough.
case "$(docker info --format '{{.Architecture}}' 2>/dev/null)" in
  aarch64|arm64) GO_IMG=${GO_IMG:-golang:1.22.7-alpine3.19} ;;
  *)             GO_IMG=${GO_IMG:-krakend/builder:2.7.2} ;;
esac
FAILED=()

cleanup() {
  docker rm -f "$RDS" "$TAG-img" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  docker image rm "$IMG" >/dev/null 2>&1
  return 0
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_ping() {
  local c=$1
  for _ in $(seq 1 50); do
    [ "$(docker exec "$c" redis-cli ping 2>/dev/null)" = PONG ] && return 0
    sleep 0.2
  done
  echo "ERROR: $c did not answer the PING"; return 1
}

# A clean redis:7-alpine with the library loaded by library.sh.
start_redis() {
  docker rm -f "$RDS" >/dev/null 2>&1
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  docker run -d --name "$RDS" --network "$NET" "$REDIS_IMG" >/dev/null || return 1
  wait_ping "$RDS" || return 1
  local out
  out=$(sh "$INFRA/redis/library.sh" | docker exec -i "$RDS" redis-cli -x FUNCTION LOAD REPLACE)
  [ "$out" = tv30 ] || { echo "ERROR: FUNCTION LOAD answered: $out"; return 1; }
  echo "tv30 library loaded in $RDS ($(docker exec "$RDS" redis-server --version | cut -d' ' -f3))"
}

mode_functions() {
  echo; echo "== functions: infra/redis/test/functions.test.js =="
  start_redis || return 1
  docker run --rm --network "$NET" -e REDIS_HOST="$RDS" \
    -v "$INFRA/redis:/redis:ro" -w /redis/test "$NODE_IMG" node --test functions.test.js
}

mode_static() {
  echo; echo "== static: infra/redis/test/writes.test.js =="
  [ -d "$ROOT/tv3ws/src" ] && [ -d "$ROOT/aop/src" ] || { echo "ERROR: needs the whole TV30 (tv3ws/ and aop/ next to infra/)"; return 1; }
  docker run --rm -v "$ROOT:/tv30:ro" -w /tv30/infra/redis/test "$NODE_IMG" node --test writes.test.js
}

mode_contract() {
  echo; echo "== contract: real adapters x functions =="
  [ -d "$ROOT/tv3ws/node_modules" ] && [ -d "$ROOT/aop/node_modules" ] \
    || { echo "ERROR: needs tv3ws/node_modules and aop/node_modules (npm install in both)"; return 1; }
  start_redis || return 1
  docker run --rm --network "$NET" -e REDIS_HOST="$RDS" -e REDIS_PORT=6379 \
    -v "$ROOT:/tv30:ro" -w /tv30/infra/redis/test "$NODE_IMG" node --test contract.test.js || return 1
  echo; echo "-- edge plugin (Go): redisStore x functions --"
  docker run --rm --network "$NET" -e TV30_REDIS_TEST="$RDS:6379" -e GOCACHE=/tmp/gocache \
    -v "$INFRA/edgegateway/plugin:/src:ro" -w /src "$GO_IMG" \
    sh -c 'go test -count=1 -run TestContractFunctions -v . >/tmp/go.out 2>&1; rc=$?; grep -E "^(=== RUN|--- |PASS|FAIL|ok|panic)|contract_test" /tmp/go.out; exit $rc' || return 1
  local findings
  findings=$(docker exec "$RDS" redis-cli FCALL_RO tv30_audit 0)
  [ -z "$findings" ] || { echo "FAIL: audit after the contract of the plugin: $findings"; return 1; }
  echo "empty audit after the contract of the plugin"
}

mode_image() {
  echo; echo "== image: build of infra/redis/Dockerfile and start =="
  docker build -q -t "$IMG" -f "$INFRA/redis/Dockerfile" "$INFRA" >/dev/null || { echo "FAIL: build of the image"; return 1; }
  echo "image $IMG built (the build already checks library, load and audit)"
  local C="$TAG-img" s ok=1 hc
  # The same healthcheck as infra/redis/docker-compose.yml, read from there.
  hc=$(sed -n 's/^ *test: \["CMD-SHELL", "\(.*\)"\]$/\1/p' "$INFRA/redis/docker-compose.yml" | sed 's/\$\$/$/g; s/\\"/"/g')
  [ -n "$hc" ] || { echo "FAIL: healthcheck not found in docker-compose.yml"; return 1; }
  echo "healthcheck of the compose: $hc"
  docker rm -f "$C" >/dev/null 2>&1
  docker run -d --name "$C" --health-cmd "$hc" --health-interval 1s --health-retries 60 "$IMG" >/dev/null || return 1
  for _ in $(seq 1 60); do
    s=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$C" 2>/dev/null)
    [ "$s" = healthy ] && break
    sleep 1
  done
  [ "$s" = healthy ] && echo "PASS  healthcheck healthy after the load" || { echo "FAIL healthcheck: '$s'"; docker logs "$C" | tail -20; ok=0; }
  local logs; logs=$(docker logs "$C" 2>&1)
  grep -q 'tv30 function library loaded' <<<"$logs" && echo "PASS  log: library loaded" || { echo "FAIL log without 'tv30 function library loaded'"; ok=0; }
  grep -q 'load done' <<<"$logs" && echo "PASS  log: load done" || { echo "FAIL log without 'load done'"; ok=0; }
  local n; n=$(docker exec "$C" redis-cli --raw SCARD users:index)
  [ "${n:-0}" -ge 1 ] && echo "PASS  initial load wrote $n profile(s) through the functions" || { echo "FAIL users:index empty"; ok=0; }
  # Id of the first profile of the load, read from the template.
  local P; P=$(sed -n 's/^[[:space:]]*"id": "\([^"]*\)".*/\1/p' "$INFRA/user-files-template/userData.json" | head -1)
  [[ "$P" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] && [ "$(docker exec "$C" redis-cli --raw SISMEMBER users:index "$P")" = 1 ] \
    && echo "PASS  profile of the load with a strict lowercase UUID id ($P), in users:index" || { echo "FAIL id of the load '$P' (strict UUID in users:index?)"; ok=0; }
  local b; b=$(docker exec "$C" redis-cli --raw HGET "user:$P" closedCaptioning)
  [ "$b" = false ] && echo "PASS  boolean of the load written as 'false'" || { echo "FAIL closedCaptioning of the load = '$b'"; ok=0; }
  # Avatar of the load: the name of a file the template has in thumbs/.
  local av f; av=$(docker exec "$C" redis-cli --raw HGET "user:$P" avatar)
  f="$INFRA/user-files-template/thumbs/$av"
  [ "$av" = "$P.svg" ] && [ -f "$f" ] && [ "$(head -c 4 "$f")" = '<svg' ] \
    && echo "PASS  avatar of the load = name of the file ($av), with the SVG in user-files-template/thumbs" || { echo "FAIL avatar of the load '$av' (expected $P.svg, with the SVG in user-files-template/thumbs)"; ok=0; }
  local v schema; v=$(docker exec "$C" redis-cli --raw FCALL_RO tv30_version 0)
  schema=$(sed -n 's/^ *"version": "\([^"]*\)".*/\1/p' "$INFRA/redis/schema.json")
  [ -n "$schema" ] && [ "$v" = "$schema" ] && echo "PASS  image with the schema version $v (the one of schema.json)" || { echo "FAIL schema version in the image '$v', in schema.json '$schema'"; ok=0; }
  local a; a=$(docker exec "$C" redis-cli FCALL_RO tv30_audit 0)
  [ -z "$a" ] && echo "PASS  empty audit after the load" || { echo "FAIL audit: $a"; ok=0; }
  # New start: library reloaded (REPLACE), load kept.
  docker exec "$C" redis-cli FCALL tv30_profile_last_access 0 "$P" 4242 >/dev/null
  docker restart "$C" >/dev/null
  for _ in $(seq 1 60); do
    s=$(docker inspect -f '{{.State.Health.Status}}' "$C" 2>/dev/null)
    [ "$s" = healthy ] && break
    sleep 1
  done
  logs=$(docker logs --since 0 "$C" 2>&1)
  [ "$(grep -c 'tv30 function library loaded' <<<"$logs")" -ge 2 ] && echo "PASS  new start reloaded the library" || { echo "FAIL library not reloaded on the new start"; ok=0; }
  grep -q 'load already applied' <<<"$logs" && echo "PASS  new start kept the load (seed:done)" || { echo "FAIL new start without 'load already applied'"; ok=0; }
  [ "$(docker exec "$C" redis-cli --raw HGET "user:$P" lastAccess)" = 4242 ] && echo "PASS  data written before the new start stays (AOF)" || { echo "FAIL lastAccess lost"; ok=0; }
  [ "$s" = healthy ] && echo "PASS  healthy again" || { echo "FAIL healthcheck after the new start: $s"; ok=0; }
  # The commander exiting brings the container down (exit 1), with the
  # message in the log.
  local cpid state
  cpid=$(docker exec "$C" sh -c "ps -o pid,args | grep '[r]edis-commander' | awk '{print \$1}' | head -1")
  if [ -n "$cpid" ]; then
    docker exec "$C" kill -9 "$cpid"
    for _ in $(seq 1 15); do
      [ "$(docker inspect -f '{{.State.Running}}' "$C")" = false ] && break
      sleep 1
    done
    state=$(docker inspect -f '{{.State.Running}} exit={{.State.ExitCode}}' "$C")
    [ "$state" = "false exit=1" ] && echo "PASS  death of the commander brought the container down (exit 1)" || { echo "FAIL death of the commander: state '$state' (expected 'false exit=1')"; ok=0; }
    grep -q "commander (pid $cpid) died" <<<"$(docker logs "$C" 2>&1)" && echo "PASS  log: commander died — bringing the whole container down" || { echo "FAIL log without the death of the commander"; ok=0; }
  else
    echo "FAIL process of the commander not found in the container"; ok=0
  fi
  [ "$ok" = 1 ]
}

MODES=("$@")
[ ${#MODES[@]} -eq 0 ] && MODES=(functions)
[ "${MODES[0]}" = all ] && MODES=(static functions contract image)
for m in "${MODES[@]}"; do
  case "$m" in
    functions|static|contract|image) "mode_$m" || FAILED+=("$m") ;;
    *) echo "unknown mode: $m (functions, static, contract, image, all)"; exit 2 ;;
  esac
done
echo
if [ ${#FAILED[@]} -eq 0 ]; then echo "== OK: ${MODES[*]} =="; exit 0; fi
echo "== FAILED: ${FAILED[*]} =="; exit 1
