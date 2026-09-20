# tv30-edgegateway — a BORDA num container so (fase 2 do plano de
# consolidacao): as duas superficies KrakenD (externa 44643 / interna
# 44642), geradas NO BUILD a partir da tabela unica de rotas (M4), e a
# documentacao (Swagger UI + os dois OpenAPI) em 8085.
#
# Build context: infra/edgegateway/
# syntax=docker/dockerfile:1.6

# --- estagio 1: gera configs e specs da tabela unica (M4) ---
FROM node:20-alpine AS gen
WORKDIR /m4
COPY generate.js routes.json ./
RUN node generate.js build

# --- estagio 2: dist estatico do Swagger UI ---
FROM swaggerapi/swagger-ui AS swagger

# --- final ---
FROM devopsfaith/krakend:2.7
USER root
RUN apk add --no-cache dumb-init busybox-extras   # busybox-extras: httpd p/ docs

# superficies (as duas variantes; o entrypoint escolhe via EDGE_VARIANT)
COPY --from=gen /m4/generated/krakend-*.json /etc/krakend/

# documentacao: UI + specs servidos da MESMA origem (sem CORS)
COPY --from=swagger /usr/share/nginx/html /docs/
COPY --from=gen /m4/generated/openapi-*.json /docs/specs/
COPY swagger-initializer.js /docs/swagger-initializer.js

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 44642 44643 8085
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
