#!/bin/bash
set -e

echo "Waiting for Redis..."
until redis-cli -h ${REDIS_HOST:-redis} -p ${REDIS_PORT:-6379} ping > /dev/null 2>&1; do
  sleep 1
done

# Migracao pro Redis eh feita pelo container redis-seed (compose) ANTES do
# mosquitto subir. O migrate.py fica embarcado em /usr/local/bin pra debug
# manual, mas nao roda no boot.

echo "Starting Mosquitto..."
exec /usr/local/sbin/mosquitto -c /mosquitto/config/mosquitto.conf
