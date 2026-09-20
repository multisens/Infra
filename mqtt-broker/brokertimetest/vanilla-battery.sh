#!/bin/bash
# 5 repeticoes do broker vanilla numa unica sessao (WSL nao pode idle-desligar no meio)
B=/mnt/d/Proj_CEFET/TV30/infra/mqtt-broker/brokertimetest
OUT=$B/interleaved.jsonl
docker rm -f mqtt-vanilla >/dev/null 2>&1
docker run -d --name mqtt-vanilla --restart unless-stopped --network ginga_net \
  -v $B/vanilla.conf:/mosquitto/config/mosquitto.conf eclipse-mosquitto:2.0.22 >/dev/null
sleep 3
for rep in 1 2 3 4 5; do
  docker run --rm -v "$B":/app -w /app --network ginga_net \
    -e MQTT_HOST=mqtt-vanilla -e TOPIC=perftest/echo -e LABEL=vanilla-r$rep \
    node:20-alpine node headless-compare.js 2>&1 \
    | grep RESULT_JSON | sed 's/^RESULT_JSON //' >> "$OUT"
  echo "vanilla rep $rep done"
done
docker rm -f mqtt-vanilla >/dev/null
wc -l "$OUT"
