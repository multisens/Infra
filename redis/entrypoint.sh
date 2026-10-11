#!/bin/sh
# Redis consolidado: server (principal) + biblioteca de funcoes (P4) + carga
# inicial + commander (debug). MORRE-INTEIRO para os dois processos (C2,
# decisao do Luis em 11/10): se o redis-server OU o commander cair, o
# container encerra por completo (o restart do compose o traz de volta). Ate
# 11/10 o commander era excecao (decisao V8/V9: a morte dele nao derrubava o
# banco).
set -u

# Marca de "pronto" do healthcheck (docker-compose.yml): so existe depois de
# a biblioteca carregar NESTA partida e de a carga inicial estar aplicada. Fica
# no sistema de arquivos do container, que sobrevive a docker restart: por
# isso sai antes de tudo.
PRONTO=/tmp/tv30-redis-pronto
rm -f "$PRONTO"

redis-server --appendonly yes --appendfsync everysec &
REDIS_PID=$!
CMD_PID=""

# Parada pedida (docker stop: o dumb-init encaminha o SIGTERM): para o
# commander e espera o redis-server terminar o desligamento dele (AOF), em vez
# de sair com o banco ainda gravando.
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

# P4 (decisao do Luis em 10/10, "Funcoes + auditoria"): toda escrita do
# codigo do projeto nas familias do alcance passa pelas funcoes da biblioteca
# tv30, que conferem o esquema (infra/redis/esquema.json, embutido por
# biblioteca.sh) e gravam de forma atomica. Carregada a CADA partida com
# FUNCTION LOAD REPLACE e ANTES da carga inicial (que tambem passa por elas):
# o Redis guarda as funcoes no AOF, e o REPLACE garante que vale a versao
# desta imagem, nao a que ficou no volume. Sem a biblioteca, toda escrita dos
# servicos falharia ("ERR Function not found"): o container encerra com a
# mensagem do Redis.
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

# Carga inicial: so no banco vazio (sem seed:done). O seed.resp (gerado no
# build por emit_seed.py) chama tv30_perfil_grava para cada perfil do
# userData.json; perfil fora do esquema e recusado pela funcao. O build ja
# confere a carga (confere-imagem.sh), entao recusa aqui so acontece com
# biblioteca e seed de origens diferentes; nesse caso seed:done NAO e
# gravado e o container fica unhealthy, com os erros no log.
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
# P4: escrita pelo commander (ou pelo redis-cli) NAO passa pelas funcoes e
# continua possivel — o Redis aplica o ACL de quem chama dentro da funcao, entao
# nao ha como liberar so a funcao. A auditoria mostra o que ficou fora do
# esquema: redis-cli FCALL_RO tv30_auditoria 0.
CMD_USER="${REDIS_COMMANDER_USER:-admin}"
CMD_PASS="${REDIS_COMMANDER_PASSWORD:-tv30-redis-admin}"
HTTP_USER="$CMD_USER" HTTP_PASSWORD="$CMD_PASS" \
  redis-commander --redis-host 127.0.0.1 --redis-port 6379 \
  --port "${COMMANDER_PORT:-18081}" >/dev/null 2>&1 &
CMD_PID=$!
echo "[redis] commander (debug) na porta ${COMMANDER_PORT:-18081}, com login (usuario: ${CMD_USER})"

# C2 (decisao do Luis em 11/10): morre-inteiro, como o edgegateway. ash nao
# tem `wait -n`: vigia por polling (o `sleep` em primeiro plano recolhe o
# processo que saiu, e o `kill -0` passa a falhar). Se um dos dois cair, o
# outro e encerrado e o container sai com 1. O redis-server recebe SIGTERM e
# a vigia espera ele terminar (AOF gravado) antes de sair.
while :; do
  for p in "$REDIS_PID" "$CMD_PID"; do
    if ! kill -0 "$p" 2>/dev/null; then
      if [ "$p" = "$CMD_PID" ]; then QUEM=commander; else QUEM=redis-server; fi
      echo "[redis] $QUEM (pid $p) morreu — derrubando o container inteiro (morre-inteiro, C2)"
      kill "$CMD_PID" "$REDIS_PID" 2>/dev/null
      wait "$REDIS_PID" 2>/dev/null
      exit 1
    fi
  done
  sleep 1
done
