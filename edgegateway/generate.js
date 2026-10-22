#!/usr/bin/env node
'use strict';
/*
 * Gera, de routes.json, as configs KrakenD das duas superficies (uma por
 * variante de backend) e os dois OpenAPI: node generate.js build.
 * Testes: node --test generate.test.js.
 */
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const GEN = path.join(HERE, 'generated');

const readJson = f => JSON.parse(fs.readFileSync(f, 'utf8'));

const PLUGIN_NAME = 'tv30-auth';
// TLS da 44643 terminado na borda, que precisa ler os cabecalhos; certificado
// do servico tls-init (.tv30/tls da raiz, montado aqui)
const TLS_DIR = '/etc/tv30/tls';
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
// cabecalhos internos escritos pelo plugin (versao negociada e Host original do
// cliente); o KrakenD so repassa ao backend o que esta em input_headers
const INTERNAL_VERSION_HEADER = 'X-TV30-API-Version';
const INTERNAL_HOST_HEADER = 'X-TV30-Host';
const INTERNAL_HEADERS = [INTERNAL_VERSION_HEADER, INTERNAL_HOST_HEADER];
// versions da rota: as versoes "X.Y" servidas (C.3.6.2); ausente => so a 2.0, a
// "current version" da C.3.6.3. De 100 em diante, extensoes do testbed.
const DEFAULT_VERSIONS = ['2.0'];
const compareVersions = (a, b) => {
  const [am, an] = a.split('.').map(Number), [bm, bn] = b.split('.').map(Number);
  return am - bm || an - bn;
};
const versionsOf = r => [...(r.versions || DEFAULT_VERSIONS)].sort(compareVersions);

const authOf = r => r.auth || 'token';
// classes: as que podem usar a rota (ausente => todas); as demais recebem 106.
// Na C.6.15.5, o nao local recebe 106.
const classesOf = r => r.classes || CLASS_VALUES;

// C.4.1.8: a C.6.1.2 "can only be accessed via the HTTP protocol"
const HTTP_ONLY_SECTIONS = ['C.6.1.2'];
// C.4.1.6: o nao local por HTTP so usa as APIs "authorization-related
// (C.6.1.2 and C.6.1.3)"; nas demais, 106.
const NONLOCAL_HTTP_SECTIONS = ['C.6.1.2', 'C.6.1.3'];
// no_service: codigo da rota token+bind sem servico DTV selecionado, quando a
// tabela da API lista o 300; sem o campo, segue o bind-token e da 108
const NO_SERVICE_VALUES = [300];

// Superficies, com as chaves de surfaces e backends do routes.json; secure = a
// do Server-SecureBaseURL. Duas superficies sao do testbed: a C.3.4 so
// define o Server-BaseURL (http, porta fixa 44642) e o Server-SecureBaseURL.
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

// superficies em que a rota existe: todas
function surfacesOf() {
  return Object.keys(SURFACE_RULES);
}

const routesOf = t => t.routes;

// surfaceErrorOf: o codigo que a rota responde a QUALQUER requisicao nesta
// superficie (0 = nenhum). So a externa tem: 106 da C.4.1.8.
function surfaceErrorOf(r, surface) {
  if (!SURFACE_RULES[surface].secure) return 0;
  const exclusivaDoLocal = !classesOf(r).includes('non-local');
  const soHTTP = !!r.api && HTTP_ONLY_SECTIONS.includes(r.api.section);
  return exclusivaDoLocal || soHTTP ? 106 : 0;
}

// rota que o nao local usa por HTTP (C.4.1.6)
const nonLocalHTTP = r => !!r.api && NONLOCAL_HTTP_SECTIONS.includes(r.api.section);

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
// errada na borda, em silencio. checkTable devolve a lista de erros (vazia =
// tabela valida); validate encerra o processo se houver algum.
function checkTable(t) {
  const errs = [];
  const seen = new Set();
  const apiSeen = new Map();
  // superficies: as mesmas de SURFACE_RULES, com porta e backends
  const known = Object.keys(SURFACE_RULES);
  const declared = Object.keys(t.surfaces || {});
  for (const s of declared) if (!known.includes(s)) errs.push(`surfaces.${s}: superficie sem regra em SURFACE_RULES (${known.join(', ')})`);
  for (const s of known) {
    if (!declared.includes(s)) errs.push(`surfaces.${s}: superficie ausente`);
    else if (!Number.isInteger(t.surfaces[s].port)) errs.push(`surfaces.${s}.port: porta ausente ou nao inteira`);
    const b = (t.backends || {})[s];
    if (!b || typeof b !== 'object' || !Object.keys(b).length) errs.push(`backends.${s}: sem variante de backend`);
  }
  // cors: so o que o plugin usa; ACAO e ACAM sao fixos em * (padrao da C.4.1.9.3)
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
    if (r.edge !== undefined) {
      if (!EDGE_HANDLERS.includes(r.edge)) errs.push(`${id}: edge "${r.edge}" invalido (${EDGE_HANDLERS.join(', ')})`);
      if (r.timeout !== undefined) errs.push(`${id}: timeout nao se aplica a rota respondida pela borda (edge)`);
    }
    // api: a linha da Tabela C.2 que a rota implementa (rota fora da norma nao tem)
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
      // a C.6.7.8 informa uma versao so, a "latest supported version"
      // (Tabela C.47): a mais recente das que a rota serve
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
  // superficie sem rota: o plugin recusa lista vazia e derrubaria a borda
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

// Cabecalhos repassados ao tv3ws: os declarados + bind-token nas rotas
// token+bind (a borda valida, e o tv3ws continua recebendo).
function headersOf(r) {
  const h = [...(r.headers || [])];
  if (authOf(r) === 'token+bind' && !h.includes('bind-token')) h.push('bind-token');
  h.push(...INTERNAL_HEADERS);
  return h;
}

function endpointFor(r, surface, host) {
  const e = { endpoint: r.path, method: r.method };
  const headers = headersOf(r);
  if (headers.length) e.input_headers = headers;
  if (r.query) e.input_query_strings = r.query;
  // no-op: sem isso o KrakenD troca o erro do tv3ws (404 + corpo C.3.2) por
  // 500 sem corpo
  e.output_encoding = 'no-op';
  // Sem "timeout" vale o padrao do KrakenD (2s): um tv3ws travado vira o erro
  // 200 em 2 s. O /tv3/authorize espera o pop-up (ate 10 s no tv3ws): 15s.
  if (r.timeout) e.timeout = r.timeout;
  // Rota com "edge": o plugin responde antes e o backend nunca e chamado; o
  // endpoint so registra o caminho no roteador (caminho fora da arvore do Gin
  // pode cair no panic de KNOWN-ISSUES.md).
  e.backend = [{ url_pattern: r.backendPath || r.path, host: [host], encoding: 'no-op' }];
  return e;
}

// Bloco do plugin tv30-auth: a politica da superficie (metodo+caminho com
// padroes {param}, auth, classes e os campos derivados abaixo). Caminho nao
// listado aqui => erro 100 no plugin, sem chegar ao roteador.
function pluginConfig(t, surface) {
  const routes = routesOf(t, surface);
  const secure = SURFACE_RULES[surface].secure;
  const c = {
    surface,
    routes: routes.map(r => {
      const p = { method: r.method, path: r.path, auth: authOf(r) };
      if (r.classes) p.classes = r.classes;
      if (r.edge) p.edge = r.edge; // respondida pelo plugin (edge.go)
      const se = surfaceErrorOf(r, surface);
      if (se) p.surface_error = se; // 106 a qualquer requisicao (C.4.1.8)
      if (!secure && nonLocalHTTP(r)) p.nonlocal_http = true; // C.4.1.6
      if (r.no_service) p.no_service = r.no_service;
      p.versions = versionsOf(r);
      return p;
    }),
    // as duas superficies listam todas as APIs: a C.6.7.9 descreve "all APIs
    // of a given category" (Tabela C.48), e a restricao aparece pelo 106 no uso
    apis: apisOf(routes),
  };
  // C.4.1.6: na superficie HTTP o nao local so usa as rotas nonlocal_http
  if (!secure) c.nonlocal_https_only = true;
  // CORS (C.4.1.9), respondido pelo plugin: allow_headers em todo OPTIONS,
  // max_age no preflight, expose_headers em toda resposta
  if (t.cors && Array.isArray(t.cors.allow_headers)) c.cors_allow_headers = t.cors.allow_headers;
  if (t.cors && Array.isArray(t.cors.expose_headers)) c.cors_expose_headers = t.cors.expose_headers;
  if (t.cors && typeof t.cors.max_age === 'string') c.cors_max_age = t.cors.max_age;
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

// generate monta todos os arquivos de uma tabela JA validada, sem gravar:
// { '<nome>.json': objeto }. build() grava; generate.test.js confere.
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
        // sem 'security/cors': o modulo CORS do KrakenD envolve o plugin e
        // responderia o preflight antes dele
        extra_config: {
          'plugin/http-server': { name: [PLUGIN_NAME], [PLUGIN_NAME]: pluginConfig(t, surface) },
        },
        endpoints: routes.map(r => endpointFor(r, surface, host)),
      };
    }
    // OpenAPI da superficie (metodos mesclados por path)
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
          `Versoes servidas (Accept-Version): ${versionsOf(r).join(', ')}; outra da 100. ` +
          'O local associado dispensa access token e bind-token nas rotas que o admitem (C.4.1.1).',
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
    if (obj.endpoints) console.log(`${out}: ${obj.endpoints.length} rotas, porta ${obj.port}`);
  }
  for (const surface of Object.keys(t.surfaces)) {
    const se = t.routes.filter(r => surfaceErrorOf(r, surface)).map(r => `${r.method} ${r.path}`);
    console.log(`superficie ${surface}: ${t.routes.length} rotas` + (se.length ? `; 106 a qualquer requisicao (C.4.1.8): ${se.join(', ')}` : ''));
  }
}

module.exports = { SURFACE_RULES, CLASS_VALUES, surfacesOf, routesOf, surfaceErrorOf, checkTable, pluginConfig, apisOf, generate };

if (require.main === module) {
  const mode = process.argv[2];
  if (mode === 'build') build();
  else { console.error('uso: node generate.js build'); process.exit(2); }
}
