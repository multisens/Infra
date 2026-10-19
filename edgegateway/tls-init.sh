#!/bin/sh
# tls-init — certificado X.509 autoassinado do receptor (A7, Joel 15/10).
#
# Roda uma vez por subida (servico tls-init do compose da raiz, com a imagem
# da borda) e so gera o certificado na PRIMEIRA: se key.pem e cert.pem ja
# existem em $TLS_DIR, nao faz nada. Fica em .tv30/tls na raiz do TV30 (fora
# do git), montado na borda (que termina o TLS da 44643, a Server-SecureBaseURL)
# e no tv3ws (HTTPS da 44653, serverCert do pareamento e wss:// dos pontos de
# entrada). C.4.1.1: "it is valid for TV 3.0 WebServices to set up its HTTPS
# server using a certificate self-signed by the platform [...] there is no
# dependence on third-party certification authorities". O cliente nao local
# confia nele porque o recebe no pareamento (Tabela C.4, serverCert).
#
# Os dois arquivos ficam com o dono do diretorio (o usuario que rodou o
# ./tv30 up, que o cria antes): o tv3ws rodando no host (dev-host) consegue
# le-los. A chave e 0600.
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
