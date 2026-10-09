#!/bin/sh
# Supervisor do edgegateway (dumb-init como PID 1 encaminha sinais).
# Regra do plano: MORRE-INTEIRO — se qualquer processo cair, o container
# encerra por completo (nada de meio-vivo).
#
# Anunciante SSDP (C.3.4; L6 = opcao A, decisao do Luis em 09/10): sobe so com
# SSDP_ENABLED=true, que o docker-compose.ssdp.yml liga junto com a rede do
# host (so Linux nativo). Ele tambem segue o morre-inteiro (decisao do Luis,
# 09/10): falha do anuncio derruba a borda inteira.
set -u

VARIANT="${EDGE_VARIANT:-linux}"

krakend run -c "/etc/krakend/krakend-external.${VARIANT}.json" &
PID_EXT=$!
krakend run -c "/etc/krakend/krakend-internal.${VARIANT}.json" &
PID_INT=$!
httpd -f -p 8085 -h /docs &
PID_DOCS=$!

PIDS="$PID_EXT $PID_INT $PID_DOCS"
PID_SSDP=""
case "${SSDP_ENABLED:-false}" in
  true|1)
    ssdp-announcer &
    PID_SSDP=$!
    PIDS="$PIDS $PID_SSDP"
    SSDP_MSG="ssdp=ligado" ;;
  *)
    SSDP_MSG="ssdp=desligado" ;;
esac

echo "[edgegateway] externa=44643 interna=44642 docs=8085 (variant=${VARIANT}, ${SSDP_MSG})"

shutdown() {
  # o anunciante primeiro, para o ssdp:byebye sair antes de o container acabar
  if [ -n "$PID_SSDP" ]; then
    kill "$PID_SSDP" 2>/dev/null
    wait "$PID_SSDP" 2>/dev/null
  fi
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
