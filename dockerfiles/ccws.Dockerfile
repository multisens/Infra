# CCWS — TV 3.0 Ginga CC WebServices (TypeScript)
# Build context: ./ccws
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
# Template embutido + entrypoint que popula /user-files se vazio
# (CCWS le userData.json no initFromRedis se Redis estiver vazio).
COPY --from=tv30-data    / /opt/user-files-template
COPY --from=tv30-scripts /entrypoint-user-files.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 44652 44653
ENTRYPOINT ["/entrypoint.sh"]
CMD ["node", "dist/server.js"]
