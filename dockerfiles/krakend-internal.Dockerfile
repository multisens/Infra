# KrakenD Internal — gateway interno (sem plugin).
# Build context: infra/krakenD_internal/

# syntax=docker/dockerfile:1.6
FROM devopsfaith/krakend:2.7

COPY krakend.linux.json /etc/krakend/krakend.json

CMD ["run", "-c", "/etc/krakend/krakend.json"]
