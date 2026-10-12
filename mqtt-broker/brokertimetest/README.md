# MQTT Broker Response Time Tester

Aplicação Node.js para medir o tempo de resposta do broker MQTT com vários clientes em paralelo. Ela usa o teste de latência do plugin C do broker (`plugin/src/response_time_tester.c`): o cliente publica em `PublisherResponseTime<clientId>/iteration<N>`, e o plugin responde em `PluginResponseTime<id>/iteration<N>`.

> Conferido no código em 2026-10-12 (`server.js`, `public/index.html`, `headless-run.js`). Até esta data, este README descrevia uma interface interativa por linha de comando, que o `server.js` não tem.

## Instalação

```bash
npm install
```

## Uso

### Interface web

```bash
npm start
```

O `server.js` sobe um servidor HTTP na porta 3000 (fixa no código). Abra `http://localhost:3000`: a página (`public/index.html`) tem os campos abaixo, e o servidor roda o teste e manda o andamento à página por WebSocket.

| Campo | Padrão da página |
|---|---|
| Endereço do broker MQTT | `localhost` |
| Porta | `1883` |
| Prefixo dos clientes | `clienteexemplo` |
| Quantidade de clientes paralelos | `3` |
| Iterações por cliente | `4` |

Com prefixo `sensor` e 10 clientes, os clientes se chamam `sensor1` a `sensor10`. Em cada iteração todos publicam ao mesmo tempo, com 100 ms entre iterações. No fim, a página mostra o total de mensagens e as latências média, mínima e máxima (tempo de ida e volta).

### Sem interface

```bash
MQTT_HOST=<broker> node headless-run.js
```

O `headless-run.js` roda a mesma lógica da página em quatro cenários (10, 20, 50 e 100 clientes, 10 iterações cada) e imprime uma linha por cenário e o JSON da rodada (linha `RESULT_JSON`, com média, mínimo, p50, p95 e máximo). O `headless-compare.js` compara a entrega com e sem plugin. A medição de 21/08/2026 e os comandos para reproduzi-la estão em `RESULTADOS_2026-08-21.md` (registro).

## Estrutura de Tópicos

- **Publisher**: `PublisherResponseTime<clientId>/iteration<N>`
- **Plugin Response**: `PluginResponseTime<id>/iteration<N>`, com o `id` do payload

## Payload

```json
{
  "testResponsetime": true,
  "localtime": "2026-02-01 20:21:56.123",
  "id": "sensor1",
  "iteration": 1
}
```
