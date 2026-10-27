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

const ESQ = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'schema.json'), 'utf8'));
const PREFIXO = ESQ.errorPrefix;

let r;
before(async () => { r = await connect(); });
after(async () => { if (r) await r.close(); });

const fcall = (nome, ...args) => r('FCALL', nome, 0, ...args);
const fcallRo = (nome, ...args) => r('FCALL_RO', nome, 0, ...args);
const plano = h => Object.entries(h).flat();
const auditoria = () => fcallRo('tv30_audit');

// ------------------------------------------------------------ JS schema --
function resolve(regra) {
    if (!regra || typeof regra !== 'object' || regra.ref === undefined) return regra;
    const base = ESQ.definitions[regra.ref];
    assert.ok(base, `definicao ${regra.ref} existe`);
    const { ref, ...resto } = regra;
    return { ...base, ...resto };
}
const familia = nome => {
    const f = ESQ.families.find(x => x.family === nome);
    assert.ok(f, `familia ${nome} no esquema`);
    return f;
};

// ------------------------------------------------------------------ data --
const P1 = '0b6e3c1a-7f2d-4e8b-9a5c-3d1f2e4a6b8c';
const P2 = '5a9d2f4e-1c3b-4d6a-8e7f-9b0c1d2e3f4a';
const FANTASMA = 'f0f0f0f0-0000-4000-8000-000000000001';
const ORFAO = 'f0f0f0f0-0000-4000-8000-000000000002';
const SEM_HASH = 'f0f0f0f0-0000-4000-8000-000000000003';
// UUID version: first digit of the third group; variant: first digit of the
// fourth group.
const UUID_VERSAO = 14;
const UUID_VARIANTE = 19;
const comDigito = (u, i, d) => u.slice(0, i) + d + u.slice(i + 1);
const SVG_EM_LINHA = '<svg viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"><rect width="100" height="100" rx="30" fill="#4d7c5b"/></svg>';
const PERFIL = {
    id: P1, nickname: 'Viewer 1', avatar: `${P1}.svg`,
    parentalControl: 'true', maxContentRating: '12',
    audioLanguage: 'pt-BR', closedCaptioningLanguage: 'pt-BR', userInterfaceLanguage: 'en',
    closedCaptioning: 'true', closedSigning: 'true', closedSigningSide: 'right', closedSigningWidth: '28',
    audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false', lastAccess: '1760000000000',
};
// Minimal profile: only the required fields (false conditions).
const PERFIL_MIN = {
    id: P1, nickname: 'Viewer 1', parentalControl: 'false', closedCaptioning: 'false', closedSigning: 'false',
    audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false',
};
const perfilArgs = (id, servicos, h) => [id, String(servicos.length), ...servicos, ...plano(h)];
const SVC = {
    serviceContextId: 'c08b2c72-fd14-4095-adaf-2e5810850c57', serviceId: '', serviceName: '',
    transportStreamId: 'c08b2c72-fd14-4095-adaf-2e5810850c57', originalNetworkId: '09e59a1d-e2e7-467e-85db-2fb5a572e2fc',
};
const CLIENTE = { class: 'standAlone', refreshToken: 'rt-1', accessToken: 'at-1' };
const ENTRADA = { alg: 'HS256', key: 'segredo', registeredAt: 1760000000000 };
const ORIGEM = 'http://users-test.apps.localhost:8080';
const RD_HANDLE = '2b7f6c1e-4d3a-4f8b-9c2d-1e0f3a4b5c6d';
const RD = {
    deviceId: '7c1d2e3f-4a5b-4c6d-8e7f-0a1b2c3d4e5f', deviceClass: 'sensory-effect', supportedTypes: '["light","wind"]',
    state: 'connected', paired: 'false', disconnectedAt: '1760000000000',
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
        if (t === 'string') v = await r('GET', k);
        else if (t === 'hash') {
            const f = await r('HGETALL', k);
            const o = {};
            for (let i = 0; i < f.length; i += 2) o[f[i]] = f[i + 1];
            v = Object.keys(o).sort().map(x => [x, o[x]]);
        } else if (t === 'set') v = (await r('SMEMBERS', k)).sort();
        else if (t === 'list') v = await r('LRANGE', k, 0, -1);
        out[k] = [t, v];
    }
    return JSON.stringify(out);
}

// Refusal: error TV30SCHEMA <function>: with the expected excerpts, and
// nothing written.
async function recusa(desc, nome, args, trechos = []) {
    const antes = await snapshot();
    let erro = null;
    try { await fcall(nome, ...args); } catch (e) { erro = e; }
    assert.ok(erro, `${desc}: esperada recusa de ${nome}, foi aceito`);
    assert.ok(erro.redis, `${desc}: erro do Redis (got ${erro.message})`);
    assert.ok(erro.message.startsWith(`${PREFIXO} ${nome}: `), `${desc}: prefixo (got ${erro.message})`);
    for (const t of trechos) assert.ok(erro.message.includes(t), `${desc}: mensagem com "${t}" (got ${erro.message})`);
    assert.equal(await snapshot(), antes, `${desc}: nada gravado`);
    return erro.message;
}

async function limpo() {
    await r('FLUSHALL');
}
beforeEach(limpo);

// ----------------------------------------------- violations of each rule --
// [value, expected excerpt of the reason, description]
function violacoes(regra, emJson = false) {
    const out = [];
    const t = regra.type;
    if (emJson) {
        if (t === 'integer') {
            out.push(['12', 'integer number in the JSON', 'inteiro em texto JSON']);
            out.push([1.5, 'integer number in the JSON', 'fracionario']);
            if (regra.min !== undefined) out.push([regra.min - 1, `minimum ${regra.min}`, 'abaixo do minimo']);
            if (regra.max !== undefined) out.push([regra.max + 1, `maximum ${regra.max}`, 'acima do maximo']);
            return out;
        }
        out.push([5, 'text in the JSON', 'numero no lugar de texto']);
    }
    switch (t) {
        case 'text':
            if (regra.min >= 1) out.push(['', 'empty', 'empty']);
            if (regra.max !== undefined) out.push(['é'.repeat(regra.max + 1), `maximum ${regra.max}`, `${regra.max + 1} caracteres multibyte`]);
            for (const c of regra.forbids || '') out.push([`a${c}b`, `cannot contain "${c}"`, `com "${c}"`]);
            break;
        case 'boolean':
            for (const v of ['True', 'False', '1', '']) out.push([v, '"true" or "false"', `"${v}"`]);
            break;
        case 'integer':
            for (const v of ['x', '1.5', '028', '-0', ' 1']) out.push([v, 'integer', `"${v}"`]);
            if (!regra.allowEmpty) out.push(['', 'integer', 'empty']);
            if (regra.min !== undefined) out.push([String(regra.min - 1), `minimum ${regra.min}`, 'abaixo do minimo']);
            if (regra.max !== undefined) out.push([String(regra.max + 1), `maximum ${regra.max}`, 'acima do maximo']);
            break;
        case 'enum':
            out.push(['nao-e-valor', 'must be one of', 'fora da lista']);
            if (regra.values.some(v => v.toLowerCase() !== v)) out.push([regra.values.find(v => v.toLowerCase() !== v).toLowerCase(), 'must be one of', 'caixa trocada']);
            else if (regra.values.some(v => v.toUpperCase() !== v)) out.push([regra.values.find(v => v.toUpperCase() !== v).toUpperCase(), 'must be one of', 'caixa trocada']);
            break;
        case 'language':
            for (const v of ['pt_BR', 'p', '', 'pt-', 'pt--BR', 'português', '123', 'pt-abcdefghi']) out.push([v, 'RFC 5646', `"${v}"`]);
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
                out.push([comDigito(ok, UUID_VERSAO, d), ['UUID version must be 1 to 8', `got ${d} in`], `versao ${d}`]);
            }
            for (const d of '01234567cdef') {
                out.push([comDigito(ok, UUID_VARIANTE, d), ['UUID variant must be the one of RFC 9562', `got ${d} in`], `variante ${d}`]);
            }
            // Both wrong: the version is checked first.
            out.push([comDigito(comDigito(ok, UUID_VERSAO, '0'), UUID_VARIANTE, 'c'), ['UUID version', 'got 0 in'], 'versao 0 e variante c']);
            out.push(['00000000-0000-0000-0000-000000000000', ['nil UUID', 'is not a profile id'], 'nulo (Nil UUID)']);
            out.push(['ffffffff-ffff-ffff-ffff-ffffffffffff', ['max UUID', 'is not a profile id'], 'maximum (Max UUID)']);
            // The case is checked first: a valid variant in uppercase (B).
            out.push([comDigito(ok, UUID_VARIANTE, 'B'), 'lowercase', 'variante B maiuscula']);
            break;
        }
        case 'fileName': {
            const ext = regra.extensions[0];
            out.push(['', 'empty', 'empty']);
            out.push([SVG_EM_LINHA, 'inline SVG is not accepted', 'SVG em linha']);
            out.push(['<svg onload="alert(1)"/>', 'inline SVG is not accepted', 'SVG com script']);
            for (const v of ['thumbs/a.svg', '../a.svg', '/a.svg', '/user-files/thumbs/a.svg', 'a/../../etc/passwd.svg', 'a\\b.svg', '..\\a.svg', `a.${ext}/`]) {
                out.push([v, ['without a directory', 'cannot contain "/" or "\\"'], `"${v}"`]);
            }
            for (const v of ['a', 'a.', '..', 'a.SVG', 'a.Png', 'a.gif', 'a.svgz', 'a.jpe', 'a.svg.txt', `a.${ext} `, `a.${ext}\n`, 'svg', 'a_svg']) {
                out.push([v, ['extension must be one of', regra.extensions.map(e => `.${e}`).join(', ')], `extensao: ${JSON.stringify(v)}`]);
            }
            for (const v of [`.${ext}`, `..${ext}`, `.oculto.${ext}`, `-a.${ext}`, `_a.${ext}`, `.a.${ext}`]) {
                out.push([v, 'must start with a letter or digit', `comeco: "${v}"`]);
            }
            for (const v of [`a b.${ext}`, `avataré.${ext}`, `a:b.${ext}`, `a%2f.${ext}`, `a?.${ext}`, `a*.${ext}`, `a\u0000b.${ext}`, `a\tb.${ext}`, `a~.${ext}`, `a+b.${ext}`]) {
                out.push([v, 'only with unaccented letters, digits', `caractere: ${JSON.stringify(v)}`]);
            }
            out.push(['a'.repeat(regra.maxName + 1) + `.${ext}`, [`${regra.maxName + 1} characters before the extension`, `maximum ${regra.maxName}`], `${regra.maxName + 1} characters before the extension`]);
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
            const valido = jsonValido(regra);
            for (const [nome, r0] of Object.entries(regra.fields)) {
                const rr = resolve(r0);
                if (rr.required) {
                    const o = { ...valido }; delete o[nome];
                    out.push([JSON.stringify(o), `member "${nome}" required`, `sem ${nome}`]);
                }
                for (const [v, m, d] of violacoes(rr, true)) {
                    out.push([JSON.stringify({ ...valido, [nome]: v }), [`member "${nome}": `, m], `${nome}: ${d}`]);
                }
            }
            if (regra.otherFields === false) out.push([JSON.stringify({ ...valido, inventado: 1 }), 'member "inventado" not in the schema', 'member extra']);
            break;
        }
        case 'jsonList':
            out.push(['nao-e-json', 'JSON list', 'texto que nao e JSON']);
            out.push(['{}', 'JSON list', 'JSON object vazio']);
            out.push(['{"a":"b"}', 'JSON list', 'JSON object']);
            out.push(['"texto"', 'JSON list', 'string JSON']);
            out.push(['', 'JSON list', 'empty']);
            for (const [v, m, d] of violacoes(resolve(regra.items), true)) {
                out.push([JSON.stringify(['ok', v]), ['item 1: ', m], `item: ${d}`]);
            }
            break;
        default:
            throw new Error(`tipo de regra sem gerador de violacoes neste teste: ${t}`);
    }
    return out;
}

// Values that satisfy the rule, the boundaries included.
function aceitacoes(regra) {
    switch (regra.type) {
        case 'text': {
            const out = [];
            if (regra.max !== undefined) out.push('é'.repeat(regra.max));
            out.push('a'.repeat(Math.max(regra.min || 0, 1)));
            if (!regra.min) out.push('');
            return out;
        }
        case 'boolean': return ['true', 'false'];
        case 'integer': {
            const out = ['0' === String(regra.min) ? '0' : String(regra.min ?? 7)];
            if (regra.max !== undefined) out.push(String(regra.max));
            if (regra.allowEmpty) out.push('');
            if (regra.min === undefined) out.push('-3');
            return out;
        }
        case 'enum': return regra.values.slice();
        case 'language': return ['pt-BR', 'en', 'pt', 'zh-Hant-TW', 'x-tv30', 'i-default', 'es-419'];
        // The initial load (v4, variant 9), a crypto.randomUUID() (what the AoP
        // generates), versions 1 and 7 and each version from 1 to 8 with each
        // variant 8, 9, a and b.
        case 'uuid': {
            const out = ['c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10', require('crypto').randomUUID(), 'f81d4fae-7dec-11d0-a765-00a0c91e6bf6',
                '017f22e2-79b0-7cc3-98c4-dc0c0c07398f'];
            for (const v of '12345678') for (const va of '89ab') out.push(comDigito(comDigito(P1, UUID_VERSAO, v), UUID_VARIANTE, va));
            return out;
        }
        // The avatar of the load, what the AoP writes, each extension, the
        // boundary sizes and ".", "_" and "-" inside the name ("a..b.svg":
        // without "/", ".." does not go up a directory).
        case 'fileName': return [
            'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10.svg', `${require('crypto').randomUUID()}.svg`,
            ...regra.extensions.map(e => `a.${e}`), 'Z.png', '0.jpg',
            'a'.repeat(regra.maxName) + '.' + regra.extensions[0], 'Avatar_1-x.webp', 'a.png.svg', 'a..b.svg', 'a-.jpeg', 'a_.svg', 'a.b.c.png',
        ];
        case 'origin': return ['http://users-test.apps.localhost:8080', 'http://a.apps.localhost', 'https://x.example', 'https://x.example:8443', 'http://[::1]:8099', 'http://127.0.0.1:65535'];
        case 'json': return ['HS256', 'HS512', 'RS256', 'RS512'].map(alg => JSON.stringify({ ...jsonValido(regra), alg }));
        case 'jsonList': return ['[]', '["light"]', '["light","wind"]', '[""]'];
        default: throw new Error(`tipo de regra sem gerador de aceitacoes neste teste: ${regra.type}`);
    }
}

function jsonValido(regra) {
    assert.ok(regra.fields.alg && regra.fields.key && regra.fields.registeredAt, 'o gerador de JSON so conhece a entrada de bind-context');
    return { ...ENTRADA };
}

// --------------------------------------------- calls assembled per slot --
// For each family: the "slots" (places where a rule applies) and how to
// assemble a valid call with the value of the slot replaced. prep runs
// first.
const indexaP1 = () => fcall('tv30_profile_write', ...perfilArgs(P1, [], PERFIL));
const SLOTS = {
    'session:current-user': [{ rotulo: 'valor', regra: f => f.value, chamada: v => ['tv30_session_user', [v]], trecho: 'value:' }],
    'session:current-service-id': [{ rotulo: 'valor', regra: f => f.value, chamada: v => ['tv30_session_service_id', [v]], trecho: 'value:' }],
    'session:current-service': campoSlots(campo => v => ['tv30_session_service', plano({ ...SVC, [campo]: v })]),
    'users:index': [{ rotulo: 'member', regra: f => f.members, chamada: v => ['tv30_profile_write', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} of the key:' }],
    'user:{id}': [
        { rotulo: '{id}', regra: f => f.parts.id, chamada: v => ['tv30_profile_write', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} of the key:' },
        // The id field has the same rule as the {id} part (slot above) and must
        // equal it (own test below): left out of the per-field slots.
        ...campoSlots(campo => v => ['tv30_profile_write', perfilArgs(P1, [], { ...PERFIL, [campo]: v })], ['id']),
    ],
    'user:{id}:consent': [
        { rotulo: 'member', regra: f => f.members, chamada: v => ['tv30_profile_write', perfilArgs(P1, ['urn:tv30:service:ok', v], PERFIL)], trecho: 'member' },
        { rotulo: '{id}', regra: f => f.parts.id, chamada: v => ['tv30_profile_write', perfilArgs(v, ['urn:x'], { ...PERFIL, id: v })], trecho: '{id} of the key:' },
    ],
    'user:{id}:broadcaster-attrs:{scid}': [
        // The profile of the value must exist for the acceptance; with an
        // invalid value the creation fails and the refusal comes from the {id}
        // part of the key.
        { rotulo: '{id}', regra: f => f.parts.id, prep: v => fcall('tv30_profile_write', ...perfilArgs(v, [], { ...PERFIL, id: v })).catch(() => undefined), chamada: v => ['tv30_broadcaster_attributes', [v, 'urn:s:1', 'cor', 'azul']], trecho: '{id} of the key:' },
        { rotulo: '{scid}', regra: f => f.parts.scid, prep: indexaP1, chamada: v => ['tv30_broadcaster_attributes', [P1, v, 'cor', 'azul']], trecho: '{scid} of the key:' },
        { rotulo: 'nome de campo', regra: f => f.otherFields.name, prep: indexaP1, chamada: v => ['tv30_broadcaster_attributes', [P1, 'urn:s:1', v, 'azul']], trecho: 'field name:' },
        // outrosCampos.value (text, min 1): empty REMOVES the field in the
        // function, so no writable value violates the rule; the audit covers it
        // (below).
    ],
    'client:{id}': [
        { rotulo: '{id}', regra: f => f.parts.id, chamada: v => ['tv30_client_write', [v, ...plano(CLIENTE)]], trecho: '{id} of the key:' },
        ...campoSlots(campo => v => ['tv30_client_write', ['c1', ...plano({ ...CLIENTE, [campo]: v })]]),
    ],
    'clients:authorized': [{ rotulo: 'member', regra: f => f.members, chamada: v => ['tv30_client_authorize', [v, ...plano(CLIENTE)]], trecho: '{id} of the key:' }],
    'clients:blocked': [{ rotulo: 'member', regra: f => f.members, chamada: v => ['tv30_client_block', [v]], trecho: '{id} of the key:' }],
    'origins:associated': [
        { rotulo: 'nome de campo', regra: f => f.otherFields.name, chamada: v => ['tv30_origin_register', [v, 'urn:tv30:service:users-test']], trecho: 'field name:' },
        { rotulo: 'valor', regra: f => f.otherFields.value, chamada: v => ['tv30_origin_register', [ORIGEM, v]], trecho: 'field "' },
    ],
    'services:announced': [{ rotulo: 'member', regra: f => f.members, chamada: v => ['tv30_announced_services', ['urn:tv30:service:ok', v]], trecho: 'services:announced member' }],
    'v100.0:remote-device:{handle}': [
        { rotulo: '{handle}', regra: f => f.parts.handle, chamada: v => ['tv30_v100_remote_device_write', [v, ...plano(RD)]], trecho: '{handle} of the key:' },
        ...campoSlots(campo => v => ['tv30_v100_remote_device_write', [RD_HANDLE, ...plano({ ...RD, [campo]: v })]]),
    ],
    'bind-context:{serviceId}': [
        { rotulo: '{serviceId}', regra: f => f.parts.serviceId, chamada: v => ['tv30_bind_register', [v, JSON.stringify(ENTRADA)]], trecho: '{serviceId} of the key:' },
        { rotulo: 'entrada', regra: f => f.items, chamada: v => ['tv30_bind_register', ['urn:tv30:service:a', v]], trecho: 'entry:' },
    ],
};
// One slot per declared field; the family comes when the cases are
// generated.
function campoSlots(montar, exceto = []) {
    return [{ porCampo: true, montar, exceto }];
}
function expandeSlots(f) {
    const out = [];
    for (const s of SLOTS[f.family] || []) {
        if (!s.porCampo) { out.push(s); continue; }
        for (const campo of Object.keys(f.fields).filter(c => !s.exceto.includes(c))) {
            out.push({ rotulo: `field ${campo}`, regra: ff => ff.fields[campo], chamada: s.montar(campo), trecho: `field ${campo}:` });
        }
    }
    return out;
}

// ------------------------------------------------------------------ load --
test('biblioteca carregada: exatamente as funcoes do schema.json, flags certas', async () => {
    const lista = await r('FUNCTION', 'LIST', 'LIBRARYNAME', ESQ.library);
    assert.equal(lista.length, 1, 'uma biblioteca tv30');
    const lib = {};
    for (let i = 0; i < lista[0].length; i += 2) lib[lista[0][i]] = lista[0][i + 1];
    assert.equal(lib.library_name, ESQ.library);
    const carregadas = {};
    for (const fn of lib.functions) {
        const o = {};
        for (let i = 0; i < fn.length; i += 2) o[fn[i]] = fn[i + 1];
        carregadas[o.name] = o.flags;
    }
    assert.deepEqual(Object.keys(carregadas).sort(), ESQ.functions.map(f => f.name).sort());
    for (const f of ESQ.functions) {
        const flags = carregadas[f.name];
        assert.ok(flags.includes('no-cluster'), `${f.name}: no-cluster`);
        assert.equal(flags.includes('no-writes'), !!f.readOnly, `${f.name}: no-writes so nas somente leitura`);
    }
    // Each function named in a family exists; each family has a writer.
    const nomes = new Set(ESQ.functions.map(f => f.name));
    for (const fam of ESQ.families) {
        assert.ok(fam.functions.length > 0, `${fam.family}: funcoes`);
        for (const n of fam.functions) assert.ok(nomes.has(n), `${fam.family} cita ${n}`);
        assert.ok(SLOTS[fam.family], `${fam.family}: o teste de recusa conhece a familia`);
    }
    assert.equal(await fcallRo('tv30_version'), ESQ.version);
});

test('FCALL_RO so roda as somente leitura; numkeys > 0 e argumentos errados sao recusados', async () => {
    await assert.rejects(fcallRo('tv30_session_user', 'u'), /write flag/);
    await assert.rejects(r('FCALL', 'tv30_session_user', 1, 'session:current-user', 'u'), e => e.message.startsWith(`${PREFIXO} tv30_session_user: call with numkeys 1`));
    await recusa('sem argumento', 'tv30_session_user', [], ['usage:']);
    await recusa('pares incompletos', 'tv30_session_service', ['serviceContextId'], ['incomplete field value pairs']);
    await recusa('n invalido', 'tv30_profile_write', [P1, 'x', 'id', P1], ['n=']);
    await recusa('n alem dos argumentos', 'tv30_profile_write', [P1, '9', 'a'], ['n=']);
    await assert.rejects(fcallRo('tv30_audit', 'x'), new RegExp(`${PREFIXO} tv30_audit: usage:`));
});

// --------------------------------------- refusal of each rule (generated) --
for (const fam of ESQ.families) {
    test(`recusa de cada regra: ${fam.family}`, async (t) => {
        let casos = 0;
        let aceitos = 0;
        for (const slot of expandeSlots(fam)) {
            const regra = resolve(slot.regra(fam));
            for (const [valor, trecho, desc] of violacoes(regra)) {
                await limpo();
                if (slot.prep) await slot.prep(valor);
                const [nome, args] = slot.chamada(valor);
                await recusa(`${fam.family} ${slot.rotulo} ${desc}`, nome, args, [slot.trecho, ...[].concat(trecho)]);
                casos++;
            }
            for (const valor of aceitacoes(regra)) {
                await limpo();
                if (slot.prep) await slot.prep(valor);
                const [nome, args] = slot.chamada(valor);
                await assert.doesNotReject(fcall(nome, ...args), `${fam.family} ${slot.rotulo} aceita ${JSON.stringify(valor).slice(0, 60)}`);
                assert.deepEqual(await auditoria(), [], `${fam.family} ${slot.rotulo}: auditoria vazia depois de gravar ${JSON.stringify(valor).slice(0, 40)}`);
                aceitos++;
            }
        }
        // Required, conditional and unexpected fields.
        if (fam.fields) {
            const [base, chamar] = {
                'session:current-service': [SVC, h => ['tv30_session_service', plano(h)]],
                'user:{id}': [PERFIL, h => ['tv30_profile_write', perfilArgs(P1, [], h)]],
                'client:{id}': [CLIENTE, h => ['tv30_client_write', ['c1', ...plano(h)]]],
                'v100.0:remote-device:{handle}': [RD, h => ['tv30_v100_remote_device_write', [RD_HANDLE, ...plano(h)]]],
            }[fam.family];
            for (const [campo, r0] of Object.entries(fam.fields)) {
                const regra = resolve(r0);
                const sem = { ...base }; delete sem[campo];
                await limpo();
                const [nome, args] = chamar(sem);
                if (regra.required) {
                    await recusa(`${fam.family} sem ${campo}`, nome, args, [`field ${campo}: required, absent`]);
                    casos++;
                } else if (regra.requiredIf) {
                    const c = regra.requiredIf;
                    assert.equal(base[c.field], c.value, `base do teste com ${c.field}=${c.value}`);
                    await recusa(`${fam.family} sem ${campo} com ${c.field}=${c.value}`, nome, args, [`field ${campo}: required when ${c.field}=${c.value}`]);
                    casos++;
                } else {
                    await assert.doesNotReject(fcall(nome, ...args), `${fam.family}: ${campo} e opcional`);
                }
            }
            if (fam.otherFields === false) {
                await limpo();
                const [nome, args] = chamar({ ...base, inventado: 'x' });
                await recusa(`${fam.family} campo extra`, nome, args, ['field "inventado": not in the schema']);
                casos++;
            }
        }
        assert.ok(casos > 0, `${fam.family}: algum caso de recusa`);
        t.diagnostic(`${fam.family}: ${casos} recusa(s) e ${aceitos} aceitacao(oes) de fronteira`);
    });
}

test('user:{id}: condicionais falsos dispensam os campos; id do hash igual ao da chave', async () => {
    await fcall('tv30_profile_write', ...perfilArgs(P1, [], PERFIL_MIN));
    assert.deepEqual(await auditoria(), []);
    await recusa('id diferente', 'tv30_profile_write', perfilArgs(P1, [], { ...PERFIL, id: P2 }), [`user:${P1} field id: must equal the {id} of the key`]);
});

test('broadcaster-attrs: atributos basicos proibidos e limit of 20 MB no resultado', async () => {
    const f = familia('user:{id}:broadcaster-attrs:{scid}');
    await indexaP1();
    for (const nome of f.forbiddenFields.names) {
        await recusa(`redefinir ${nome}`, 'tv30_broadcaster_attributes', [P1, 'urn:s:1', nome, 'x'], [`field ${nome}: basic attribute`]);
        await recusa(`remover ${nome}`, 'tv30_broadcaster_attributes', [P1, 'urn:s:1', nome, ''], [`field ${nome}: basic attribute`]);
    }
    // Exactly at the limit: accepted; one byte above: refused (sum of names
    // and values).
    const nome = 'grande';
    await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', nome, 'x'.repeat(f.maxBytes - nome.length));
    await recusa('acima do teto', 'tv30_broadcaster_attributes', [P1, 'urn:s:1', 'b', 'y'], [`above the limit of ${f.maxBytes}`]);
    // Removing frees room.
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s:1', nome, '', 'b', 'y'), 2);
    assert.deepEqual(await r('HGETALL', `user:${P1}:broadcaster-attrs:urn:s:1`), ['b', 'y']);
});

// ---------------------------------------------------- effects (acceptance) --
test('sessao: SET dos dois espelhos (vazio vale) e o hash regravado inteiro', async () => {
    assert.equal(await fcall('tv30_session_user', P1), 1);
    assert.equal(await r('GET', 'session:current-user'), P1);
    await fcall('tv30_session_user', '');
    assert.equal(await r('GET', 'session:current-user'), '');
    await fcall('tv30_session_service_id', 'urn:tv30:service:users-test');
    assert.equal(await r('GET', 'session:current-service-id'), 'urn:tv30:service:users-test');
    await fcall('tv30_session_service', ...plano({ ...SVC, serviceId: '101', serviceName: 'Canal' }));
    await fcall('tv30_session_service', ...plano(SVC));
    const h = await r('HGETALL', 'session:current-service');
    assert.equal(h.length, 10);
    assert.equal(h[h.indexOf('serviceId') + 1], '');
    assert.deepEqual(await auditoria(), []);
});

test('perfil: grava os tres juntos, visibilidade acumula, ultimo acesso so em perfil indexado, remocao leva tudo', async () => {
    await fcall('tv30_profile_write', ...perfilArgs(P1, ['urn:a', 'urn:b'], PERFIL_MIN));
    await fcall('tv30_profile_write', ...perfilArgs(P1, ['urn:c'], { ...PERFIL_MIN, nickname: 'Outro' }));
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
    await recusa('instante invalido', 'tv30_profile_last_access', [P1, 'ontem'], ['field lastAccess:']);

    await fcall('tv30_profile_write', ...perfilArgs(P2, [], { ...PERFIL_MIN, id: P2 }));
    await fcall('tv30_broadcaster_attributes', P1, 'urn:tv30:service:x', 'cor', 'azul');
    await fcall('tv30_broadcaster_attributes', P1, 'urn:y', 'k', 'v');
    await fcall('tv30_broadcaster_attributes', P2, 'urn:y', 'k', 'v');
    assert.deepEqual(await auditoria(), []);
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
    assert.deepEqual(await auditoria(), []);
});

// The limit check is the function's own: no read before the call.
test('perfil: limit of users:index na gravacao; id novo alem dele recusado, regravar existente aceito, remover libera', async () => {
    const f = familia('users:index');
    assert.ok(Number.isInteger(f.maxMembers) && f.maxMembers > 0, 'teto declarado no esquema');
    assert.ok(f.maxMembersSource, 'fonte do teto');
    const ids = Array.from({ length: f.maxMembers }, (_, i) => `0b6e3c1a-7f2d-4e8b-9a5c-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids) await fcall('tv30_profile_write', ...perfilArgs(id, ['urn:a'], { ...PERFIL_MIN, id }));
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);

    const novo = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    const msg = await recusa('id novo com o conjunto no teto', 'tv30_profile_write', perfilArgs(novo, ['urn:b'], { ...PERFIL_MIN, id: novo }),
        [`users:index limit of ${f.maxMembers} profiles reached (${f.maxMembersSource})`, novo]);
    assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    assert.equal(await r('EXISTS', `user:${novo}`), 0);

    // Rewriting one of those at the limit: accepted, with a set of the same
    // size.
    assert.equal(await fcall('tv30_profile_write', ...perfilArgs(ids[2], ['urn:c'], { ...PERFIL_MIN, id: ids[2], nickname: 'Regravado' })), 1);
    assert.equal(await r('HGET', `user:${ids[2]}`, 'nickname'), 'Regravado');
    assert.deepEqual((await r('SMEMBERS', `user:${ids[2]}:consent`)).sort(), ['urn:a', 'urn:c']);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);

    // With one removed, the new one goes in; and the next one is refused
    // again.
    await fcall('tv30_profile_remove', ids[0]);
    assert.equal(await fcall('tv30_profile_write', ...perfilArgs(novo, [], { ...PERFIL_MIN, id: novo })), 1);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembers);
    await recusa('de novo no teto', 'tv30_profile_write', perfilArgs(ids[0], [], { ...PERFIL_MIN, id: ids[0] }), ['limit of']);
    assert.deepEqual(await auditoria(), []);
});

test('perfil: teto conta o que esta no Redis, inclusive member gravado a mao', async () => {
    const f = familia('users:index');
    const ids = Array.from({ length: f.maxMembers + 1 }, (_, i) => `5a9d2f4e-1c3b-4d6a-8e7f-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids.slice(0, f.maxMembers)) await fcall('tv30_profile_write', ...perfilArgs(id, [], { ...PERFIL_MIN, id }));
    // One more, outside the functions: the audit reports the excess.
    await r('SADD', 'users:index', ids[f.maxMembers]);
    await r('HSET', `user:${ids[f.maxMembers]}`, ...plano({ ...PERFIL_MIN, id: ids[f.maxMembers] }));
    const achados = await auditoria();
    assert.deepEqual(achados, [['users:index', 'users:index', `${f.maxMembers + 1} members, above the limit of ${f.maxMembers} (${f.maxMembersSource})`]]);
    // Above the limit: an existing one can still be rewritten; a new id cannot.
    assert.equal(await fcall('tv30_profile_write', ...perfilArgs(ids[0], [], { ...PERFIL_MIN, id: ids[0], nickname: 'Ainda' })), 1);
    const novo = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    await recusa('id novo acima do teto', 'tv30_profile_write', perfilArgs(novo, [], { ...PERFIL_MIN, id: novo }), [`limit of ${f.maxMembers} profiles`]);
});

// The last access checks only the written field, and the removal does not
// check the id.
test('id que nao e UUID, gravado a mao: auditoria aponta, gravacao recusada, remocao passa', async () => {
    const velho = 'user_1760000000000';
    await r('SADD', 'users:index', velho);
    await r('HSET', `user:${velho}`, ...plano({ ...PERFIL_MIN, id: velho }));
    await r('SADD', `user:${velho}:consent`, 'urn:s');
    const achados = await auditoria();
    for (const k of ['users:index', `user:${velho}`, `user:${velho}:consent`]) {
        assert.ok(achados.some(([c, , m]) => c === k && m.includes('RFC 9562')), `${k} apontado: ${JSON.stringify(achados)}`);
    }
    await recusa('regravar o perfil', 'tv30_profile_write', perfilArgs(velho, [], { ...PERFIL_MIN, id: velho }), ['{id} of the key: must be a UUID']);
    await recusa('atributo de emissora no perfil', 'tv30_broadcaster_attributes', [velho, 'urn:s', 'cor', 'azul'], ['{id} of the key: must be a UUID']);
    assert.equal(await fcall('tv30_profile_last_access', velho, '5'), 1);
    assert.equal(await fcall('tv30_profile_remove', velho), 3);
    assert.deepEqual(await auditoria(), []);
});

// Replacing the avatar with the file name, or removing it, empties the
// audit.
test('avatar em SVG em linha, gravado a mao: auditoria aponta, regravacao recusada, o resto segue', async () => {
    await r('SADD', 'users:index', P1);
    await r('HSET', `user:${P1}`, ...plano({ ...PERFIL_MIN, avatar: SVG_EM_LINHA }));
    const achados = await auditoria();
    assert.deepEqual(achados.map(([k, f]) => [k, f]), [[`user:${P1}`, 'user:{id}']], JSON.stringify(achados));
    assert.ok(achados[0][2].startsWith('field avatar: inline SVG is not accepted'), achados[0][2]);
    assert.ok(!achados[0][2].includes('\n'), 'motivo numa linha so');
    await recusa('regravar com o SVG', 'tv30_profile_write', perfilArgs(P1, [], { ...PERFIL_MIN, avatar: SVG_EM_LINHA }), ['field avatar: inline SVG is not accepted']);
    assert.equal(await fcall('tv30_profile_last_access', P1, '7'), 1);
    assert.equal(await fcall('tv30_broadcaster_attributes', P1, 'urn:s', 'cor', 'azul'), 1);
    await r('HSET', `user:${P1}`, 'avatar', `${P1}.svg`);
    assert.deepEqual(await auditoria(), []);
    await fcall('tv30_profile_write', ...perfilArgs(P1, [], PERFIL_MIN));
    assert.equal(await r('HEXISTS', `user:${P1}`, 'avatar'), 0);
    assert.deepEqual(await auditoria(), []);
    // 1 (SREM) + user:{P1} + 1 broadcaster-attrs (no consent)
    assert.equal(await fcall('tv30_profile_remove', P1), 3);
});

// Catches a deviation between the schema and the expression of the contract
// that the cases generated from the schema itself do not catch (an extension
// removed from the list, maxNome changed).
const AVATAR_CONTRATO = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$/;
test('avatar: a regra do esquema decide igual a expressao do contrato', async (t) => {
    const regra = resolve(familia('user:{id}').fields.avatar);
    const corpus = new Set([
        ...violacoes(regra).map(([v]) => v), ...aceitacoes(regra),
        ...['svg', 'png', 'jpg', 'jpeg', 'webp', 'gif', 'SVG', 'tiff', 'svgz', 'jpe'].map(e => `x.${e}`),
        'a'.repeat(127) + '.svg', 'a'.repeat(128) + '.webp', 'a'.repeat(129) + '.png', 'a'.repeat(128) + '..svg', 'a'.repeat(127) + '..svg',
    ]);
    // Deterministic generator (Park-Miller) over an alphabet with the letters
    // of the extensions and the characters the rule refuses.
    let s = 12345;
    const rnd = n => { s = (s * 16807) % 2147483647; return s % n; };
    const alfabeto = ['a', 'Z', '0', '.', '_', '-', '/', ' ', 'é', 's', 'v', 'g', 'p', 'n', 'j', 'e', 'w', 'b', 'G', '\\', '<'];
    const exts = ['svg', 'png', 'jpg', 'jpeg', 'webp', 'Svg', 'sv', 'gif'];
    for (let i = 0; i < 400; i++) {
        let v = '';
        const n = 1 + rnd(9);
        for (let j = 0; j < n; j++) v += alfabeto[rnd(alfabeto.length)];
        corpus.add(v);
        corpus.add(`${v}.${exts[rnd(exts.length)]}`);
    }
    let aceitos = 0;
    for (const v of corpus) {
        await limpo();
        let erro = null;
        try { await fcall('tv30_profile_write', ...perfilArgs(P1, [], { ...PERFIL_MIN, avatar: v })); } catch (e) { erro = e; }
        const esperado = AVATAR_CONTRATO.test(v);
        assert.equal(erro === null, esperado, `${JSON.stringify(v)}: o contrato ${esperado ? 'aceita' : 'recusa'}, a funcao ${erro ? 'recusou: ' + erro.message : 'aceitou'}`);
        if (erro) assert.ok(erro.message.startsWith(`${PREFIXO} tv30_profile_write: user:${P1} field avatar: `), erro.message);
        if (esperado) aceitos++;
    }
    assert.ok(aceitos >= 50 && aceitos < corpus.size - 50, `${aceitos} aceitos de ${corpus.size}`);
    t.diagnostic(`avatar: ${corpus.size} valores, ${aceitos} aceitos pela expressao do contrato e pela funcao`);
});

test('UUID estrito: id da carga e crypto.randomUUID() do AoP aceitos; nulo e maximo recusados', async () => {
    const ids = ['c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10', ...Array.from({ length: 20 }, () => require('crypto').randomUUID())];
    for (const id of ids) {
        await limpo();
        await fcall('tv30_profile_write', ...perfilArgs(id, ['urn:s'], { ...PERFIL_MIN, id }));
        assert.deepEqual(await r('SMEMBERS', 'users:index'), [id], id);
        assert.deepEqual(await auditoria(), [], id);
    }
    for (const [id, m] of [['00000000-0000-0000-0000-000000000000', 'nil UUID'], ['ffffffff-ffff-ffff-ffff-ffffffffffff', 'max UUID']]) {
        await limpo();
        const msg = await recusa(m, 'tv30_profile_write', perfilArgs(id, [], { ...PERFIL_MIN, id }), [`{id} of the key: ${m}`, 'is not a profile id']);
        assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    }
});

test('atributos de emissora: perfil precisa estar indexado; vazio remove', async () => {
    await recusa('perfil inexistente', 'tv30_broadcaster_attributes', [P1, 'urn:s:1', 'cor', 'azul'], ['outside users:index (invariant profile-dependents)']);
    await indexaP1();
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
    assert.deepEqual(await auditoria(), []);

    // Inclusion on boot: a blocked client does not go in; without a record it
    // is refused; with a record it goes in.
    assert.equal(await fcall('tv30_client_add_authorized', 'c1'), 0);
    await recusa('sem client:{id}', 'tv30_client_add_authorized', ['c9'], ['invariant authorized-has-record']);
    await r('HSET', 'client:velho', 'class', 'standAlone', 'refreshToken', 'r');
    assert.equal(await fcall('tv30_client_add_authorized', 'velho'), 1);
    assert.equal(await fcall('tv30_client_add_authorized', 'velho'), 0);
    assert.deepEqual(await auditoria(), []);
});

test('registro existente: campo novo conferido, o resto ja gravado nao trava (a auditoria mostra)', async () => {
    // Boolean "False" written by hand, outside the schema.
    await fcall('tv30_profile_write', ...perfilArgs(P1, [], PERFIL_MIN));
    await r('HSET', `user:${P1}`, 'parentalControl', 'False');
    assert.equal(await fcall('tv30_profile_last_access', P1, '99'), 1);
    assert.deepEqual(await auditoria(), [[`user:${P1}`, 'user:{id}', 'field parentalControl: must be "true" or "false" (got "False")']]);
    // A new client checks everything; an existing one only what is written.
    await recusa('cliente novo incompleto', 'tv30_client_write', ['c1', 'accessToken', 'a'], ['field class: required, absent']);
});

test('dispositivos remotos da 100.0: regrava inteiro, remove e limpa so a propria familia', async () => {
    const key = `v100.0:remote-device:${RD_HANDLE}`;
    const other = 'v100.0:remote-device:3c8a7d2f-5e4b-4a9c-8d1e-2f3a4b5c6d7e';
    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...plano(RD));
    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...plano({ ...RD, state: 'registered', paired: 'false', supportedTypes: '[]' }));
    const h = await r('HGETALL', key);
    assert.deepEqual(Object.fromEntries(Array.from({ length: h.length / 2 }, (_, i) => [h[2 * i], h[2 * i + 1]])),
        { ...RD, state: 'registered', paired: 'false', supportedTypes: '[]' });
    assert.deepEqual(await auditoria(), []);
    // The write takes the whole record: a missing field is refused.
    const { state, ...withoutState } = RD;
    await recusa('registro incompleto', 'tv30_v100_remote_device_write', [RD_HANDLE, ...plano(withoutState)], ['field state: required, absent']);

    assert.equal(await fcall('tv30_v100_remote_device_remove', RD_HANDLE), 1);
    assert.equal(await fcall('tv30_v100_remote_device_remove', RD_HANDLE), 0);
    assert.equal(await r('EXISTS', key), 0);

    await fcall('tv30_v100_remote_device_write', RD_HANDLE, ...plano(RD));
    await fcall('tv30_v100_remote_device_write', other.split(':').pop(), ...plano(RD));
    await fcall('tv30_client_authorize', 'c1', ...plano(CLIENTE));
    assert.equal(await fcall('tv30_v100_remote_devices_clear'), 2);
    assert.equal(await r('EXISTS', key), 0);
    assert.equal(await r('EXISTS', other), 0);
    assert.equal(await r('EXISTS', 'client:c1'), 1, 'a limpeza nao toca outras familias');
    assert.equal(await fcall('tv30_v100_remote_devices_clear'), 0);
    assert.deepEqual(await auditoria(), []);
});

test('origens: registra, remove, limpa', async () => {
    await fcall('tv30_origin_register', ORIGEM, 'urn:tv30:service:users-test');
    await fcall('tv30_origin_register', 'http://uff.apps.localhost:8080', 'current-service');
    assert.equal(await fcall('tv30_origin_remove', ORIGEM), 1);
    assert.equal(await fcall('tv30_origin_remove', ORIGEM), 0);
    assert.deepEqual(await r('HGETALL', 'origins:associated'), ['http://uff.apps.localhost:8080', 'current-service']);
    assert.equal(await fcall('tv30_origins_clear'), 1);
    assert.equal(await fcall('tv30_origins_clear'), 0);
    assert.equal(await fcall('tv30_origin_remove', ORIGEM), 0);
});

test('servicos anunciados: o conjunto regravado inteiro; repetido conta uma vez; sem argumento, apaga', async () => {
    assert.equal(await fcall('tv30_announced_services', 'urn:tv30:service:a', 'urn:tv30:service:b', 'urn:tv30:service:a'), 2);
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), ['urn:tv30:service:a', 'urn:tv30:service:b']);
    // What left the BAMT leaves the set.
    assert.equal(await fcall('tv30_announced_services', 'urn:tv30:service:b', 'urn:tv30:service:c'), 2);
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), ['urn:tv30:service:b', 'urn:tv30:service:c']);
    assert.deepEqual(await auditoria(), []);
    assert.equal(await fcall('tv30_announced_services'), 0);
    assert.equal(await r('EXISTS', 'services:announced'), 0);
    assert.equal(await fcall('tv30_announced_services'), 0);
    // More services than one SADD batch.
    const muitos = Array.from({ length: 1000 }, (_, i) => `urn:tv30:service:s${i}`);
    assert.equal(await fcall('tv30_announced_services', ...muitos), 1000);
    assert.deepEqual(await auditoria(), []);
});

test('bind-context: RPUSH da entrada exata, LREM de todas as iguais', async () => {
    const e1 = JSON.stringify(ENTRADA);
    const e2 = JSON.stringify({ alg: 'RS256', key: '-----BEGIN PUBLIC KEY-----\nMFww\n-----END PUBLIC KEY-----', registeredAt: 1 });
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e1), 1);
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e2), 2);
    assert.equal(await fcall('tv30_bind_register', 'urn:tv30:service:a', e1), 3);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e1, e2, e1]);
    assert.deepEqual(await auditoria(), []);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', e1), 2);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', 'nao-tem'), 0);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:b', e1), 0);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e2]);
});

// ------------------------------------------------------------- atomicity --
test('atomicidade: tipo errado numa chave posterior ou member invalido no fim nao deixam gravacao parcial', async () => {
    // Profile: users:index of another type, so user:{id} is not created.
    await r('SET', 'users:index', 'x');
    await recusa('users:index STRING', 'tv30_profile_write', perfilArgs(P1, [], PERFIL), ['users:index in Redis is string, the schema asks for set']);
    await limpo();
    // Profile: consent of another type (the last key written).
    await r('SET', `user:${P1}:consent`, 'x');
    await recusa('consent STRING', 'tv30_profile_write', perfilArgs(P1, ['urn:a'], PERFIL), [`user:${P1}:consent in Redis is string`]);
    await limpo();
    // Profile: the last invalid service brings the whole operation down.
    await recusa('ultimo servico vazio', 'tv30_profile_write', perfilArgs(P1, ['urn:a', 'urn:b', ''], PERFIL), [`user:${P1}:consent member`]);
    // Rewriting an existing profile with invalid data keeps the previous one
    // intact.
    await fcall('tv30_profile_write', ...perfilArgs(P1, ['urn:a'], PERFIL));
    await recusa('regravacao invalida', 'tv30_profile_write', perfilArgs(P1, ['urn:b'], { ...PERFIL, nickname: '' }), ['field nickname:']);
    // Client: clients:blocked of another type, so neither client:{id} nor
    // clients:authorized.
    await limpo();
    await r('LPUSH', 'clients:blocked', 'x');
    await recusa('clients:blocked LIST', 'tv30_client_authorize', ['c1', ...plano(CLIENTE)], ['clients:blocked in Redis is list']);
    await limpo();
    await r('SADD', 'clients:blocked', 'c1');
    await recusa('autorizar com classe invalida', 'tv30_client_authorize', ['c1', ...plano({ ...CLIENTE, class: 'outra' })], ['field class:']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1'], 'continua bloqueado');
    // Session: the previous hash intact.
    await limpo();
    await fcall('tv30_session_service', ...plano({ ...SVC, serviceName: 'Antes' }));
    await recusa('servico invalido', 'tv30_session_service', plano({ ...SVC, serviceId: 'x' }), ['field serviceId:']);
    // Attributes: a forbidden name among several valid ones.
    await limpo();
    await indexaP1();
    await recusa('um proibido entre validos', 'tv30_broadcaster_attributes', [P1, 'urn:s', 'a', '1', 'nickname', 'x', 'z', '2'], ['field nickname:']);
    // bind-context of another type.
    await limpo();
    await r('SET', 'bind-context:urn:a', 'x');
    await recusa('bind-context STRING', 'tv30_bind_register', ['urn:a', JSON.stringify(ENTRADA)], ['in Redis is string, the schema asks for list']);
    await recusa('bind remove em STRING', 'tv30_bind_remove', ['urn:a', 'x'], ['in Redis is string']);
    // Announced services: the previous set intact.
    await limpo();
    await fcall('tv30_announced_services', 'urn:antes');
    await recusa('ultimo servico vazio', 'tv30_announced_services', ['urn:a', 'urn:b', ''], ['services:announced member "": empty']);
    await recusa('servico longo demais', 'tv30_announced_services', ['urn:a', 'x'.repeat(257)], ['maximum 256']);
    await limpo();
    await r('SET', 'services:announced', 'x');
    await recusa('services:announced STRING', 'tv30_announced_services', ['urn:a'], ['services:announced in Redis is string, the schema asks for set']);
    await recusa('services:announced STRING, sem argumento', 'tv30_announced_services', [], ['in Redis is string']);
});

// ----------------------------------------------------------------- audit --
test('auditoria: cada tipo de problema gravado a mao aparece com chave, familia e motivo; limpa, volta vazia', async () => {
    assert.deepEqual(await auditoria(), [], 'banco vazio');
    // Valid state of every family.
    await fcall('tv30_session_user', P1);
    await fcall('tv30_session_service_id', 'urn:s');
    await fcall('tv30_session_service', ...plano(SVC));
    await fcall('tv30_profile_write', ...perfilArgs(P1, ['urn:s'], PERFIL));
    await fcall('tv30_broadcaster_attributes', P1, 'urn:s', 'cor', 'azul');
    await fcall('tv30_client_authorize', 'c1', ...plano(CLIENTE));
    await fcall('tv30_client_block', 'c2');
    await fcall('tv30_origin_register', ORIGEM, 'urn:s');
    await fcall('tv30_bind_register', 'urn:s', JSON.stringify(ENTRADA));
    await fcall('tv30_announced_services', 'urn:s', 'urn:t');
    assert.deepEqual(await auditoria(), [], 'todas as familias validas');
    const valido = await snapshot();

    // Manual writes (what redis-cli or the commander allow), one of each kind.
    const manuais = [
        [['SET', `user:${P1}:profile`, 'x'], [`user:${P1}:profile`, '-', 'key without family']],
        [['HSET', 'session:x', 'a', '1'], ['session:x', '-', 'key without family']],
        [['SET', 'session:current-service', 'x'], ['session:current-service', 'session:current-service', 'in Redis is string, the schema asks for hash']],
        [['SET', 'session:current-user', 'x'.repeat(65)], ['session:current-user', 'session:current-user', 'value: has 65 characters, maximum 64']],
        [['HSET', `user:${P1}`, 'parentalControl', 'False'], [`user:${P1}`, 'user:{id}', 'field parentalControl:']],
        [['HDEL', `user:${P1}`, 'nickname'], [`user:${P1}`, 'user:{id}', 'field nickname: required, absent']],
        [['HSET', `user:${P1}`, 'extra', '1'], [`user:${P1}`, 'user:{id}', 'field "extra": not in the schema']],
        // Avatar as an inline SVG, and a path with a directory.
        [['HSET', `user:${P1}`, 'avatar', SVG_EM_LINHA], [`user:${P1}`, 'user:{id}', 'field avatar: inline SVG is not accepted']],
        [['HSET', `user:${P1}`, 'avatar', '../segredo.svg'], [`user:${P1}`, 'user:{id}', 'field avatar: only the file name, without a directory']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'empty', ''], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'field "empty": empty']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'nickname', 'x'], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'field nickname: basic attribute']],
        [['SADD', `user:${P1}:consent`, ''], [`user:${P1}:consent`, 'user:{id}:consent', 'member "": empty']],
        [['HSET', 'client:c1', 'class', 'admin'], ['client:c1', 'client:{id}', 'field class:']],
        [['HSET', 'origins:associated', 'http://X.example', 'urn:s'], ['origins:associated', 'origins:associated', 'field name: host must be in lowercase']],
        [['RPUSH', 'bind-context:urn:s', '{"alg":"none","key":"k","registeredAt":1}'], ['bind-context:urn:s', 'bind-context:{serviceId}', 'item 1: member "alg"']],
        [['SADD', 'services:announced', ''], ['services:announced', 'services:announced', 'member "": empty']],
        [['SET', 'services:announced', 'x'], ['services:announced', 'services:announced', 'in Redis is string, the schema asks for set']],
        [['HSET', 'services:outra', 'a', '1'], ['services:outra', '-', 'key without family']],
        [['SET', 'users:index', 'x'], ['users:index', 'users:index', 'in Redis is string, the schema asks for set']],
        [['SADD', 'users:index', FANTASMA, ORFAO, SEM_HASH, 'f0f0f0f0-0000-4000-8000-000000000004', 'f0f0f0f0-0000-4000-8000-000000000005'], ['users:index', 'users:index', '6 members, above the limit of 5']],
        // Ids that are not lowercase UUIDs.
        [['SADD', 'users:index', 'user_1760000000000'], ['users:index', 'users:index', 'member "user_1760000000000": must be a UUID in the format of RFC 9562']],
        [['HSET', 'user:c3167a18-5dc5', 'id', 'c3167a18-5dc5'], ['user:c3167a18-5dc5', 'user:{id}', '{id} of the key: must be a UUID']],
        [['SADD', `user:${P1.toUpperCase()}:consent`, 'urn:s'], [`user:${P1.toUpperCase()}:consent`, 'user:{id}:consent', '{id} of the key: UUID must be in lowercase']],
        // Nil UUID, version and variant outside RFC 9562.
        [['SADD', 'users:index', '00000000-0000-0000-0000-000000000000'], ['users:index', 'users:index', 'member "00000000-0000-0000-0000-000000000000": nil UUID']],
        [['SADD', `user:${comDigito(P1, UUID_VERSAO, '9')}:consent`, 'urn:s'], [`user:${comDigito(P1, UUID_VERSAO, '9')}:consent`, 'user:{id}:consent', '{id} of the key: UUID version must be 1 to 8 (1st digit of the 3rd group; got 9']],
        [['HSET', `user:${comDigito(P1, UUID_VARIANTE, 'c')}`, 'id', comDigito(P1, UUID_VARIANTE, 'c')], [`user:${comDigito(P1, UUID_VARIANTE, 'c')}`, 'user:{id}', '{id} of the key: UUID variant must be the one of RFC 9562 (1st digit of the 4th group in 8, 9, a or b; got c']],
        // Invariants
        [['HSET', `user:${FANTASMA}`, 'id', FANTASMA, 'nickname', 'F', 'parentalControl', 'false', 'closedCaptioning', 'false', 'closedSigning', 'false', 'audioDescription', 'false', 'dialogEnhancement', 'false', 'voiceGuidance', 'false'],
            [`user:${FANTASMA}`, 'user:{id}', 'id outside users:index (invariant profile-indexed)']],
        [['SREM', 'users:index', P1], [`user:${P1}`, 'user:{id}', 'invariant profile-indexed']],
        [['SADD', 'users:index', SEM_HASH], ['users:index', 'users:index', `member "${SEM_HASH}" without the hash user:${SEM_HASH} (invariant profile-indexed)`]],
        [['SADD', `user:${ORFAO}:consent`, 'urn:s'], [`user:${ORFAO}:consent`, 'user:{id}:consent', `profile "${ORFAO}" outside users:index (invariant profile-dependents)`]],
        [['HSET', `user:${ORFAO}:broadcaster-attrs:urn:s`, 'a', '1'], [`user:${ORFAO}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'invariant profile-dependents']],
        [['SADD', 'clients:blocked', 'c1'], ['clients:authorized', 'clients:authorized', 'member "c1" also in clients:blocked (invariant disjoint-clients)']],
        [['SADD', 'clients:authorized', 'c9'], ['clients:authorized', 'clients:authorized', 'member "c9" without the hash client:c9 (invariant authorized-has-record)']],
    ];
    for (const [cmd, [chave, fam, trecho]] of manuais) {
        await r('FLUSHALL');
        // Restores the valid state and applies the manual write.
        for (const [k, [t, v]] of Object.entries(JSON.parse(valido))) {
            if (t === 'string') await r('SET', k, v);
            else if (t === 'hash') await r('HSET', k, ...v.flat());
            else if (t === 'set') await r('SADD', k, ...v);
            else if (t === 'list') await r('RPUSH', k, ...v);
        }
        assert.deepEqual(await auditoria(), [], 'estado valido restaurado');
        if (cmd[0] === 'SET' && cmd[1] === 'users:index') await r('DEL', 'users:index');
        if (cmd[0] === 'SET' && cmd[1] === 'session:current-service') await r('DEL', 'session:current-service');
        await r(...cmd);
        const achados = await auditoria();
        assert.ok(achados.some(([k, f, m]) => k === chave && f === fam && m.includes(trecho)),
            `${cmd.join(' ').slice(0, 60)}: esperado [${chave}, ${fam}, ...${trecho}...], veio ${JSON.stringify(achados)}`);
        for (const a of achados) assert.equal(a.length, 3, 'cada achado e [chave, familia, motivo]');
    }
    // Cleanup: back to empty.
    await r('FLUSHALL');
    assert.deepEqual(await auditoria(), []);
});

test('auditoria nao olha chaves fora do alcance (schema.json, foraDoAlcance)', async () => {
    await r('SADD', 'remote-devices:index', 'h1');
    await r('HSET', 'remote-device:h1', 'deviceClass', 'x');
    await r('SET', 'seed:done', '1');
    await r('SET', 'tv30:meu-componente:exemplo', 'v');
    assert.deepEqual(await auditoria(), []);
    for (const f of ESQ.outOfScope) {
        assert.ok(!ESQ.auditedPrefixes.some(p => f.key.startsWith(p)), `${f.key} fora dos prefixos auditados`);
    }
});
