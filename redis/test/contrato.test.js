// Contrato: os adaptadores reais do tv3ws e do aop contra as funcoes num Redis
// real (os dubles dos testes de unidade imitam so o efeito). O que cada servico
// grava passa no esquema e dado fora dele volta TV30ESQUEMA. Rode por run.sh contrato.
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { connect } = require('./resp');

const TV30 = path.join(__dirname, '..', '..', '..');
const TV3WS = path.join(TV30, 'tv3ws');
const AOP = path.join(TV30, 'aop');

// O AoP grava o avatar em <USER_DATA_PATH>/thumbs/<id>.svg. O TV30 esta montado
// so para leitura, entao aqui o USER_DATA_PATH e um temporario, definido antes
// de carregar o AoP (vale se ele le o ambiente na carga ou na chamada).
const USER_DATA = fs.mkdtempSync(path.join(os.tmpdir(), 'tv30-contrato-'));
const THUMBS = path.join(USER_DATA, 'thumbs');
fs.mkdirSync(THUMBS);
process.env.USER_DATA_PATH = USER_DATA;
// o mesmo formato do tipo nomeArquivo do esquema.json
const AVATAR_FORMATO = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$/;
// um SVG da paleta do formulario do AoP (aop/src/modules/profile-manager/
// avatar.js, cor Azul), como a tela de criacao o manda
const SVG_AZUL = '<svg viewBox="0 0 100 100" preserveAspectRatio="xMidYMid meet" xmlns="http://www.w3.org/2000/svg">' +
    '<rect width="100" height="100" rx="30" fill="#4d5b7c"/>' +
    '<circle cx="50" cy="40" r="14" fill="none" stroke="#ffffff" stroke-width="5"/>' +
    '<path d="M 14 90 a 36 36 0 0 1 72 0" fill="none" stroke="#ffffff" stroke-width="5" stroke-linecap="round"/>' +
    '</svg>';
const arquivoAvatar = id => path.join(THUMBS, `${id}.svg`);

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
    fs.rmSync(USER_DATA, { recursive: true, force: true });
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
    // registro fora de clients:authorized, gravado a mao (o backfill o inclui)
    await r('HSET', 'client:legado', 'class', 'local-autonomous', 'refreshToken', 'rt');
    assert.equal(await auth.backfillAuthorizedClients(), 1);
    assert.deepEqual(await auditoria(), []);
    // classe fora da lista: a funcao recusa (a C.4.1 so tem tres)
    await assert.rejects(auth.AuthorizeClient('c-x', 'admin'), esquema);
    assert.equal(await r('SISMEMBER', 'clients:authorized', 'c-x'), 0);
});

test('AoP: gestor de perfis de verdade — formulario -> Tabela 7 no esquema; ultimo acesso; sexto recusado; remocao', async () => {
    await tv.sessaoServicoId('urn:tv30:service:users-test');
    const bodies = [
        { nickname: 'Viewer 1' },
        // com o avatar escolhido no formulario
        { nickname: 'é'.repeat(20), parentalControl: true, maxContentRating: 'L', audioLanguage: 'en', userInterfaceLanguage: 'pt-BR', avatar: SVG_AZUL },
        { nickname: 'Libras', closedSigning: true, closedSigningSide: 'left', closedSigningWidth: '14' },
        { nickname: 'Legenda', closedCaptioning: true, closedCaptioningLanguage: 'es-419', audioDescription: true, dialogEnhancement: 'on', voiceGuidance: 1 },
        { nickname: 'Visivel', accessConsent: ['urn:tv30:service:webmedia', 'urn:tv30:service:uff'] },
    ];
    const ids = [];
    // o AoP gera o id com crypto.randomUUID(): versao 4, variante da RFC 9562
    const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
    for (const b of bodies) {
        const { user } = await profiles.createProfile(b);
        assert.match(user.id, UUID, 'id do perfil criado pelo AoP');
        ids.push(user.id);
    }
    assert.equal(new Set(ids).size, 5);
    assert.equal(await r('SCARD', 'users:index'), 5);
    assert.deepEqual(await auditoria(), [], 'perfis montados pelo AoP cumprem o esquema');
    // avatar = <id>.svg, com o SVG do formulario no arquivo; sem avatar no
    // formulario, o campo pode faltar (o avatar padrao de cada tela)
    for (const [i, id] of ids.entries()) {
        const av = await r('HGET', `user:${id}`, 'avatar');
        if (av === null && !bodies[i].avatar) continue;
        assert.equal(av, `${id}.svg`, `avatar do perfil ${i} (${bodies[i].nickname})`);
        assert.match(av, AVATAR_FORMATO);
        const svg = fs.readFileSync(arquivoAvatar(id), 'utf8');
        assert.ok(svg.startsWith('<svg'), `arquivo do avatar do perfil ${i} e SVG: ${svg.slice(0, 40)}`);
    }
    assert.ok(fs.readFileSync(arquivoAvatar(ids[1]), 'utf8').includes('#4d5b7c'), 'o SVG escolhido no formulario vai para o arquivo');
    assert.equal(await profiles.touchLastAccess(ids[1], 5), true);
    assert.equal(await profiles.touchLastAccess('desconhecido', 5), false);
    // atributo de emissora no perfil que sera removido abaixo (o tv3ws grava)
    assert.equal(await tv.atributosEmissora(ids[0], 'urn:tv30:service:users-test', { cor: 'azul' }), 1);
    await assert.rejects(tv.atributosEmissora(ids[0], 'urn:tv30:service:users-test', { nickname: 'x' }), esquema);
    // UUID valido fora de users:index (invariante perfil-dependentes) e id que
    // nao e UUID
    await assert.rejects(tv.atributosEmissora('9d1e2f3a-4b5c-4d6e-8f70-81a2b3c4d5e6', 'urn:s', { cor: 'azul' }), { message: /perfil-dependentes/ });
    await assert.rejects(tv.atributosEmissora('user_1760000000000', 'urn:s', { cor: 'azul' }), { message: /^TV30ESQUEMA .*RFC 9562/ });
    // o UUID nulo e a versao fora de 1 a 8 voltam como TV30ESQUEMA, com a parte que falhou
    await assert.rejects(tv.atributosEmissora('00000000-0000-0000-0000-000000000000', 'urn:s', { cor: 'azul' }), { message: /^TV30ESQUEMA .*UUID nulo/ });
    await assert.rejects(tv.atributosEmissora('9d1e2f3a-4b5c-9d6e-8f70-81a2b3c4d5e6', 'urn:s', { cor: 'azul' }), { message: /^TV30ESQUEMA .*versao do UUID deve ser de 1 a 8/ });
    // sexto perfil: a funcao que grava recusa pelo teto do esquema, e o AoP
    // responde a mensagem da tela; sem despejo nem arquivo novo
    const thumbsAntes = fs.readdirSync(THUMBS).sort();
    await assert.rejects(profiles.createProfile({ nickname: 'Sexto' }),
        err => err instanceof profiles.ValidationError && err.message === 'O limite de 5 perfis foi atingido. O perfil não foi criado.');
    const novo = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    await assert.rejects(aop.perfilGrava(novo, [], {
        id: novo, nickname: 'Direto', parentalControl: 'false', closedCaptioning: 'false', closedSigning: 'false',
        audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false',
    }), err => aop.tetoDePerfis(err) === 5);
    assert.equal(await r('SCARD', 'users:index'), 5);
    assert.equal(await r('HGET', `user:${ids[0]}:broadcaster-attrs:urn:tv30:service:users-test`, 'cor'), 'azul');
    assert.deepEqual(fs.readdirSync(THUMBS).sort(), thumbsAntes, 'a recusa nao deixa arquivo de avatar');
    // no diretorio de miniaturas, so os avatares dos perfis que existem
    const vivos = [];
    for (const id of await r('SMEMBERS', 'users:index')) {
        const av = await r('HGET', `user:${id}`, 'avatar');
        if (av !== null) vivos.push(av);
    }
    assert.deepEqual(fs.readdirSync(THUMBS).sort(), vivos.sort(), 'arquivos de thumbs/ = avatares dos perfis existentes');
    // a remocao pelo gestor leva visibilidade e atributos de emissora na mesma
    // funcao (tv30_perfil_remove) e depois o arquivo do avatar
    assert.ok(fs.existsSync(arquivoAvatar(ids[0])));
    await profiles.removeProfile(ids[0]);
    assert.deepEqual(await r('KEYS', `user:${ids[0]}*`), [], 'a remocao leva visibilidade e atributos de emissora juntos');
    assert.equal(await r('SISMEMBER', 'users:index', ids[0]), 0);
    assert.equal(fs.existsSync(arquivoAvatar(ids[0])), false, 'o arquivo do avatar nao existe mais');
    assert.deepEqual(await auditoria(), []);
    // com quatro, o quinto volta a caber
    const { user } = await profiles.createProfile({ nickname: 'Depois' });
    assert.equal(await r('SCARD', 'users:index'), 5);
    assert.equal(await r('SISMEMBER', 'users:index', user.id), 1);
    assert.deepEqual(await auditoria(), []);
});

test('AoP: perfil padrao na base sem perfil cumpre o esquema; o unico perfil nao e removido', async () => {
    for (const id of await r('SMEMBERS', 'users:index')) await profiles.removeProfile(id).catch(() => aop.perfilRemove(id));
    assert.equal(await r('SCARD', 'users:index'), 0);
    const p = await profiles.ensureDefaultProfile();
    assert.ok(p, 'perfil padrao criado');
    assert.deepEqual(await r('SMEMBERS', 'users:index'), [p.id]);
    assert.equal(await r('HGET', `user:${p.id}`, 'nickname'), 'Viewer 1');
    assert.equal(await r('EXISTS', `user:${p.id}:consent`), 0, 'sem visibilidade');
    assert.deepEqual(await auditoria(), [], 'o perfil padrao cumpre o esquema');
    assert.equal(await profiles.ensureDefaultProfile(), null);
    await assert.rejects(profiles.removeProfile(p.id), err => err instanceof profiles.ValidationError);
    assert.equal(await r('SISMEMBER', 'users:index', p.id), 1);
});

test('AoP: aop-transport-layer-metadata-lls de verdade — BAMT -> services:announced pela funcao, o mesmo em aop/services; retirada; recusa', async () => {
    const lls = require(path.join(AOP, 'src', 'modules', 'transport-layer-metadata-lls'));
    await r('SADD', 'services:announced', 'urn:tv30:service:da-partida-anterior');
    const publicados = [];
    await lls.start((t, m) => publicados.push([t, m]));
    assert.equal(await r('EXISTS', 'services:announced'), 0, 'a partida tira a lista anterior');
    const bam = (g, n) => ({ globalServiceId: g, appVersion: 1, appName: n, appIcon: '<svg/>', bannerIcon: '<svg/>', backgroundColor: '#000000', foregroundColor: '#ffffff' });
    const W = 'urn:tv30:service:webmedia', U = 'urn:tv30:service:uff', X = 'Ação Ü!';
    await lls.receive(JSON.stringify([bam(W, 'WebMedia'), bam(U, 'UFF')]), 'tlm/lls/bs1/bamt');
    await lls.receive(JSON.stringify([bam(U, 'UFF'), bam(X, 'X')]), 'tlm/lls/bs2/bamt');
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), [W, U, X].sort());
    assert.deepEqual(publicados.at(-1), ['aop/services', JSON.stringify([W, U, X].map(serviceContextId => ({ serviceContextId })))]);
    assert.deepEqual((await lls.services()).map(s => s.serviceContextId), [W, U, X]);
    assert.deepEqual(await auditoria(), []);
    await lls.receive('', 'tlm/lls/bs1/bamt');
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), [U, X].sort());
    // globalServiceId acima do idServico do esquema: a funcao recusa e o
    // conjunto anterior fica
    const errorOrig = console.error;
    console.error = () => undefined;
    try {
        await lls.receive(JSON.stringify([bam('x'.repeat(257), 'Longo')]), 'tlm/lls/bs3/bamt');
    } finally { console.error = errorOrig; }
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), [U, X].sort());
    assert.deepEqual(publicados.at(-1), ['aop/services', JSON.stringify([U, X].map(serviceContextId => ({ serviceContextId })))]);
    await lls.receive('', 'tlm/lls/bs3/bamt');
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
