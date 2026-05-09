# Broadcaster Service (TypeScript + EJS templates)
# Build context: ./bcast

# syntax=docker/dockerfile:1.6
FROM node:23-alpine AS builder
WORKDIR /app
COPY package*.json tsconfig.json ./
RUN npm ci
COPY src ./src
COPY scripts ./scripts
# `clean` step usa rm -rf (ok no alpine); build = clean + tsc + copy-templates
RUN npm run build

FROM node:23-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package*.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY --from=builder /app/dist ./dist
COPY public ./public
EXPOSE 8081
CMD ["node", "dist/index.js"]
