// Teste ESTATICO das escritas no Redis (P4, decisao do Luis em 10/10): falha
// se algum servico — tv3ws, aop, plugin da borda, carga inicial (e a migracao
// manual) — emitir comando de escrita numa familia do alcance fora do
// adaptador das funcoes. Regras em ./escritas.js. Precisa do TV30 inteiro
// (tv3ws/ e aop/ ao lado de infra/). Roda com node puro:
//   node --test infra/redis/test/escritas.test.js
//   (ou bash infra/redis/test/run.sh estatico, num node:20-alpine)
'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { verificaModuloJs, verificaGo, verificaPy, modulosTv30, esquema } = require('./escritas');

const INFRA = path.join(__dirname, '..', '..');
const TV30 = path.join(INFRA, '..');
const MODULOS = modulosTv30(TV30);

for (const [nome, cfg] of Object.entries(MODULOS)) {
    test(`${nome}: escrita nas familias do alcance so pelo adaptador ${cfg.adaptador}`, (t) => {
        assert.ok(fs.existsSync(path.join(cfg.raiz, cfg.fontes)), `${nome}/${cfg.fontes} existe (rode no TV30 com os submodulos)`);
        const r = verificaModuloJs(cfg);
        assert.deepEqual(r.violacoes, [], r.violacoes.join('\n'));
        assert.ok(r.adaptadorFuncoes.length > 0, 'o adaptador chama alguma funcao');
        t.diagnostic(`${r.arquivos} arquivo(s); escritas por arquivo: ${JSON.stringify(r.escritasPorArquivo)}; funcoes no adaptador: ${r.adaptadorFuncoes.join(', ')}`);
    });
}

test('plugin da borda (Go): nenhum comando de escrita alem de FCALL', (t) => {
    const r = verificaGo(path.join(INFRA, 'edgegateway', 'plugin'));
    assert.deepEqual(r.violacoes, [], r.violacoes.join('\n'));
    assert.ok(r.arquivos > 5);
    t.diagnostic(`${r.arquivos} arquivo(s) .go (sem os _test.go)`);
});

test('carga inicial e migracao manual (Python): so FCALL tv30_perfil_grava', (t) => {
    const arquivos = [path.join(INFRA, 'redis', 'emit_seed.py'), path.join(INFRA, 'mqtt-broker', 'infra', 'migrate_to_redis.py')];
    const r = verificaPy(arquivos);
    assert.deepEqual(r.violacoes, [], r.violacoes.join('\n'));
    const fonte = fs.readFileSync(arquivos[1], 'utf8');
    assert.match(fonte, /'FCALL', 'tv30_perfil_grava'/, 'perfil_fcall_args monta a chamada da funcao');
    t.diagnostic(`${r.arquivos} arquivo(s) .py`);
});

test('funcoes chamadas pelos adaptadores existem e cobrem as familias de cada dono', () => {
    const esq = esquema();
    const declaradas = new Set(esq.funcoes.map(f => f.nome));
    const usadas = new Set();
    for (const cfg of Object.values(MODULOS)) verificaModuloJs(cfg).adaptadorFuncoes.forEach(f => usadas.add(f));
    const go = fs.readFileSync(path.join(INFRA, 'edgegateway', 'plugin', 'redis.go'), 'utf8');
    for (const m of go.matchAll(/"(tv30_[a-z_]+)"/g)) usadas.add(m[1]);
    usadas.add('tv30_perfil_grava'); // carga inicial (teste acima)
    for (const f of usadas) assert.ok(declaradas.has(f), `${f} declarada no esquema.json`);
    // toda funcao de escrita do esquema tem quem a chame
    for (const f of esq.funcoes.filter(x => !x.somenteLeitura)) {
        assert.ok(usadas.has(f.nome), `funcao ${f.nome} sem chamador no codigo`);
    }
});
