'use strict';
// Tests of generate.js, without dependencies: node --test generate.test.js.
// They also run in the image build.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const g = require('./generate.js');

const real = () => JSON.parse(fs.readFileSync(path.join(__dirname, 'routes.json'), 'utf8'));
const key = r => `${r.method} ${r.path}`;
const keys = rs => rs.map(key).sort();

// Minimal valid table, for the synthetic cases.
function mini(routes) {
  return {
    backends: { external: { linux: 'https://x:1' }, internal: { linux: 'http://x:2' } },
    surfaces: { external: { port: 44643, title: 'ext' }, internal: { port: 44642, title: 'int' } },
    cors: { allow_headers: ['Content-Type'] },
    routes: routes.map(r => ({ headers: ['Accept-Version'], ...r })),
  };
}

test('routes.json atual passa na validacao', () => {
  assert.deepEqual(g.checkTable(real()), []);
});

const plugin = (files, s) => files[`krakend-${s}.linux.json`].extra_config['plugin/http-server']['tv30-auth'];
const pk = r => `${r.method} ${r.path}`;
const EXCLUSIVAS_DO_LOCAL = [
  'DELETE /tv3/bind-context',
  'DELETE /tv3/remote-device/device/{handle}',
  'GET /tv3/remote-device/device/{handle}',
  'GET /tv3/remote-device/devices/{classId}',
  'POST /tv3/bind-context',
];

test('as duas superficies tem todas as rotas', () => {
  const t = real();
  for (const s of ['external', 'internal']) assert.equal(g.routesOf(t, s).length, t.routes.length, s);
  for (const r of t.routes) assert.deepEqual(g.surfacesOf(r), ['external', 'internal'], key(r));
  const files = g.generate(t);
  for (const s of ['external', 'internal']) assert.equal(plugin(files, s).routes.length, 26, s);
});

test('na externa, 106 nas exclusivas do cliente local e na C.6.1.2 (C.4.1.8); na interna, nenhuma', () => {
  const t = real();
  const com106 = s => t.routes.filter(r => g.surfaceErrorOf(r, s) === 106).map(key).sort();
  assert.deepEqual(com106('external'), [...EXCLUSIVAS_DO_LOCAL, 'GET /tv3/authorize'].sort());
  assert.deepEqual(com106('internal'), []);
  // /tv3/token (C.6.1.3) and GET /tv3/bind-context (C.6.8.3) stay on the
  // external surface.
  for (const k of ['GET /tv3/token', 'GET /tv3/bind-context', 'POST /tv3/remote-device', 'DELETE /tv3/remote-device/{handle}']) {
    assert.equal(g.surfaceErrorOf(t.routes.find(r => key(r) === k), 'external'), 0, k);
  }
  const files = g.generate(t);
  const ext = plugin(files, 'external').routes.filter(r => r.surface_error).map(pk).sort();
  assert.deepEqual(ext, com106('external'));
  assert.ok(plugin(files, 'internal').routes.every(r => !r.surface_error));
  // The endpoints stay registered in the router of the external surface (the
  // plugin answers first).
  for (const v of Object.keys(t.backends.external)) {
    const eps = files[`krakend-external.${v}.json`].endpoints.map(e => `${e.method} ${e.endpoint}`);
    for (const r of t.routes.filter(r => g.surfaceErrorOf(r, 'external') === 106)) {
      for (const p of g.internalPathsOf(r)) assert.ok(eps.includes(`${r.method} ${p}`), `${r.method} ${p} (${v})`);
    }
  }
});

test('na interna, nonlocal_https_only e so a C.6.1.2/C.6.1.3 isentas (C.4.1.6); na externa, nada disso', () => {
  const files = g.generate(real());
  const int = plugin(files, 'internal'), ext = plugin(files, 'external');
  assert.equal(int.nonlocal_https_only, true);
  assert.ok(!('nonlocal_https_only' in ext));
  assert.deepEqual(int.routes.filter(r => r.nonlocal_http).map(pk).sort(), ['GET /tv3/authorize', 'GET /tv3/token']);
  assert.ok(ext.routes.every(r => !('nonlocal_http' in r)));
});

test('no_service 300 na C.6.4.7 e da C.6.14.1 a C.6.14.6; nao na C.6.16.3', () => {
  const t = real();
  const com300 = t.routes.filter(r => r.no_service === 300).map(r => r.api.section).sort();
  assert.deepEqual([...new Set(com300)].sort(), ['C.6.14.1', 'C.6.14.2', 'C.6.14.3', 'C.6.14.4', 'C.6.14.5', 'C.6.14.6', 'C.6.4.7']);
  assert.ok(!t.routes.find(r => r.api && r.api.section === 'C.6.16.3').no_service);
  for (const r of t.routes.filter(r => r.no_service)) assert.equal(r.auth, 'token+bind', key(r));
  const files = g.generate(t);
  for (const s of ['external', 'internal']) assert.equal(plugin(files, s).routes.filter(r => r.no_service === 300).length, 8, s);
  // Validation: value and auth.
  assert.ok(g.checkTable(mini([{ method: 'GET', path: '/a', auth: 'token+bind', no_service: 108 }])).some(e => /no_service 108 invalido/.test(e)));
  assert.ok(g.checkTable(mini([{ method: 'GET', path: '/a', auth: 'token', no_service: 300 }])).some(e => /so vale em rota token\+bind/.test(e)));
});

test('not_in_catalog 304 so na C.6.14.2 e na C.6.14.5 com {serviceContextId}; nao em current-service', () => {
  const t = real();
  assert.deepEqual(keys(t.routes.filter(r => r.not_in_catalog === 304)), ['GET /tv3/{serviceContextId}/users/{userid}', 'POST /tv3/{serviceContextId}/users/{userid}']);
  assert.ok(!t.routes.find(r => key(r) === 'GET /tv3/current-service/users/{userid}').not_in_catalog);
  const files = g.generate(t);
  for (const s of ['external', 'internal']) {
    assert.deepEqual(plugin(files, s).routes.filter(r => r.not_in_catalog === 304).map(pk).sort(), ['GET /tv3/{serviceContextId}/users/{userid}', 'POST /tv3/{serviceContextId}/users/{userid}'], s);
    assert.match(files[`openapi-${s}.json`].paths['/tv3/{serviceContextId}/users/{userid}'].get.description, /fora do Application Catalog: 304/);
  }
  // Validation: value, auth and the parameter in the path.
  const one = r => g.checkTable(mini([{ method: 'GET', auth: 'token+bind', ...r }]));
  assert.ok(one({ path: '/tv3/{serviceContextId}/x', not_in_catalog: 300 }).some(e => /not_in_catalog 300 invalido/.test(e)));
  assert.ok(one({ path: '/tv3/{serviceContextId}/x', auth: 'token', not_in_catalog: 304 }).some(e => /so vale em rota token\+bind com \{serviceContextId\}/.test(e)));
  assert.ok(one({ path: '/tv3/current-service/x', not_in_catalog: 304 }).some(e => /so vale em rota token\+bind com \{serviceContextId\}/.test(e)));
  assert.deepEqual(one({ path: '/tv3/{serviceContextId}/x', not_in_catalog: 304 }), []);
});

// POST /tv3/{serviceContextId}/users is not in Table C.2: no surface has it,
// and the edge answers 100.
test('POST /tv3/{serviceContextId}/users fora da tabela', () => {
  const t = real();
  assert.ok(!t.routes.some(r => key(r) === 'POST /tv3/{serviceContextId}/users'), 'rota fora da norma');
  assert.ok(t.routes.some(r => key(r) === 'POST /tv3/current-service/users'), 'a da norma (C.6.14.1) fica');
  const files = g.generate(t);
  for (const [name, cfg] of Object.entries(files)) {
    if (name.startsWith('krakend-')) {
      assert.ok(!cfg.endpoints.some(e => e.method === 'POST' && e.endpoint === '/tv3/{serviceContextId}/users'), `${name}: endpoint`);
    } else {
      assert.ok(!cfg.paths['/tv3/{serviceContextId}/users'], `${name}: OpenAPI`);
    }
  }
});

test('o campo "surfaces" por rota eh recusado (a superficie eh derivada)', () => {
  const t = mini([{ method: 'GET', path: '/health', auth: 'none', surfaces: ['internal'] }]);
  const errs = g.checkTable(t);
  assert.equal(errs.length, 1, errs.join('\n'));
  assert.match(errs[0], /GET \/health: o campo "surfaces" nao vale por rota/);
});

test('superficies e backends tem de casar com SURFACE_RULES', () => {
  const sem = mini([{ method: 'GET', path: '/health', auth: 'none' }]);
  delete sem.surfaces.external;
  assert.ok(g.checkTable(sem).some(e => e.startsWith('surfaces.external: superficie ausente')));

  const extra = mini([{ method: 'GET', path: '/health', auth: 'none' }]);
  extra.surfaces.dmz = { port: 1, title: 'dmz' };
  assert.ok(g.checkTable(extra).some(e => e.startsWith('surfaces.dmz: superficie sem regra')));

  const semBackend = mini([{ method: 'GET', path: '/health', auth: 'none' }]);
  delete semBackend.backends.internal;
  assert.ok(g.checkTable(semBackend).some(e => e.startsWith('backends.internal:')));
});

test('tabela sem nenhuma rota eh erro (o plugin recusaria lista vazia)', () => {
  const errs = g.checkTable(mini([]));
  assert.deepEqual(errs.map(e => e.split(':')[0]).sort(), ['surfaces.external', 'surfaces.internal']);
});

test('arquivos gerados: 2 variantes x 2 superficies + 2 OpenAPI', () => {
  const t = real();
  const files = g.generate(t);
  const want = [];
  for (const s of Object.keys(t.surfaces)) {
    for (const v of Object.keys(t.backends[s])) want.push(`krakend-${s}.${v}.json`);
    want.push(`openapi-${s}.json`);
  }
  assert.deepEqual(Object.keys(files).sort(), want.sort());
  assert.equal(want.length, 6);
});

test('em cada config KrakenD, rotas do plugin = rotas da superficie, e um endpoint por par rota x versao', () => {
  const t = real();
  const files = g.generate(t);
  for (const [name, cfg] of Object.entries(files)) {
    if (!name.startsWith('krakend-')) continue;
    const [surface, variant] = name.replace(/^krakend-|\.json$/g, '').split('.');
    const plugin = cfg.extra_config['plugin/http-server']['tv30-auth'];
    const esperadas = keys(g.routesOf(t, surface));
    assert.equal(plugin.surface, surface, name);
    assert.equal(cfg.port, t.surfaces[surface].port, name);
    assert.deepEqual(keys(plugin.routes), esperadas, `${name}: rotas do plugin`);
    const pares = g.routesOf(t, surface).flatMap(r => g.internalPathsOf(r).map(p => `${r.method} ${p}`)).sort();
    assert.deepEqual(cfg.endpoints.map(e => `${e.method} ${e.endpoint}`).sort(), pares, `${name}: endpoints`);
    // Target: the backend of the variant, on the same internal path.
    for (const e of cfg.endpoints) {
      assert.deepEqual(e.backend, [{ url_pattern: e.endpoint, host: [t.backends[surface][variant]], encoding: 'no-op' }], `${name}: ${e.method} ${e.endpoint}`);
    }
  }
});

test('caminho interno: /v<X.Y> antes do publico de 100 em diante; abaixo de 100, o publico', () => {
  assert.equal(g.internalPathOf('/tv3/remote-device/devices/{classId}', '2.0'), '/tv3/remote-device/devices/{classId}');
  assert.equal(g.internalPathOf('/tv3/remote-device/devices/{classId}', '99.9'), '/tv3/remote-device/devices/{classId}');
  assert.equal(g.internalPathOf('/tv3/remote-device/devices/{classId}', '100.0'), '/v100.0/tv3/remote-device/devices/{classId}');
  assert.equal(g.internalPathOf('/tv3/x', '101.12'), '/v101.12/tv3/x');
  // The versions of the standard share the public path; each extension has
  // its own.
  assert.deepEqual(g.internalPathsOf({ path: '/tv3/x', versions: ['101.0', '2.1', '2.0', '100.0'] }), ['/tv3/x', '/v100.0/tv3/x', '/v101.0/tv3/x']);
  assert.deepEqual(g.internalPathsOf({ path: '/tv3/x', versions: ['100.0'] }), ['/v100.0/tv3/x']);
  assert.deepEqual(g.internalPathsOf({ path: '/tv3/x' }), ['/tv3/x']);

  const files = g.generate(real());
  const eps = files['krakend-internal.linux.json'].endpoints.map(e => `${e.method} ${e.endpoint}`);
  for (const k of ['GET /tv3/remote-device/devices/{classId}', 'GET /v100.0/tv3/remote-device/devices/{classId}',
    'GET /v100.0/tv3/remote-device/device/{handle}', 'DELETE /v100.0/tv3/remote-device/device/{handle}']) {
    assert.ok(eps.includes(k), k);
  }
  // A route only of 100.0 has no endpoint on the public path.
  assert.ok(!eps.includes('GET /tv3/remote-device/device/{handle}'));
  assert.ok(!eps.includes('DELETE /tv3/remote-device/device/{handle}'));
  assert.equal(eps.filter(k => k.includes(' /v')).length, 3);
  // The plugin and the OpenAPI know only the public path: a /v... path sent
  // by the client gets 100.
  for (const s of ['external', 'internal']) {
    assert.ok(plugin(files, s).routes.every(r => r.path.startsWith('/tv3/') || ['/health', '/manifest'].includes(r.path)), s);
    assert.ok(Object.keys(files[`openapi-${s}.json`].paths).every(p => !p.startsWith('/v')), `openapi-${s}`);
  }
});

test('caminho declarado com o prefixo /v<X.Y> e recusado', () => {
  for (const p of ['/v100.0/tv3/x', '/v2.0/tv3/x', '/v100.0']) {
    assert.ok(g.checkTable(mini([{ method: 'GET', path: p }])).some(e => /reservado ao caminho interno/.test(e)), p);
  }
  for (const p of ['/v', '/video/x', '/v100/tv3/x', '/tv3/v100.0/x']) {
    assert.deepEqual(g.checkTable(mini([{ method: 'GET', path: p }])), [], p);
  }
});

test('CORS do plugin: allow_headers, expose_headers e max_age do routes.json chegam as duas superficies, sem o modulo CORS do KrakenD', () => {
  const t = real();
  const files = g.generate(t);
  for (const [name, cfg] of Object.entries(files)) {
    if (!name.startsWith('krakend-')) continue;
    const plugin = cfg.extra_config['plugin/http-server']['tv30-auth'];
    assert.deepEqual(plugin.cors_allow_headers, t.cors.allow_headers, name);
    assert.deepEqual(plugin.cors_expose_headers, t.cors.expose_headers, name);
    assert.equal(plugin.cors_max_age, t.cors.max_age, name);
    // Without the KrakenD CORS module: it wraps the plugin and would answer the
    // preflight before it.
    assert.ok(!('security/cors' in cfg.extra_config), name);
  }
  // Without max_age and expose_headers in routes.json, the plugin gets no
  // such fields.
  const c = g.pluginConfig(mini([{ method: 'GET', path: '/a' }]), 'internal');
  assert.ok(!('cors_max_age' in c) && !('cors_expose_headers' in c));
  // cors block: only the fields the plugin uses, in the right formats.
  const comCors = cors => { const m = mini([{ method: 'GET', path: '/a' }]); m.cors = cors; return g.checkTable(m); };
  assert.deepEqual(comCors({ allow_headers: ['Content-Type'], expose_headers: ['X-A'], max_age: '12h' }), []);
  for (const [nome, cors] of Object.entries({
    'sem bloco': undefined,
    'allow_origins (sem uso)': { allow_headers: ['Content-Type'], allow_origins: ['*'] },
    'allow_methods (sem uso)': { allow_headers: ['Content-Type'], allow_methods: ['GET'] },
    'allow_headers vazio': { allow_headers: [] },
    'allow_headers com virgula': { allow_headers: ['Content-Type, Authorization'] },
    'expose_headers nao lista': { allow_headers: ['Content-Type'], expose_headers: 'X-A' },
    'max_age sem unidade': { allow_headers: ['Content-Type'], max_age: '43200' },
    'max_age zero': { allow_headers: ['Content-Type'], max_age: '0s' },
  })) assert.ok(comCors(cors).length > 0, nome);
});

test('as duas superficies listam todas as APIs, na ordem da Tabela C.2', () => {
  const t = real();
  const files = g.generate(t);
  const apis = s => plugin(files, s).apis.map(a => a.id);
  const todas = [...new Set(t.routes.filter(r => r.api).map(r => r.api.id))].sort();
  assert.deepEqual([...apis('internal')].sort(), todas);
  assert.deepEqual(apis('external'), apis('internal'));
  assert.equal(todas.length, 20);
});

test('OpenAPI: as duas superficies com todas as rotas; a externa documenta o 106 da C.4.1.8', () => {
  const files = g.generate(real());
  const ext = files['openapi-external.json'], int = files['openapi-internal.json'];
  assert.deepEqual(Object.keys(ext.paths['/tv3/bind-context']).sort(), ['delete', 'get', 'post']);
  assert.deepEqual(Object.keys(int.paths['/tv3/bind-context']).sort(), ['delete', 'get', 'post']);
  assert.equal(ext.paths['/tv3/bind-context'].post['x-tv30-surface-error'], 106);
  assert.match(ext.paths['/tv3/bind-context'].post.description, /responde 106 a qualquer requisicao/);
  assert.ok(!('x-tv30-surface-error' in int.paths['/tv3/bind-context'].post));
  assert.ok(!('x-tv30-surface-error' in ext.paths['/tv3/bind-context'].get));
  assert.match(int.paths['/tv3/current-service'].get.description, /nao local recebe 106 nesta superficie/);
  assert.doesNotMatch(int.paths['/tv3/token'].get.description, /nao local recebe 106/);
  assert.deepEqual(ext.paths['/health'].get['x-tv30-surfaces'], ['external', 'internal']);
  assert.match(ext.info.description, /C\.4\.1\.8/);
  // TLS on 44643.
  assert.equal(ext.servers[0].url, 'https://localhost:44643');
  assert.equal(int.servers[0].url, 'http://localhost:44642');
});

test('versoes por rota: a C.6.15.5 serve 2.0 e 100.0, as rotas por handle so 100.0', () => {
  const t = real();
  const files = g.generate(t);
  const pr = plugin(files, 'internal').routes;
  const vers = k => pr.find(r => pk(r) === k).versions;
  assert.deepEqual(vers('GET /tv3/remote-device/devices/{classId}'), ['2.0', '100.0']);
  assert.deepEqual(vers('GET /tv3/remote-device/device/{handle}'), ['100.0']);
  assert.deepEqual(vers('DELETE /tv3/remote-device/device/{handle}'), ['100.0']);
  // The others, only 2.0 (no field in routes.json).
  for (const r of pr.filter(r => !r.path.startsWith('/tv3/remote-device/device'))) {
    if (pk(r) !== 'GET /tv3/remote-device/devices/{classId}') assert.deepEqual(r.versions, ['2.0'], pk(r));
  }
  // C.6.7.8 reports the latest version of the API.
  const apis = plugin(files, 'external').apis;
  assert.equal(apis.find(a => a.id === 'tv3ws-remotedevice-communication').version, '100.0');
  assert.ok(apis.filter(a => a.id !== 'tv3ws-remotedevice-communication').every(a => a.version === '2.0'));
  // The internal header goes in input_headers of every route, not in the
  // OpenAPI.
  for (const v of Object.keys(t.backends.internal)) {
    for (const e of files[`krakend-internal.${v}.json`].endpoints) assert.ok(e.input_headers.includes('X-TV30-API-Version'), `${e.method} ${e.endpoint}`);
  }
  assert.ok(!files['openapi-internal.json'].paths['/tv3/current-service'].get.parameters.some(p => p.name === 'X-TV30-API-Version'));
  assert.deepEqual(files['openapi-internal.json'].paths['/tv3/remote-device/devices/{classId}'].get['x-tv30-versions'], ['2.0', '100.0']);
});

test('validacao de versions e de api.version (a mais recente da rota)', () => {
  const r = extra => mini([{ method: 'GET', path: '/a', api: { id: 'tv3ws-a', section: 'C.6.1.2', version: '2.0' }, ...extra }]);
  assert.deepEqual(g.checkTable(r({})), []);
  assert.ok(g.checkTable(r({ versions: [] })).some(e => /versions nao eh lista nao vazia/.test(e)));
  assert.ok(g.checkTable(r({ versions: ['2'] })).some(e => /fora do formato X\.Y/.test(e)));
  assert.ok(g.checkTable(r({ versions: ['2.0', '2.0'] })).some(e => /repetida/.test(e)));
  assert.ok(g.checkTable(r({ versions: ['2.0', '100.0'] })).some(e => /diferente da mais recente que a rota serve \(100\.0\)/.test(e)));
  assert.ok(g.checkTable(mini([{ method: 'GET', path: '/a', headers: ['Accept-Version', 'X-TV30-API-Version'] }])).some(e => /cabecalho interno da borda/.test(e)));
});

test('TLS so na superficie segura, com o certificado do tls-init', () => {
  const files = g.generate(real());
  for (const [name, cfg] of Object.entries(files)) {
    if (!name.startsWith('krakend-')) continue;
    if (name.startsWith('krakend-external.')) {
      assert.deepEqual(cfg.tls, { public_key: '/etc/tv30/tls/cert.pem', private_key: '/etc/tv30/tls/key.pem', min_version: 'TLS12' }, name);
    } else {
      assert.equal(cfg.tls, undefined, name);
    }
  }
});
