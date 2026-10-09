# tv30-edgegateway — a BORDA num container so (fase 2 do plano de
# consolidacao): as duas superficies KrakenD (externa 44643 / interna
# 44642), geradas NO BUILD a partir da tabela unica de rotas (M4), a
# validacao de credenciais (plugin tv30-auth, item 9) e a documentacao
# (Swagger UI + os dois OpenAPI) em 8085.
#
# Build context: infra/edgegateway/
# syntax=docker/dockerfile:1.6

# --- estagio 0: plugin Go tv30-auth (validacao de credenciais na borda) ---
# Um .so de Go so carrega num binario compilado com o MESMO Go, a MESMA
# libc e a mesma arquitetura: o builder oficial e a imagem final ficam na
# MESMA versao FIXA (2.7.2) — tag flutuante quebraria o carregamento num
# rebuild futuro. vet + testes rodam aqui: plugin quebrado nao vira imagem.
FROM krakend/builder:2.7.2 AS plugin
WORKDIR /src
COPY plugin/ ./
RUN go vet ./... \
 && go test -count=1 ./... \
 && mkdir -p /out \
 && go build -buildmode=plugin -o /out/tv30-auth.so . \
 && go env GOVERSION | sed 's/^go//' > /out/go-version \
 && echo "MUSL-$(/lib/ld-musl-*.so.1 2>&1 | sed -n 's/^Version //p')_(alpine-$(cat /etc/alpine-release))" > /out/libc-version \
 && : > /out/go.sum

# --- estagio 0b: anunciante SSDP da borda (C.3.4; L6 = opcao A, 09/10) ---
# Binario estatico, so biblioteca padrao; roda so com SSDP_ENABLED=true (borda
# em rede do host, docker-compose.ssdp.yml, so Linux nativo).
FROM krakend/builder:2.7.2 AS ssdp
WORKDIR /src
COPY ssdp/ ./
RUN go vet ./... \
 && go test -count=1 ./... \
 && CGO_ENABLED=0 go build -trimpath -o /out/ssdp-announcer .

# --- estagio 1: gera configs e specs da tabela unica (M4) ---
FROM node:20-alpine AS gen
WORKDIR /m4
COPY generate.js routes.json ./
RUN node generate.js build

# --- estagio 2: dist estatico do Swagger UI ---
FROM swaggerapi/swagger-ui AS swagger

# --- final ---
FROM devopsfaith/krakend:2.7.2
USER root
RUN apk add --no-cache dumb-init busybox-extras   # busybox-extras: httpd p/ docs

# plugin: o build FALHA (em vez de a borda subir sem validacao, em silencio)
# se o .so nao casar com ESTE binario — check-plugin compara Go e libc do
# builder com os do krakend; test-plugin carrega o .so como plugin de servidor.
COPY --from=plugin /out/tv30-auth.so /opt/krakend/plugins/tv30-auth.so
COPY --from=plugin /out/go-version /out/libc-version /out/go.sum /tmp/plugin-check/
RUN krakend check-plugin -g "$(cat /tmp/plugin-check/go-version)" \
                         -l "$(cat /tmp/plugin-check/libc-version)" \
                         -s /tmp/plugin-check/go.sum \
 && krakend test-plugin -s /opt/krakend/plugins/tv30-auth.so \
 && rm -rf /tmp/plugin-check

COPY --from=ssdp /out/ssdp-announcer /usr/local/bin/ssdp-announcer

# superficies (variantes linux, windows e host; o entrypoint escolhe via EDGE_VARIANT)
COPY --from=gen /m4/generated/krakend-*.json /etc/krakend/
RUN for f in /etc/krakend/krakend-*.json; do krakend check -c "$f" || exit 1; done

# documentacao: UI + specs servidos da MESMA origem (sem CORS)
COPY --from=swagger /usr/share/nginx/html /docs/
COPY --from=gen /m4/generated/openapi-*.json /docs/specs/
COPY swagger-initializer.js /docs/swagger-initializer.js

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 44642 44643 8085
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
