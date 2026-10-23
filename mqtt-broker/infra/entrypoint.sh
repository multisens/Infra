#!/bin/bash
set -e

# The broker does not use Redis; migrate_to_redis.py is in /usr/local/bin for
# manual debugging.

echo "Starting Mosquitto..."
exec /usr/local/sbin/mosquitto -c /mosquitto/config/mosquitto.conf
