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

const PLUGIN_NAME = 'tv30-auth';
// Directory of the TLS certificate of the external surface.
const TLS_DIR = '/etc/tv30/tls';
const PLUGIN_FOLDER = '/opt/krakend/plugins/';
const AUTH_VALUES = ['none', 'token', 'token+bind'];
const CLASS_NON_LOCAL = 'nonLocal';
const CLASS_VALUES = ['associated', 'standAlone', CLASS_NON_LOCAL];
// Names of the edge handlers of the plugin.
const EDGE_HANDLERS = ['bind-context-register', 'bind-context-list', 'bind-context-remove', 'api-info', 'api-list'];
// Formats of the API ids (Table C.2), sections and versions (C.3.6.2).
const API_ID = /^[a-z0-9]+(-[a-z0-9]+)+$/;
const API_SUBSYSTEMS = ['ncl', 'nclua', 'tv3ws'];
const API_SECTION = /^C(\.[0-9]+)+$/;
const API_VERSION = /^[1-9][0-9]*\.[0-9]+$/;
const API_KEYS = ['id', 'section', 'version'];
// Internal headers written by the plugin, the negotiated version and the
// original Host of the client; KrakenD passes on to the backend only the
// headers in input_headers.
const INTERNAL_VERSION_HEADER = 'X-TV30-API-Version';
const INTERNAL_HOST_HEADER = 'X-TV30-Host';
const INTERNAL_HEADERS = [INTERNAL_VERSION_HEADER, INTERNAL_HOST_HEADER];
// versions of a route: the "X.Y" versions served (C.3.6.2); when absent, only
// 2.0 (C.3.6.3). From 100 on, testbed extensions.
const DEFAULT_VERSIONS = ['2.0'];
// Path from the edge to the service: for a testbed extension, /v<X.Y>
// followed by the public path; otherwise, the public path. The client uses
// only the public path, with Accept-Version.
const EXTENSION_MAJOR = 100;
const INTERNAL_PREFIX = /^\/v[0-9]+\.[0-9]+(\/|$)/;
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
// Keys of the cors block that the plugin reads.
const CORS_KEYS = ['allow_headers', 'expose_headers', 'max_age'];
const HEADER_NAME = /^[A-Za-z0-9-]+$/;
const MAX_AGE_FORMAT = /^[1-9][0-9]*(s|m|h)$/;
const TIMEOUT_FORMAT = /^[1-9][0-9]*(ms|s)$/;
const SURFACE_ERROR_CODE = 106;
// Surfaces, with the keys of surfaces and backends of routes.json; secure is
// the Server-SecureBaseURL surface.
const SURFACE_RULES = {
  external: {
    secure: true,
    doc: 'Server-SecureBaseURL (C.3.4). Every route of the table; those exclusive to the local client and C.6.1.2 answer 106 to any request (C.4.1.8).',
  },
  internal: {
    secure: false,
    doc: 'Server-BaseURL (C.3.4), HTTP. Every route of the table; the non-local client only uses C.6.1.2 and C.6.1.3 and gets 106 on the others (C.4.1.6).',
  },
};
// Error body of the standard (C.3.2): every failure answers 404 with it.
const ERROR_SCHEMA = {
  type: 'object',
  required: ['error', 'description'],
  properties: {
    error: { type: 'integer', description: 'code of Table C.1 / C.3.3 (e.g. 100, 101, 104, 105, 106, 107, 108, 200, 300, 304)' },
    description: { type: 'string' },
  },
};
const AUTH_DOC = {
  none: 'no credential',
  token: 'access token (Authorization: Bearer)',
  'token+bind': 'access token + bind-token (Security requirements = shall)',
};

// checkTable returns the list of errors of the table, empty for a valid
// table; validate exits the process if there is any.
function checkTable(t) {
  const errs = [];
  errs.push(...checkSurfaces(t));
  errs.push(...checkCors(t.cors));
  if (t.auth !== undefined) {
    errs.push('auth: unused block: the serviceContextId and the globalServiceId come from the service, with no constant');
  }
  errs.push(...checkRoutes(t.routes));
  // A surface without a route is an error.
  if (errs.length === 0) {
    for (const s of Object.keys(SURFACE_RULES)) {
      if (routesOf(t).length === 0) {
        errs.push(`surfaces.${s}: no route (rule: ${SURFACE_RULES[s].doc})`);
      }
    }
  }
  return errs;
}

function validate(t) {
  const errs = checkTable(t);
  if (errs.length > 0) {
    console.error('invalid routes.json:\n  ' + errs.join('\n  '));
    process.exit(1);
  }
}

// generate builds every file of an already validated table, without writing
// them: { '<name>.json': object }.
function generate(t) {
  const files = {};
  for (const surface of Object.keys(t.surfaces)) {
    const routes = routesOf(t);
    for (const variant of Object.keys(t.backends[surface])) {
      const host = t.backends[surface][variant];
      files[`krakend-${surface}.${variant}.json`] = krakendConfig(t, surface, variant, host, routes);
    }
    files[`openapi-${surface}.json`] = openApi(t, surface, routes);
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
    if (obj.endpoints) {
      console.log(`${out}: ${obj.endpoints.length} endpoints, port ${obj.port}`);
    }
  }
  for (const surface of Object.keys(t.surfaces)) {
    const withError = t.routes.filter(r => surfaceErrorOf(r, surface)).map(r => `${r.method} ${r.path}`);
    let line = `surface ${surface}: ${t.routes.length} routes`;
    if (withError.length > 0) {
      line += `; 106 to any request (C.4.1.8): ${withError.join(', ')}`;
    }
    console.log(line);
  }
}

// pluginConfig returns the tv30-auth block of the surface: method and path
// with {param} patterns, auth, classes and the derived fields below. A path
// not listed gets error 100 from the plugin, without reaching the router.
function pluginConfig(t, surface) {
  const routes = routesOf(t);
  const secure = SURFACE_RULES[surface].secure;
  const c = {
    surface,
    routes: routes.map(r => pluginRoute(r, surface, secure)),
    // Both surfaces list every API.
    apis: apisOf(routes),
  };
  // C.4.1.6: on the HTTP surface a non-local client only uses the
  // nonlocal_http routes.
  if (!secure) {
    c.nonlocal_https_only = true;
  }
  // CORS (C.4.1.9), answered by the plugin: allow_headers on every OPTIONS,
  // max_age on the preflight, expose_headers on every response.
  if (t.cors && Array.isArray(t.cors.allow_headers)) {
    c.cors_allow_headers = t.cors.allow_headers;
  }
  if (t.cors && Array.isArray(t.cors.expose_headers)) {
    c.cors_expose_headers = t.cors.expose_headers;
  }
  if (t.cors && typeof t.cors.max_age === 'string') {
    c.cors_max_age = t.cors.max_age;
  }
  return c;
}

// surfacesOf returns the surfaces where a route exists: all of them.
function surfacesOf() {
  return Object.keys(SURFACE_RULES);
}

function routesOf(t) {
  return t.routes;
}

// surfaceErrorOf returns the code the route answers to any request on this
// surface, 0 for none: 106 of C.4.1.8, only on the external surface.
function surfaceErrorOf(r, surface) {
  if (!SURFACE_RULES[surface].secure) {
    return 0;
  }
  const localOnly = !classesOf(r).includes(CLASS_NON_LOCAL);
  const httpOnly = !!r.api && HTTP_ONLY_SECTIONS.includes(r.api.section);
  return localOnly || httpOnly ? SURFACE_ERROR_CODE : 0;
}

// apisOf returns the APIs implemented on a surface (C.6.7.8, C.6.7.9):
// {id, version}, without repetition, in the order of Table C.2.
function apisOf(routes) {
  const byId = new Map();
  for (const r of routes) {
    if (r.api && !byId.has(r.api.id)) {
      byId.set(r.api.id, r.api);
    }
  }
  return [...byId.values()]
    .sort((a, b) => compareSections(a.section, b.section) || a.id.localeCompare(b.id))
    .map(a => ({ id: a.id, version: a.version }));
}

function internalPathOf(p, v) {
  return isExtensionVersion(v) ? `/v${v}${p}` : p;
}

// Internal paths of a route, one per route and version pair; the versions of
// the standard share the public path.
function internalPathsOf(r) {
  const paths = versionsOf(r).map(v => internalPathOf(r.path, v));
  return [...new Set(paths)];
}

// Errors of the surfaces and backends blocks: the same surfaces as
// SURFACE_RULES, with port and backends.
function checkSurfaces(t) {
  const errs = [];
  const known = Object.keys(SURFACE_RULES);
  const declared = Object.keys(t.surfaces || {});
  for (const s of declared) {
    if (!known.includes(s)) {
      errs.push(`surfaces.${s}: surface with no rule in SURFACE_RULES (${known.join(', ')})`);
    }
  }
  for (const s of known) {
    if (!declared.includes(s)) {
      errs.push(`surfaces.${s}: missing surface`);
    } else if (!Number.isInteger(t.surfaces[s].port)) {
      errs.push(`surfaces.${s}.port: port missing or not an integer`);
    }
    const b = (t.backends || {})[s];
    if (!b || typeof b !== 'object' || Object.keys(b).length === 0) {
      errs.push(`backends.${s}: no backend variant`);
    }
  }
  return errs;
}

// Errors of the cors block: only what the plugin uses; ACAO and ACAM are
// fixed at *, the default of C.4.1.9.3.
function checkCors(cors) {
  const errs = [];
  if (!cors || typeof cors !== 'object' || Array.isArray(cors)) {
    errs.push('cors: missing block');
    return errs;
  }
  for (const k of Object.keys(cors)) {
    if (!CORS_KEYS.includes(k)) {
      errs.push(`cors.${k}: unused field (the plugin reads ${CORS_KEYS.join(', ')}; ACAO and ACAM are * by C.4.1.9.3)`);
    }
  }
  if (!isHeaderList(cors.allow_headers) || cors.allow_headers.length === 0) {
    errs.push('cors.allow_headers: non-empty list of header names');
  }
  if (cors.expose_headers !== undefined && !isHeaderList(cors.expose_headers)) {
    errs.push('cors.expose_headers: list of header names');
  }
  if (cors.max_age !== undefined && !MAX_AGE_FORMAT.test(cors.max_age)) {
    errs.push(`cors.max_age "${cors.max_age}" invalid (e.g. 12h, 600s)`);
  }
  return errs;
}

// Errors of the routes: each route on its own, plus duplicates and api.id
// with different section or version.
function checkRoutes(routes) {
  const errs = [];
  const seen = new Set();
  const apiSeen = new Map();
  for (const r of routes) {
    const id = `${r.method} ${r.path}`;
    if (seen.has(id)) {
      errs.push(`${id}: duplicated route`);
    }
    seen.add(id);
    errs.push(...checkRoute(r, id));
    if (r.api !== undefined) {
      errs.push(...checkApi(r, id, apiSeen));
    }
  }
  return errs;
}

// Errors of one route, apart from its api field.
function checkRoute(r, id) {
  const errs = [];
  if (INTERNAL_PREFIX.test(r.path || '')) {
    errs.push(`${id}: path with the /v<X.Y> prefix, reserved to the internal path (edge -> service)`);
  }
  if (r.surfaces !== undefined) {
    errs.push(`${id}: the "surfaces" field is not valid per route: both surfaces have every route (SURFACE_RULES)`);
  }
  if (r.auth !== undefined && !AUTH_VALUES.includes(r.auth)) {
    errs.push(`${id}: auth "${r.auth}" invalid (${AUTH_VALUES.join(', ')})`);
  }
  errs.push(...checkClasses(r, id));
  if (!(r.headers || []).includes('Accept-Version')) {
    errs.push(`${id}: every route passes Accept-Version on`);
  }
  if (r.timeout !== undefined && !TIMEOUT_FORMAT.test(r.timeout)) {
    errs.push(`${id}: timeout "${r.timeout}" invalid (e.g. 15s, 500ms)`);
  }
  errs.push(...checkVersions(r, id));
  for (const ih of INTERNAL_HEADERS) {
    if (r.headers && r.headers.some(h => h.toLowerCase() === ih.toLowerCase())) {
      errs.push(`${id}: ${ih} is an internal header of the edge; it does not go in headers`);
    }
  }
  errs.push(...checkServiceCodes(r, id));
  if (r.edge !== undefined) {
    if (!EDGE_HANDLERS.includes(r.edge)) {
      errs.push(`${id}: edge "${r.edge}" invalid (${EDGE_HANDLERS.join(', ')})`);
    }
    if (r.timeout !== undefined) {
      errs.push(`${id}: timeout does not apply to a route answered by the edge (edge)`);
    }
  }
  return errs;
}

function checkClasses(r, id) {
  const errs = [];
  if (r.classes === undefined) {
    return errs;
  }
  if (!Array.isArray(r.classes)) {
    errs.push(`${id}: classes is not a list`);
    return errs;
  }
  for (const c of r.classes) {
    if (!CLASS_VALUES.includes(c)) {
      errs.push(`${id}: class "${c}" invalid (${CLASS_VALUES.join(', ')})`);
    }
  }
  return errs;
}

function checkVersions(r, id) {
  const errs = [];
  if (r.versions === undefined) {
    return errs;
  }
  if (!Array.isArray(r.versions) || r.versions.length === 0) {
    errs.push(`${id}: versions is not a non-empty list`);
    return errs;
  }
  for (const v of r.versions) {
    if (typeof v !== 'string' || !API_VERSION.test(v)) {
      errs.push(`${id}: versions with "${v}" outside the X.Y format (C.3.6.2)`);
    }
  }
  if (new Set(r.versions).size !== r.versions.length) {
    errs.push(`${id}: versions with a repeated version`);
  }
  return errs;
}

// Errors of no_service and not_in_catalog.
function checkServiceCodes(r, id) {
  const errs = [];
  if (r.no_service !== undefined) {
    if (!NO_SERVICE_VALUES.includes(r.no_service)) {
      errs.push(`${id}: no_service ${JSON.stringify(r.no_service)} invalid (${NO_SERVICE_VALUES.join(', ')})`);
    }
    if (authOf(r) !== 'token+bind') {
      errs.push(`${id}: no_service only applies to a token+bind route`);
    }
  }
  if (r.not_in_catalog !== undefined) {
    if (!NOT_IN_CATALOG_VALUES.includes(r.not_in_catalog)) {
      errs.push(`${id}: not_in_catalog ${JSON.stringify(r.not_in_catalog)} invalid (${NOT_IN_CATALOG_VALUES.join(', ')})`);
    }
    const hasContext = String(r.path).split('/').includes('{serviceContextId}');
    if (authOf(r) !== 'token+bind' || !hasContext) {
      errs.push(`${id}: not_in_catalog only applies to a token+bind route with {serviceContextId}`);
    }
  }
  return errs;
}

// Errors of the api field: the row of Table C.2 the route implements; a
// route outside the standard has none. apiSeen keeps the first route of
// each api.id.
function checkApi(r, id, apiSeen) {
  const errs = [];
  const a = r.api;
  if (!a || typeof a !== 'object' || Array.isArray(a)) {
    errs.push(`${id}: api is not an object {id, section, version}`);
    return errs;
  }
  const extra = Object.keys(a).filter(k => !API_KEYS.includes(k));
  if (extra.length > 0) {
    errs.push(`${id}: api with unknown field (${extra.join(', ')})`);
  }
  const subsystem = String(a.id).split('-')[0];
  if (!API_ID.test(a.id || '') || !API_SUBSYSTEMS.includes(subsystem)) {
    errs.push(`${id}: api.id "${a.id}" invalid (<subsystem>-<name>, subsystems ${API_SUBSYSTEMS.join(', ')})`);
  }
  if (!API_SECTION.test(a.section || '')) {
    errs.push(`${id}: api.section "${a.section}" invalid (e.g. C.6.3.1)`);
  }
  if (!API_VERSION.test(a.version || '')) {
    errs.push(`${id}: api.version "${a.version}" invalid (X.Y, C.3.6.2)`);
  } else if (versionsOf(r).every(v => API_VERSION.test(v))) {
    // C.6.7.8 reports a single version, the "latest supported version"
    // (Table C.47): the latest one the route serves.
    const latest = versionsOf(r).slice(-1)[0];
    if (a.version !== latest) {
      errs.push(`${id}: api.version ${a.version} differs from the latest version the route serves (${latest})`);
    }
  }
  const prev = apiSeen.get(a.id);
  if (prev && (prev.section !== a.section || prev.version !== a.version)) {
    errs.push(`${id}: api.id ${a.id} with section/version different from ${prev.route} (${prev.section} ${prev.version})`);
  }
  if (!prev) {
    apiSeen.set(a.id, { section: a.section, version: a.version, route: id });
  }
  return errs;
}

// The KrakenD configuration of one surface and backend variant.
function krakendConfig(t, surface, variant, host, routes) {
  const config = {
    $schema: 'https://www.krakend.io/schema/v2.7/krakend.json',
    version: 3,
    name: `TV30 edgegateway — ${surface} (${variant})`,
    port: t.surfaces[surface].port,
  };
  if (SURFACE_RULES[surface].secure) {
    config.tls = { public_key: `${TLS_DIR}/cert.pem`, private_key: `${TLS_DIR}/key.pem`, min_version: 'TLS12' };
  }
  config.allow_insecure_connections = true;
  config.plugin = { pattern: '.so', folder: PLUGIN_FOLDER };
  // No 'security/cors': the KrakenD CORS module wraps the plugin and would
  // answer the preflight before it.
  config.extra_config = {
    'plugin/http-server': { name: [PLUGIN_NAME], [PLUGIN_NAME]: pluginConfig(t, surface) },
  };
  config.endpoints = routes.flatMap(r => endpointsFor(r, host));
  return config;
}

// The OpenAPI of one surface, methods merged by path.
function openApi(t, surface, routes) {
  const paths = {};
  for (const r of routes) {
    paths[r.path] = paths[r.path] || {};
    paths[r.path][r.method.toLowerCase()] = openApiOperation(r, surface);
  }
  const scheme = SURFACE_RULES[surface].secure ? 'https' : 'http';
  return {
    openapi: '3.0.0',
    info: {
      title: t.surfaces[surface].title,
      version: '2.0',
      description: 'Generated from routes.json — edit it there, not this file. ' + SURFACE_RULES[surface].doc,
    },
    servers: [{ url: `${scheme}://localhost:${t.surfaces[surface].port}` }],
    paths,
    components: { schemas: { Error: ERROR_SCHEMA } },
  };
}

function openApiOperation(r, surface) {
  const auth = authOf(r);
  const se = surfaceErrorOf(r, surface);
  const operation = {
    summary: r.path,
    description: openApiDescription(r, surface, auth, se),
    'x-tv30-auth': auth,
  };
  if (r.classes) {
    operation['x-tv30-classes'] = r.classes;
  }
  operation['x-tv30-surfaces'] = surfacesOf(r);
  operation['x-tv30-versions'] = versionsOf(r);
  if (se) {
    operation['x-tv30-surface-error'] = se;
  }
  if (r.api) {
    operation['x-tv30-api'] = r.api;
  }
  if (r.edge) {
    operation['x-tv30-edge'] = r.edge;
  }
  const headers = headersOf(r).filter(h => !INTERNAL_HEADERS.includes(h));
  operation.parameters = [
    ...headers.map(h => ({ name: h, in: 'header', required: h === 'Authorization' && auth !== 'none', schema: { type: 'string' } })),
    ...(r.query || []).map(q => ({ name: q, in: 'query', required: false, schema: { type: 'string' } })),
  ];
  operation.responses = {
    200: { description: 'OK' },
    404: { description: 'Error (C.3.2): status 404 + {error, description}', content: { 'application/json': { schema: { $ref: '#/components/schemas/Error' } } } },
  };
  return operation;
}

function openApiDescription(r, surface, auth, se) {
  const parts = [];
  if (r.api) {
    parts.push(`API ${r.api.id} (${r.api.section}, version ${r.api.version}).`);
  }
  if (r.edge) {
    parts.push('Answered by the edge itself (plugin tv30-auth), not passed on to tv3ws.');
  }
  parts.push(`Credential: ${AUTH_DOC[auth]}. Classes: ${(r.classes || ['all']).join(', ')}.`);
  if (se) {
    parts.push(`On this surface it answers ${se} to any request: only through the Server-BaseURL (C.4.1.8).`);
  }
  const nonLocalOnHttp = !SURFACE_RULES[surface].secure && !nonLocalHTTP(r) && classesOf(r).includes(CLASS_NON_LOCAL);
  if (nonLocalOnHttp) {
    parts.push('The non-local client gets 106 on this surface (HTTP): use the Server-SecureBaseURL (C.4.1.6).');
  }
  if (r.no_service) {
    parts.push(`No DTV service selected: ${r.no_service}.`);
  }
  if (r.not_in_catalog) {
    parts.push(`Service of the path outside the Application Catalog: ${r.not_in_catalog}.`);
  }
  parts.push(`Versions served (Accept-Version): ${versionsOf(r).join(', ')}; any other gives 100.`);
  parts.push('The associated local client needs no access token on the routes that admit it and, in its own service context, no bind-token (C.4.1.1).');
  return parts.join(' ');
}

// pluginConfig entry of one route.
function pluginRoute(r, surface, secure) {
  const p = { method: r.method, path: r.path, auth: authOf(r) };
  if (r.classes) {
    p.classes = r.classes;
  }
  // Answered by the plugin (edge.go).
  if (r.edge) {
    p.edge = r.edge;
  }
  // 106 to any request (C.4.1.8).
  const se = surfaceErrorOf(r, surface);
  if (se) {
    p.surface_error = se;
  }
  // C.4.1.6
  if (!secure && nonLocalHTTP(r)) {
    p.nonlocal_http = true;
  }
  if (r.no_service) {
    p.no_service = r.no_service;
  }
  if (r.not_in_catalog) {
    p.not_in_catalog = r.not_in_catalog;
  }
  p.versions = versionsOf(r);
  return p;
}

// endpointsFor returns one endpoint per internal path of the route, with the
// same path on the backend of the surface.
function endpointsFor(r, host) {
  return internalPathsOf(r).map(p => {
    const e = { endpoint: p, method: r.method };
    const headers = headersOf(r);
    if (headers.length > 0) {
      e.input_headers = headers;
    }
    if (r.query) {
      e.input_query_strings = r.query;
    }
    // no-op: without it, KrakenD replaces a backend error (404 and a C.3.2
    // body) with a 500 without body.
    e.output_encoding = 'no-op';
    // Without "timeout", the KrakenD default of 2 s applies.
    if (r.timeout) {
      e.timeout = r.timeout;
    }
    // For a route with "edge", the plugin answers first and the backend is
    // never called; the endpoint keeps the path in the router.
    e.backend = [{ url_pattern: p, host: [host], encoding: 'no-op' }];
    return e;
  });
}

// headersOf returns the headers passed on to the backend: the declared ones,
// the bind-token on token+bind routes, and the internal headers.
function headersOf(r) {
  const h = [...(r.headers || [])];
  if (authOf(r) === 'token+bind' && !h.includes('bind-token')) {
    h.push('bind-token');
  }
  h.push(...INTERNAL_HEADERS);
  return h;
}

function readJson(f) {
  return JSON.parse(fs.readFileSync(f, 'utf8'));
}

function compareVersions(a, b) {
  const [am, an] = a.split('.').map(Number);
  const [bm, bn] = b.split('.').map(Number);
  return am - bm || an - bn;
}

function versionsOf(r) {
  const versions = [...(r.versions || DEFAULT_VERSIONS)];
  return versions.sort(compareVersions);
}

function isExtensionVersion(v) {
  return Number(v.split('.')[0]) >= EXTENSION_MAJOR;
}

function authOf(r) {
  return r.auth || 'token';
}

// classes: those that may use the route, all when absent; the others get
// 106.
function classesOf(r) {
  return r.classes || CLASS_VALUES;
}

// Whether the route is one a non-local client uses over HTTP (C.4.1.6).
function nonLocalHTTP(r) {
  return !!r.api && NONLOCAL_HTTP_SECTIONS.includes(r.api.section);
}

function isHeaderList(v) {
  return Array.isArray(v) && v.every(h => typeof h === 'string' && HEADER_NAME.test(h));
}

// compareSections orders sections numerically (C.6.1.2 < C.6.1.3 < ... <
// C.6.16.3), the order of Table C.2.
function compareSections(a, b) {
  const pa = a.split('.').slice(1).map(Number);
  const pb = b.split('.').slice(1).map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? -1) - (pb[i] ?? -1);
    if (d) {
      return d;
    }
  }
  return 0;
}

module.exports = { SURFACE_RULES, CLASS_VALUES, surfacesOf, routesOf, surfaceErrorOf, checkTable, pluginConfig, apisOf, internalPathOf, internalPathsOf, generate };

if (require.main === module) {
  const mode = process.argv[2];
  if (mode === 'build') {
    build();
  } else {
    console.error('usage: node generate.js build');
    process.exit(2);
  }
}
