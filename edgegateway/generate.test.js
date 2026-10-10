'use strict';
// Testes do generate.js (tabela unica de rotas, M4). Sem dependencias:
//   node --test generate.test.js
// Tambem rodam no build da imagem (Dockerfile, estagio gen): tabela que
// quebra a regra nao vira imagem.
//
// Foco: P1.9 (decisao do Luis em 10/10) — superficies com rotas distintas,
// derivadas de "classes" (ausente = todas). A externa (44643) so expoe as
// rotas que admitem o cliente nao local; a interna (44642), todas.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const g = require('./generate.js');

const real = () => JSON.parse(fs.readFileSync(path.join(__dirname, 'routes.json'), 'utf8'));
const key = r => `${r.method} ${r.path}`;
const keys = rs => rs.map(key).sort();

// tabela minima valida, para os casos sinteticos
function mini(routes) {
  return {
    backends: { external: { linux: 'https://x:1' }, internal: { linux: 'http://x:2' } },
    surfaces: { external: { port: 44643, title: 'ext' }, internal: { port: 44642, title: 'int' } },
    auth: { current_service_context_id: 'scid' },
    cors: { allow_headers: ['Content-Type'] },
    routes: routes.map(r => ({ headers: ['Accept-Version'], ...r })),
  };
}

test('routes.json atual passa na validacao', () => {
  assert.deepEqual(g.checkTable(real()), []);
});

test('regra: externa so com rotas que admitem non-local; interna com todas', () => {
  const t = real();
  const ext = g.routesOf(t, 'external');
  const int = g.routesOf(t, 'internal');
  assert.equal(int.length, t.routes.length);
  for (const r of t.routes) {
    const admite = !r.classes || r.classes.includes('non-local');
    assert.equal(ext.includes(r), admite, `${key(r)} (classes ${JSON.stringify(r.classes)})`);
  }
});

test('rotas sem restricao de classe ficam nas duas; /health e /manifest nas duas', () => {
  const t = real();
  for (const r of t.routes.filter(r => !r.classes)) assert.deepEqual(g.surfacesOf(r), ['external', 'internal'], key(r));
  for (const p of ['/health', '/manifest']) {
    const r = t.routes.find(r => r.path === p && r.method === 'GET');
    assert.ok(r, p);
    assert.deepEqual(g.surfacesOf(r), ['external', 'internal'], p);
  }
});

test('POST e DELETE /tv3/bind-context (so local-associated) ficam so na interna', () => {
  const t = real();
  const fora = t.routes.filter(r => !g.surfacesOf(r).includes('external'));
  assert.deepEqual(keys(fora), ['DELETE /tv3/bind-context', 'POST /tv3/bind-context']);
  // o GET da mesma API (autonomo e nao local) continua nas duas
  const get = t.routes.find(r => key(r) === 'GET /tv3/bind-context');
  assert.deepEqual(g.surfacesOf(get), ['external', 'internal']);
});

test('derivacao em tabela sintetica', () => {
  const cases = [
    [undefined, ['external', 'internal']],
    [['non-local'], ['external', 'internal']],
    [['local-associated', 'non-local'], ['external', 'internal']],
    [['local-autonomous', 'non-local'], ['external', 'internal']],
    [['local-autonomous'], ['internal']],
    [['local-associated'], ['internal']],
    [['local-associated', 'local-autonomous'], ['internal']],
    [[], ['internal']],
  ];
  for (const [classes, want] of cases) {
    const r = { method: 'GET', path: '/x', ...(classes ? { classes } : {}) };
    assert.deepEqual(g.surfacesOf(r), want, JSON.stringify(classes));
  }
});

test('o campo "surfaces" por rota eh recusado (a superficie eh derivada)', () => {
  const t = mini([{ method: 'GET', path: '/health', auth: 'none', surfaces: ['internal'] }]);
  const errs = g.checkTable(t);
  assert.equal(errs.length, 1, errs.join('\n'));
  assert.match(errs[0], /GET \/health: o campo "surfaces" da rota saiu \(P1\.9\)/);
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

test('superficie sem nenhuma rota eh erro (o plugin recusaria lista vazia)', () => {
  const t = mini([{ method: 'POST', path: '/so-associado', auth: 'none', classes: ['local-associated'] }]);
  const errs = g.checkTable(t);
  assert.deepEqual(errs.map(e => e.split(':')[0]), ['surfaces.external']);
});

test('arquivos gerados: 3 variantes x 2 superficies + 2 OpenAPI', () => {
  const t = real();
  const files = g.generate(t);
  const want = [];
  for (const s of Object.keys(t.surfaces)) {
    for (const v of Object.keys(t.backends[s])) want.push(`krakend-${s}.${v}.json`);
    want.push(`openapi-${s}.json`);
  }
  assert.deepEqual(Object.keys(files).sort(), want.sort());
  assert.equal(want.length, 8);
});

test('em cada config KrakenD, endpoints = rotas do plugin = rotas da superficie', () => {
  const t = real();
  const files = g.generate(t);
  for (const [name, cfg] of Object.entries(files)) {
    if (!name.startsWith('krakend-')) continue;
    const surface = name.split('-')[1].split('.')[0];
    const plugin = cfg.extra_config['plugin/http-server']['tv30-auth'];
    const esperadas = keys(g.routesOf(t, surface));
    assert.equal(plugin.surface, surface, name);
    assert.equal(cfg.port, t.surfaces[surface].port, name);
    assert.deepEqual(cfg.endpoints.map(e => `${e.method} ${e.endpoint}`).sort(), esperadas, `${name}: endpoints`);
    assert.deepEqual(keys(plugin.routes), esperadas, `${name}: rotas do plugin`);
    if (surface === 'external') {
      assert.ok(!esperadas.includes('POST /tv3/bind-context'), name);
      assert.ok(!esperadas.includes('DELETE /tv3/bind-context'), name);
      assert.ok(esperadas.includes('GET /tv3/bind-context'), name);
    }
  }
});

test('APIs da C.6.7.8/C.6.7.9 por superficie: a externa nao lista as que nao oferece', () => {
  const t = real();
  const files = g.generate(t);
  const apis = s => files[`krakend-${s}.linux.json`].extra_config['plugin/http-server']['tv30-auth'].apis.map(a => a.id);
  const todas = [...new Set(t.routes.filter(r => r.api).map(r => r.api.id))].sort();
  assert.deepEqual([...apis('internal')].sort(), todas);
  const ext = apis('external');
  assert.ok(!ext.includes('tv3ws-bind-context-register'));
  assert.ok(!ext.includes('tv3ws-bind-context-remove'));
  assert.ok(ext.includes('tv3ws-bind-context-list'));
  assert.equal(ext.length, todas.length - 2);
  // a ordem continua a da Tabela C.2 (a mesma relativa da interna)
  assert.deepEqual(ext, apis('internal').filter(id => ext.includes(id)));
});

test('OpenAPI por superficie: /tv3/bind-context so com GET na externa', () => {
  const files = g.generate(real());
  const ext = files['openapi-external.json'], int = files['openapi-internal.json'];
  assert.deepEqual(Object.keys(ext.paths['/tv3/bind-context']), ['get']);
  assert.deepEqual(Object.keys(int.paths['/tv3/bind-context']).sort(), ['delete', 'get', 'post']);
  assert.deepEqual(int.paths['/tv3/bind-context'].post['x-tv30-surfaces'], ['internal']);
  assert.match(int.paths['/tv3/bind-context'].post.description, /So nesta superficie \(internal; P1\.9\)/);
  assert.deepEqual(ext.paths['/health'].get['x-tv30-surfaces'], ['external', 'internal']);
  assert.match(ext.info.description, /P1\.9/);
  assert.equal(ext.servers[0].url, 'http://localhost:44643');
  assert.equal(int.servers[0].url, 'http://localhost:44642');
});
