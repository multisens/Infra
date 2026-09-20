// Headless runner do MQTT Broker Response Time Tester.
// Mesma lógica do server.js (rajada por iteração, 100ms entre iterações),
// sem UI: roda cenários em sequência e imprime tabela + JSON.
const mqtt = require('mqtt');

const HOST = process.env.MQTT_HOST || 'localhost';
const PORT = process.env.MQTT_PORT || 1883;
const ITERATIONS = 10;
const SCENARIOS = [10, 20, 50, 100];

function formatTimestamp() {
  const now = new Date();
  const p = (n, l = 2) => String(n).padStart(l, '0');
  return `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())} ` +
    `${p(now.getHours())}:${p(now.getMinutes())}:${p(now.getSeconds())}.${p(now.getMilliseconds(), 3)}`;
}

const pct = (sorted, q) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];

async function runScenario(numClients) {
  const clients = [];
  const results = [];
  const prefix = `perftest${numClients}c`;

  for (let c = 1; c <= numClients; c++) {
    const clientId = `${prefix}n${c}`;
    const client = mqtt.connect(`mqtt://${HOST}:${PORT}`, { clientId });
    await new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error(`timeout conectando ${clientId}`)), 10000);
      client.on('connect', () => { clearTimeout(t); resolve(); });
      client.on('error', (e) => { clearTimeout(t); reject(e); });
    });
    const sentTimestamps = {};
    client.subscribe(`PluginResponseTime${clientId}/#`);
    client.on('message', (topic, message) => {
      const response = JSON.parse(message.toString());
      if (sentTimestamps[response.iteration]) {
        results.push({ client: clientId, iteration: response.iteration,
          latency: Date.now() - sentTimestamps[response.iteration] });
      }
    });
    clients.push({ client, clientId, sentTimestamps });
  }

  await new Promise(r => setTimeout(r, 500));

  for (let i = 1; i <= ITERATIONS; i++) {
    for (const { client, clientId, sentTimestamps } of clients) {
      sentTimestamps[i] = Date.now();
      client.publish(`PublisherResponseTime${clientId}/iteration${i}`, JSON.stringify({
        testResponsetime: true, localtime: formatTimestamp(), id: clientId, iteration: i
      }));
    }
    await new Promise(r => setTimeout(r, 100));
  }

  await new Promise(r => setTimeout(r, 3000));
  for (const { client } of clients) client.end();

  const lat = results.map(r => r.latency).sort((a, b) => a - b);
  const expected = numClients * ITERATIONS;
  return {
    clients: numClients,
    expected,
    received: lat.length,
    lost: expected - lat.length,
    avg: lat.length ? +(lat.reduce((a, b) => a + b, 0) / lat.length).toFixed(2) : null,
    min: lat[0] ?? null,
    p50: lat.length ? pct(lat, 0.50) : null,
    p95: lat.length ? pct(lat, 0.95) : null,
    max: lat[lat.length - 1] ?? null,
  };
}

(async () => {
  const out = [];
  for (const n of SCENARIOS) {
    process.stdout.write(`Cenario ${n} clientes x ${ITERATIONS} iteracoes... `);
    const r = await runScenario(n);
    console.log(`ok (${r.received}/${r.expected} respostas, media ${r.avg}ms)`);
    out.push(r);
    await new Promise(r2 => setTimeout(r2, 1500));
  }
  console.log('\nRESULT_JSON ' + JSON.stringify({
    date: formatTimestamp(), host: HOST, port: PORT, iterations: ITERATIONS, scenarios: out
  }));
})().catch(e => { console.error('ERRO:', e.message); process.exit(1); });
