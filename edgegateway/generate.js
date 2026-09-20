#!/usr/bin/env node
'use strict';
/*
 * M4 — Tabela única de rotas do edgegateway.
 *
 * A FONTE DE VERDADE é routes.json (uma entrada por rota, com metadados e as
 * superfícies em que ela existe). Este script:
 *
 *   node generate.js extract   — (uso único/auditoria) destila routes.json a
 *                                partir dos configs krakend existentes
 *   node generate.js build     — gera generated/krakend-{external,internal}.
 *                                {linux,windows}.json + os dois OpenAPI
 *   node generate.js verify    — compara o gerado com os configs legados e
 *                                imprime as diferenças (as intencionais estão
 *                                documentadas no README da pasta)
 *
 * Toda rota nova entra SOMENTE em routes.json (uma edição, quatro configs e
 * duas specs de graça).
 */
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const INFRA = path.resolve(HERE, '..');
const GEN = path.join(HERE, 'generated');

const LEGACY = {
  external: { linux: 'gateway-external/krakend.linux.json', windows: 'gateway-external/krakend.json' },
  internal: { linux: 'gateway-internal/krakend.linux.json', windows: 'gateway-internal/krakend.json' },
};

const readJson = f => JSON.parse(fs.readFileSync(f, 'utf8'));
const key = e => `${e.method || 'GET'} ${e.endpoint}`;

// ---------------------------------------------------------------- extract --
function extract() {
  const table = {
    backends: {
      external: { linux: 'https://ccws:44653', windows: 'https://host.docker.internal:44655' },
      internal: { linux: 'http://ccws:44652', windows: 'http://host.docker.internal:44654' },
    },
    surfaces: {
      external: { port: 44643, title: 'TV 3.0 WebServices — superficie externa' },
      internal: { port: 44642, title: 'TV 3.0 WebServices — superficie interna' },
    },
    // CORS unificado nas duas superficies (C.4.1.9 pede allow-origin * com
    // preflight; hoje so a variante linux do gateway interno declarava).
    cors: {
      allow_origins: ['*'],
      allow_methods: ['GET', 'POST', 'PUT', 'DELETE', 'OPTIONS'],
      allow_headers: ['Content-Type', 'Authorization', 'Accept-Version'],
      expose_headers: ['Content-Length'],
      max_age: '12h',
      allow_credentials: false,
    },
    routes: [],
  };

  const seen = new Map();
  for (const surface of ['internal', 'external']) {
    for (const variant of ['linux', 'windows']) {
      const cfg = readJson(path.join(INFRA, LEGACY[surface][variant]));
      for (const e of cfg.endpoints) {
        const k = key(e);
        if (!seen.has(k)) {
          const r = { path: e.endpoint, method: e.method || 'GET', surfaces: [] };
          if (e.input_headers) r.headers = e.input_headers;
          if (e.input_query_strings) r.query = e.input_query_strings;
          if (e.output_encoding === 'no-op') r.noop = true;
          seen.set(k, r);
          table.routes.push(r);
        }
        const r = seen.get(k);
        if (!r.surfaces.includes(surface)) r.surfaces.push(surface);
        if (e.output_encoding === 'no-op') r.noop = true;
      }
    }
  }
  fs.writeFileSync(path.join(HERE, 'routes.json'), JSON.stringify(table, null, 2) + '\n');
  console.log(`routes.json: ${table.routes.length} rotas extraidas`);
}

// ------------------------------------------------------------------ build --
function endpointFor(r, surface, host) {
  const e = { endpoint: r.path, method: r.method };
  if (r.headers) e.input_headers = r.headers;
  if (r.query) e.input_query_strings = r.query;
  if (r.noop) e.output_encoding = 'no-op';
  e.backend = [Object.assign({ url_pattern: r.backendPath || r.path, host: [host] },
                             r.noop ? { encoding: 'no-op' } : {})];
  return e;
}

function build() {
  const t = readJson(path.join(HERE, 'routes.json'));
  fs.mkdirSync(GEN, { recursive: true });

  for (const surface of Object.keys(t.surfaces)) {
    for (const variant of Object.keys(t.backends[surface])) {
      const host = t.backends[surface][variant];
      const cfg = {
        $schema: 'https://www.krakend.io/schema/v2.7/krakend.json',
        version: 3,
        name: `TV30 edgegateway — ${surface} (${variant})`,
        port: t.surfaces[surface].port,
        allow_insecure_connections: true,
        extra_config: { 'security/cors': t.cors },
        endpoints: t.routes.filter(r => r.surfaces.includes(surface))
                           .map(r => endpointFor(r, surface, host)),
      };
      const out = path.join(GEN, `krakend-${surface}.${variant}.json`);
      fs.writeFileSync(out, JSON.stringify(cfg, null, 2) + '\n');
      console.log(`${out}: ${cfg.endpoints.length} rotas, porta ${cfg.port}`);
    }
    // OpenAPI da superficie (metodos mesclados por path)
    const paths = {};
    for (const r of t.routes.filter(r => r.surfaces.includes(surface))) {
      paths[r.path] = paths[r.path] || {};
      paths[r.path][r.method.toLowerCase()] = {
        summary: r.path,
        parameters: [
          ...(r.headers || []).map(h => ({ name: h, in: 'header', required: h === 'Authorization', schema: { type: 'string' } })),
          ...(r.query || []).map(q => ({ name: q, in: 'query', required: false, schema: { type: 'string' } })),
        ],
        responses: { 200: { description: 'OK' }, 401: { description: 'Unauthorized' }, 500: { description: 'Internal Server Error' } },
      };
    }
    const spec = {
      openapi: '3.0.0',
      info: { title: t.surfaces[surface].title, version: '2.0', description: 'Gerado da tabela unica de rotas (M4) — edite routes.json, nao este arquivo.' },
      servers: [{ url: `http://localhost:${t.surfaces[surface].port}` }],
      paths,
    };
    fs.writeFileSync(path.join(GEN, `openapi-${surface}.json`), JSON.stringify(spec, null, 2) + '\n');
  }
}

// ----------------------------------------------------------------- verify --
function normalize(e) {
  return JSON.stringify({
    h: e.input_headers || [], q: e.input_query_strings || [],
    oe: e.output_encoding || '', be: (e.backend || []).map(b => ({ u: b.url_pattern, e: b.encoding || '' })),
  });
}
function verify() {
  let intentional = 0, unexpected = 0;
  for (const surface of Object.keys(LEGACY)) {
    for (const variant of Object.keys(LEGACY[surface])) {
      const legacy = readJson(path.join(INFRA, LEGACY[surface][variant]));
      const gen = readJson(path.join(GEN, `krakend-${surface}.${variant}.json`));
      const L = new Map(legacy.endpoints.map(e => [key(e), e]));
      const G = new Map(gen.endpoints.map(e => [key(e), e]));
      for (const [k, g] of G) {
        if (!L.has(k)) { console.log(`[+] ${surface}/${variant}: ${k} (unificacao de variantes — intencional)`); intentional++; }
        else if (normalize(L.get(k)) !== normalize(g)) {
          // no-op adicionado na unificacao tambem e intencional
          if (normalize(Object.assign({}, L.get(k), { output_encoding: 'no-op', backend: g.backend })) === normalize(g)) {
            console.log(`[~] ${surface}/${variant}: ${k} ganhou no-op (unificacao — intencional)`); intentional++;
          } else { console.log(`[!] ${surface}/${variant}: ${k} DIVERGE`); unexpected++; }
        }
      }
      for (const k of L.keys()) if (!G.has(k)) { console.log(`[-] ${surface}/${variant}: ${k} SUMIU`); unexpected++; }
    }
  }
  console.log(`\nverify: ${intentional} diferencas intencionais, ${unexpected} inesperadas`);
  process.exit(unexpected ? 1 : 0);
}

const mode = process.argv[2];
if (mode === 'extract') extract();
else if (mode === 'build') build();
else if (mode === 'verify') verify();
else { console.error('uso: node generate.js extract|build|verify'); process.exit(2); }
