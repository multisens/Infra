# tv30-swagger — Swagger UI unico para os dois gateways TV3 WS.
#
# Nao guarda spec nenhum: aponta (via URLS) para os /openapi.json gerados
# pelos middlewares a partir do krakend.json de cada gateway. O Swagger UI roda
# no browser, entao as URLs sao as portas dos middlewares no HOST (3000/3001).
FROM swaggerapi/swagger-ui

ENV URLS='[{"url":"http://localhost:3000/openapi.json","name":"TV3 WS Gateway External"},{"url":"http://localhost:3001/openapi.json","name":"TV3 WS Gateway Internal"}]'
ENV URLS_PRIMARY_NAME="TV3 WS Gateway External"
ENV CORS=true
