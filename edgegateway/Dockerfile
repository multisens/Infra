# tv30-edgegateway: the two KrakenD surfaces (external 44643, internal
# 44642), generated at build time from routes.json, with the tv30-auth
# plugin, and the documentation (Swagger UI and the two OpenAPI files) on
# 8085. Build context: infra/edgegateway/

# A Go .so loads only in a binary with the same Go, the same libc and the
# same architecture. amd64: the official builder (amd64 only). arm64: the Go
# 1.22.7 and musl 1.2.4 of devopsfaith/krakend:2.7.2 arm64, with
# binutils-gold (Go links plugins on arm64 with gold); the amd64 builder
# under QEMU does not work (Go fails with lfstack.push). Versions are pinned:
# a floating tag would break loading on a rebuild. Any other architecture
# fails the build.
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
# busybox-extras: httpd for the docs; openssl: tls-init.
RUN apk add --no-cache dumb-init busybox-extras openssl

# The build fails if the .so does not match this binary (check-plugin: Go
# and libc; test-plugin: loading of the .so).
COPY --from=plugin /out/tv30-auth.so /opt/krakend/plugins/tv30-auth.so
COPY --from=plugin /out/go-version /out/libc-version /out/go.sum /tmp/plugin-check/
RUN krakend check-plugin -g "$(cat /tmp/plugin-check/go-version)" \
                         -l "$(cat /tmp/plugin-check/libc-version)" \
                         -s /tmp/plugin-check/go.sum \
 && krakend test-plugin -s /opt/krakend/plugins/tv30-auth.so \
 && rm -rf /tmp/plugin-check

# Surfaces, linux and windows variants; the entrypoint chooses by
# EDGE_VARIANT.
COPY --from=gen /m4/generated/krakend-*.json /etc/krakend/
RUN for f in /etc/krakend/krakend-*.json; do krakend check -c "$f" || exit 1; done

# Documentation: UI and specs served from the same origin.
COPY --from=swagger /usr/share/nginx/html /docs/
COPY --from=gen /m4/generated/openapi-*.json /docs/specs/
COPY swagger-initializer.js /docs/swagger-initializer.js

# Generator of the receiver certificate.
COPY tls-init.sh /usr/local/bin/tv30-tls-init
RUN chmod +x /usr/local/bin/tv30-tls-init
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 44642 44643 8085
ENTRYPOINT ["dumb-init", "--", "/entrypoint.sh"]
