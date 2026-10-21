# tv30-redis: Redis com a biblioteca de funcoes tv30, carga inicial gerada no
# build (aplicada por redis-cli --pipe) e redis-commander (debug). O build roda
# confere-imagem.sh: biblioteca que nao carrega ou carga fora do esquema nao vira imagem.
# Build context: infra/ (template de userData e migrate_to_redis.py do mqtt-broker)
# syntax=docker/dockerfile:1.6

FROM python:3.11-alpine AS seedgen
WORKDIR /gen
COPY mqtt-broker/infra/migrate_to_redis.py ./migrate_to_redis.py
COPY redis/emit_seed.py ./
COPY user-files-template/userData.json ./
RUN python emit_seed.py userData.json /seed.resp

# redis-commander com versao fixada: o login da UI depende de HTTP_USER e
# HTTP_PASSWORD, que a 0.9.0 mapeia; sem eles a UI sobe aberta, sem aviso.
# Antes de trocar a versao, confira o mapeamento (scripts/test-consolidacao.sh).
FROM redis:7-alpine
RUN apk add --no-cache nodejs npm dumb-init && \
    npm install -g --omit=dev redis-commander@0.9.0 && \
    npm cache clean --force

COPY --from=seedgen /seed.resp /seed.resp
COPY redis/esquema.json redis/funcoes.lua redis/biblioteca.sh redis/confere-imagem.sh /opt/tv30-redis/
RUN sh /opt/tv30-redis/confere-imagem.sh
COPY redis/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 6379 18081
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
