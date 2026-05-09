# Broadcaster App (Node JS)
# Build context: ./apps

# syntax=docker/dockerfile:1.6
FROM node:23-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package*.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY src ./src
EXPOSE 8082
CMD ["node", "src/server.js"]
