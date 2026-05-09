# users-test — App de teste da plataforma TV30 (TypeScript)
# Build context: ./users-test

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
COPY src/public ./src/public
EXPOSE 8083
CMD ["node", "dist/server.js"]
