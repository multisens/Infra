// Comparador de latência pub->sub (auto-eco via broker, sem depender do
// eco do plugin): N publishers -> 1 tópico -> 1 listener dedicado.
// Mesma cadência do brokertimetest: rajada simultânea, 100ms entre iterações.
// Uso: MQTT_HOST=<broker> TOPIC=<tópico> node headless-compare.js
const mqtt = require('mqtt');

const HOST = process.env.MQTT_HOST || 'localhost';
const PORT = process.env.MQTT_PORT || 1883;
const TOPIC = process.env.TOPIC || 'perftest/echo';
const LABEL = process.env.LABEL || `${HOST} ${TOPIC}`;
const ITERATIONS = 10;
const SCENARIOS = [10, 20, 50, 100];

const pct = (sorted, q) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];

function connect(clientId) {
  return new Promise((resolve, reject) => {
    const client = mqtt.connect(`mqtt://${HOST}:${PORT}`, { clientId });
    const t = setTimeout(() => reject(new Error(`timeout conectando ${clientId}`)), 10000);
    client.on('connect', () => { clearTimeout(t); resolve(client); });
    client.on('error', (e) => { clearTimeout(t); reject(e); });
  });
}

async function runScenario(numClients) {
  const sent = {};      // chave c<n>i<i> -> ts de envio
  const latencies = [];

  const listener = await connect(`cmplistener${numClients}`);
  listener.subscribe(TOPIC);
  listener.on('message', (topic, message) => {
    const key = JSON.parse(message.toString()).timestamp;
    if (sent[key] !== undefined) latencies.push(Date.now() - sent[key]);
  });

  const pubs = [];
  for (let c = 1; c <= numClients; c++) {
    pubs.push(await connect(`cmppub${numClients}c${c}`));
  }
  await new Promise(r => setTimeout(r, 500));

  for (let i = 1; i <= ITERATIONS; i++) {
    for (let c = 0; c < pubs.length; c++) {
      const key = `c${c + 1}i${i}`;
      sent[key] = Date.now();
      pubs[c].publish(TOPIC, JSON.stringify({ value: 1, timestamp: key }));
    }
    await new Promise(r => setTimeout(r, 100));
  }

  await new Promise(r => setTimeout(r, 3000));
  listener.end();
  for (const p of pubs) p.end();

  const lat = latencies.sort((a, b) => a - b);
  const expected = numClients * ITERATIONS;
  return {
    clients: numClients, expected, received: lat.length, lost: expected - lat.length,
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
    process.stdout.write(`[${LABEL}] ${n} clientes x ${ITERATIONS}... `);
    const r = await runScenario(n);
    console.log(`ok (${r.received}/${r.expected}, media ${r.avg}ms)`);
    out.push(r);
    await new Promise(r2 => setTimeout(r2, 1500));
  }
  console.log('RESULT_JSON ' + JSON.stringify({ label: LABEL, host: HOST, topic: TOPIC, scenarios: out }));
})().catch(e => { console.error('ERRO:', e.message); process.exit(1); });
