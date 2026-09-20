# Resultados — tempo de resposta broker + plugin (2026-08-21)

Medição do round-trip cliente → broker (Mosquitto 2.0.22 + plugin C com
validação de schema e autorização via Redis) → resposta do plugin
(`response_time_tester`), via `headless-run.js` (mesma lógica da tela
`brokertimetest`: rajada simultânea de todos os clientes por iteração,
100 ms entre iterações, QoS 0).

**Ambiente:** WSL2 (Ubuntu 24.04) + Docker; broker `mqtt-broker`
(imagem `luiscrjr/tv30-mosquitto`); cliente de teste em container
`node:20-alpine` na mesma rede Docker (`ginga_net`) — sem atravessar a
fronteira Windows↔WSL. 10 iterações por cliente.

| Clientes paralelos | Mensagens | Perdidas | Média (ms) | Mín (ms) | p50 (ms) | p95 (ms) | Máx (ms) |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 10  | 100  | 0 | 2,30  | 1 | 2  | 4  | 5  |
| 20  | 200  | 0 | 3,68  | 1 | 3  | 8  | 12 |
| 50  | 500  | 0 | 8,16  | 2 | 7  | 17 | 24 |
| 100 | 1000 | 0 | 18,67 | 3 | 18 | 36 | 43 |

## Leitura

- Escala de forma aproximadamente linear com o tamanho da rajada
  (~0,18 ms por cliente adicional na média): o custo dominante é o
  enfileiramento da rajada simultânea, não o plugin em si.
- Mesmo no pior caso (100 clientes publicando no mesmo instante), o
  round-trip máximo observado foi 43 ms e o p95 ficou em 36 ms — folga
  ampla para os padrões de tráfego do testbed.
- **Correção (2026-08-21):** a autorização em duas camadas está
  **comentada** no plugin atual (`mosquitto_plugin.c:143-147`, "DESABILITADO:
  todos os clientes liberados") — o Redis é conectado na inicialização mas
  **não é consultado por mensagem**. Estes números cobrem o caminho do
  callback + validação de schema, sem custo de autorização.

## Reproduzir

```bash
docker run --rm -v <repo>/infra/mosquitto_plugin/brokertimetest:/app -w /app \
  -e MQTT_HOST=mosquitto --network ginga_net node:20-alpine node headless-run.js
```

JSON bruto da rodada:

```json
{"date":"2026-08-21 18:54:07.827","host":"mosquitto","port":1883,"iterations":10,"scenarios":[
 {"clients":10,"expected":100,"received":100,"lost":0,"avg":2.3,"min":1,"p50":2,"p95":4,"max":5},
 {"clients":20,"expected":200,"received":200,"lost":0,"avg":3.68,"min":1,"p50":3,"p95":8,"max":12},
 {"clients":50,"expected":500,"received":500,"lost":0,"avg":8.16,"min":2,"p50":7,"p95":17,"max":24},
 {"clients":100,"expected":1000,"received":1000,"lost":0,"avg":18.67,"min":3,"p50":18,"p95":36,"max":43}]}
```

---

# Comparação com × sem plugin (auto-eco, mesma rodada)

Metodologia diferente da tabela acima (por isso os valores não são
comparáveis entre as duas tabelas): aqui **N publishers → 1 tópico →
1 listener dedicado**, medindo só a entrega pub→sub pelo broker — sem o
eco do plugin, que não existe no broker vanilla. Mesma rajada e cadência.
Brokers na mesma versão (Mosquitto 2.0.22), mesma rede Docker.
`headless-compare.js`.

**5 repetições por configuração** (rodadas únicas mostraram variação
maior que a diferença entre configurações — inclusive inversões
espúrias tipo "plugin mais rápido que vanilla"). Latência média em ms:
**mediana das 5 rodadas (média ± desvio-padrão)**. Dados brutos em
`interleaved.jsonl`.

| Clientes | Vanilla (sem plugin) | Plugin, tópico neutro | Plugin, validação de schema* |
|---:|---:|---:|---:|
| 10  | 1,44 (1,40 ± 0,24) | 1,16 (1,33 ± 0,36) | 1,16 (1,21 ± 0,18) |
| 20  | 1,48 (1,49 ± 0,36) | 1,22 (1,22 ± 0,13) | 1,15 (1,30 ± 0,37) |
| 50  | 2,43 (2,44 ± 0,12) | 2,58 (2,69 ± 0,74) | 2,46 (4,10 ± 3,42**) |
| 100 | 3,33 (4,08 ± 1,15) | 4,14 (4,13 ± 0,80) | 3,44 (4,01 ± 1,12) |

\* tópico `sensor/room1/temperature`, payload validado contra o JSON
schema em cada publish.
\** uma rodada outlier (média 10,92 ms) infla média e dp; a mediana
(2,46) é representativa. Soluços transitórios desse tipo apareceram
esporadicamente em todas as configurações.

**Conclusão:** as três configurações são **estatisticamente
indistinguíveis** — as diferenças entre elas (0,1–0,8 ms) são menores
que a variação entre rodadas da mesma configuração (dp até 1,2 ms,
faixas sobrepostas: vanilla com 100 clientes variou de 3,13 a 6,07 ms
entre rodadas). O overhead do plugin, com ou sem validação de schema,
fica abaixo do ruído de medição do ambiente (WSL2 + Docker). Zero perda
em todas as 60 medições (15 rodadas × 4 cenários).

Reproduzir a comparação:

```bash
docker run -d --name mqtt-vanilla --network ginga_net \
  -v <repo>/infra/mosquitto_plugin/brokertimetest/vanilla.conf:/mosquitto/config/mosquitto.conf \
  eclipse-mosquitto:2.0.22
docker run --rm -v <repo>/infra/mosquitto_plugin/brokertimetest:/app -w /app \
  --network ginga_net -e MQTT_HOST=<mqtt-vanilla|mosquitto> \
  -e TOPIC=<perftest/echo|sensor/room1/temperature> \
  node:20-alpine node headless-compare.js
```
