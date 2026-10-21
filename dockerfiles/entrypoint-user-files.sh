#!/bin/sh
# Popula /user-files com o template embutido na imagem, se estiver vazio.
set -e

if [ -d /user-files ] && [ -d /opt/user-files-template ]; then
    if [ -z "$(ls -A /user-files 2>/dev/null)" ]; then
        echo "[entrypoint] /user-files vazio — populando do template embutido"
        # cp -a, e nao cp -rn (o do busybox nao trata /.); com /user-files vazio, nada e sobrescrito
        cp -a /opt/user-files-template/. /user-files/ || true
    fi
fi

exec "$@"
