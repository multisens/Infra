# tv30-edgegateway: as duas superficies KrakenD (externa 44643, interna 44642),
# geradas no build de routes.json, com o plugin tv30-auth, e a documentacao
# (Swagger UI + os dois OpenAPI) em 8085. Build context: infra/edgegateway/

# Um .so de Go so carrega num binario com o mesmo Go, a mesma libc e a mesma
# arquitetura. amd64: o builder oficial (so existe para amd64). arm64: o Go
# 1.22.7 e a musl 1.2.4 do devopsfaith/krakend:2.7.2 arm64, com binutils-gold
# (o Go liga plugin em arm64 com o gold); emular o builder amd64 nao serve, o
# Go cai sob o QEMU (lfstack.push). Versoes fixas: tag flutuante quebraria o
# carregamento num rebuild. Outra arquitetura falha no build.
ARG TARGETARCH
FROM --platform=linux/amd64 krakend/builder:2.7.2 AS gobuild-amd64
FROM golang:1.22.7-alpine3.19 AS gobuild-arm64
RUN apk add --no-cache gcc musl-dev binutils-gold
FROM gobuild-${TARGETARCH} AS gobuild

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

FROM node:20-alpine AS gen
WORKDIR /m4
COPY generate.js generate.test.js routes.json ./
RUN node --test generate.test.js \
 && node generate.js build

FROM swaggerapi/swagger-ui AS swagger

FROM devopsfaith/krakend:2.7.2
USER root
RUN apk add --no-cache dumb-init busybox-extras openssl   # busybox-extras: httpd p/ docs; openssl: tls-init

# o build falha se o .so nao casar com este binario (check-plugin: Go e libc;
# test-plugin: carga do .so); sem isso a borda subiria sem validacao, em silencio
COPY --from=plugin /out/tv30-auth.so /opt/krakend/plugins/tv30-auth.so
COPY --from=plugin /out/go-version /out/libc-version /out/go.sum /tmp/plugin-check/
RUN krakend check-plugin -g "$(cat /tmp/plugin-check/go-version)" \
                         -l "$(cat /tmp/plugin-check/libc-version)" \
                         -s /tmp/plugin-check/go.sum \
 && krakend test-plugin -s /opt/krakend/plugins/tv30-auth.so \
 && rm -rf /tmp/plugin-check

# superficies (variantes linux e windows; o entrypoint escolhe via EDGE_VARIANT)
COPY --from=gen /m4/generated/krakend-*.json /etc/krakend/
RUN for f in /etc/krakend/krakend-*.json; do krakend check -c "$f" || exit 1; done

# documentacao: UI + specs servidos da MESMA origem (sem CORS)
COPY --from=swagger /usr/share/nginx/html /docs/
COPY --from=gen /m4/generated/openapi-*.json /docs/specs/
COPY swagger-initializer.js /docs/swagger-initializer.js

# gerador do certificado do receptor, rodado pelo servico tls-init do compose da raiz
COPY tls-init.sh /usr/local/bin/tv30-tls-init
RUN chmod +x /usr/local/bin/tv30-tls-init
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 44642 44643 8085
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
