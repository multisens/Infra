// Tests of the "tv30" library on a real Redis (run by
// infra/redis/test/run.sh). The refusal cases are generated from
// schema.json: each rule gets values that violate it, and the function must
// answer TV30SCHEMA without writing anything. A new rule gets cases without
// editing this file, if the kind of rule is known.
'use strict';
const { test, before, after, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { connect } = require('./resp');

const SCHEMA = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'schema.json'), 'utf8'));
const PREFIX = SCHEMA.errorPrefix;

// ------------------------------------------------------------------ data --
const P1 = '0b6e3c1a-7f2d-4e8b-9a5c-3d1f2e4a6b8c';
const P2 = '5a9d2f4e-1c3b-4d6a-8e7f-9b0c1d2e3f4a';
const GHOST = 'f0f0f0f0-0000-4000-8000-000000000001';
const ORPHAN = 'f0f0f0f0-0000-4000-8000-000000000002';
const NO_HASH = 'f0f0f0f0-0000-4000-8000-000000000003';
// UUID version: first digit of the third group; variant: first digit of the
// fourth group.
const UUID_VERSION_DIGIT = 14;
const UUID_VARIANT_DIGIT = 19;
const SVG_INLINE = '<svg viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"><rect width="100" height="100" rx="30" fill="#4d7c5b"/></svg>';
// The image of the avatar set that every test starts with (resetDatabase).
const AVATAR = 'green.svg';
const AVATAR_IMG = { label: 'Verde', type: 'image/svg+xml', body: SVG_INLINE };
const PROFILE = {
    id: P1, nickname: 'Viewer 1', avatar: AVATAR,
    parentalControl: 'true', maxContentRating: '12',
    audioLanguage: 'pt-BR', closedCaptioningLanguage: 'pt-BR', userInterfaceLanguage: 'en',
    closedCaptioning: 'true', closedSigning: 'true', closedSigningSide: 'right', closedSigningWidth: '28',
    audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false', lastAccess: '1760000000000',
};
// Minimal profile: only the required fields (false conditions).
const PROFILE_MIN = {
    id: P1, nickname: 'Viewer 1', parentalControl: 'false', closedCaptioning: 'false', closedSigning: 'false',
    audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false',
};
const SVC = {
    serviceContextId: 'urn:tv30:service:users-test', serviceId: '', serviceName: '',
};
const CLIENT = { class: 'standAlone', refreshToken: 'rt-1', accessToken: 'at-1' };
const BIND_ENTRY = { alg: 'HS256', key: 'segredo', registeredAt: 1760000000000 };
// A valid BAM and a valid record of services:announced.
const BAM = {
    globalServiceId: 'urn:tv30:service:a', appVersion: 1, appName: 'Canal A', backgroundColor: '#000000',
    foregroundColor: '#ffffff', appIcon: SVG_INLINE, bannerIcon: SVG_INLINE, fallbackMediaURLs: ['http://x.example/a.mpd'],
};
const ANNOUNCED_RECORD = { bsid: 10, serviceId: 1001, majorChannelNo: 10, minorChannelNo: 1, mainAvOverTheAir: true, receptionQuality: 80, bam: BAM };
const ORIGIN = 'http://users-test.apps.localhost:8080';
// A file of the Application Context Cache: tv30_cache_put arguments.
const CTX = 'urn:tv30:ctx:a';
const CACHE_BODY = '<html></html>';
const META = { contentType: 'text/html', lastModified: 1763000000000, size: Buffer.byteLength(CACHE_BODY) };
const LARGE = String(1e9);
const APP = { serviceContextId: 'urn:tv30:service:users-test', appId: 'urn:tv30:app:1', origin: 'http://ctx-0123456789abcdef.apps.localhost:8081' };
const RD_HANDLE = '2b7f6c1e-4d3a-4f8b-9c2d-1e0f3a4b5c6d';
const RD = {
    deviceId: '7c1d2e3f-4a5b-4c6d-8e7f-0a1b2c3d4e5f', deviceClass: 'sensory-effect', supportedTypes: '["light","wind"]',
    state: 'connected', paired: 'false', disconnectedAt: '1760000000000', registeredBy: 'dispositivo-1',
};


// Catches a deviation between the schema and the expression of the contract
// that the cases generated from the schema itself do not catch (an extension
// removed from the list, maxName changed).
const AVATAR_CONTRACT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.svg$/;

// --------------------------------------------- calls assembled per slot --
// For each family: the "slots" (places where a rule applies) and how to
// assemble a valid call with the value of the slot replaced. prep runs
// first.
const SLOTS = {
    'avatar:{name}': [
        { label: '{name}', rule: f => f.parts.name, call: v => ['tv30_avatar_write', avatarArgs(v, AVATAR_IMG)], excerpt: '{name} of the key:' },
        ...perFieldSlots(fieldName => v => ['tv30_avatar_write', avatarArgs('x.svg', { ...AVATAR_IMG, [fieldName]: v })]),
    ],
    'session:current-user': [{ label: 'valor', rule: f => f.value, call: v => ['tv30_session_user', [v]], excerpt: 'value:' }],
    // The id is written only with the hash, as its serviceContextId; empty,
    // by the call without arguments.
    'session:current-service-id': [{ label: 'valor', rule: f => f.value, call: v => ['tv30_session_service', v === '' ? [] : flatten({ ...SVC, serviceContextId: v })], excerpt: 'field serviceContextId:' }],
    'session:current-service': perFieldSlots(fieldName => v => ['tv30_session_service', flatten({ ...SVC, [fieldName]: v })]),
    'cache:services': [
        { label: 'nome de campo', rule: f => f.otherFields.name, call: v => cachePutCall({ ctx: v }), excerpt: 'field name:' },
        { label: 'valor', rule: f => f.otherFields.value, call: v => cachePutCall({ scid: v }), excerpt: 'field "' },
    ],
    'cache:last-access': [
        { label: 'valor', rule: f => f.otherFields.value, call: v => cachePutCall({ now: v }), excerpt: 'field "' },
    ],
    'cache:meta:{appContextId}': [
        { label: 'caminho', rule: f => f.otherFields.name, call: v => cachePutCall({ path: v }), excerpt: 'field name:' },
        { label: 'metadados', rule: f => f.otherFields.value, call: v => cachePutCall({ meta: v }), excerpt: 'field "' },
    ],
    'cache:body:{appContextId}': [
        { label: 'caminho', rule: f => f.otherFields.name, call: v => cachePutCall({ path: v }), excerpt: 'field name:' },
        { label: 'corpo', rule: f => f.otherFields.value, call: v => cachePutCall({ body: v }), excerpt: 'field "' },
    ],
    'session:current-app': perFieldSlots(fieldName => v => ['tv30_current_app', flatten({ ...APP, [fieldName]: v })]),
    'users:index': [{ label: 'member', rule: f => f.members, call: v => ['tv30_profile_write', profileArgs(v, [], { ...PROFILE, id: v })], excerpt: '{id} of the key:' }],
    'user:{id}': [
        { label: '{id}', rule: f => f.parts.id, call: v => ['tv30_profile_write', profileArgs(v, [], { ...PROFILE, id: v })], excerpt: '{id} of the key:' },
        // The id field has the same rule as the {id} part (slot above) and must
        // equal it (own test below): left out of the per-field slots.
        ...perFieldSlots(fieldName => v => ['tv30_profile_write', profileArgs(P1, [], { ...PROFILE, [fieldName]: v })], ['id'], { avatar: writeAvatar }),
    ],
    'user:{id}:consent': [
        { label: 'member', rule: f => f.members, call: v => ['tv30_profile_write', profileArgs(P1, ['urn:tv30:service:ok', v], PROFILE)], excerpt: 'member' },
        { label: '{id}', rule: f => f.parts.id, call: v => ['tv30_profile_write', profileArgs(v, ['urn:x'], { ...PROFILE, id: v })], excerpt: '{id} of the key:' },
    ],
    'user:{id}:broadcaster-attrs:{scid}': [
        // The profile of the value must exist for the acceptance; with an
        // invalid value the creation fails and the refusal comes from the {id}
        // part of the key.
        { label: '{id}', rule: f => f.parts.id, prep: v => fcall('tv30_profile_write', ...profileArgs(v, [], { ...PROFILE, id: v })).catch(() => undefined), call: v => ['tv30_broadcaster_attributes', [v, 'urn:s:1', 'cor', 'azul']], excerpt: '{id} of the key:' },
        { label: '{scid}', rule: f => f.parts.scid, prep: indexP1, call: v => ['tv30_broadcaster_attributes', [P1, v, 'cor', 'azul']], excerpt: '{scid} of the key:' },
        { label: 'nome de campo', rule: f => f.otherFields.name, prep: indexP1, call: v => ['tv30_broadcaster_attributes', [P1, 'urn:s:1', v, 'azul']], excerpt: 'field name:' },
        // otherFields.value (text, min 1): empty REMOVES the field in the
        // function, so no writable value violates the rule; the audit covers it
        // (below).
    ],
    'client:{id}': [
        { label: '{id}', rule: f => f.parts.id, call: v => ['tv30_client_write', [v, ...flatten(CLIENT)]], excerpt: '{id} of the key:' },
        ...perFieldSlots(fieldName => v => ['tv30_client_write', ['c1', ...flatten({ ...CLIENT, [fieldName]: v })]]),
    ],
    'clients:authorized': [{ label: 'member', rule: f => f.members, call: v => ['tv30_client_authorize', [v, ...flatten(CLIENT)]], excerpt: '{id} of the key:' }],
    'clients:blocked': [{ label: 'member', rule: f => f.members, call: v => ['tv30_client_block', [v]], excerpt: '{id} of the key:' }],
    'origins:associated': [
        { label: 'nome de campo', rule: f => f.otherFields.name, call: v => ['tv30_origin_register', [v, 'urn:tv30:service:users-test']], excerpt: 'field name:' },
        { label: 'valor', rule: f => f.otherFields.value, call: v => ['tv30_origin_register', [ORIGIN, v]], excerpt: 'field "' },
    ],
    'services:announced': [
        { label: 'nome de campo', rule: f => f.otherFields.name, call: v => ['tv30_announced_services', [v, JSON.stringify(ANNOUNCED_RECORD)]], excerpt: 'field name:' },
        { label: 'registro', rule: f => f.otherFields.value, call: v => ['tv30_announced_services', ['urn:tv30:service:a', v]], excerpt: 'field "' },
    ],
    'v100.0:remote-device:{handle}': [
        { label: '{handle}', rule: f => f.parts.handle, call: v => ['tv30_v100_remote_device_write', [v, ...flatten(RD)]], excerpt: '{handle} of the key:' },
        ...perFieldSlots(fieldName => v => ['tv30_v100_remote_device_write', [RD_HANDLE, ...flatten({ ...RD, [fieldName]: v })]]),
    ],
    'bind-context:{serviceId}': [
        { label: '{serviceId}', rule: f => f.parts.serviceId, call: v => ['tv30_bind_register', [v, JSON.stringify(BIND_ENTRY)]], excerpt: '{serviceId} of the key:' },
        { label: 'entrada', rule: f => f.items, call: v => ['tv30_bind_register', ['urn:tv30:service:a', v]], excerpt: 'entry:' },
    ],
};

let r;

before(async () => {
    r = await connect();
});
after(async () => {
    if (r) {
        await r.close();
    }
});
beforeEach(resetDatabase);

// ------------------------------------------------------------------ load --
test('biblioteca carregada: exatamente as funcoes do schema.json, flags certas', async () => {
    const list = await r('FUNCTION', 'LIST', 'LIBRARYNAME', SCHEMA.library);
    assert.equal(list.length, 1, 'uma biblioteca tv30');
    const lib = {};
    for (let i = 0; i < list[0].length; i += 2) {
        lib[list[0][i]] = list[0][i + 1];
    }
    assert.equal(lib.library_name, SCHEMA.library);
    const loaded = {};
    for (const fn of lib.functions) {
        const o = {};
        for (let i = 0; i < fn.length; i += 2) {
            o[fn[i]] = fn[i + 1];
        }
        loaded[o.name] = o.flags;
    }
    assert.deepEqual(Object.keys(loaded).sort(), SCHEMA.functions.map(f => f.name).sort());
    for (const f of SCHEMA.functions) {
        const flags = loaded[f.name];
        assert.ok(flags.includes('no-cluster'), `${f.name}: no-cluster`);
        assert.equal(flags.includes('no-writes'), !!f.readOnly, `${f.name}: no-writes so nas somente leitura`);
    }
    // Each function named in a family exists; each family with a writer in
    // the testbed is known to the refusal test. A family without functions
    // is only declared: its owner is outside the testbed.
    const fnNames = new Set(SCHEMA.functions.map(f => f.name));
    for (const fam of SCHEMA.families) {
        for (const n of fam.functions) {
            assert.ok(fnNames.has(n), `${fam.family} cita ${n}`);
        }
        if (fam.functions.length === 0) {
            assert.ok(/no writer/.test(fam.owner), `${fam.family}: sem funcoes, o dono declara que nao ha escritor`);
            continue;
        }
        assert.ok(SLOTS[fam.family], `${fam.family}: o teste de recusa conhece a familia`);
    }
    assert.equal(await fcallRo('tv30_version'), SCHEMA.version);
});

test('FCALL_RO so roda as somente leitura; numkeys > 0 e argumentos errados sao recusados', async () => {
    await assert.rejects(fcallRo('tv30_session_user', 'u'), /write flag/);
    await assert.rejects(r('FCALL', 'tv30_session_user', 1, 'session:current-user', 'u'), e => e.message.startsWith(`${PREFIX} tv30_session_user: call with numkeys 1`));
    await expectRefusal('sem argumento', 'tv30_session_user', [], ['usage:']);
    await expectRefusal('pares incompletos', 'tv30_session_service', ['serviceContextId'], ['incomplete field value pairs']);
    await expectRefusal('n invalido', 'tv30_profile_write', [P1, 'x', 'id', P1], ['n=']);
    await expectRefusal('n alem dos argumentos', 'tv30_profile_write', [P1, '9', 'a'], ['n=']);
    await assert.rejects(fcallRo('tv30_audit', 'x'), new RegExp(`${PREFIX} tv30_audit: usage:`));
});

// --------------------------------------- refusal of each rule (generated) --
for (const fam of SCHEMA.families.filter(f => f.functions.length > 0)) {
    test(`recusa de cada regra: ${fam.family}`, async (t) => {
        let cases = 0;
        let accepted = 0;
        for (const slot of expandSlots(fam)) {
            const rule = resolve(slot.rule(fam));
            for (const [value, excerpt, desc] of violations(rule)) {
                await resetDatabase();
                if (slot.prep) {
                    await slot.prep(value);
                }
                const [fnName, args] = slot.call(value);
                await expectRefusal(`${fam.family} ${slot.label} ${desc}`, fnName, args, [slot.excerpt, ...[].concat(excerpt)]);
                cases++;
            }
            for (const value of acceptances(rule)) {
                await resetDatabase();
                if (slot.prep) {
                    await slot.prep(value);
                }
                const [fnName, args] = slot.call(value);
                await assert.doesNotReject(fcall(fnName, ...args), `${fam.family} ${slot.label} aceita ${JSON.stringify(value).slice(0, 60)}`);
                assert.deepEqual(await audit(), [], `${fam.family} ${slot.label}: auditoria vazia depois de gravar ${JSON.stringify(value).slice(0, 40)}`);
                accepted++;
            }
        }
        // Required, conditional and unexpected fields. tv30_avatar_write takes
        // the fields as positional arguments: none can be absent or extra.
        if (fam.fields && fam.family !== 'avatar:{name}') {
            const [base, callWith] = {
                'session:current-service': [SVC, h => ['tv30_session_service', flatten(h)]],
                'session:current-app': [APP, h => ['tv30_current_app', flatten(h)]],
                'user:{id}': [PROFILE, h => ['tv30_profile_write', profileArgs(P1, [], h)]],
                'client:{id}': [CLIENT, h => ['tv30_client_write', ['c1', ...flatten(h)]]],
                'v100.0:remote-device:{handle}': [RD, h => ['tv30_v100_remote_device_write', [RD_HANDLE, ...flatten(h)]]],
            }[fam.family];
            for (const [fieldName, r0] of Object.entries(fam.fields)) {
                const rule = resolve(r0);
                const without = { ...base };
                delete without[fieldName];
                await resetDatabase();
                const [fnName, args] = callWith(without);
                if (rule.required) {
                    await expectRefusal(`${fam.family} sem ${fieldName}`, fnName, args, [`field ${fieldName}: required, absent`]);
                    cases++;
                } else if (rule.requiredIf) {
                    const c = rule.requiredIf;
                    assert.equal(base[c.field], c.value, `base do teste com ${c.field}=${c.value}`);
                    await expectRefusal(`${fam.family} sem ${fieldName} com ${c.field}=${c.value}`, fnName, args, [`field ${fieldName}: required when ${c.field}=${c.value}`]);
                    cases++;
                } else {
                    await assert.doesNotReject(fcall(fnName, ...args), `${fam.family}: ${fieldName} e opcional`);
                }
            }
            if (fam.otherFields === false) {
                await resetDatabase();
                const [fnName, args] = callWith({ ...base, invented: 'x' });
                await expectRefusal(`${fam.family} campo extra`, fnName, args, ['field "invented": not in the schema']);
                cases++;
            }
        }
        assert.ok(cases > 0, `${fam.family}: algum caso de recusa`);
        t.diagnostic(`${fam.family}: ${cases} recusa(s) e ${accepted} aceitacao(oes) de fronteira`);
    });
}

test('avatar do perfil: so imagem do conjunto; a imagem e reescrita inteira', async () => {
    await expectRefusal('avatar fora do conjunto', 'tv30_profile_write', profileArgs(P1, [], { ...PROFILE, avatar: 'blue.svg' }),
        [`user:${P1} field avatar: "blue.svg" names no image of the avatar set (avatar:blue.svg does not exist; invariant profile-avatar)`]);
    await fcall('tv30_avatar_write', ...avatarArgs('blue.svg', { ...AVATAR_IMG, label: 'Azul' }));
    await fcall('tv30_profile_write', ...profileArgs(P1, [], { ...PROFILE, avatar: 'blue.svg' }));
    await fcall('tv30_avatar_write', ...avatarArgs('blue.svg', { ...AVATAR_IMG, label: 'Azul escuro' }));
    assert.equal(await r('HGET', 'avatar:blue.svg', 'label'), 'Azul escuro');
    assert.deepEqual(await audit(), []);
    await expectRefusal('argumento a menos', 'tv30_avatar_write', ['blue.svg', 'Azul', 'image/svg+xml'], ['usage:']);
});

test('user:{id}: condicionais falsos dispensam os campos; id do hash igual ao da chave', async () => {
    await fcall('tv30_profile_write', ...profileArgs(P1, [], PROFILE_MIN));
    assert.deepEqual(await audit(), []);
    await expectRefusal('id diferente', 'tv30_profile_write', profileArgs(P1, [], { ...PROFILE, id: P2 }), [`user:${P1} field id: must equal the {id} of the key`]);
});

test('broadcaster-attrs: atributos basicos proibidos e limit of 20 MB no resultado', async () => {
    const f = familyOf('user:{id}:broadcaster-attrs:{scid}');
    await indexP1();
    for (const attr of f.forbiddenFields.names) {
        await expectRefusal(`redefinir ${attr}`, 'tv30_broadcaster_attributes', [P1, 'urn:s:1', attr, 'x'], [`field ${attr}: basic attribute`]);
        await expectRefusal(`remover ${attr}`, 'tv30_broadcaster_attributes', [P1, 'urn:s:1', attr, ''], [`field ${attr}: basic attribute`]);
    }
    // Exactly at the limit: accepted; one byte above: refused (sum of names
    // and values).
    const attr = 'grande';
    await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', attr, 'x'.repeat(f.maxBytes - attr.length));
    await expectRefusal('acima do teto', 'tv30_broadcaster_attributes', [P1, 'urn:s:1', 'b', 'y'], [`above the limit of ${f.maxBytes}`]);
    // Removing frees room.
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', attr, '', 'b', 'y'), 2);
    assert.deepEqual(await r('HGETALL', `user:${P1}:broadcaster-attrs:urn:s:1`), ['b', 'y']);
});

// ---------------------------------------------------- effects (acceptance) --
test('cache: grava, conta a quota do servico, despeja o servico de acesso mais antigo, toca, remove e limpa pela data', async () => {
    const put = (o) => {
        const [n, a] = cachePutCall(o);
        return fcall(n, ...a);
    };
    const payload = 'x'.repeat(100);
    assert.deepEqual(await put({ ctx: 'urn:a', scid: 'urn:s1', body: payload, now: '10' }), [1]);
    assert.deepEqual(await put({ ctx: 'urn:a', scid: 'urn:s1', path: 'b.js', body: payload, now: '11' }), [1]);
    // Quota of 250 bytes by service: the third file of s1 is refused, nothing
    // written.
    assert.deepEqual(await put({ ctx: 'urn:a', scid: 'urn:s1', path: 'c.js', body: payload, now: '12', quota: '250' }), [0]);
    assert.equal(await r('HEXISTS', 'cache:meta:urn:a', 'c.js'), 0);
    // Rewriting a file counts only its new size.
    assert.deepEqual(await put({ ctx: 'urn:a', scid: 'urn:s1', body: payload, now: '13', quota: '250' }), [1]);
    assert.deepEqual(await put({ ctx: 'urn:b', scid: 'urn:s2', body: payload, now: '20' }), [1]);
    assert.deepEqual(await put({ ctx: 'urn:c', scid: 'urn:s3', body: payload, now: '30' }), [1]);
    // s1 read after s2: s2 is the oldest service. A total of 400 bytes takes
    // it whole.
    assert.equal(await fcall('tv30_cache_touch', 'urn:a', '40'), 1);
    assert.equal(await fcall('tv30_cache_touch', 'urn:x', '40'), 0);
    assert.deepEqual(await put({ ctx: 'urn:d', scid: 'urn:s4', body: payload, now: '50', total: '400' }), [1, 'urn:b']);
    assert.equal(await r('EXISTS', 'cache:meta:urn:b'), 0);
    assert.equal(await r('HEXISTS', 'cache:services', 'urn:b'), 0);
    // A file larger than the total, even with every other service evicted:
    // refused, and nothing evicted.
    assert.deepEqual(await put({ ctx: 'urn:e', scid: 'urn:s5', body: payload, now: '60', total: '50' }), [0]);
    assert.deepEqual((await r('HKEYS', 'cache:services')).sort(), ['urn:a', 'urn:c', 'urn:d']);
    // The size in the metadata must be that of the body.
    await expectRefusal('tamanho errado', 'tv30_cache_put', ['urn:a', 'urn:s1', 'z', JSON.stringify({ ...META, size: 1 }), '1', LARGE, LARGE, 'abc'], ['differs from the body']);
    assert.deepEqual(await audit(), []);
    // @clearAppContextCacheDate: only the files before the date.
    await put({ ctx: 'urn:f', scid: 'urn:s6', path: 'novo', meta: JSON.stringify({ ...META, lastModified: 2000, size: 3 }), body: 'abc' });
    await put({ ctx: 'urn:f', scid: 'urn:s6', path: 'velho', meta: JSON.stringify({ ...META, lastModified: 1000, size: 3 }), body: 'abc' });
    assert.equal(await fcall('tv30_cache_clear_before', 'urn:f', '1500'), 1);
    assert.deepEqual(await r('HKEYS', 'cache:body:urn:f'), ['novo']);
    assert.equal(await fcall('tv30_cache_clear_before', 'urn:f', '3000'), 1);
    assert.equal(await r('HEXISTS', 'cache:services', 'urn:f'), 0, 'sem arquivos, o contexto sai do cache');
    // STORED_REMOVE.
    assert.equal(await fcall('tv30_cache_remove', 'urn:a'), 1);
    assert.equal(await fcall('tv30_cache_remove', 'urn:a'), 0);
    assert.equal(await r('EXISTS', 'cache:body:urn:a'), 0);
    assert.deepEqual(await audit(), []);
});

test('aplicacao corrente: hash regravado inteiro; sem argumentos, so o DEL', async () => {
    assert.equal(await fcall('tv30_current_app', ...flatten({ ...APP, appId: 'urn:tv30:app:antes' })), 1);
    assert.equal(await fcall('tv30_current_app', ...flatten(APP)), 1);
    const h = await r('HGETALL', 'session:current-app');
    assert.equal(h.length, 6);
    assert.equal(h[h.indexOf('appId') + 1], APP.appId);
    assert.deepEqual(await audit(), []);
    assert.equal(await fcall('tv30_current_app'), 0);
    assert.equal(await r('EXISTS', 'session:current-app'), 0);
    assert.equal(await fcall('tv30_current_app'), 0);
    await expectRefusal('origem invalida', 'tv30_current_app', flatten({ ...APP, origin: 'http://Ctx.apps.localhost' }), ['field origin:']);
    assert.equal(await r('EXISTS', 'session:current-app'), 0, 'a recusa nao grava');
});

test('sessao: SET do perfil (vazio vale); o hash do servico regravado inteiro com o id; sem argumentos, sem servico', async () => {
    assert.equal(await fcall('tv30_session_user', P1), 1);
    assert.equal(await r('GET', 'session:current-user'), P1);
    await fcall('tv30_session_user', '');
    assert.equal(await r('GET', 'session:current-user'), '');
    await fcall('tv30_session_service', ...flatten({ ...SVC, serviceId: '101', serviceName: 'Canal' }));
    await fcall('tv30_session_service', ...flatten(SVC));
    assert.equal(await r('GET', 'session:current-service-id'), SVC.serviceContextId);
    const h = await r('HGETALL', 'session:current-service');
    assert.equal(h.length, 6);
    assert.equal(h[h.indexOf('serviceId') + 1], '');
    assert.deepEqual(await audit(), []);
    assert.equal(await fcall('tv30_session_service'), 1);
    assert.equal(await r('EXISTS', 'session:current-service'), 0);
    assert.equal(await r('GET', 'session:current-service-id'), '');
    assert.deepEqual(await audit(), []);
});

test('perfil: grava os tres juntos, visibilidade acumula, ultimo acesso so em perfil indexado, remocao leva tudo', async () => {
    await fcall('tv30_profile_write', ...profileArgs(P1, ['urn:a', 'urn:b'], PROFILE_MIN));
    await fcall('tv30_profile_write', ...profileArgs(P1, ['urn:c'], { ...PROFILE_MIN, nickname: 'Outro' }));
    assert.deepEqual((await r('SMEMBERS', `user:${P1}:consent`)).sort(), ['urn:a', 'urn:b', 'urn:c']);
    assert.equal(await r('HGET', `user:${P1}`, 'nickname'), 'Outro');
    assert.deepEqual(await r('SMEMBERS', 'users:index'), [P1]);

    assert.equal(await fcall('tv30_profile_last_access', P1, '1234'), 1);
    assert.equal(await r('HGET', `user:${P1}`, 'lastAccess'), '1234');
    assert.equal(await fcall('tv30_profile_last_access', 'desconhecido', '1'), 0);
    assert.equal(await fcall('tv30_profile_last_access', P2, '1'), 0, 'UUID valido outside users:index: 0');
    assert.equal(await fcall('tv30_profile_last_access', 'a:b', '1'), 0, 'id que nunca seria perfil: 0, sem erro (espelho de topico)');
    assert.equal(await r('EXISTS', 'user:desconhecido'), 0, 'sem hash fantasma');
    assert.equal(await r('EXISTS', `user:${P2}`), 0, 'sem hash fantasma');
    await expectRefusal('instante invalido', 'tv30_profile_last_access', [P1, 'ontem'], ['field lastAccess:']);

    await fcall('tv30_profile_write', ...profileArgs(P2, [], { ...PROFILE_MIN, id: P2 }));
    await fcall('tv30_broadcaster_attributes', P1, 'urn:tv30:service:x', 'cor', 'azul');
    await fcall('tv30_broadcaster_attributes', P1, 'urn:y', 'k', 'v');
    await fcall('tv30_broadcaster_attributes', P2, 'urn:y', 'k', 'v');
    assert.deepEqual(await audit(), []);
    // 1 (SREM) + user:{P1} + consent + 2 broadcaster-attrs
    assert.equal(await fcall('tv30_profile_remove', P1), 5);
    assert.deepEqual((await r('KEYS', `user:${P1}*`)), []);
    assert.equal(await r('EXISTS', `user:${P2}:broadcaster-attrs:urn:y`), 1, 'remover P1 nao alcanca P2');
    // The glob of the id is escaped: an id with a wildcard (never a UUID, so
    // never a profile) that, unescaped, would match P2 does not reach P2.
    assert.equal(await fcall('tv30_profile_remove', P2.slice(0, 9) + '*'), 0);
    assert.equal(await r('EXISTS', `user:${P2}:broadcaster-attrs:urn:y`), 1);
    assert.equal(await r('EXISTS', `user:${P2}`), 1);
    assert.equal(await fcall('tv30_profile_remove', P2), 3);
    assert.equal(await fcall('tv30_profile_remove', 'nao-existe'), 0);
    assert.deepEqual(await audit(), []);
});

// The limit check is the function's own: no read before the call.
test('perfil: limit of users:index na gravacao; id novo alem dele recusado, regravar existente aceito, remover libera', async () => {
    const f = familyOf('users:index');
    assert.ok(Number.isInteger(f.maxMembers) && f.maxMembers > 0, 'teto declarado no esquema');
    assert.ok(f.maxMembersSource, 'fonte do teto');
    const ids = Array.from({ length: f.maxMembers }, (_, i) => `0b6e3c1a-7f2d-4e8b-9a5c-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids) {
        await fcall('tv30_profile_write', ...profileArgs(id, ['urn:a'], { ...PROFILE_MIN, id }));
    }
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);

    const newId = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    const msg = await expectRefusal('id novo com o conjunto no teto', 'tv30_profile_write', profileArgs(newId, ['urn:b'], { ...PROFILE_MIN, id: newId }),
        [`users:index limit of ${f.maxMembers} profiles reached (${f.maxMembersSource})`, newId]);
    assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    assert.equal(await r('EXISTS', `user:${newId}`), 0);

    // Rewriting one of those at the limit: accepted, with a set of the same
    // size.
    assert.equal(await fcall('tv30_profile_write', ...profileArgs(ids[2], ['urn:c'], { ...PROFILE_MIN, id: ids[2], nickname: 'Regravado' })), 1);
    assert.equal(await r('HGET', `user:${ids[2]}`, 'nickname'), 'Regravado');
    assert.deepEqual((await r('SMEMBERS', `user:${ids[2]}:consent`)).sort(), ['urn:a', 'urn:c']);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);

    // With one removed, the new one goes in; and the next one is refused
    // again.
    await fcall('tv30_profile_remove', ids[0]);
    assert.equal(await fcall('tv30_profile_write', ...profileArgs(newId, [], { ...PROFILE_MIN, id: newId })), 1);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);
    await expectRefusal('de novo no teto', 'tv30_profile_write', profileArgs(ids[0], [], { ...PROFILE_MIN, id: ids[0] }), ['limit of']);
    assert.deepEqual(await audit(), []);
});

test('perfil: teto conta o que esta no Redis, inclusive member gravado a mao', async () => {
    const f = familyOf('users:index');
    const ids = Array.from({ length: f.maxMembers + 1 }, (_, i) => `5a9d2f4e-1c3b-4d6a-8e7f-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids.slice(0, f.maxMembers)) {
        await fcall('tv30_profile_write', ...profileArgs(id, [], { ...PROFILE_MIN, id }));
    }
    // One more, outside the functions: the audit reports the excess.
    await r('SADD', 'users:index', ids[f.maxMembers]);
    await r('HSET', `user:${ids[f.maxMembers]}`, ...flatten({ ...PROFILE_MIN, id: ids[f.maxMembers] }));
    const findings = await audit();
    assert.deepEqual(findings, [['users:index', 'users:index', `${f.maxMembers + 1} members, above the limit of ${f.maxMembers} (${f.maxMembersSource})`]]);
    // Above the limit: an existing one can still be rewritten; a new id cannot.
    assert.equal(await fcall('tv30_profile_write', ...profileArgs(ids[0], [], { ...PROFILE_MIN, id: ids[0], nickname: 'Ainda' })), 1);
    const newId = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    await expectRefusal('id novo acima do teto', 'tv30_profile_write', profileArgs(newId, [], { ...PROFILE_MIN, id: newId }), [`limit of ${f.maxMembers} profiles`]);
});

// The last access checks only the written field, and the removal does not
// check the id.
test('id que nao e UUID, gravado a mao: auditoria aponta, gravacao recusada, remocao passa', async () => {
    const legacyId = 'user_1760000000000';
    await r('SADD', 'users:index', legacyId);
    await r('HSET', `user:${legacyId}`, ...flatten({ ...PROFILE_MIN, id: legacyId }));
    await r('SADD', `user:${legacyId}:consent`, 'urn:s');
    const findings = await audit();
    for (const k of ['users:index', `user:${legacyId}`, `user:${legacyId}:consent`]) {
        assert.ok(findings.some(([c, , m]) => c === k && m.includes('RFC 9562')), `${k} apontado: ${JSON.stringify(findings)}`);
    }
    await expectRefusal('regravar o perfil', 'tv30_profile_write', profileArgs(legacyId, [], { ...PROFILE_MIN, id: legacyId }), ['{id} of the key: must be a UUID']);
    await expectRefusal('atributo de emissora no perfil', 'tv30_broadcaster_attributes', [legacyId, 'urn:s', 'cor', 'azul'], ['{id} of the key: must be a UUID']);
    assert.equal(await fcall('tv30_profile_last_access', legacyId, '5'), 1);
    assert.equal(await fcall('tv30_profile_remove', legacyId), 3);
    assert.deepEqual(await audit(), []);
});

// Replacing the avatar with the file name, or removing it, empties the
// audit.
test('avatar em SVG em linha, gravado a mao: auditoria aponta, regravacao recusada, o resto segue', async () => {
    await r('SADD', 'users:index', P1);
    await r('HSET', `user:${P1}`, ...flatten({ ...PROFILE_MIN, avatar: SVG_INLINE }));
    const findings = await audit();
    assert.deepEqual(findings.map(([k, f]) => [k, f]), [[`user:${P1}`, 'user:{id}']], JSON.stringify(findings));
    assert.ok(findings[0][2].startsWith('field avatar: inline SVG is not accepted'), findings[0][2]);
    assert.ok(!findings[0][2].includes('\n'), 'motivo numa linha so');
    await expectRefusal('regravar com o SVG', 'tv30_profile_write', profileArgs(P1, [], { ...PROFILE_MIN, avatar: SVG_INLINE }), ['field avatar: inline SVG is not accepted']);
    assert.equal(await fcall('tv30_profile_last_access', P1, '7'), 1);
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s', 'cor', 'azul'), 1);
    await r('HSET', `user:${P1}`, 'avatar', AVATAR);
    assert.deepEqual(await audit(), []);
    await fcall('tv30_profile_write', ...profileArgs(P1, [], PROFILE_MIN));
    assert.equal(await r('HEXISTS', `user:${P1}`, 'avatar'), 0);
    assert.deepEqual(await audit(), []);
    // 1 (SREM) + user:{P1} + 1 broadcaster-attrs (no consent)
    assert.equal(await fcall('tv30_profile_remove', P1), 3);
});

test('avatar: a regra do esquema decide igual a expressao do contrato', async (t) => {
    const rule = resolve(familyOf('user:{id}').fields.avatar);
    const corpus = new Set([
        ...violations(rule).map(([v]) => v), ...acceptances(rule),
        ...['svg', 'png', 'jpg', 'jpeg', 'webp', 'gif', 'SVG', 'tiff', 'svgz', 'jpe'].map(e => `x.${e}`),
        'a'.repeat(127) + '.svg', 'a'.repeat(128) + '.webp', 'a'.repeat(129) + '.png', 'a'.repeat(128) + '..svg', 'a'.repeat(127) + '..svg',
    ]);
    // Deterministic generator (Park-Miller) over an alphabet with the letters
    // of the extensions and the characters the rule refuses.
    let s = 12345;
    const rnd = n => {
        s = (s * 16807) % 2147483647;
        return s % n;
    };
    const alphabet = ['a', 'Z', '0', '.', '_', '-', '/', ' ', 'é', 's', 'v', 'g', 'p', 'n', 'j', 'e', 'w', 'b', 'G', '\\', '<'];
    const exts = ['svg', 'svg', 'svg', 'svg', 'png', 'Svg', 'sv', 'svgz'];
    for (let i = 0; i < 400; i++) {
        let v = '';
        const n = 1 + rnd(9);
        for (let j = 0; j < n; j++) {
            v += alphabet[rnd(alphabet.length)];
        }
        corpus.add(v);
        corpus.add(`${v}.${exts[rnd(exts.length)]}`);
    }
    let accepted = 0;
    for (const v of corpus) {
        await resetDatabase();
        let error = null;
        await writeAvatar(v);
        try {
            await fcall('tv30_profile_write', ...profileArgs(P1, [], { ...PROFILE_MIN, avatar: v }));
        } catch (e) {
            error = e;
        }
        const expected = AVATAR_CONTRACT.test(v);
        assert.equal(error === null, expected, `${JSON.stringify(v)}: o contrato ${expected ? 'aceita' : 'recusa'}, a funcao ${error ? 'recusou: ' + error.message : 'aceitou'}`);
        if (error) {
            assert.ok(error.message.startsWith(`${PREFIX} tv30_profile_write: user:${P1} field avatar: `), error.message);
        }
        if (expected) {
            accepted++;
        }
    }
    assert.ok(accepted >= 50 && accepted < corpus.size - 50, `${accepted} aceitos de ${corpus.size}`);
    t.diagnostic(`avatar: ${corpus.size} valores, ${accepted} aceitos pela expressao do contrato e pela funcao`);
});

test('UUID estrito: id da carga e crypto.randomUUID() do AoP aceitos; nulo e maximo recusados', async () => {
    const ids = ['c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10', ...Array.from({ length: 20 }, () => require('crypto').randomUUID())];
    for (const id of ids) {
        await resetDatabase();
        await fcall('tv30_profile_write', ...profileArgs(id, ['urn:s'], { ...PROFILE_MIN, id }));
        assert.deepEqual(await r('SMEMBERS', 'users:index'), [id], id);
        assert.deepEqual(await audit(), [], id);
    }
    for (const [id, m] of [['00000000-0000-0000-0000-000000000000', 'nil UUID'], ['ffffffff-ffff-ffff-ffff-ffffffffffff', 'max UUID']]) {
        await resetDatabase();
        const msg = await expectRefusal(m, 'tv30_profile_write', profileArgs(id, [], { ...PROFILE_MIN, id }), [`{id} of the key: ${m}`, 'is not a profile id']);
        assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    }
});

test('atributos de emissora: perfil precisa estar indexado; vazio remove', async () => {
    await expectRefusal('perfil inexistente', 'tv30_broadcaster_attributes', [P1, 'urn:s:1', 'cor', 'azul'], ['outside users:index (invariant profile-dependents)']);
    await indexP1();
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', 'cor', 'azul', 'tam', 'G'), 2);
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', 'cor', ''), 1);
    assert.deepEqual(await r('HGETALL', `user:${P1}:broadcaster-attrs:urn:s:1`), ['tam', 'G']);
});

test('clientes: autorizar e bloquear trocam o id de conjunto (disjuntos); grava acrescenta; inclusao no boot', async () => {
    await fcall('tv30_client_block', 'c1');
    await fcall('tv30_client_authorize', 'c1', 'class', 'standAlone', 'refreshToken', 'r1');
    assert.deepEqual(await r('SMEMBERS', 'clients:authorized'), ['c1']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), []);
    await fcall('tv30_client_write', 'c1', 'accessToken', 'a1');
    await fcall('tv30_client_write', 'c1', 'refreshToken', 'r2');
    assert.deepEqual((await r('HGETALL', 'client:c1')), ['class', 'standAlone', 'refreshToken', 'r2', 'accessToken', 'a1']);
    await fcall('tv30_client_block', 'c1');
    assert.deepEqual(await r('SMEMBERS', 'clients:authorized'), []);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1']);
    assert.equal(await r('EXISTS', 'client:c1'), 1, 'o registro fica depois do bloqueio');
    assert.deepEqual(await audit(), []);

    // Inclusion on boot: a blocked client does not go in; without a record it
    // is refused; with a record it goes in.
    assert.equal(await fcall('tv30_client_add_authorized', 'c1'), 0);
    await expectRefusal('sem client:{id}', 'tv30_client_add_authorized', ['c9'], ['invariant authorized-has-record']);
    await r('HSET', 'client:velho', 'class', 'standAlone', 'refreshToken', 'r');
    assert.equal(await fcall('tv30_client_add_authorized', 'velho'), 1);
    assert.equal(await fcall('tv30_client_add_authorized', 'velho'), 0);
    assert.deepEqual(await audit(), []);
});

test('registro existente: campo novo conferido, o resto ja gravado nao trava (a auditoria mostra)', async () => {
    // Boolean "False" written by hand, outside the schema.
    await fcall('tv30_profile_write', ...profileArgs(P1, [], PROFILE_MIN));
    await r('HSET', `user:${P1}`, 'parentalControl', 'False');
    assert.equal(await fcall('tv30_profile_last_access', P1, '99'), 1);
    assert.deepEqual(await audit(), [[`user:${P1}`, 'user:{id}', 'field parentalControl: must be "true" or "false" (got "False")']]);
    // A new client checks everything; an existing one only what is written.
    await expectRefusal('cliente novo incompleto', 'tv30_client_write', ['c1', 'accessToken', 'a'], ['field class: required, absent']);
});

test('dispositivos remotos da 100.0: regrava inteiro, remove e limpa so a propria familia', async () => {
    const key = `v100.0:remote-device:${RD_HANDLE}`;
    const other = 'v100.0:remote-device:3c8a7d2f-5e4b-4a9c-8d1e-2f3a4b5c6d7e';
    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...flatten(RD));
    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...flatten({ ...RD, state: 'registered', paired: 'false', supportedTypes: '[]' }));
    const h = await r('HGETALL', key);
    assert.deepEqual(Object.fromEntries(Array.from({ length: h.length / 2 }, (_, i) => [h[2 * i], h[2 * i + 1]])),
        { ...RD, state: 'registered', paired: 'false', supportedTypes: '[]' });
    assert.deepEqual(await audit(), []);
    // The write takes the whole record: a missing field is refused.
    const { state, ...withoutState } = RD;
    await expectRefusal('registro incompleto', 'tv30_v100_remote_device_write', [RD_HANDLE, ...flatten(withoutState)], ['field state: required, absent']);

    assert.equal(await fcall('tv30_v100_remote_device_remove', RD_HANDLE), 1);
    assert.equal(await fcall('tv30_v100_remote_device_remove', RD_HANDLE), 0);
    assert.equal(await r('EXISTS', key), 0);

    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...flatten(RD));
    await fcall('tv30_v100_remote_device_write', other.split(':').pop(), ...flatten(RD));
    await fcall('tv30_client_authorize', 'c1', ...flatten(CLIENT));
    assert.equal(await fcall('tv30_v100_remote_devices_clear'), 2);
    assert.equal(await r('EXISTS', key), 0);
    assert.equal(await r('EXISTS', other), 0);
    assert.equal(await r('EXISTS', 'client:c1'), 1, 'a limpeza nao toca outras familias');
    assert.equal(await fcall('tv30_v100_remote_devices_clear'), 0);
    assert.deepEqual(await audit(), []);
});

test('origens: registra, remove, limpa', async () => {
    await fcall('tv30_origin_register', ORIGIN, 'urn:tv30:service:users-test');
    await fcall('tv30_origin_register', 'http://uff.apps.localhost:8080', 'current-service');
    assert.equal(await fcall('tv30_origin_remove', ORIGIN), 1);
    assert.equal(await fcall('tv30_origin_remove', ORIGIN), 0);
    assert.deepEqual(await r('HGETALL', 'origins:associated'), ['http://uff.apps.localhost:8080', 'current-service']);
    assert.equal(await fcall('tv30_origins_clear'), 1);
    assert.equal(await fcall('tv30_origins_clear'), 0);
    assert.equal(await fcall('tv30_origin_remove', ORIGIN), 0);
});

test('servicos anunciados: o hash regravado inteiro; sem argumento, apaga; o conjunto de versao anterior e substituido', async () => {
    const reg = (gsid) => JSON.stringify({ ...ANNOUNCED_RECORD, bam: { ...BAM, globalServiceId: gsid } });
    const A = 'urn:tv30:service:a';
    const B = 'urn:tv30:service:b';
    const C = 'urn:tv30:service:c';
    assert.equal(await fcall('tv30_announced_services', A, reg(A), B, reg(B)), 2);
    assert.deepEqual((await r('HKEYS', 'services:announced')).sort(), [A, B]);
    assert.deepEqual(JSON.parse(await r('HGET', 'services:announced', A)), JSON.parse(reg(A)));
    // What left the catalog leaves the hash.
    assert.equal(await fcall('tv30_announced_services', B, reg(B), C, reg(C)), 2);
    assert.deepEqual((await r('HKEYS', 'services:announced')).sort(), [B, C]);
    assert.deepEqual(await audit(), []);
    assert.equal(await fcall('tv30_announced_services'), 0);
    assert.equal(await r('EXISTS', 'services:announced'), 0);
    assert.equal(await fcall('tv30_announced_services'), 0);
    // The set of the previous schema version gives way to the hash.
    await r('SADD', 'services:announced', A);
    assert.equal(await fcall('tv30_announced_services', A, reg(A)), 1);
    assert.equal(await r('TYPE', 'services:announced'), 'hash');
    // More services than one HSET batch.
    const many = Array.from({ length: 300 }, (_, i) => `urn:tv30:service:s${i}`).flatMap(g => [g, reg(g)]);
    assert.equal(await fcall('tv30_announced_services', ...many), 300);
    assert.deepEqual(await audit(), []);
});

test('bind-context: RPUSH da entrada exata, LREM de todas as iguais', async () => {
    const e1 = JSON.stringify(BIND_ENTRY);
    const e2 = JSON.stringify({ alg: 'RS256', key: '-----BEGIN PUBLIC KEY-----\nMFww\n-----END PUBLIC KEY-----', registeredAt: 1 });
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e1), 1);
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e2), 2);
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e1), 3);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e1, e2, e1]);
    assert.deepEqual(await audit(), []);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', e1), 2);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', 'nao-tem'), 0);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:b', e1), 0);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e2]);
});

// ------------------------------------------------------------- atomicity --
test('atomicidade: tipo errado numa chave posterior ou member invalido no fim nao deixam gravacao parcial', async () => {
    // Profile: users:index of another type, so user:{id} is not created.
    await r('SET', 'users:index', 'x');
    await expectRefusal('users:index STRING', 'tv30_profile_write', profileArgs(P1, [], PROFILE), ['users:index in Redis is string, the schema asks for set']);
    await resetDatabase();
    // Profile: consent of another type (the last key written).
    await r('SET', `user:${P1}:consent`, 'x');
    await expectRefusal('consent STRING', 'tv30_profile_write', profileArgs(P1, ['urn:a'], PROFILE), [`user:${P1}:consent in Redis is string`]);
    await resetDatabase();
    // Profile: the last invalid service brings the whole operation down.
    await expectRefusal('ultimo servico vazio', 'tv30_profile_write', profileArgs(P1, ['urn:a', 'urn:b', ''], PROFILE), [`user:${P1}:consent member`]);
    // Rewriting an existing profile with invalid data keeps the previous one
    // intact.
    await fcall('tv30_profile_write', ...profileArgs(P1, ['urn:a'], PROFILE));
    await expectRefusal('regravacao invalida', 'tv30_profile_write', profileArgs(P1, ['urn:b'], { ...PROFILE, nickname: '' }), ['field nickname:']);
    // Client: clients:blocked of another type, so neither client:{id} nor
    // clients:authorized.
    await resetDatabase();
    await r('LPUSH', 'clients:blocked', 'x');
    await expectRefusal('clients:blocked LIST', 'tv30_client_authorize', ['c1', ...flatten(CLIENT)], ['clients:blocked in Redis is list']);
    await resetDatabase();
    await r('SADD', 'clients:blocked', 'c1');
    await expectRefusal('autorizar com classe invalida', 'tv30_client_authorize', ['c1', ...flatten({ ...CLIENT, class: 'outra' })], ['field class:']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1'], 'continua bloqueado');
    // Session: the previous hash intact.
    await resetDatabase();
    await fcall('tv30_session_service', ...flatten({ ...SVC, serviceName: 'Antes' }));
    await expectRefusal('servico invalido', 'tv30_session_service', flatten({ ...SVC, serviceId: 'x' }), ['field serviceId:']);
    // Attributes: a forbidden name among several valid ones.
    await resetDatabase();
    await indexP1();
    await expectRefusal('um proibido entre validos', 'tv30_broadcaster_attributes', [P1, 'urn:s', 'a', '1', 'nickname', 'x', 'z', '2'], ['field nickname:']);
    // bind-context of another type.
    await resetDatabase();
    await r('SET', 'bind-context:urn:a', 'x');
    await expectRefusal('bind-context STRING', 'tv30_bind_register', ['urn:a', JSON.stringify(BIND_ENTRY)], ['in Redis is string, the schema asks for list']);
    await expectRefusal('bind remove em STRING', 'tv30_bind_remove', ['urn:a', 'x'], ['in Redis is string']);
    // Announced services: the previous set intact.
    await resetDatabase();
    await fcall('tv30_announced_services', 'urn:antes', JSON.stringify(ANNOUNCED_RECORD));
    await expectRefusal('registro sem par', 'tv30_announced_services', ['urn:a', JSON.stringify(ANNOUNCED_RECORD), 'urn:b'], ['usage:']);
    await expectRefusal('servico repetido', 'tv30_announced_services', ['urn:a', JSON.stringify(ANNOUNCED_RECORD), 'urn:a', JSON.stringify(ANNOUNCED_RECORD)], ['repeated']);
    await expectRefusal('servico longo demais', 'tv30_announced_services', ['x'.repeat(257), JSON.stringify(ANNOUNCED_RECORD)], ['maximum 256']);
    assert.deepEqual(await r('HKEYS', 'services:announced'), ['urn:antes'], 'a recusa deixa o catalogo anterior');
});

// ----------------------------------------------------------------- audit --
test('auditoria: cada tipo de problema gravado a mao aparece com chave, familia e motivo; limpa, volta vazia', async () => {
    assert.deepEqual(await audit(), [], 'banco vazio');
    // Valid state of every family.
    await fcall('tv30_session_user', P1);
    await fcall('tv30_session_service', ...flatten(SVC));
    await fcall('tv30_current_app', ...flatten(APP));
    await fcall(...cachePutCall().flat());
    await fcall('tv30_profile_write', ...profileArgs(P1, ['urn:s'], PROFILE));
    await fcall('tv30_broadcaster_attributes', P1, 'urn:s', 'cor', 'azul');
    await fcall('tv30_client_authorize', 'c1', ...flatten(CLIENT));
    await fcall('tv30_client_block', 'c2');
    await fcall('tv30_origin_register', ORIGIN, 'urn:s');
    await fcall('tv30_bind_register', 'urn:s', JSON.stringify(BIND_ENTRY));
    await fcall('tv30_announced_services', 'urn:s', JSON.stringify(ANNOUNCED_RECORD));
    assert.deepEqual(await audit(), [], 'todas as familias validas');
    const valid = await snapshot();

    // Manual writes (what redis-cli or the commander allow), one of each kind.
    const manualWrites = [
        [['SET', `user:${P1}:profile`, 'x'], [`user:${P1}:profile`, '-', 'key without family']],
        [['HSET', 'session:x', 'a', '1'], ['session:x', '-', 'key without family']],
        [['SET', 'session:current-service', 'x'], ['session:current-service', 'session:current-service', 'in Redis is string, the schema asks for hash']],
        [['SET', 'session:current-user', 'x'.repeat(65)], ['session:current-user', 'session:current-user', 'value: has 65 characters, maximum 64']],
        [['HSET', `user:${P1}`, 'parentalControl', 'False'], [`user:${P1}`, 'user:{id}', 'field parentalControl:']],
        [['HDEL', `user:${P1}`, 'nickname'], [`user:${P1}`, 'user:{id}', 'field nickname: required, absent']],
        [['HSET', `user:${P1}`, 'extra', '1'], [`user:${P1}`, 'user:{id}', 'field "extra": not in the schema']],
        // Avatar as an inline SVG, and a path with a directory.
        [['HSET', `user:${P1}`, 'avatar', SVG_INLINE], [`user:${P1}`, 'user:{id}', 'field avatar: inline SVG is not accepted']],
        [['HSET', `user:${P1}`, 'avatar', '../segredo.svg'], [`user:${P1}`, 'user:{id}', 'field avatar: only the file name, without a directory']],
        // Avatar of the set: a name outside it, the image removed, an image
        // out of the schema.
        [['HSET', `user:${P1}`, 'avatar', 'blue.svg'], [`user:${P1}`, 'user:{id}', 'field avatar: "blue.svg" names no image of the avatar set (invariant profile-avatar)']],
        [['DEL', `avatar:${AVATAR}`], [`user:${P1}`, 'user:{id}', 'invariant profile-avatar']],
        [['HSET', `avatar:${AVATAR}`, 'type', 'image/png'], [`avatar:${AVATAR}`, 'avatar:{name}', 'field type:']],
        [['HSET', 'avatar:a.png', 'label', 'A', 'type', 'image/svg+xml', 'body', 'x'], ['avatar:a.png', 'avatar:{name}', '{name} of the key: extension must be one of .svg']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'empty', ''], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'field "empty": empty']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'nickname', 'x'], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'field nickname: basic attribute']],
        [['SADD', `user:${P1}:consent`, ''], [`user:${P1}:consent`, 'user:{id}:consent', 'member "": empty']],
        [['HSET', 'client:c1', 'class', 'admin'], ['client:c1', 'client:{id}', 'field class:']],
        [['HSET', 'origins:associated', 'http://X.example', 'urn:s'], ['origins:associated', 'origins:associated', 'field name: host must be in lowercase']],
        [['RPUSH', 'bind-context:urn:s', '{"alg":"none","key":"k","registeredAt":1}'], ['bind-context:urn:s', 'bind-context:{serviceId}', 'item 1: member "alg"']],
        [['HSET', 'services:announced', 'urn:s', '{"bsid":10}'], ['services:announced', 'services:announced', 'member "bam" required']],
        [['SET', 'services:announced', 'x'], ['services:announced', 'services:announced', 'in Redis is string, the schema asks for hash']],
        [['HSET', 'services:outra', 'a', '1'], ['services:outra', '-', 'key without family']],
        [['SET', 'users:index', 'x'], ['users:index', 'users:index', 'in Redis is string, the schema asks for set']],
        [['SADD', 'users:index', GHOST, ORPHAN, NO_HASH, 'f0f0f0f0-0000-4000-8000-000000000004', 'f0f0f0f0-0000-4000-8000-000000000005'], ['users:index', 'users:index', '6 members, above the limit of 5']],
        // Ids that are not lowercase UUIDs.
        [['SADD', 'users:index', 'user_1760000000000'], ['users:index', 'users:index', 'member "user_1760000000000": must be a UUID in the format of RFC 9562']],
        [['HSET', 'user:c3167a18-5dc5', 'id', 'c3167a18-5dc5'], ['user:c3167a18-5dc5', 'user:{id}', '{id} of the key: must be a UUID']],
        [['SADD', `user:${P1.toUpperCase()}:consent`, 'urn:s'], [`user:${P1.toUpperCase()}:consent`, 'user:{id}:consent', '{id} of the key: UUID must be in lowercase']],
        // Nil UUID, version and variant outside RFC 9562.
        [['SADD', 'users:index', '00000000-0000-0000-0000-000000000000'], ['users:index', 'users:index', 'member "00000000-0000-0000-0000-000000000000": nil UUID']],
        [['SADD', `user:${withDigit(P1, UUID_VERSION_DIGIT, '9')}:consent`, 'urn:s'], [`user:${withDigit(P1, UUID_VERSION_DIGIT, '9')}:consent`, 'user:{id}:consent', '{id} of the key: UUID version must be 1 to 8 (1st digit of the 3rd group; got 9']],
        [['HSET', `user:${withDigit(P1, UUID_VARIANT_DIGIT, 'c')}`, 'id', withDigit(P1, UUID_VARIANT_DIGIT, 'c')], [`user:${withDigit(P1, UUID_VARIANT_DIGIT, 'c')}`, 'user:{id}', '{id} of the key: UUID variant must be the one of RFC 9562 (1st digit of the 4th group in 8, 9, a or b; got c']],
        // Invariants
        [['HSET', `user:${GHOST}`, 'id', GHOST, 'nickname', 'F', 'parentalControl', 'false', 'closedCaptioning', 'false', 'closedSigning', 'false', 'audioDescription', 'false', 'dialogEnhancement', 'false', 'voiceGuidance', 'false'],
            [`user:${GHOST}`, 'user:{id}', 'id outside users:index (invariant profile-indexed)']],
        [['SREM', 'users:index', P1], [`user:${P1}`, 'user:{id}', 'invariant profile-indexed']],
        [['SADD', 'users:index', NO_HASH], ['users:index', 'users:index', `member "${NO_HASH}" without the hash user:${NO_HASH} (invariant profile-indexed)`]],
        [['SADD', `user:${ORPHAN}:consent`, 'urn:s'], [`user:${ORPHAN}:consent`, 'user:{id}:consent', `profile "${ORPHAN}" outside users:index (invariant profile-dependents)`]],
        [['HSET', `user:${ORPHAN}:broadcaster-attrs:urn:s`, 'a', '1'], [`user:${ORPHAN}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'invariant profile-dependents']],
        [['SADD', 'clients:blocked', 'c1'], ['clients:authorized', 'clients:authorized', 'member "c1" also in clients:blocked (invariant disjoint-clients)']],
        [['SADD', 'clients:authorized', 'c9'], ['clients:authorized', 'clients:authorized', 'member "c9" without the hash client:c9 (invariant authorized-has-record)']],
    ];
    for (const [cmd, [wantKey, fam, excerpt]] of manualWrites) {
        await r('FLUSHALL');
        // Restores the valid state and applies the manual write.
        for (const [k, [t, v]] of Object.entries(JSON.parse(valid))) {
            switch (t) {
                case 'string':
                    await r('SET', k, v);
                    break;
                case 'hash':
                    await r('HSET', k, ...v.flat());
                    break;
                case 'set':
                    await r('SADD', k, ...v);
                    break;
                case 'list':
                    await r('RPUSH', k, ...v);
                    break;
                default:
                    break;
            }
        }
        assert.deepEqual(await audit(), [], 'estado valido restaurado');
        if (cmd[0] === 'SET' && cmd[1] === 'users:index') {
            await r('DEL', 'users:index');
        }
        if (cmd[0] === 'SET' && cmd[1] === 'session:current-service') {
            await r('DEL', 'session:current-service');
        }
        await r(...cmd);
        const findings = await audit();
        assert.ok(findings.some(([k, f, m]) => k === wantKey && f === fam && m.includes(excerpt)),
            `${cmd.join(' ').slice(0, 60)}: esperado [${wantKey}, ${fam}, ...${excerpt}...], veio ${JSON.stringify(findings)}`);
        for (const a of findings) {
            assert.equal(a.length, 3, 'cada achado e [chave, familia, motivo]');
        }
    }
    // Cleanup: back to empty.
    await r('FLUSHALL');
    assert.deepEqual(await audit(), []);
});

test('auditoria nao olha chaves fora do alcance (schema.json, outOfScope)', async () => {
    await r('SADD', 'remote-devices:index', 'h1');
    await r('HSET', 'remote-device:h1', 'deviceClass', 'x');
    await r('SET', 'seed:done', '1');
    await r('SET', 'tv30:meu-componente:exemplo', 'v');
    assert.deepEqual(await audit(), []);
    for (const f of SCHEMA.outOfScope) {
        assert.ok(!SCHEMA.auditedPrefixes.some(p => f.key.startsWith(p)), `${f.key} fora dos prefixos auditados`);
    }
});

// ------------------------------------------------------------- helpers --
function fcall(fnName, ...args) {
    return r('FCALL', fnName, 0, ...args);
}

function fcallRo(fnName, ...args) {
    return r('FCALL_RO', fnName, 0, ...args);
}

function flatten(h) {
    return Object.entries(h).flat();
}

function audit() {
    return fcallRo('tv30_audit');
}

function withDigit(u, i, d) {
    return u.slice(0, i) + d + u.slice(i + 1);
}

function avatarArgs(fileName, h) {
    return [fileName, h.label, h.type, h.body];
}

function profileArgs(id, services, h) {
    return [id, String(services.length), ...services, ...flatten(h)];
}

// A tv30_cache_put call: the function and its arguments.
function cachePutCall(o = {}) {
    const v = { ctx: CTX, scid: 'urn:tv30:service:1', path: 'index.html', body: CACHE_BODY, now: '1763000000000', quota: LARGE, total: LARGE, ...o };
    const meta = o.meta ?? JSON.stringify({ ...META, size: Buffer.byteLength(v.body) });
    return ['tv30_cache_put', [v.ctx, v.scid, v.path, meta, v.now, v.quota, v.total, v.body]];
}

function indexP1() {
    return fcall('tv30_profile_write', ...profileArgs(P1, [], PROFILE));
}

// An image of the set with the name v, for a profile that names it; a name
// out of the rule is not written (the refusal comes from the profile).
function writeAvatar(v) {
    return fcall('tv30_avatar_write', ...avatarArgs(v, AVATAR_IMG)).catch(() => undefined);
}

// ------------------------------------------------------------ JS schema --
function resolve(rule) {
    if (!rule || typeof rule !== 'object' || rule.ref === undefined) {
        return rule;
    }
    const base = SCHEMA.definitions[rule.ref];
    assert.ok(base, `definicao ${rule.ref} existe`);
    const { ref, ...rest } = rule;
    return { ...base, ...rest };
}

function familyOf(familyName) {
    const f = SCHEMA.families.find(x => x.family === familyName);
    assert.ok(f, `familia ${familyName} no esquema`);
    return f;
};

async function snapshot() {
    const keys = new Set();
    let cur = '0';
    do {
        const [n, ks] = await r('SCAN', cur, 'COUNT', 1000);
        cur = n;
        ks.forEach(k => keys.add(k));
    } while (cur !== '0');
    const out = {};
    for (const k of [...keys].sort()) {
        const t = await r('TYPE', k);
        let v;
        switch (t) {
            case 'string':
                v = await r('GET', k);
                break;
            case 'hash': {
                const f = await r('HGETALL', k);
                const o = {};
                for (let i = 0; i < f.length; i += 2) {
                    o[f[i]] = f[i + 1];
                }
                v = Object.keys(o).sort().map(x => [x, o[x]]);
                break;
            }
            case 'set':
                v = (await r('SMEMBERS', k)).sort();
                break;
            case 'list':
                v = await r('LRANGE', k, 0, -1);
                break;
            default:
                break;
        }
        out[k] = [t, v];
    }
    return JSON.stringify(out);
}

// Refusal: error TV30SCHEMA <function>: with the expected excerpts, and
// nothing written.
async function expectRefusal(desc, fnName, args, excerpts = []) {
    const previous = await snapshot();
    let error = null;
    try {
        await fcall(fnName, ...args);
    } catch (e) {
        error = e;
    }
    assert.ok(error, `${desc}: esperada recusa de ${fnName}, foi aceito`);
    assert.ok(error.redis, `${desc}: erro do Redis (got ${error.message})`);
    assert.ok(error.message.startsWith(`${PREFIX} ${fnName}: `), `${desc}: prefixo (got ${error.message})`);
    for (const t of excerpts) {
        assert.ok(error.message.includes(t), `${desc}: mensagem com "${t}" (got ${error.message})`);
    }
    assert.equal(await snapshot(), previous, `${desc}: nada gravado`);
    return error.message;
}

async function resetDatabase() {
    await r('FLUSHALL');
    await fcall('tv30_avatar_write', ...avatarArgs(AVATAR, AVATAR_IMG));
}

// ----------------------------------------------- violations of each rule --
// [value, expected excerpt of the reason, description]
function violations(rule, asJson = false) {
    const out = [];
    const t = rule.type;
    if (asJson) {
        if (t === 'boolean') {
            out.push(['true', 'true or false in the JSON', 'booleano em texto JSON']);
            out.push([1, 'true or false in the JSON', 'numero no lugar de booleano']);
            return out;
        }
        if (t === 'json' || t === 'jsonList') {
            const kind = t === 'json' ? 'JSON object' : 'JSON list';
            out.push(['texto', kind, 'texto no lugar de JSON aninhado']);
            // The violations of the text form, as nested values; those that
            // are not JSON are covered by the case above.
            for (const [v, m, d] of violations(rule)) {
                let nested;
                try {
                    nested = JSON.parse(v);
                } catch {
                    continue;
                }
                if (typeof nested !== 'object' || nested === null) {
                    continue;
                }
                out.push([nested, m, `aninhado: ${d}`]);
            }
            return out;
        }
        if (t === 'integer') {
            out.push(['12', 'integer number in the JSON', 'inteiro em texto JSON']);
            out.push([1.5, 'integer number in the JSON', 'fracionario']);
            if (rule.min !== undefined) {
                out.push([rule.min - 1, `minimum ${rule.min}`, 'abaixo do minimo']);
            }
            if (rule.max !== undefined) {
                out.push([rule.max + 1, `maximum ${rule.max}`, 'acima do maximo']);
            }
            return out;
        }
        out.push([5, 'text in the JSON', 'numero no lugar de texto']);
    }
    switch (t) {
        case 'text':
            if (rule.min >= 1) {
                out.push(['', 'empty', 'empty']);
            }
            if (rule.max !== undefined) {
                out.push(['é'.repeat(rule.max + 1), `maximum ${rule.max}`, `${rule.max + 1} caracteres multibyte`]);
            }
            for (const c of rule.forbids || '') {
                out.push([`a${c}b`, `cannot contain "${c}"`, `com "${c}"`]);
            }
            break;
        case 'boolean':
            for (const v of ['True', 'False', '1', '']) {
                out.push([v, '"true" or "false"', `"${v}"`]);
            }
            break;
        case 'bytes':
            if (rule.max !== undefined) {
                out.push(['x'.repeat(rule.max + 1), `maximum ${rule.max}`, 'acima do maximo']);
            }
            break;
        case 'integer':
            for (const v of ['x', '1.5', '028', '-0', ' 1']) {
                out.push([v, 'integer', `"${v}"`]);
            }
            if (!rule.allowEmpty) {
                out.push(['', 'integer', 'empty']);
            }
            if (rule.min !== undefined) {
                out.push([String(rule.min - 1), `minimum ${rule.min}`, 'abaixo do minimo']);
            }
            if (rule.max !== undefined) {
                out.push([String(rule.max + 1), `maximum ${rule.max}`, 'acima do maximo']);
            }
            break;
        case 'enum':
            out.push(['nao-e-valor', 'must be one of', 'fora da lista']);
            if (rule.values.some(v => v.toLowerCase() !== v)) {
                out.push([rule.values.find(v => v.toLowerCase() !== v).toLowerCase(), 'must be one of', 'caixa trocada']);
            } else if (rule.values.some(v => v.toUpperCase() !== v)) {
                out.push([rule.values.find(v => v.toUpperCase() !== v).toUpperCase(), 'must be one of', 'caixa trocada']);
            }
            break;
        case 'language':
            for (const v of ['pt_BR', 'p', '', 'pt-', 'pt--BR', 'português', '123', 'pt-abcdefghi']) {
                out.push([v, 'RFC 5646', `"${v}"`]);
            }
            break;
        case 'uuid': {
            const ok = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
            for (const [v, d] of [
                ['', 'empty'], ['c3167a18-5dc5', 'UUID truncado'], ['user_1760000000000', 'user_<epoch-ms>'],
                ['p1', 'curto'], [ok.slice(0, -1), 'um digito a menos'], [ok + '0', 'um digito a mais'],
                [ok.replace(/-/g, ''), 'sem hifens'], [`{${ok}}`, 'entre chaves'], [`urn:uuid:${ok}`, 'forma URN'],
                ['g' + ok.slice(1), 'digito nao hexadecimal'], [ok.replace(/-/g, '_'), 'separador errado'],
                [` ${ok}`, 'espaco antes'], [`${ok} `, 'espaco depois'], ['c3167a185-dc5-4a6e-9b1d-3f2a7c5e8d10', 'grupos 9-3'],
            ]) out.push([v, 'RFC 9562', d]);
            out.push([ok.toUpperCase(), 'lowercase', 'maiusculas']);
            out.push(['C' + ok.slice(1), 'lowercase', 'uma maiuscula']);
            // Version 1 to 8 and the variant of RFC 9562; the message gives the
            // part and the digit.
            for (const d of '09f') {
                out.push([withDigit(ok, UUID_VERSION_DIGIT, d), ['UUID version must be 1 to 8', `got ${d} in`], `versao ${d}`]);
            }
            for (const d of '01234567cdef') {
                out.push([withDigit(ok, UUID_VARIANT_DIGIT, d), ['UUID variant must be the one of RFC 9562', `got ${d} in`], `variante ${d}`]);
            }
            // Both wrong: the version is checked first.
            out.push([withDigit(withDigit(ok, UUID_VERSION_DIGIT, '0'), UUID_VARIANT_DIGIT, 'c'), ['UUID version', 'got 0 in'], 'versao 0 e variante c']);
            out.push(['00000000-0000-0000-0000-000000000000', ['nil UUID', 'is not a profile id'], 'nulo (Nil UUID)']);
            out.push(['ffffffff-ffff-ffff-ffff-ffffffffffff', ['max UUID', 'is not a profile id'], 'maximum (Max UUID)']);
            // The case is checked first: a valid variant in uppercase (B).
            out.push([withDigit(ok, UUID_VARIANT_DIGIT, 'B'), 'lowercase', 'variante B maiuscula']);
            break;
        }
        case 'fileName': {
            const ext = rule.extensions[0];
            out.push(['', 'empty', 'empty']);
            out.push([SVG_INLINE, 'inline SVG is not accepted', 'SVG em linha']);
            out.push(['<svg onload="alert(1)"/>', 'inline SVG is not accepted', 'SVG com script']);
            for (const v of ['thumbs/a.svg', '../a.svg', '/a.svg', '/user-files/thumbs/a.svg', 'a/../../etc/passwd.svg', 'a\\b.svg', '..\\a.svg', `a.${ext}/`]) {
                out.push([v, ['without a directory', 'cannot contain "/" or "\\"'], `"${v}"`]);
            }
            for (const v of ['a', 'a.', '..', 'a.SVG', 'a.Png', 'a.gif', 'a.svgz', 'a.jpe', 'a.svg.txt', `a.${ext} `, `a.${ext}\n`, 'svg', 'a_svg']) {
                out.push([v, ['extension must be one of', rule.extensions.map(e => `.${e}`).join(', ')], `extensao: ${JSON.stringify(v)}`]);
            }
            for (const v of [`.${ext}`, `..${ext}`, `.oculto.${ext}`, `-a.${ext}`, `_a.${ext}`, `.a.${ext}`]) {
                out.push([v, 'must start with a letter or digit', `comeco: "${v}"`]);
            }
            for (const v of [`a b.${ext}`, `avataré.${ext}`, `a:b.${ext}`, `a%2f.${ext}`, `a?.${ext}`, `a*.${ext}`, `a\u0000b.${ext}`, `a\tb.${ext}`, `a~.${ext}`, `a+b.${ext}`]) {
                out.push([v, 'only with unaccented letters, digits', `caractere: ${JSON.stringify(v)}`]);
            }
            out.push(['a'.repeat(rule.maxName + 1) + `.${ext}`, [`${rule.maxName + 1} characters before the extension`, `maximum ${rule.maxName}`], `${rule.maxName + 1} characters before the extension`]);
            break;
        }
        case 'origin':
            for (const [v, m] of [
                ['ftp://x.example', 'http:// or https://'], ['x.example', 'http:// or https://'],
                ['http://X.example', 'lowercase'], ['http://x.example/', 'no path'], ['http://x.example/app', 'no path'],
                ['http://x.example:80', 'default port'], ['https://x.example:443', 'default port'],
                ['http://x.example:0', 'outside 1 to 65535'], ['http://x.example:65536', 'outside 1 to 65535'],
                ['http://x.example:080', 'leading zero'], ['http://', 'http:// or https://'],
            ]) out.push([v, m, `"${v}"`]);
            break;
        case 'json': {
            out.push(['nao-e-json', 'JSON object', 'texto que nao e JSON']);
            out.push(['[1,2]', 'JSON object', 'array JSON']);
            out.push(['"texto"', 'JSON object', 'string JSON']);
            const valid = validJson(rule);
            for (const [member, r0] of Object.entries(rule.fields)) {
                const rr = resolve(r0);
                if (rr.required) {
                    const o = { ...valid };
                    delete o[member];
                    out.push([JSON.stringify(o), `member "${member}" required`, `sem ${member}`]);
                }
                for (const [v, m, d] of violations(rr, true)) {
                    out.push([JSON.stringify({ ...valid, [member]: v }), [`member "${member}": `, ...[].concat(m)], `${member}: ${d}`]);
                }
            }
            if (rule.otherFields === false) {
                out.push([JSON.stringify({ ...valid, invented: 1 }), 'member "invented" not in the schema', 'member extra']);
            }
            break;
        }
        case 'jsonList':
            out.push(['nao-e-json', 'JSON list', 'texto que nao e JSON']);
            out.push(['{}', 'JSON list', 'JSON object vazio']);
            out.push(['{"a":"b"}', 'JSON list', 'JSON object']);
            out.push(['"texto"', 'JSON list', 'string JSON']);
            out.push(['', 'JSON list', 'empty']);
            for (const [v, m, d] of violations(resolve(rule.items), true)) {
                const first = resolve(rule.items).type === 'integer' ? 0 : 'ok';
                out.push([JSON.stringify([first, v]), ['item 1: ', ...[].concat(m)], `item: ${d}`]);
            }
            break;
        default:
            throw new Error(`tipo de regra sem gerador de violacoes neste teste: ${t}`);
    }
    return out;
}

// Values that satisfy the rule, the boundaries included.
function acceptances(rule) {
    switch (rule.type) {
        case 'text': {
            const out = [];
            if (rule.max !== undefined) {
                out.push('é'.repeat(rule.max));
            }
            out.push('a'.repeat(Math.max(rule.min || 0, 1)));
            if (!rule.min) {
                out.push('');
            }
            return out;
        }
        case 'boolean': return ['true', 'false'];
        case 'bytes': return ['', 'x', '\u0000\u00ff\n'];
        case 'integer': {
            const out = ['0' === String(rule.min) ? '0' : String(rule.min ?? 7)];
            if (rule.max !== undefined) {
                out.push(String(rule.max));
            }
            if (rule.allowEmpty) {
                out.push('');
            }
            if (rule.min === undefined) {
                out.push('-3');
            }
            return out;
        }
        case 'enum': return rule.values.slice();
        case 'language': return ['pt-BR', 'en', 'pt', 'zh-Hant-TW', 'x-tv30', 'i-default', 'es-419'];
        // The initial load (v4, variant 9), a crypto.randomUUID() (what the AoP
        // generates), versions 1 and 7 and each version from 1 to 8 with each
        // variant 8, 9, a and b.
        case 'uuid': {
            const out = ['c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10', require('crypto').randomUUID(), 'f81d4fae-7dec-11d0-a765-00a0c91e6bf6',
                '017f22e2-79b0-7cc3-98c4-dc0c0c07398f'];
            for (const v of '12345678') {
                for (const va of '89ab') {
                    out.push(withDigit(withDigit(P1, UUID_VERSION_DIGIT, v), UUID_VARIANT_DIGIT, va));
                }
            }
            return out;
        }
        // The avatar of the load, what the AoP writes, each extension, the
        // boundary sizes and ".", "_" and "-" inside the name ("a..b.svg":
        // without "/", ".." does not go up a directory).
        case 'fileName': {
            const ext = rule.extensions[0];
            return [
                AVATAR, `${require('crypto').randomUUID()}.${ext}`,
                ...rule.extensions.map(e => `a.${e}`), `Z.${ext}`, `0.${ext}`,
                'a'.repeat(rule.maxName) + '.' + ext, `Avatar_1-x.${ext}`, `a.png.${ext}`, `a..b.${ext}`, `a-.${ext}`, `a_.${ext}`, `a.b.c.${ext}`,
            ];
        }
        case 'origin': return ['http://users-test.apps.localhost:8080', 'http://a.apps.localhost', 'https://x.example', 'https://x.example:8443', 'http://[::1]:8099', 'http://127.0.0.1:65535'];
        case 'json':
            if (rule.fields.contentType) {
                return [JSON.stringify(META), JSON.stringify({ ...META, lastModified: 0 })];
            }
            if (rule.fields.bam) {
                return [JSON.stringify(ANNOUNCED_RECORD), JSON.stringify({ ...ANNOUNCED_RECORD, mainAvOverTheAir: false, groupingType: 'mandatoryGrouping' })];
            }
            return ['HS256', 'HS512', 'RS256', 'RS512'].map(alg => JSON.stringify({ ...validJson(rule), alg }));
        case 'jsonList': return ['[]', '["light"]', '["light","wind"]', '[""]'];
        default: throw new Error(`tipo de regra sem gerador de aceitacoes neste teste: ${rule.type}`);
    }
}

function validJson(rule) {
    if (rule.fields.contentType) {
        return { ...META };
    }
    if (rule.fields.bam) {
        return { ...ANNOUNCED_RECORD };
    }
    if (rule.fields.appName) {
        return { ...BAM };
    }
    assert.ok(rule.fields.alg && rule.fields.key && rule.fields.registeredAt, 'o gerador de JSON so conhece bind-context, services:announced e o BAM');
    return { ...BIND_ENTRY };
}

// One slot per declared field; the family comes when the cases are
// generated.
function perFieldSlots(build, excluded = [], preps = {}) {
    return [{ perField: true, build, excluded, preps }];
}
function expandSlots(f) {
    const out = [];
    for (const s of SLOTS[f.family] || []) {
        if (!s.perField) {
            out.push(s);
            continue;
        }
        for (const fieldName of Object.keys(f.fields).filter(c => !s.excluded.includes(c))) {
            out.push({ label: `field ${fieldName}`, rule: ff => ff.fields[fieldName], prep: s.preps[fieldName], call: s.build(fieldName), excerpt: `field ${fieldName}:` });
        }
    }
    return out;
}
