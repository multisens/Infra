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

# D-L1 (Luis, 03/10): a UI do commander exige usuario e senha; a conexao
# com o banco (6379) continua SEM senha (requirepass intocado). No
# redis-commander 0.9.0 isso NAO e HTTP basic auth: GET / (formulario de
# login) e os estaticos abrem sem credencial; POST /signin troca
# usuario/senha por um token e as rotas de dados (/connections, /apiv1,
# /apiv2, /tools) respondem 401 sem ele. O commander recebe as credenciais
# pelas variaveis HTTP_USER/HTTP_PASSWORD que ele le
# (config/custom-environment-variables.json; equivalem a
# --http-auth-username/--http-auth-password; versao fixada no Dockerfile).
# Assim a senha fica fora da linha de comando (ps) e do log. Ela NAO fica
# so no processo do commander: o compose repassa REDIS_COMMANDER_PASSWORD em
# environment, entao a senha esta no ambiente do container inteiro (docker
# inspect, docker exec ... env). Tirar dali exige ler de arquivo ou secret,
# decisao de desenho nao tomada. O padrao repete o do compose: senha vazia
# desligaria o login, entao vazio tambem cai no padrao.
CMD_USER="${REDIS_COMMANDER_USER:-admin}"
CMD_PASS="${REDIS_COMMANDER_PASSWORD:-tv30-redis-admin}"
HTTP_USER="$CMD_USER" HTTP_PASSWORD="$CMD_PASS" \
  redis-commander --redis-host 127.0.0.1 --redis-port 6379 \
  --port "${COMMANDER_PORT:-18081}" >/dev/null 2>&1 &
echo "[redis] commander (debug) na porta ${COMMANDER_PORT:-18081}, com login (usuario: ${CMD_USER})"

trap 'kill "$REDIS_PID" 2>/dev/null' TERM INT
wait "$REDIS_PID"
