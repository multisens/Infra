# Documentação técnica — `infra/` do TV30

Documentação técnica da infraestrutura do testbed TV 3.0 (borda, broker MQTT e Redis) e de como os serviços da raiz do TV30 (`tv3ws`, `aop`, `bcast`) falam com ela. O código é o árbitro: onde um documento divergir dele, vale o código. Mensageria, gateways e Redis são decisões deste projeto e não aparecem na ABNT NBR 25608; onde um documento cita a norma, diz a seção.

## Índice

| Arquivo | Conteúdo |
|---|---|
| [00-diagrama-unificado.md](00-diagrama-unificado.md) | Diagrama unificado do estado atual: containers, portas, superfícies e quem fala com quem (MQTT, HTTP direto, Redis) |
| [01-visao-geral.md](01-visao-geral.md) | Visão geral: o caminho de uma requisição de cliente e as responsabilidades de cada serviço |
| [02-rede-docker.md](02-rede-docker.md) | Containers, portas publicadas, rede `ginga_net`, profiles e ordem de subida |
| [03-pipeline-mqtt.md](03-pipeline-mqtt.md) | Plugin C do broker MQTT: validação de esquema na publicação e teste de latência |
| [04-pipeline-http.md](04-pipeline-http.md) | A borda (`edgegateway`): plugin `tv30-auth` de validação de credenciais, as APIs que ele responde (C.6.8, C.6.7.8 e C.6.7.9) e o roteador KrakenD |
| [05-autenticacao.md](05-autenticacao.md) | Classes de cliente, emissão do access token, validação na borda (bind-token) e descoberta SSDP |
| [06-modelo-redis.md](06-modelo-redis.md) | Modelo de dados do Redis: famílias de chave, quem escreve e quem lê, funções do esquema (P4) |
| [07-usuarios-perfis.md](07-usuarios-perfis.md) | Usuários e perfis: a Tabela 7 da norma, as APIs C.6.14 e o que o testbed grava no Redis |
| [08-mqtt-map.md](08-mqtt-map.md) | Mapa das publicações e assinaturas MQTT de `aop`, `tv3ws` e `bcast` |
| [09-schema-validation.md](09-schema-validation.md) | Como adicionar ou alterar regras de esquema no plugin do Mosquitto |
