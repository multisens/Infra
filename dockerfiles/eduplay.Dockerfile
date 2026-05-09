# EduPlay — Quiz logic server (TypeScript + Socket.IO)
# Build context: ./eduplay

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
EXPOSE 8082
CMD ["node", "dist/server.js"]
