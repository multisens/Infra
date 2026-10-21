#!/bin/sh
# tls-init: certificado autoassinado do receptor (a C.4.1.1 o admite), gerado
# so se ainda nao existe em $TLS_DIR; usado pela borda (44643) e pelo tv3ws. O
# cliente nao local confia nele porque o recebe no pareamento (Tabela C.4).
# Os arquivos ficam com o dono do diretorio: o tv3ws no host (dev-host) os le.
set -eu
DIR=${TLS_DIR:-/tls}
mkdir -p "$DIR"
if [ -s "$DIR/cert.pem" ] && [ -s "$DIR/key.pem" ]; then
  echo "[tls-init] certificado existente: $(openssl x509 -in "$DIR/cert.pem" -noout -subject -enddate | tr '\n' ' ')"
  exit 0
fi
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -subj "/CN=TV 3.0 Receiver/O=TV30 testbed" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout "$DIR/key.pem.tmp" -out "$DIR/cert.pem.tmp" 2>/dev/null
chmod 600 "$DIR/key.pem.tmp"
chmod 644 "$DIR/cert.pem.tmp"
OWNER=$(stat -c '%u:%g' "$DIR")
chown "$OWNER" "$DIR/key.pem.tmp" "$DIR/cert.pem.tmp" 2>/dev/null || true
mv "$DIR/key.pem.tmp" "$DIR/key.pem"
mv "$DIR/cert.pem.tmp" "$DIR/cert.pem"
echo "[tls-init] certificado autoassinado gerado em $DIR: $(openssl x509 -in "$DIR/cert.pem" -noout -subject -enddate | tr '\n' ' ')"
