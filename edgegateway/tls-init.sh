#!/bin/sh
# Generates the receiver's self-signed certificate (cert.pem, key.pem) in
# $TLS_DIR, default /tls, only if either file is missing or empty. The files
# get the owner of the directory.
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
