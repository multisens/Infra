#!/usr/bin/env node
'use strict';
/*
 * M4 — Tabela única de rotas do edgegateway.
 *
 * As rotas são declaradas só em routes.json (uma entrada por rota, com metadados). Este
 * script:
 *
 *   node generate.js build     — gera generated/krakend-{external,internal}.
 *                                <variante>.json (uma por backend de
 *                                routes.json: linux, windows, host) + os dois
 *                                OpenAPI
 *
 * Testes: node --test generate.test.js (tambem rodam no build da imagem).
 *
 * Superficie de cada rota (P1.9, decisao do Luis em 10/10): DERIVADA do campo
 * "classes", e nao mais declarada por rota (o campo "surfaces" das rotas saiu;
 * o build recusa quem o usar). A externa (44643) so expoe as rotas que
 * admitem o cliente nao local; a interna (44642) expoe todas. Rota sem
 * "classes" (todas as classes, inclusive /health e /manifest) fica nas duas.
 * Numa superficie em que nao existe, a rota responde 100, como qualquer
 * caminho nao declarado (o plugin nem a conhece la). Ver SURFACE_RULES.
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
const classesOf = r => r.classes || CLASS_VALUES;

// P1.9 (decisao do Luis em 10/10): superficies com rotas distintas. Cada
// superficie expoe as rotas cuja lista "classes" (ausente => todas) inclui a
// classe em "requires"; requires null => todas as rotas. As chaves daqui sao
// as de routes.json (surfaces e backends): o build falha se divergirem.
// Duas superficies com conjuntos de rota distintos sao decisao de
// implementacao do projeto: a norma (C.3.4, p. 216 do PDF) so define o
// Server-BaseURL (http, porta fixa 44642, que o cliente local usa direto) e o
// Server-SecureBaseURL (https, porta livre).
// PENDENTE (Joel): ligado a lacuna L3 (44643 ainda em HTTP). A C.4.1.8 (p.
// 206-207; p. 224-225 do PDF) manda responder 106, e nao 100, ao nao local
// que chama por HTTPS uma API de uso exclusivo do cliente local; com esta
// regra, POST e DELETE /tv3/bind-context (so local-associated) dao 100 na
// 44643. Enquanto a 44643 nao tiver TLS, a C.4.1.8 nao se aplica a ela ao pe
// da letra; com TLS, falta decidir se essas rotas dao 106 ali. A mesma secao
// diz que a C.6.1.2 (/tv3/authorize) so se acessa por HTTP; esta regra a
// mantem na 44643, porque ela admite o nao local.
// C.6.15.5 so para o cliente local (decisao do Luis em 12/10): GET
// /tv3/remote-device/devices/{classId} ganhou "classes" local-associated e
// local-autonomous no routes.json. Por esta regra, ela sai da 44643 (la da
// 100, como as duas da C.6.8 acima), e na 44642 o nao local recebe o 106 da
// regra de classe do plugin.
// PENDENTE (Joel), na F-1 (este mesmo ponto; docs/decisoes-pendentes.md da
// raiz, secao F, P1.9): o codigo exato que o nao local recebe nessa API. A
// Tabela C.78 (p. 339; p. 357 do PDF) lista 107 ("If invoked from a
// non-local client"); a C.4.1.8 fala em 106 para o nao local que chama uma
// API de uso exclusivo do cliente local. Ate a decisao, ficam o 106 da borda
// na 44642 e o 100 na 44643.
const SURFACE_RULES = {
  external: {
    requires: 'non-local',
    doc: 'Expoe so as rotas que admitem o cliente nao local (non-local); as demais existem so na superficie interna e respondem 100 aqui (P1.9).',
  },
  internal: {
    requires: null,
    doc: 'Expoe todas as rotas da tabela (P1.9).',
  },
};

// superficies em que a rota existe, na ordem de SURFACE_RULES
function surfacesOf(r) {
  return Object.keys(SURFACE_RULES).filter(s => {
    const need = SURFACE_RULES[s].requires;
    return need === null || classesOf(r).includes(need);
  });
}

const routesOf = (t, surface) => t.routes.filter(r => surfacesOf(r).includes(surface));

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
  for (const r of t.routes) {
    const id = `${r.method} ${r.path}`;
    if (seen.has(id)) errs.push(`${id}: rota duplicada`);
    seen.add(id);
    if (r.surfaces !== undefined) errs.push(`${id}: o campo "surfaces" da rota saiu (P1.9): a superficie eh derivada de "classes" (generate.js, SURFACE_RULES)`);
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
  // superficie sem rota: o plugin recusa lista vazia e derrubaria a borda.
  // So com as rotas validas (classes fora do formato quebraria surfacesOf).
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
// plugin, sem chegar ao roteador — inclusive a rota que so existe na outra
// superficie (P1.9).
function pluginConfig(t, surface) {
  const routes = routesOf(t, surface);
  const c = {
    surface,
    routes: routes.map(r => {
      const p = { method: r.method, path: r.path, auth: authOf(r) };
      if (r.classes) p.classes = r.classes;
      if (r.edge) p.edge = r.edge; // respondida pelo plugin (edge.go)
      return p;
    }),
    // APIs implementadas nesta superficie, para a C.6.7.8/C.6.7.9. Com a
    // P1.9, a externa deixa de listar as APIs das rotas que so existem na
    // interna (hoje tv3ws-bind-context-register, tv3ws-bind-context-remove e,
    // desde 12/10, tv3ws-remotedevice-communication, a C.6.15.5, que so tem
    // essa rota), e GET /tv3/api-info/<id> delas da 101 na 44643.
    // DECIDIDO (Luis, 11/10; F-2): cada superficie lista so as APIs que
    // oferece. A lista por superficie ja era assim desde a D-0510-3 (05/10),
    // mas com as duas superficies iguais a diferenca nao aparecia. A C.6.7.9
    // (Tabela C.46, p. 301 do PDF) descreve "all APIs of a given category",
    // no campo "receiverApis", sem falar de superficie.
    apis: apisOf(routes),
  };
  // PENDENTE (Joel): lacuna L2 — serviceContextId constante do tv3ws
  // (tv3ws/src/core.ts): nas rotas /tv3/{serviceContextId}/... o plugin
  // trata este valor (e current-service) como o servico corrente.
  // A4 (decisao do Luis em 11/10): POST /tv3/{serviceContextId}/users, que nao
  // existe na norma (so POST /tv3/current-service/users, C.6.14.1, bind-token
  // shall), saiu do routes.json: a borda responde 100 a ela. Ate 11/10 ficava
  // como token+bind (com a regra L2 acima) para nao abrir um atalho sem
  // bind-token ao MESMO handler do tv3ws.
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
        allow_insecure_connections: true,
        plugin: { pattern: '.so', folder: PLUGIN_FOLDER },
        extra_config: {
          'security/cors': t.cors,
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
      paths[r.path] = paths[r.path] || {};
      paths[r.path][r.method.toLowerCase()] = {
        summary: r.path,
        description: (r.api ? `API ${r.api.id} (${r.api.section}, versao ${r.api.version}). ` : '') +
          (r.edge ? 'Respondida pela propria borda (plugin tv30-auth), sem repasse ao tv3ws. ' : '') +
          `Credencial: ${AUTH_DOC[auth]}. Classes: ${(r.classes || ['todas']).join(', ')}. ` +
          (where.length < Object.keys(SURFACE_RULES).length ? `So nesta superficie (${where.join(', ')}; P1.9). ` : '') +
          'O local associado dispensa access token e bind-token nas rotas que o admitem (C.4.1.1).',
        'x-tv30-auth': auth,
        ...(r.classes ? { 'x-tv30-classes': r.classes } : {}),
        'x-tv30-surfaces': where,
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
    files[`openapi-${surface}.json`] = {
      openapi: '3.0.0',
      info: {
        title: t.surfaces[surface].title,
        version: '2.0',
        description: 'Gerado da tabela unica de rotas (M4) — edite routes.json, nao este arquivo. ' + SURFACE_RULES[surface].doc,
      },
      servers: [{ url: `http://localhost:${t.surfaces[surface].port}` }],
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
  // rotas que ficam fora de alguma superficie (P1.9), para o log do build
  for (const surface of Object.keys(t.surfaces)) {
    const out = t.routes.filter(r => !surfacesOf(r).includes(surface)).map(r => `${r.method} ${r.path}`);
    console.log(`superficie ${surface}: ${t.routes.length - out.length} de ${t.routes.length} rotas` + (out.length ? `; fora: ${out.join(', ')}` : ''));
  }
}

module.exports = { SURFACE_RULES, CLASS_VALUES, surfacesOf, routesOf, checkTable, pluginConfig, apisOf, generate };

if (require.main === module) {
  const mode = process.argv[2];
  if (mode === 'build') build();
  else { console.error('uso: node generate.js build'); process.exit(2); }
}
