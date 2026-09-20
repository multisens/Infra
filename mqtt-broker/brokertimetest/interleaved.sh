#!/bin/bash
# 5 repeticoes intercaladas de 3 configuracoes (vanilla / plugin neutro / plugin validacao)
B=/mnt/d/Proj_CEFET/TV30/infra/mqtt-broker/brokertimetest
OUT=$B/interleaved.jsonl
: > "$OUT"
for rep in 1 2 3 4 5; do
  for cfg in "mqtt-vanilla perftest/echo vanilla" \
             "mosquitto perftest/echo neutro" \
             "mosquitto sensor/room1/temperature validacao"; do
    set -- $cfg
    docker run --rm -v "$B":/app -w /app --network ginga_net \
      -e MQTT_HOST=$1 -e TOPIC=$2 -e LABEL=$3-r$rep \
      node:20-alpine node headless-compare.js 2>&1 \
      | grep RESULT_JSON | sed 's/^RESULT_JSON //' >> "$OUT"
    echo "rep $rep $3 done"
  done
done
wc -l "$OUT"
