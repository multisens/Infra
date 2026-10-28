# tv30-redis: Redis with the tv30 function library, the initial load
# generated at build time (applied by redis-cli --pipe) and redis-commander
# (debug). The build runs check-image.sh: a library that does not load or
# a load outside the schema fails the build. Build context: infra/ (the
# userData template and migrate_to_redis.py of mqtt-broker).

FROM python:3.11-alpine AS seedgen
WORKDIR /gen
COPY mqtt-broker/infra/migrate_to_redis.py ./migrate_to_redis.py
COPY redis/emit_seed.py ./
COPY user-files-template/userData.json ./
RUN python emit_seed.py userData.json /seed.resp

# redis-commander with a pinned version: the UI login depends on HTTP_USER
# and HTTP_PASSWORD, which 0.9.0 maps; without them the UI starts open,
# without a warning. Before changing the version, check the mapping
# (scripts/test-consolidacao.sh).
FROM redis:7-alpine
RUN apk add --no-cache nodejs npm dumb-init && \
    npm install -g --omit=dev redis-commander@0.9.0 && \
    npm cache clean --force

COPY --from=seedgen /seed.resp /seed.resp
COPY redis/schema.json redis/functions.lua redis/library.sh redis/check-image.sh /opt/tv30-redis/
RUN sh /opt/tv30-redis/check-image.sh
COPY redis/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 6379 18081
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
