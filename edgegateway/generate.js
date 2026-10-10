#!/usr/bin/env node
'use strict';
/*
 * M4 — Tabela única de rotas do edgegateway.
 *
 * A FONTE DE VERDADE é routes.json (uma entrada por rota, com metadados e as
 * superfícies em que ela existe). Este script:
 *
 *   node generate.js build     — gera generated/krakend-{external,internal}.
 *                                <variante>.json (uma por backend de
 *                                routes.json: linux, windows, host) + os dois
 *                                OpenAPI
 *
 * (Os modos extract e verify, que liam os configs KrakenD anteriores à tabela
 * única em gateway-{external,internal}/, saíram em 10/10: esses arquivos não
 * existem mais.)
 *
 * Toda rota nova entra SOMENTE em routes.json (uma edição, as configs de
 * todas as variantes e as duas specs de graça).
 *
 * Politica de credencial por rota (item 9, plugin tv30-auth — ver
 * plugin/README.md): campo "auth" = none | token | token+bind (ausente =>
 * token) e "classes" = classes de cliente PERMITIDAS (ausente => todas;
 * valores do claim `class` do tv3ws: local-associated, local-autonomous,
 * non-local). O build falha em valor invalido.
 *
 * Campo opcional "timeout" (ex.: "15s"): espera maxima pelo backend nessa
 * rota; ausente => padrao do KrakenD (2s).
 *
 * Campo opcional "api" = {id, section, version}: a linha da Tabela C.2 da
 * norma que a rota implementa (rotas do testbed fora da norma nao tem). A
 * lista das APIs implementadas e as versoes das APIs C.6.7.8/C.6.7.9 saem
 * daqui (reuniao de 05/10 com o Joel, D-0510-3). O mesmo id em rotas
 * diferentes (ex.: current-service e {serviceContextId}) tem de repetir
 * secao e versao.
 *
 * Campo opcional "edge" = nome do handler do plugin que RESPONDE a rota na
 * propria borda, sem repasse ao tv3ws (reuniao de 05/10: C.6.8, D-0510-2, e
 * C.6.7.8/C.6.7.9, D-0510-3). Escolha documentada: o endpoint dessas rotas
 * CONTINUA gerado no KrakenD (mesmo backend das demais), mas o plugin
 * responde antes e ele nunca eh alcancado. Assim o caminho fica registrado
 * no roteador como o de qualquer rota — o preflight CORS (que passa ao
 * modulo CORS) segue o mesmo caminho ja testado, e a arvore do Gin nao muda
 * (caminho nao registrado ali pode cair no panic de KNOWN-ISSUES.md).
 */
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const GEN = path.join(HERE, 'generated');

const readJson = f => JSON.parse(fs.readFileSync(f, 'utf8'));

// ------------------------------------------------------------------ build --
// Plugin de validacao de credenciais na borda (item 9): compilado no
// Dockerfile e copiado para PLUGIN_FOLDER; as DUAS superficies o carregam.
const PLUGIN_NAME = 'tv30-auth';
const PLUGIN_FOLDER = '/opt/krakend/plugins/';
const AUTH_VALUES = ['none', 'token', 'token+bind'];
const CLASS_VALUES = ['local-associated', 'local-autonomous', 'non-local'];
// handlers do plugin (plugin/edge.go): o mesmo conjunto, para o build falhar
// antes do container (o plugin tambem recusa nome desconhecido ao subir)
const EDGE_HANDLERS = ['bind-context-register', 'bind-context-list', 'bind-context-remove', 'api-info', 'api-list'];
// formatos da Tabela C.2 / C.3.6.2 (os mesmos do plugin, apiinfo.go)
const API_ID = /^[a-z0-9]+(-[a-z0-9]+)+$/;
const API_SUBSYSTEMS = ['ncl', 'nclua', 'tv3ws'];
const API_SECTION = /^C(\.[0-9]+)+$/;
const API_VERSION = /^[1-9][0-9]*\.[0-9]+$/;

const authOf = r => r.auth || 'token';

// ordem da Tabela C.2 = ordem das secoes (C.6.1.2 < C.6.1.3 < ... < C.6.16.3)
function compareSections(a, b) {
  const pa = a.split('.').slice(1).map(Number), pb = b.split('.').slice(1).map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? -1) - (pb[i] ?? -1);
    if (d) return d;
  }
  return 0;
}

// APIs implementadas numa superficie (C.6.7.8/C.6.7.9): {id, version}, sem
// repeticao, na ordem da Tabela C.2.
function apisOf(routes) {
  const byId = new Map();
  for (const r of routes) if (r.api && !byId.has(r.api.id)) byId.set(r.api.id, r.api);
  return [...byId.values()]
    .sort((a, b) => compareSections(a.section, b.section) || a.id.localeCompare(b.id))
    .map(a => ({ id: a.id, version: a.version }));
}

// Erro na tabela derruba o build: config gerada errada viraria politica
// errada na borda, em silencio.
function validate(t) {
  const errs = [];
  const seen = new Set();
  const apiSeen = new Map();
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
    if (r.edge !== undefined) {
      if (!EDGE_HANDLERS.includes(r.edge)) errs.push(`${id}: edge "${r.edge}" invalido (${EDGE_HANDLERS.join(', ')})`);
      if (r.timeout !== undefined) errs.push(`${id}: timeout nao se aplica a rota respondida pela borda (edge)`);
      if (String(r.edge).startsWith('bind-context-') && !(t.auth && t.auth.current_service_context_id)) {
        errs.push(`${id}: edge ${r.edge} exige auth.current_service_context_id (devolvido pela C.6.8)`);
      }
    }
    if (r.api !== undefined) {
      const a = r.api;
      if (!a || typeof a !== 'object' || Array.isArray(a)) { errs.push(`${id}: api nao eh objeto {id, section, version}`); continue; }
      const extra = Object.keys(a).filter(k => !['id', 'section', 'version'].includes(k));
      if (extra.length) errs.push(`${id}: api com campo desconhecido (${extra.join(', ')})`);
      if (!API_ID.test(a.id || '') || !API_SUBSYSTEMS.includes(String(a.id).split('-')[0])) {
        errs.push(`${id}: api.id "${a.id}" invalido (<subsistema>-<nome>, subsistemas ${API_SUBSYSTEMS.join(', ')})`);
      }
      if (!API_SECTION.test(a.section || '')) errs.push(`${id}: api.section "${a.section}" invalida (ex.: C.6.3.1)`);
      if (!API_VERSION.test(a.version || '')) errs.push(`${id}: api.version "${a.version}" invalida (X.Y, C.3.6.2)`);
      const prev = apiSeen.get(a.id);
      if (prev && (prev.section !== a.section || prev.version !== a.version)) {
        errs.push(`${id}: api.id ${a.id} com secao/versao diferente de ${prev.route} (${prev.section} ${prev.version})`);
      }
      if (!prev) apiSeen.set(a.id, { section: a.section, version: a.version, route: id });
    }
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
  // Rota com "edge": o plugin responde antes e este backend nunca eh chamado
  // (ver o cabecalho deste arquivo: o endpoint so mantem o caminho no roteador).
  e.backend = [{ url_pattern: r.backendPath || r.path, host: [host], encoding: 'no-op' }];
  return e;
}

// Bloco do plugin tv30-auth: a politica da superficie (metodo+caminho com
// padroes {param}, auth, classes). Caminho nao listado aqui => erro 100 no
// plugin, sem chegar ao roteador.
function pluginConfig(t, surface) {
  const routes = t.routes.filter(r => r.surfaces.includes(surface));
  const c = {
    surface,
    routes: routes.map(r => {
      const p = { method: r.method, path: r.path, auth: authOf(r) };
      if (r.classes) p.classes = r.classes;
      if (r.edge) p.edge = r.edge; // respondida pelo plugin (edge.go)
      return p;
    }),
    // APIs implementadas nesta superficie, para a C.6.7.8/C.6.7.9
    apis: apisOf(routes),
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
    error: { type: 'integer', description: 'codigo da Tabela C.1 / C.3.3 (ex.: 100, 101, 104, 105, 106, 107, 108, 200, 300)' },
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
        description: (r.api ? `API ${r.api.id} (${r.api.section}, versao ${r.api.version}). ` : '') +
          (r.edge ? 'Respondida pela propria borda (plugin tv30-auth), sem repasse ao tv3ws. ' : '') +
          `Credencial: ${AUTH_DOC[auth]}. Classes: ${(r.classes || ['todas']).join(', ')}. ` +
          'O local associado dispensa access token e bind-token nas rotas que o admitem (C.4.1.1).',
        'x-tv30-auth': auth,
        ...(r.classes ? { 'x-tv30-classes': r.classes } : {}),
        ...(r.api ? { 'x-tv30-api': r.api } : {}),
        ...(r.edge ? { 'x-tv30-edge': r.edge } : {}),
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

const mode = process.argv[2];
if (mode === 'build') build();
else { console.error('uso: node generate.js build'); process.exit(2); }
