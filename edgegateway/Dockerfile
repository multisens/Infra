# tv30-edgegateway — a BORDA num container so (fase 2 do plano de
# consolidacao): as duas superficies KrakenD (externa 44643 / interna
# 44642), geradas NO BUILD a partir da tabela unica de rotas (M4), a
# validacao de credenciais (plugin tv30-auth, item 9) e a documentacao
# (Swagger UI + os dois OpenAPI) em 8085.
#
# Build context: infra/edgegateway/
# syntax=docker/dockerfile:1.6

# --- estagio 0: toolchain Go, escolhida pela arquitetura ---
# Um .so de Go so carrega num binario compilado com o MESMO Go, a MESMA
# libc e a mesma arquitetura. A arquitetura vem do proprio Docker
# (TARGETARCH); o sistema dentro do build e sempre Linux, tambem no Mac.
# - amd64 (o Linux de destino): o builder oficial, krakend/builder:2.7.2,
#   como antes. Ele so existe para amd64.
# - arm64 (Mac com Apple Silicon, Linux em ARM): golang:1.22.7-alpine3.19,
#   o mesmo Go (1.22.7) e a mesma libc (musl 1.2.4, Alpine 3.19.4) com que
#   foi compilado o devopsfaith/krakend:2.7.2 arm64. O binutils-gold entra
#   porque o Go liga plugin em arm64 com o gold. Emular o builder amd64 no
#   ARM nao serve: o Go cai sob o QEMU (lfstack.push; medido em 18/10).
# Versoes FIXAS nos dois casos: tag flutuante quebraria o carregamento num
# rebuild futuro. O check-plugin do estagio final confere Go e libc do .so
# contra o krakend nas duas arquiteturas. Outra arquitetura falha no build.
ARG TARGETARCH
FROM --platform=linux/amd64 krakend/builder:2.7.2 AS gobuild-amd64
FROM golang:1.22.7-alpine3.19 AS gobuild-arm64
RUN apk add --no-cache gcc musl-dev binutils-gold
FROM gobuild-${TARGETARCH} AS gobuild

# --- estagio 0a: plugin Go tv30-auth (validacao de credenciais na borda) ---
# vet + testes rodam aqui: plugin quebrado nao vira imagem.
FROM gobuild AS plugin
WORKDIR /src
COPY plugin/ ./
RUN go vet ./... \
 && go test -count=1 ./... \
 && mkdir -p /out \
 && go build -buildmode=plugin -o /out/tv30-auth.so . \
 && go env GOVERSION | sed 's/^go//' > /out/go-version \
 && echo "MUSL-$(/lib/ld-musl-*.so.1 2>&1 | sed -n 's/^Version //p')_(alpine-$(cat /etc/alpine-release))" > /out/libc-version \
 && : > /out/go.sum

# --- estagio 1: gera configs e specs da tabela unica (M4) ---
# Testes do gerador antes (superficie derivada de classes, P1.9): tabela que
# quebra a regra nao vira imagem, como o plugin Go no estagio 0.
FROM node:20-alpine AS gen
WORKDIR /m4
COPY generate.js generate.test.js routes.json ./
RUN node --test generate.test.js \
 && node generate.js build

# --- estagio 2: dist estatico do Swagger UI ---
FROM swaggerapi/swagger-ui AS swagger

# --- final ---
FROM devopsfaith/krakend:2.7.2
USER root
RUN apk add --no-cache dumb-init busybox-extras openssl   # busybox-extras: httpd p/ docs; openssl: tls-init (A7)

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

# superficies (variantes linux e windows; o entrypoint escolhe via EDGE_VARIANT).
# O anunciante SSDP saiu da imagem em 15/10 (complemento do Joel): roda nativo
# no host, fora do Docker (infra/edgegateway/ssdp, ./tv30 up).
COPY --from=gen /m4/generated/krakend-*.json /etc/krakend/
RUN for f in /etc/krakend/krakend-*.json; do krakend check -c "$f" || exit 1; done

# documentacao: UI + specs servidos da MESMA origem (sem CORS)
COPY --from=swagger /usr/share/nginx/html /docs/
COPY --from=gen /m4/generated/openapi-*.json /docs/specs/
COPY swagger-initializer.js /docs/swagger-initializer.js

# A7 (Joel, 15/10): gerador do certificado do receptor, que o servico tls-init
# do compose da raiz roda com esta imagem
COPY tls-init.sh /usr/local/bin/tv30-tls-init
RUN chmod +x /usr/local/bin/tv30-tls-init
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 44642 44643 8085
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
