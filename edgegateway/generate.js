#!/usr/bin/env node
'use strict';
/*
 * Builds, from routes.json, the KrakenD configurations of the two surfaces
 * (one per backend variant) and the two OpenAPI files:
 * node generate.js build. Tests: node --test generate.test.js.
 */
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const GEN = path.join(HERE, 'generated');

const readJson = f => JSON.parse(fs.readFileSync(f, 'utf8'));

const PLUGIN_NAME = 'tv30-auth';
// Directory of the TLS certificate of the external surface.
const TLS_DIR = '/etc/tv30/tls';
const PLUGIN_FOLDER = '/opt/krakend/plugins/';
const AUTH_VALUES = ['none', 'token', 'token+bind'];
const CLASS_VALUES = ['local-associated', 'local-autonomous', 'non-local'];
// Names of the edge handlers of the plugin.
const EDGE_HANDLERS = ['bind-context-register', 'bind-context-list', 'bind-context-remove', 'api-info', 'api-list'];
// Formats of the API ids (Table C.2), sections and versions (C.3.6.2).
const API_ID = /^[a-z0-9]+(-[a-z0-9]+)+$/;
const API_SUBSYSTEMS = ['ncl', 'nclua', 'tv3ws'];
const API_SECTION = /^C(\.[0-9]+)+$/;
const API_VERSION = /^[1-9][0-9]*\.[0-9]+$/;
// Internal headers written by the plugin, the negotiated version and the
// original Host of the client; KrakenD passes on to the backend only the
// headers in input_headers.
const INTERNAL_VERSION_HEADER = 'X-TV30-API-Version';
const INTERNAL_HOST_HEADER = 'X-TV30-Host';
const INTERNAL_HEADERS = [INTERNAL_VERSION_HEADER, INTERNAL_HOST_HEADER];
// versions of a route: the "X.Y" versions served (C.3.6.2); when absent, only
// 2.0 (C.3.6.3). From 100 on, testbed extensions.
const DEFAULT_VERSIONS = ['2.0'];
const compareVersions = (a, b) => {
  const [am, an] = a.split('.').map(Number), [bm, bn] = b.split('.').map(Number);
  return am - bm || an - bn;
};
const versionsOf = r => [...(r.versions || DEFAULT_VERSIONS)].sort(compareVersions);
// Path from the edge to the service: for a testbed extension, /v<X.Y>
// followed by the public path; otherwise, the public path. The client uses
// only the public path, with Accept-Version.
const EXTENSION_MAJOR = 100;
const isExtensionVersion = v => Number(v.split('.')[0]) >= EXTENSION_MAJOR;
const internalPathOf = (p, v) => (isExtensionVersion(v) ? `/v${v}${p}` : p);
const INTERNAL_PREFIX = /^\/v[0-9]+\.[0-9]+(\/|$)/;
// Internal paths of a route, one per route and version pair; the versions of
// the standard share the public path.
const internalPathsOf = r => [...new Set(versionsOf(r).map(v => internalPathOf(r.path, v)))];

const authOf = r => r.auth || 'token';
// classes: those that may use the route, all when absent; the others get
// 106.
const classesOf = r => r.classes || CLASS_VALUES;

// C.4.1.8: C.6.1.2 "can only be accessed via the HTTP protocol".
const HTTP_ONLY_SECTIONS = ['C.6.1.2'];
// C.4.1.6: a non-local client over HTTP only uses the "authorization-related
// (C.6.1.2 and C.6.1.3)" APIs; on the others, 106.
const NONLOCAL_HTTP_SECTIONS = ['C.6.1.2', 'C.6.1.3'];
// no_service: the code of a token+bind route when no DTV service is
// selected, for an API whose table lists 300; without the field, the
// bind-token check gives 108.
const NO_SERVICE_VALUES = [300];
// not_in_catalog: the code of a token+bind route with {serviceContextId}
// when the service of the path is not in the Application Catalog (Tables
// C.71 and C.74).
const NOT_IN_CATALOG_VALUES = [304];

// Surfaces, with the keys of surfaces and backends of routes.json; secure is
// the Server-SecureBaseURL surface.
const SURFACE_RULES = {
  external: {
    secure: true,
    doc: 'Server-SecureBaseURL (C.3.4). Todas as rotas da tabela; as de uso exclusivo do cliente local e a C.6.1.2 respondem 106 a qualquer requisicao (C.4.1.8).',
  },
  internal: {
    secure: false,
    doc: 'Server-BaseURL (C.3.4), HTTP. Todas as rotas da tabela; o cliente nao local so usa a C.6.1.2 e a C.6.1.3 e recebe 106 nas demais (C.4.1.6).',
  },
};

// surfacesOf returns the surfaces where a route exists: all of them.
function surfacesOf() {
  return Object.keys(SURFACE_RULES);
}

const routesOf = t => t.routes;

// surfaceErrorOf returns the code the route answers to any request on this
// surface, 0 for none: 106 of C.4.1.8, only on the external surface.
function surfaceErrorOf(r, surface) {
  if (!SURFACE_RULES[surface].secure) return 0;
  const exclusivaDoLocal = !classesOf(r).includes('non-local');
  const soHTTP = !!r.api && HTTP_ONLY_SECTIONS.includes(r.api.section);
  return exclusivaDoLocal || soHTTP ? 106 : 0;
}

// Whether the route is one a non-local client uses over HTTP (C.4.1.6).
const nonLocalHTTP = r => !!r.api && NONLOCAL_HTTP_SECTIONS.includes(r.api.section);

// compareSections orders sections numerically (C.6.1.2 < C.6.1.3 < ... <
// C.6.16.3), the order of Table C.2.
function compareSections(a, b) {
  const pa = a.split('.').slice(1).map(Number), pb = b.split('.').slice(1).map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? -1) - (pb[i] ?? -1);
    if (d) return d;
  }
  return 0;
}

// apisOf returns the APIs implemented on a surface (C.6.7.8, C.6.7.9):
// {id, version}, without repetition, in the order of Table C.2.
function apisOf(routes) {
  const byId = new Map();
  for (const r of routes) if (r.api && !byId.has(r.api.id)) byId.set(r.api.id, r.api);
  return [...byId.values()]
    .sort((a, b) => compareSections(a.section, b.section) || a.id.localeCompare(b.id))
    .map(a => ({ id: a.id, version: a.version }));
}

// checkTable returns the list of errors of the table, empty for a valid
// table; validate exits the process if there is any.
function checkTable(t) {
  const errs = [];
  const seen = new Set();
  const apiSeen = new Map();
  // Surfaces: the same as SURFACE_RULES, with port and backends.
  const known = Object.keys(SURFACE_RULES);
  const declared = Object.keys(t.surfaces || {});
  for (const s of declared) if (!known.includes(s)) errs.push(`surfaces.${s}: superficie sem regra em SURFACE_RULES (${known.join(', ')})`);
  for (const s of known) {
    if (!declared.includes(s)) errs.push(`surfaces.${s}: superficie ausente`);
    else if (!Number.isInteger(t.surfaces[s].port)) errs.push(`surfaces.${s}.port: porta ausente ou nao inteira`);
    const b = (t.backends || {})[s];
    if (!b || typeof b !== 'object' || !Object.keys(b).length) errs.push(`backends.${s}: sem variante de backend`);
  }
  // cors: only what the plugin uses; ACAO and ACAM are fixed at *, the
  // default of C.4.1.9.3.
  const cors = t.cors;
  if (!cors || typeof cors !== 'object' || Array.isArray(cors)) errs.push('cors: bloco ausente');
  else {
    const CORS_KEYS = ['allow_headers', 'expose_headers', 'max_age'];
    for (const k of Object.keys(cors)) if (!CORS_KEYS.includes(k)) errs.push(`cors.${k}: campo sem uso (o plugin le ${CORS_KEYS.join(', ')}; ACAO e ACAM sao * pela C.4.1.9.3)`);
    const lista = v => Array.isArray(v) && v.every(h => typeof h === 'string' && /^[A-Za-z0-9-]+$/.test(h));
    if (!lista(cors.allow_headers) || !cors.allow_headers.length) errs.push('cors.allow_headers: lista nao vazia de nomes de cabecalho');
    if (cors.expose_headers !== undefined && !lista(cors.expose_headers)) errs.push('cors.expose_headers: lista de nomes de cabecalho');
    if (cors.max_age !== undefined && !/^[1-9][0-9]*(s|m|h)$/.test(cors.max_age)) errs.push(`cors.max_age "${cors.max_age}" invalido (ex.: 12h, 600s)`);
  }
  if (t.auth !== undefined) errs.push('auth: bloco sem uso: o serviceContextId e o globalServiceId do servico, sem constante');
  for (const r of t.routes) {
    const id = `${r.method} ${r.path}`;
    if (seen.has(id)) errs.push(`${id}: rota duplicada`);
    seen.add(id);
    if (INTERNAL_PREFIX.test(r.path || '')) errs.push(`${id}: caminho com o prefixo /v<X.Y>, reservado ao caminho interno (borda -> servico)`);
    if (r.surfaces !== undefined) errs.push(`${id}: o campo "surfaces" nao vale por rota: as duas superficies tem todas as rotas (SURFACE_RULES)`);
    if (r.auth !== undefined && !AUTH_VALUES.includes(r.auth)) errs.push(`${id}: auth "${r.auth}" invalido (${AUTH_VALUES.join(', ')})`);
    if (r.classes !== undefined) {
      if (!Array.isArray(r.classes)) errs.push(`${id}: classes nao eh lista`);
      else for (const c of r.classes) if (!CLASS_VALUES.includes(c)) errs.push(`${id}: classe "${c}" invalida (${CLASS_VALUES.join(', ')})`);
    }
    if (!(r.headers || []).includes('Accept-Version')) errs.push(`${id}: toda rota repassa Accept-Version`);
    if (r.timeout !== undefined && !/^[1-9][0-9]*(ms|s)$/.test(r.timeout)) errs.push(`${id}: timeout "${r.timeout}" invalido (ex.: 15s, 500ms)`);
    if (r.versions !== undefined) {
      if (!Array.isArray(r.versions) || !r.versions.length) errs.push(`${id}: versions nao eh lista nao vazia`);
      else {
        for (const v of r.versions) if (typeof v !== 'string' || !API_VERSION.test(v)) errs.push(`${id}: versions com "${v}" fora do formato X.Y (C.3.6.2)`);
        if (new Set(r.versions).size !== r.versions.length) errs.push(`${id}: versions com versao repetida`);
      }
    }
    for (const ih of INTERNAL_HEADERS) {
      if (r.headers && r.headers.some(h => h.toLowerCase() === ih.toLowerCase())) {
        errs.push(`${id}: ${ih} e cabecalho interno da borda; nao vai em headers`);
      }
    }
    if (r.no_service !== undefined) {
      if (!NO_SERVICE_VALUES.includes(r.no_service)) errs.push(`${id}: no_service ${JSON.stringify(r.no_service)} invalido (${NO_SERVICE_VALUES.join(', ')})`);
      if (authOf(r) !== 'token+bind') errs.push(`${id}: no_service so vale em rota token+bind`);
    }
    if (r.not_in_catalog !== undefined) {
      if (!NOT_IN_CATALOG_VALUES.includes(r.not_in_catalog)) errs.push(`${id}: not_in_catalog ${JSON.stringify(r.not_in_catalog)} invalido (${NOT_IN_CATALOG_VALUES.join(', ')})`);
      if (authOf(r) !== 'token+bind' || !String(r.path).split('/').includes('{serviceContextId}')) {
        errs.push(`${id}: not_in_catalog so vale em rota token+bind com {serviceContextId}`);
      }
    }
    if (r.edge !== undefined) {
      if (!EDGE_HANDLERS.includes(r.edge)) errs.push(`${id}: edge "${r.edge}" invalido (${EDGE_HANDLERS.join(', ')})`);
      if (r.timeout !== undefined) errs.push(`${id}: timeout nao se aplica a rota respondida pela borda (edge)`);
    }
    // api: the row of Table C.2 the route implements; a route outside the
    // standard has none.
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
      // C.6.7.8 reports a single version, the "latest supported version"
      // (Table C.47): the latest one the route serves.
      else if (Array.isArray(versionsOf(r)) && versionsOf(r).every(v => API_VERSION.test(v))) {
        const ultima = versionsOf(r).slice(-1)[0];
        if (a.version !== ultima) errs.push(`${id}: api.version ${a.version} diferente da mais recente que a rota serve (${ultima})`);
      }
      const prev = apiSeen.get(a.id);
      if (prev && (prev.section !== a.section || prev.version !== a.version)) {
        errs.push(`${id}: api.id ${a.id} com secao/versao diferente de ${prev.route} (${prev.section} ${prev.version})`);
      }
      if (!prev) apiSeen.set(a.id, { section: a.section, version: a.version, route: id });
    }
  }
  // A surface without a route is an error.
  if (!errs.length) {
    for (const s of known) if (!routesOf(t, s).length) errs.push(`surfaces.${s}: nenhuma rota (regra: ${SURFACE_RULES[s].doc})`);
  }
  return errs;
}

function validate(t) {
  const errs = checkTable(t);
  if (errs.length) {
    console.error('routes.json invalido:\n  ' + errs.join('\n  '));
    process.exit(1);
  }
}

// headersOf returns the headers passed on to the backend: the declared ones,
// the bind-token on token+bind routes, and the internal headers.
function headersOf(r) {
  const h = [...(r.headers || [])];
  if (authOf(r) === 'token+bind' && !h.includes('bind-token')) h.push('bind-token');
  h.push(...INTERNAL_HEADERS);
  return h;
}

// endpointsFor returns one endpoint per internal path of the route, with the
// same path on the backend of the surface.
function endpointsFor(r, host) {
  return internalPathsOf(r).map(p => {
    const e = { endpoint: p, method: r.method };
    const headers = headersOf(r);
    if (headers.length) e.input_headers = headers;
    if (r.query) e.input_query_strings = r.query;
    // no-op: without it, KrakenD replaces a backend error (404 and a C.3.2
    // body) with a 500 without body.
    e.output_encoding = 'no-op';
    // Without "timeout", the KrakenD default of 2 s applies.
    if (r.timeout) e.timeout = r.timeout;
    // For a route with "edge", the plugin answers first and the backend is
    // never called; the endpoint keeps the path in the router.
    e.backend = [{ url_pattern: p, host: [host], encoding: 'no-op' }];
    return e;
  });
}

// pluginConfig returns the tv30-auth block of the surface: method and path
// with {param} patterns, auth, classes and the derived fields below. A path
// not listed gets error 100 from the plugin, without reaching the router.
function pluginConfig(t, surface) {
  const routes = routesOf(t, surface);
  const secure = SURFACE_RULES[surface].secure;
  const c = {
    surface,
    routes: routes.map(r => {
      const p = { method: r.method, path: r.path, auth: authOf(r) };
      if (r.classes) p.classes = r.classes;
      // Answered by the plugin (edge.go).
      if (r.edge) p.edge = r.edge;
      const se = surfaceErrorOf(r, surface);
      // 106 to any request (C.4.1.8).
      if (se) p.surface_error = se;
      // C.4.1.6
      if (!secure && nonLocalHTTP(r)) p.nonlocal_http = true;
      if (r.no_service) p.no_service = r.no_service;
      if (r.not_in_catalog) p.not_in_catalog = r.not_in_catalog;
      p.versions = versionsOf(r);
      return p;
    }),
    // Both surfaces list every API.
    apis: apisOf(routes),
  };
  // C.4.1.6: on the HTTP surface a non-local client only uses the
  // nonlocal_http routes.
  if (!secure) c.nonlocal_https_only = true;
  // CORS (C.4.1.9), answered by the plugin: allow_headers on every OPTIONS,
  // max_age on the preflight, expose_headers on every response.
  if (t.cors && Array.isArray(t.cors.allow_headers)) c.cors_allow_headers = t.cors.allow_headers;
  if (t.cors && Array.isArray(t.cors.expose_headers)) c.cors_expose_headers = t.cors.expose_headers;
  if (t.cors && typeof t.cors.max_age === 'string') c.cors_max_age = t.cors.max_age;
  return c;
}

// Error body of the standard (C.3.2): every failure answers 404 with it.
const ERROR_SCHEMA = {
  type: 'object',
  required: ['error', 'description'],
  properties: {
    error: { type: 'integer', description: 'codigo da Tabela C.1 / C.3.3 (ex.: 100, 101, 104, 105, 106, 107, 108, 200, 300, 304)' },
    description: { type: 'string' },
  },
};

const AUTH_DOC = {
  none: 'sem credencial',
  token: 'access token (Authorization: Bearer)',
  'token+bind': 'access token + bind-token (Security requirements = shall)',
};

// generate builds every file of an already validated table, without writing
// them: { '<name>.json': object }.
function generate(t) {
  const files = {};
  for (const surface of Object.keys(t.surfaces)) {
    const routes = routesOf(t, surface);
    for (const variant of Object.keys(t.backends[surface])) {
      const host = t.backends[surface][variant];
      files[`krakend-${surface}.${variant}.json`] = {
        $schema: 'https://www.krakend.io/schema/v2.7/krakend.json',
        version: 3,
        name: `TV30 edgegateway — ${surface} (${variant})`,
        port: t.surfaces[surface].port,
        ...(SURFACE_RULES[surface].secure ? { tls: { public_key: `${TLS_DIR}/cert.pem`, private_key: `${TLS_DIR}/key.pem`, min_version: 'TLS12' } } : {}),
        allow_insecure_connections: true,
        plugin: { pattern: '.so', folder: PLUGIN_FOLDER },
        // No 'security/cors': the KrakenD CORS module wraps the plugin and
        // would answer the preflight before it.
        extra_config: {
          'plugin/http-server': { name: [PLUGIN_NAME], [PLUGIN_NAME]: pluginConfig(t, surface) },
        },
        endpoints: routes.flatMap(r => endpointsFor(r, host)),
      };
    }
    // OpenAPI of the surface, methods merged by path.
    const paths = {};
    for (const r of routes) {
      const auth = authOf(r);
      const where = surfacesOf(r);
      const se = surfaceErrorOf(r, surface);
      paths[r.path] = paths[r.path] || {};
      paths[r.path][r.method.toLowerCase()] = {
        summary: r.path,
        description: (r.api ? `API ${r.api.id} (${r.api.section}, versao ${r.api.version}). ` : '') +
          (r.edge ? 'Respondida pela propria borda (plugin tv30-auth), sem repasse ao tv3ws. ' : '') +
          `Credencial: ${AUTH_DOC[auth]}. Classes: ${(r.classes || ['todas']).join(', ')}. ` +
          (se ? `Nesta superficie responde ${se} a qualquer requisicao: so pelo Server-BaseURL (C.4.1.8). ` : '') +
          (!SURFACE_RULES[surface].secure && !nonLocalHTTP(r) && classesOf(r).includes('non-local')
            ? 'O cliente nao local recebe 106 nesta superficie (HTTP): use o Server-SecureBaseURL (C.4.1.6). ' : '') +
          (r.no_service ? `Sem servico DTV selecionado: ${r.no_service}. ` : '') +
          (r.not_in_catalog ? `Servico do caminho fora do Application Catalog: ${r.not_in_catalog}. ` : '') +
          `Versoes servidas (Accept-Version): ${versionsOf(r).join(', ')}; outra da 100. ` +
          'O local associado dispensa access token nas rotas que o admitem e, no proprio contexto de servico, o bind-token (C.4.1.1).',
        'x-tv30-auth': auth,
        ...(r.classes ? { 'x-tv30-classes': r.classes } : {}),
        'x-tv30-surfaces': where,
        'x-tv30-versions': versionsOf(r),
        ...(se ? { 'x-tv30-surface-error': se } : {}),
        ...(r.api ? { 'x-tv30-api': r.api } : {}),
        ...(r.edge ? { 'x-tv30-edge': r.edge } : {}),
        parameters: [
          ...headersOf(r).filter(h => !INTERNAL_HEADERS.includes(h)).map(h => ({ name: h, in: 'header', required: h === 'Authorization' && auth !== 'none', schema: { type: 'string' } })),
          ...(r.query || []).map(q => ({ name: q, in: 'query', required: false, schema: { type: 'string' } })),
        ],
        responses: {
          200: { description: 'OK' },
          404: { description: 'Erro (C.3.2): status 404 + {error, description}', content: { 'application/json': { schema: { $ref: '#/components/schemas/Erro' } } } },
        },
      };
    }
    files[`openapi-${surface}.json`] = {
      openapi: '3.0.0',
      info: {
        title: t.surfaces[surface].title,
        version: '2.0',
        description: 'Gerado de routes.json — edite la, nao este arquivo. ' + SURFACE_RULES[surface].doc,
      },
      servers: [{ url: `${SURFACE_RULES[surface].secure ? 'https' : 'http'}://localhost:${t.surfaces[surface].port}` }],
      paths,
      components: { schemas: { Erro: ERROR_SCHEMA } },
    };
  }
  return files;
}

function build() {
  const t = readJson(path.join(HERE, 'routes.json'));
  validate(t);
  fs.mkdirSync(GEN, { recursive: true });
  for (const [name, obj] of Object.entries(generate(t))) {
    const out = path.join(GEN, name);
    fs.writeFileSync(out, JSON.stringify(obj, null, 2) + '\n');
    if (obj.endpoints) console.log(`${out}: ${obj.endpoints.length} endpoints, porta ${obj.port}`);
  }
  for (const surface of Object.keys(t.surfaces)) {
    const se = t.routes.filter(r => surfaceErrorOf(r, surface)).map(r => `${r.method} ${r.path}`);
    console.log(`superficie ${surface}: ${t.routes.length} rotas` + (se.length ? `; 106 a qualquer requisicao (C.4.1.8): ${se.join(', ')}` : ''));
  }
}

module.exports = { SURFACE_RULES, CLASS_VALUES, surfacesOf, routesOf, surfaceErrorOf, checkTable, pluginConfig, apisOf, internalPathOf, internalPathsOf, generate };

if (require.main === module) {
  const mode = process.argv[2];
  if (mode === 'build') build();
  else { console.error('uso: node generate.js build'); process.exit(2); }
}
