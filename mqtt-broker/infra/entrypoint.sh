#!/bin/bash
set -e

# O broker nao depende mais do Redis: o controle de acesso a topicos foi
# removido por decisao de desenho (o plugin so faz validacao de schema).
# O migrate.py fica embarcado em /usr/local/bin pra debug manual.

echo "Starting Mosquitto..."
exec /usr/local/sbin/mosquitto -c /mosquitto/config/mosquitto.conf
