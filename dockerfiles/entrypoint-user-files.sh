#!/bin/sh
# Fills /user-files with the template embedded in the image, if it is empty.
set -e

if [ -d /user-files ] && [ -d /opt/user-files-template ]; then
    if [ -z "$(ls -A /user-files 2>/dev/null)" ]; then
        echo "[entrypoint] /user-files vazio — populando do template embutido"
        # cp -a, not cp -rn (the busybox one does not handle /.); with
        # /user-files empty, nothing is overwritten.
        cp -a /opt/user-files-template/. /user-files/ || true
    fi
fi

exec "$@"
