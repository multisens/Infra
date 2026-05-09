# KrakenD External — gateway com plugin consent-validator embutido.
# Build context: infra/krakenD_external/

# syntax=docker/dockerfile:1.6
FROM devopsfaith/krakend:2.7

COPY krakend.linux.json /etc/krakend/krakend.json
COPY plugins/consent-validator.so /etc/krakend/plugins/consent-validator.so

CMD ["run", "-c", "/etc/krakend/krakend.json"]
