# Arquitetura — `infra/`

Resumo da infraestrutura compartilhada do testbed TV 3.0: a borda (`edgegateway`), o broker MQTT e o Redis, e como os serviços da raiz do TV30 (`tv3ws`, `aop`, `bcast`) falam com ela. Mensageria, gateways e Redis são decisões deste projeto e não aparecem na ABNT NBR 25608; da norma vêm as APIs de `/tv3` (Anexo C), as classes de cliente (C.4.1), a porta 44642 do `Server-BaseURL` e a descoberta SSDP (C.3.4).

O detalhe está em [`docs/`](docs/README.md): containers e portas em [00](docs/00-diagrama-unificado.md) e [02](docs/02-rede-docker.md), a borda em [04](docs/04-pipeline-http.md) e [05](docs/05-autenticacao.md), o broker em [03](docs/03-pipeline-mqtt.md) e [09](docs/09-schema-validation.md), o Redis em [06](docs/06-modelo-redis.md) e em [`redis/README.md`](redis/README.md).

## Decisões de implementação

| Decisão do projeto | Onde está |
|---|---|
| A stack sobe de uma vez pelo compose da raiz do TV30, que inclui o `docker-compose.yml` desta pasta (e este, o de `redis/`); sozinho, o compose da `infra/` não é projeto válido | `docker-compose.yml`, [README](README.md) |
| Rede `ginga_net`, bridge criada pelo próprio projeto; dentro dela os serviços se acham pelo nome | `docker-compose.yml`, `redis/docker-compose.yml` |
| A borda é um container só, com duas superfícies KrakenD: interna (44642, HTTP) e externa (44643, HTTPS, TLS terminado no KrakenD), geradas no build de uma tabela única de rotas | `edgegateway/routes.json`, `edgegateway/generate.js` |
| A validação de credenciais fica toda na borda, no plugin Go `tv30-auth`, carregado nas duas superfícies; o tv3ws não confere token nem classe, e as portas dele (44652, 44653) não são publicadas no host | `edgegateway/plugin/` |
| O plugin responde ele mesmo a C.6.8 (`/tv3/bind-context`) e a C.6.7.8/C.6.7.9 (`/tv3/api-info`); as demais rotas vão ao tv3ws, com status e corpo intactos (menos o 5xx do próprio KrakenD, trocado por 404 `{error: 200}`) | `edgegateway/plugin/edge.go`, `edgegateway/plugin/handler.go` |
| O plugin C do broker só valida o esquema do payload na publicação e responde ao teste de latência; não consulta o Redis nem decide acesso por cliente | `mqtt-broker/plugin/` |
| Toda escrita do código nas famílias de chave do testbed passa por funções do Redis que conferem o esquema; a auditoria mostra o que entrou por fora | `redis/esquema.json`, `redis/funcoes.lua` |
| A 6379 fica sem senha e sem ACL, publicada só em `127.0.0.1`; só a interface do Redis Commander exige login | `redis/docker-compose.yml` |
| O anunciante SSDP roda nativo no host, fora do Docker, subido pelo `./tv30 up` da raiz | `edgegateway/ssdp/` |
