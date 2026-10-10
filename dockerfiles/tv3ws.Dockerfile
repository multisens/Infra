# tv3ws — TV 3.0 Ginga CC WebServices (TypeScript)
# Build context: ./tv3ws
# Additional contexts (via --build-context):
#   tv30-data    -> infra/user-files-template
#   tv30-scripts -> infra/dockerfiles

# syntax=docker/dockerfile:1.6
FROM node:23-alpine AS builder
WORKDIR /app
COPY package*.json tsconfig.json ./
RUN npm ci
COPY src ./src
RUN npm run build

FROM node:23-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package*.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY --from=builder /app/dist ./dist
# Template embutido + entrypoint que popula /user-files se vazio. O tv3ws nao
# le mais o userData.json (a semeadura initFromRedis saiu na D-0510-5; a carga
# inicial dos perfis e do container redis). Do /user-files, o codigo dele so
# usa o USER_THUMBS (/user-files/thumbs no compose da raiz).
COPY --from=tv30-data    / /opt/user-files-template
COPY --from=tv30-scripts /entrypoint-user-files.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 44652 44653
ENTRYPOINT ["/entrypoint.sh"]
CMD ["node", "dist/server.js"]
