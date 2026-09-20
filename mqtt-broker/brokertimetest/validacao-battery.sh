#!/bin/bash
# 5 repeticoes: broker com plugin, topico com validacao de schema
B=/mnt/d/Proj_CEFET/TV30/infra/mqtt-broker/brokertimetest
OUT=$B/validacao-only.jsonl
: > "$OUT"
docker start mqtt-broker >/dev/null 2>&1
sleep 2
for rep in 1 2 3 4 5; do
  docker run --rm -v "$B":/app -w /app --network ginga_net \
    -e MQTT_HOST=mosquitto -e TOPIC=sensor/room1/temperature -e LABEL=validacao-r$rep \
    node:20-alpine node headless-compare.js 2>&1 \
    | grep RESULT_JSON | sed 's/^RESULT_JSON //' >> "$OUT"
  echo "rep $rep done"
done
wc -l "$OUT"
