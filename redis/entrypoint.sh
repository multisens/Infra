#!/bin/sh
# redis-server, biblioteca de funcoes tv30, carga inicial e commander (debug).
# Se o redis-server ou o commander cair, o container encerra.
set -u

# Marca do healthcheck: so existe depois de a biblioteca carregar nesta partida
# e a carga estar aplicada. O /tmp sobrevive a docker restart: sai antes de tudo.
PRONTO=/tmp/tv30-redis-pronto
rm -f "$PRONTO"

redis-server --appendonly yes --appendfsync everysec &
REDIS_PID=$!
CMD_PID=""

# docker stop (o dumb-init repassa o SIGTERM): espera o redis-server terminar o
# desligamento (AOF) em vez de sair com o banco ainda gravando.
shutdown() {
  [ -n "$CMD_PID" ] && kill "$CMD_PID" 2>/dev/null
  kill "$REDIS_PID" 2>/dev/null
  wait "$REDIS_PID" 2>/dev/null
  exit 0
}
trap shutdown TERM INT

until redis-cli ping >/dev/null 2>&1; do
  kill -0 "$REDIS_PID" 2>/dev/null || { echo "[redis] ERRO: o redis-server saiu durante a partida"; exit 1; }
  sleep 0.2
done

# A biblioteca carrega a cada partida, antes da carga inicial: o Redis guarda as
# funcoes no AOF, e o REPLACE faz valer a versao desta imagem, nao a do volume.
SAIDA=$(sh /opt/tv30-redis/biblioteca.sh /opt/tv30-redis/esquema.json /opt/tv30-redis/funcoes.lua \
  | redis-cli -x FUNCTION LOAD REPLACE 2>&1)
if [ "$SAIDA" != tv30 ]; then
  echo "[redis] ERRO: a biblioteca de funcoes tv30 NAO carregou: $SAIDA"
  echo "[redis] sem ela toda escrita dos servicos falha; container encerrado"
  kill "$REDIS_PID" 2>/dev/null
  wait "$REDIS_PID"
  exit 1
fi
echo "[redis] biblioteca de funcoes tv30 carregada (esquema versao $(redis-cli --raw FCALL_RO tv30_versao 0))"

# Carga inicial so no banco vazio. O build ja confere a carga, entao recusa aqui
# so com biblioteca e seed de origens diferentes: sem seed:done, fica unhealthy.
if [ "$(redis-cli --raw EXISTS seed:done)" = "0" ]; then
  echo "[redis] aplicando carga inicial pelas funcoes (redis-cli --pipe)..."
  RES=$(redis-cli --pipe < /seed.resp 2>&1)
  if echo "$RES" | grep -q '^errors: 0,'; then
    redis-cli SET seed:done 1 >/dev/null
    echo "[redis] carga concluida ($(echo "$RES" | sed -n 's/^errors: 0, replies: \([0-9]*\).*/\1/p') perfil(is))"
  else
    echo "[redis] ERRO: a carga inicial foi recusada pelo esquema:"
    echo "$RES" | grep -v '^All data transferred\|^Last reply received' | sed 's/^/[redis]   /'
    echo "[redis] seed:done NAO gravado: o container fica unhealthy (corrija o userData.json de infra/user-files-template e reconstrua a imagem)"
  fi
else
  echo "[redis] carga ja aplicada (seed:done presente) — mantida"
fi
[ "$(redis-cli --raw EXISTS seed:done)" = "1" ] && touch "$PRONTO"

# Login so na UI do commander; a 6379 segue sem senha. Na 0.9.0 nao e HTTP basic:
# GET / e os estaticos abrem sem credencial, POST /signin troca usuario e senha
# por token e as rotas de dados respondem 401 sem ele. HTTP_USER/HTTP_PASSWORD
# tiram a senha do ps e do log, mas o compose a poe no ambiente do container
# (docker inspect). Senha vazia desligaria o login: vazio cai no padrao.
# Escrita pelo commander nao passa pelas funcoes: FCALL_RO tv30_auditoria 0 a mostra.
CMD_USER="${REDIS_COMMANDER_USER:-admin}"
CMD_PASS="${REDIS_COMMANDER_PASSWORD:-tv30-redis-admin}"
HTTP_USER="$CMD_USER" HTTP_PASSWORD="$CMD_PASS" \
  redis-commander --redis-host 127.0.0.1 --redis-port 6379 \
  --port "${COMMANDER_PORT:-18081}" >/dev/null 2>&1 &
CMD_PID=$!
echo "[redis] commander (debug) na porta ${COMMANDER_PORT:-18081}, com login (usuario: ${CMD_USER})"

# ash nao tem `wait -n`: vigia por polling (o `sleep` em primeiro plano recolhe o
# processo que saiu, e o `kill -0` passa a falhar). Se um cair, o outro e
# encerrado; a vigia espera o AOF do redis-server e sai com 1.
while :; do
  for p in "$REDIS_PID" "$CMD_PID"; do
    if ! kill -0 "$p" 2>/dev/null; then
      if [ "$p" = "$CMD_PID" ]; then QUEM=commander; else QUEM=redis-server; fi
      echo "[redis] $QUEM (pid $p) morreu — derrubando o container inteiro (morre-inteiro)"
      kill "$CMD_PID" "$REDIS_PID" 2>/dev/null
      wait "$REDIS_PID" 2>/dev/null
      exit 1
    fi
  done
  sleep 1
done
