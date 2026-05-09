# AoP — Application-Oriented Platform (Node JS)
# Build context: ./aop
# Additional contexts (via --build-context):
#   tv30-data    -> infra/user-files-template
#   tv30-scripts -> infra/dockerfiles

# syntax=docker/dockerfile:1.6
FROM node:23-alpine
WORKDIR /app
COPY package*.json ./
# Nao usar --omit=dev: aop/src/server.js importa http-proxy-middleware
# que esta em devDependencies (bug do package.json upstream).
RUN npm ci && npm cache clean --force
ENV NODE_ENV=production
COPY src ./src
COPY public ./public
# Template embutido + entrypoint que popula /user-files se vazio.
COPY --from=tv30-data    / /opt/user-files-template
COPY --from=tv30-scripts /entrypoint-user-files.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
CMD ["node", "src/server.js"]
