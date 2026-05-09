#!/bin/sh
# Bootstrap /user-files com template embutido na imagem se estiver vazio.
# Idempotente: cp -n nao sobrescreve nada existente.
set -e

if [ -d /user-files ] && [ -d /opt/user-files-template ]; then
    if [ -z "$(ls -A /user-files 2>/dev/null)" ]; then
        echo "[entrypoint] /user-files vazio — populando do template embutido"
        # cp -a preserva permissoes/timestamps; busybox cp -rn nao trata /. corretamente.
        # Como ja verificamos que /user-files esta vazio, nao ha risco de overwrite.
        cp -a /opt/user-files-template/. /user-files/ || true
    fi
fi

exec "$@"
