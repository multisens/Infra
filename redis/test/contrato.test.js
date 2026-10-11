// CONTRATO entre os adaptadores de verdade e as funcoes do Redis (P4, decisao
// do Luis em 10/10). Os dubles dos testes de unidade (tv3ws e aop) imitam so
// o EFEITO das funcoes; aqui o codigo dos servicos fala com um Redis REAL com
// a biblioteca tv30 carregada: prova que o formato que cada servico grava
// passa no esquema (inclusive o que o gestor de perfis monta a partir do
// formulario), que dado fora do esquema volta como erro TV30ESQUEMA e que, no
// fim, a auditoria esta vazia.
//
// Rode por: bash infra/redis/test/run.sh contrato (Redis descartavel +
// node:20-alpine com o TV30 montado; usa tv3ws/node_modules e
// aop/node_modules). O contrato do plugin Go roda em seguida, no mesmo Redis
// (edgegateway/plugin/contrato_test.go).
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { connect } = require('./resp');

const TV30 = path.join(__dirname, '..', '..', '..');
const TV3WS = path.join(TV30, 'tv3ws');
const AOP = path.join(TV30, 'aop');

// tv3ws e TypeScript: ts-node do proprio tv3ws, so transpilando
require(path.join(TV3WS, 'node_modules', 'ts-node')).register({
    project: path.join(TV3WS, 'tsconfig.json'), transpileOnly: true,
});
const tv = require(path.join(TV3WS, 'src', 'redis-funcoes.ts'));
const tvRedis = require(path.join(TV3WS, 'src', 'redis-client.ts')).default;
const auth = require(path.join(TV3WS, 'src', 'modules', 'auth-manager', 'manager.ts'));
const aop = require(path.join(AOP, 'src', 'redis-funcoes.js'));
const aopRedis = require(path.join(AOP, 'src', 'redis-client.js'));
const profiles = require(path.join(AOP, 'src', 'modules', 'profile-manager', 'service.js'));
const appHosts = require(path.join(AOP, 'src', 'app-hosts.js'));

let r;
const auditoria = () => r('FCALL_RO', 'tv30_auditoria', 0);
// recusa da funcao: o ioredis rejeita com ReplyError cuja message e o erro do Redis
const esquema = { message: /^TV30ESQUEMA / };

before(async () => {
    r = await connect();
    await r('FLUSHALL');
});
after(async () => {
    await tvRedis.quit();
    await aopRedis.quit();
    await r.close();
});

test('tv3ws: espelhos da sessao e o hash do servico corrente', async () => {
    await tv.sessaoUsuario('');
    await tv.sessaoUsuario('c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10');
    await tv.sessaoServicoId('urn:tv30:service:users-test');
    await tv.sessaoServico({
        serviceContextId: 'c08b2c72-fd14-4095-adaf-2e5810850c57', serviceId: '', serviceName: '',
        transportStreamId: 'c08b2c72-fd14-4095-adaf-2e5810850c57', originalNetworkId: '09e59a1d-e2e7-467e-85db-2fb5a572e2fc',
    });
    assert.equal(await r('GET', 'session:current-service-id'), 'urn:tv30:service:users-test');
    assert.equal((await r('HGETALL', 'session:current-service')).length, 10);
    await assert.rejects(tv.sessaoUsuario('x'.repeat(65)), esquema);
    await assert.rejects(tv.sessaoServico({ serviceContextId: 'c' }), esquema);
    assert.deepEqual(await auditoria(), []);
});

test('tv3ws: auth-manager de verdade (autorizar, emitir e rotacionar, bloquear, incluir no boot)', async () => {
    const ok = 'f81d4fae-7dec-11d0-a765-00a0c91e6bf6';
    await auth.AuthorizeClient(ok, 'local-autonomous');
    const [token] = await auth.getClientAccessToken(ok);
    await auth.rotateRefreshToken(ok);
    const h = await r('HGETALL', `client:${ok}`);
    const o = {}; for (let i = 0; i < h.length; i += 2) o[h[i]] = h[i + 1];
    assert.equal(o.class, 'local-autonomous');
    assert.equal(o.accessToken, token);
    assert.ok(o.refreshToken);
    await auth.BlockClient('cliente-recusado');
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['cliente-recusado']);
    await auth.AuthorizeClient('cliente-associado', 'local-associated');
    await auth.AuthorizeClient('cliente-nao-local', 'non-local');
    // registro anterior ao conjunto (gravado a mao, como a base antiga)
    await r('HSET', 'client:legado', 'class', 'local-autonomous', 'refreshToken', 'rt');
    assert.equal(await auth.backfillAuthorizedClients(), 1);
    assert.deepEqual(await auditoria(), []);
    // classe fora da lista: a funcao recusa (a C.4.1 so tem tres)
    await assert.rejects(auth.AuthorizeClient('c-x', 'admin'), esquema);
    assert.equal(await r('SISMEMBER', 'clients:authorized', 'c-x'), 0);
});

test('AoP: gestor de perfis de verdade — formulario -> Tabela 7 no esquema; ultimo acesso; despejo P3', async () => {
    await tv.sessaoServicoId('urn:tv30:service:users-test');
    const bodies = [
        { nickname: 'Viewer 1' },
        { nickname: 'é'.repeat(20), parentalControl: true, maxContentRating: 'L', audioLanguage: 'en', userInterfaceLanguage: 'pt-BR' },
        { nickname: 'Libras', closedSigning: true, closedSigningSide: 'left', closedSigningWidth: '14' },
        { nickname: 'Legenda', closedCaptioning: true, closedCaptioningLanguage: 'es-419', audioDescription: true, dialogEnhancement: 'on', voiceGuidance: 1 },
        { nickname: 'Visivel', accessConsent: ['urn:tv30:service:webmedia', 'urn:tv30:service:uff'] },
    ];
    const ids = [];
    // F-3 (decisao do Luis em 11/10): o AoP gera o id do perfil com
    // crypto.randomUUID(); o esquema exige UUID em minusculas (RFC 9562)
    const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
    for (const b of bodies) {
        const { user } = await profiles.createProfile(b);
        assert.match(user.id, UUID, 'id do perfil criado pelo AoP');
        ids.push(user.id);
        await new Promise(res => setTimeout(res, 2)); // lastAccess distintos (ordem do despejo)
    }
    assert.equal(new Set(ids).size, 5);
    assert.equal(await r('SCARD', 'users:index'), 5);
    assert.deepEqual(await auditoria(), [], 'perfis montados pelo AoP cumprem o esquema');
    assert.equal(await profiles.touchLastAccess(ids[1], 5), true);
    assert.equal(await profiles.touchLastAccess('desconhecido', 5), false);
    // atributo de emissora no perfil que sera despejado (o tv3ws grava)
    assert.equal(await tv.atributosEmissora(ids[0], 'urn:tv30:service:users-test', { cor: 'azul' }), 1);
    await assert.rejects(tv.atributosEmissora(ids[0], 'urn:tv30:service:users-test', { nickname: 'x' }), esquema);
    // UUID valido fora de users:index (invariante perfil-dependentes) e id no
    // formato antigo (fora do esquema)
    await assert.rejects(tv.atributosEmissora('9d1e2f3a-4b5c-4d6e-8f70-81a2b3c4d5e6', 'urn:s', { cor: 'azul' }), { message: /perfil-dependentes/ });
    await assert.rejects(tv.atributosEmissora('user_1760000000000', 'urn:s', { cor: 'azul' }), { message: /^TV30ESQUEMA .*RFC 9562/ });
    // sexto perfil: despeja o de ultimo acesso mais antigo (ids[1] ficou com 5)
    const { evicted } = await profiles.createProfile({ nickname: 'Sexto' });
    assert.equal(evicted.id, ids[1]);
    assert.equal(await r('EXISTS', `user:${ids[1]}`), 0);
    assert.equal(await r('SCARD', 'users:index'), 5);
    const { evicted: e2 } = await profiles.createProfile({ nickname: 'Setimo' });
    assert.equal(e2.id, ids[0]);
    assert.deepEqual(await r('KEYS', `user:${ids[0]}*`), [], 'o despejo leva os atributos de emissora juntos');
    assert.deepEqual(await auditoria(), []);
});

test('AoP: origens montadas por app-hosts cumprem o esquema; registro, remocao e limpeza', async () => {
    const cfg = appHosts.appsConfig({ APPS_DOMAIN: 'apps.localhost', AOP_PUBLIC_PORT: '8080' });
    const cfg80 = appHosts.appsConfig({ APPS_DOMAIN: 'tv30.example', AOP_PUBLIC_PORT: '80' });
    const origens = [];
    for (const [id, c] of [['urn:tv30:app:k1a2b3', cfg], ['Ação Ü!', cfg], ['x'.repeat(100), cfg], ['urn:tv30:service:users-test', cfg80]]) {
        const d = appHosts.describeApp(id, 'http://bcast:8081', c);
        assert.ok(d && d.origin, `describeApp(${id})`);
        await aop.origemRegistra(d.origin, 'urn:tv30:service:users-test');
        origens.push(d.origin);
    }
    await aop.origemRegistra('http://uff.apps.localhost:8080', 'current-service');
    assert.deepEqual(await auditoria(), [], `origens: ${origens.join(' ')}`);
    await assert.rejects(aop.origemRegistra('http://bcast:8081/', 'urn:s'), esquema);
    await assert.rejects(aop.origemRegistra('http://Uff.apps.localhost:8080', 'urn:s'), esquema);
    await aop.origemRemove(origens[0]);
    assert.equal(await r('HEXISTS', 'origins:associated', origens[0]), 0);
    await aop.origensLimpa();
    assert.equal(await r('EXISTS', 'origins:associated'), 0);
    assert.deepEqual(await auditoria(), []);
});
