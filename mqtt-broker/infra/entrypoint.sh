#!/bin/bash
set -e

# O broker nao usa o Redis (o plugin so valida esquema); o migrate_to_redis.py
# fica em /usr/local/bin para depuracao manual.

echo "Starting Mosquitto..."
exec /usr/local/sbin/mosquitto -c /mosquitto/config/mosquitto.conf
