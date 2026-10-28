// Fails if tv3ws, aop, the edge plugin, the initial load or the manual
// migration writes to a family in scope outside the adapter of the functions
// (rules in ./writes.js). Needs the whole TV30:
//   node --test infra/redis/test/writes.test.js
'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { checkJsModule, checkGo, checkPy, tv30Modules, schema } = require('./writes');

const INFRA = path.join(__dirname, '..', '..');
const TV30 = path.join(INFRA, '..');
const MODULES = tv30Modules(TV30);

for (const [name, cfg] of Object.entries(MODULES)) {
    test(`${name}: escrita nas familias do alcance so pelo adaptador ${cfg.adapter}`, (t) => {
        assert.ok(fs.existsSync(path.join(cfg.root, cfg.sources)), `${name}/${cfg.sources} existe (rode no TV30 com os submodulos)`);
        const r = checkJsModule(cfg);
        assert.deepEqual(r.violations, [], r.violations.join('\n'));
        assert.ok(r.adapterFunctions.length > 0, 'o adaptador chama alguma funcao');
        t.diagnostic(`${r.files} arquivo(s); escritas por arquivo: ${JSON.stringify(r.writesPerFile)}; funcoes no adaptador: ${r.adapterFunctions.join(', ')}`);
    });
}

test('plugin da borda (Go): nenhum comando de escrita alem de FCALL', (t) => {
    const r = checkGo(path.join(INFRA, 'edgegateway', 'plugin'));
    assert.deepEqual(r.violations, [], r.violations.join('\n'));
    assert.ok(r.files > 5);
    t.diagnostic(`${r.files} arquivo(s) .go (sem os _test.go)`);
});

test('carga inicial e migracao manual (Python): so FCALL tv30_profile_write', (t) => {
    const files = [path.join(INFRA, 'redis', 'emit_seed.py'), path.join(INFRA, 'mqtt-broker', 'infra', 'migrate_to_redis.py')];
    const r = checkPy(files);
    assert.deepEqual(r.violations, [], r.violations.join('\n'));
    const source = fs.readFileSync(files[1], 'utf8');
    assert.match(source, /'FCALL', 'tv30_profile_write'/, 'profile_fcall_args monta a chamada da funcao');
    t.diagnostic(`${r.files} arquivo(s) .py`);
});

test('funcoes chamadas pelos adaptadores existem e cobrem as familias de cada dono', () => {
    const s = schema();
    const declared = new Set(s.functions.map(f => f.name));
    const used = new Set();
    for (const cfg of Object.values(MODULES)) checkJsModule(cfg).adapterFunctions.forEach(f => used.add(f));
    const go = fs.readFileSync(path.join(INFRA, 'edgegateway', 'plugin', 'redis.go'), 'utf8');
    for (const m of go.matchAll(/"(tv30_[a-z0-9_]+)"/g)) used.add(m[1]);
    // Initial load (test above).
    used.add('tv30_profile_write');
    for (const f of used) assert.ok(declared.has(f), `${f} declarada no schema.json`);
    // Every write function of the schema has a caller.
    for (const f of s.functions.filter(x => !x.readOnly)) {
        assert.ok(used.has(f.name), `funcao ${f.name} sem chamador no codigo`);
    }
});
