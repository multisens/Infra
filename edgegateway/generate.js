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
 *
 * Politica de credencial por rota (item 9, plugin tv30-auth — ver
 * plugin/README.md): campo "auth" = none | token | token+bind (ausente =>
 * token) e "classes" = classes de cliente PERMITIDAS (ausente => todas;
 * valores do claim `class` do tv3ws: local-associated, local-autonomous,
 * non-local). O build falha em valor invalido.
 *
 * Campo opcional "timeout" (ex.: "15s"): espera maxima pelo backend nessa
 * rota; ausente => padrao do KrakenD (2s).
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
      external: { linux: 'https://tv3ws:44653', windows: 'https://host.docker.internal:44655' },
      internal: { linux: 'http://tv3ws:44652', windows: 'http://host.docker.internal:44654' },
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
// Plugin de validacao de credenciais na borda (item 9): compilado no
// Dockerfile e copiado para PLUGIN_FOLDER; as DUAS superficies o carregam.
const PLUGIN_NAME = 'tv30-auth';
const PLUGIN_FOLDER = '/opt/krakend/plugins/';
const AUTH_VALUES = ['none', 'token', 'token+bind'];
const CLASS_VALUES = ['local-associated', 'local-autonomous', 'non-local'];

const authOf = r => r.auth || 'token';

// Erro na tabela derruba o build: config gerada errada viraria politica
// errada na borda, em silencio.
function validate(t) {
  const errs = [];
  const seen = new Set();
  for (const r of t.routes) {
    const id = `${r.method} ${r.path}`;
    if (seen.has(id)) errs.push(`${id}: rota duplicada`);
    seen.add(id);
    if (r.auth !== undefined && !AUTH_VALUES.includes(r.auth)) errs.push(`${id}: auth "${r.auth}" invalido (${AUTH_VALUES.join(', ')})`);
    if (r.classes !== undefined) {
      if (!Array.isArray(r.classes)) errs.push(`${id}: classes nao eh lista`);
      else for (const c of r.classes) if (!CLASS_VALUES.includes(c)) errs.push(`${id}: classe "${c}" invalida (${CLASS_VALUES.join(', ')})`);
    }
    if (!(r.headers || []).includes('Accept-Version')) errs.push(`${id}: toda rota repassa Accept-Version`);
    if (r.timeout !== undefined && !/^[1-9][0-9]*(ms|s)$/.test(r.timeout)) errs.push(`${id}: timeout "${r.timeout}" invalido (ex.: 15s, 500ms)`);
  }
  if (errs.length) {
    console.error('routes.json invalido:\n  ' + errs.join('\n  '));
    process.exit(1);
  }
}

// Cabecalhos repassados ao tv3ws: os declarados + bind-token nas rotas
// token+bind (a borda valida, e o tv3ws continua recebendo).
function headersOf(r) {
  const h = [...(r.headers || [])];
  if (authOf(r) === 'token+bind' && !h.includes('bind-token')) h.push('bind-token');
  return h;
}

function endpointFor(r, surface, host) {
  const e = { endpoint: r.path, method: r.method };
  const headers = headersOf(r);
  if (headers.length) e.input_headers = headers;
  if (r.query) e.input_query_strings = r.query;
  // Toda rota eh proxy puro (no-op): status e corpo do backend passam
  // intactos. Sem isso o KrakenD engole o erro do tv3ws (404 + corpo
  // C.3.2 {error, description}) e devolve 500 sem corpo — quebraria a
  // camada comum de erro do item 6.
  e.output_encoding = 'no-op';
  // Espera maxima pelo tv3ws. Sem o campo vale o padrao do KrakenD (2s).
  // GET /tv3/authorize espera a resposta do espectador ao pop-up (10 s no
  // tv3ws, client-identification/service.ts): com 2s a borda devolvia 500
  // enquanto o tv3ws seguia e autorizava o cliente (medido em 02/10).
  if (r.timeout) e.timeout = r.timeout;
  e.backend = [{ url_pattern: r.backendPath || r.path, host: [host], encoding: 'no-op' }];
  return e;
}

// Bloco do plugin tv30-auth: a politica da superficie (metodo+caminho com
// padroes {param}, auth, classes). Caminho nao listado aqui => erro 100 no
// plugin, sem chegar ao roteador.
function pluginConfig(t, surface) {
  const c = {
    surface,
    routes: t.routes.filter(r => r.surfaces.includes(surface)).map(r => {
      const p = { method: r.method, path: r.path, auth: authOf(r) };
      if (r.classes) p.classes = r.classes;
      return p;
    }),
  };
  // PENDENTE (Joel): lacuna L2 — serviceContextId constante do tv3ws
  // (tv3ws/src/core.ts): nas rotas /tv3/{serviceContextId}/... o plugin
  // trata este valor (e current-service) como o servico corrente.
  // PENDENTE (Joel): POST /tv3/{serviceContextId}/users nao existe na norma
  // (so POST /tv3/current-service/users, C.6.14.1, bind-token shall), mas o
  // tv3ws a atende com o MESMO handler. Esta como token+bind (e a regra L2
  // acima) para nao abrir um atalho sem bind-token; falta decidir se a rota
  // fica ou sai da tabela.
  if (t.auth && t.auth.current_service_context_id) c.current_service_context_id = t.auth.current_service_context_id;
  // Cabecalhos que o plugin devolve em Access-Control-Allow-Headers no
  // OPTIONS que nao e preflight (C.4.1.9.3): os mesmos do modulo CORS.
  if (t.cors && Array.isArray(t.cors.allow_headers)) c.cors_allow_headers = t.cors.allow_headers;
  return c;
}

// Corpo de erro da norma (C.3.2): toda falha responde 404 com este corpo.
const ERROR_SCHEMA = {
  type: 'object',
  required: ['error', 'description'],
  properties: {
    error: { type: 'integer', description: 'codigo da Tabela C.1 / C.3.3 (ex.: 100, 104, 106, 107, 108, 200)' },
    description: { type: 'string' },
  },
};

const AUTH_DOC = {
  none: 'sem credencial',
  token: 'access token (Authorization: Bearer)',
  'token+bind': 'access token + bind-token (Security requirements = shall)',
};

function build() {
  const t = readJson(path.join(HERE, 'routes.json'));
  validate(t);
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
        plugin: { pattern: '.so', folder: PLUGIN_FOLDER },
        extra_config: {
          'security/cors': t.cors,
          'plugin/http-server': { name: [PLUGIN_NAME], [PLUGIN_NAME]: pluginConfig(t, surface) },
        },
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
      const auth = authOf(r);
      paths[r.path] = paths[r.path] || {};
      paths[r.path][r.method.toLowerCase()] = {
        summary: r.path,
        description: `Credencial: ${AUTH_DOC[auth]}. Classes: ${(r.classes || ['todas']).join(', ')}. ` +
          'O local associado dispensa access token e bind-token nas rotas que o admitem (C.4.1.1).',
        'x-tv30-auth': auth,
        ...(r.classes ? { 'x-tv30-classes': r.classes } : {}),
        parameters: [
          ...headersOf(r).map(h => ({ name: h, in: 'header', required: h === 'Authorization' && auth !== 'none', schema: { type: 'string' } })),
          ...(r.query || []).map(q => ({ name: q, in: 'query', required: false, schema: { type: 'string' } })),
        ],
        responses: {
          200: { description: 'OK' },
          404: { description: 'Erro (C.3.2): status 404 + {error, description}', content: { 'application/json': { schema: { $ref: '#/components/schemas/Erro' } } } },
        },
      };
    }
    const spec = {
      openapi: '3.0.0',
      info: { title: t.surfaces[surface].title, version: '2.0', description: 'Gerado da tabela unica de rotas (M4) — edite routes.json, nao este arquivo.' },
      servers: [{ url: `http://localhost:${t.surfaces[surface].port}` }],
      paths,
      components: { schemas: { Erro: ERROR_SCHEMA } },
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
