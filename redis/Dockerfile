# tv30-redis — armazenamento consolidado (fase 3 do plano): Redis + carga
# inicial (seed gerado NO BUILD, aplicado via redis-cli --pipe — elimina o
# pip-install em tempo de partida) + redis-commander embutido como processo
# auxiliar de debug.
#
# Semantica registrada (decisao V8/V9): o redis-server e o processo
# principal — se ELE morrer, o container cai; se o COMMANDER morrer, o
# banco segue (excecao consciente ao morre-inteiro, por ser ferramenta de
# inspecao). O healthcheck so fica saudavel APOS a carga (chave seed:done),
# substituindo o antigo depends_on service_completed_successfully.
#
# Build context: infra/  (precisa do template de userData e do script de
# normalizacao compartilhado com o mqtt-broker)
# syntax=docker/dockerfile:1.6

# --- estagio 1: gera o seed em protocolo RESP a partir do template ---
FROM python:3.11-alpine AS seedgen
WORKDIR /gen
COPY mqtt-broker/infra/migrate_to_redis.py ./migrate_to_redis.py
COPY redis/emit_seed.py ./
COPY user-files-template/userData.json ./
RUN python emit_seed.py userData.json /seed.resp

# --- final ---
FROM redis:7-alpine
RUN apk add --no-cache nodejs npm dumb-init && \
    npm install -g --omit=dev redis-commander && \
    npm cache clean --force

COPY --from=seedgen /seed.resp /seed.resp
COPY redis/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 6379 18081
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
