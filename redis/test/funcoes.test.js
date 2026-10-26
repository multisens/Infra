// Tests of the "tv30" library on a real Redis (run by
// infra/redis/test/run.sh). The refusal cases are generated from
// esquema.json: each rule gets values that violate it, and the function must
// answer TV30ESQUEMA without writing anything. A new rule gets cases without
// editing this file, if the kind of rule is known.
'use strict';
const { test, before, after, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { connect } = require('./resp');

const ESQ = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'esquema.json'), 'utf8'));
const PREFIXO = ESQ.prefixoErro;

let r;
before(async () => { r = await connect(); });
after(async () => { if (r) await r.close(); });

const fcall = (nome, ...args) => r('FCALL', nome, 0, ...args);
const fcallRo = (nome, ...args) => r('FCALL_RO', nome, 0, ...args);
const plano = h => Object.entries(h).flat();
const auditoria = () => fcallRo('tv30_auditoria');

// ------------------------------------------------------------ JS schema --
function resolve(regra) {
    if (!regra || typeof regra !== 'object' || regra.ref === undefined) return regra;
    const base = ESQ.definicoes[regra.ref];
    assert.ok(base, `definicao ${regra.ref} existe`);
    const { ref, ...resto } = regra;
    return { ...base, ...resto };
}
const familia = nome => {
    const f = ESQ.familias.find(x => x.familia === nome);
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
const CLIENTE = { class: 'local-autonomous', refreshToken: 'rt-1', accessToken: 'at-1' };
const ENTRADA = { alg: 'HS256', key: 'segredo', registeredAt: 1760000000000 };
const ORIGEM = 'http://users-test.apps.localhost:8080';

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

// Refusal: error TV30ESQUEMA <function>: with the expected excerpts, and
// nothing written.
async function recusa(desc, nome, args, trechos = []) {
    const antes = await snapshot();
    let erro = null;
    try { await fcall(nome, ...args); } catch (e) { erro = e; }
    assert.ok(erro, `${desc}: esperada recusa de ${nome}, foi aceito`);
    assert.ok(erro.redis, `${desc}: erro do Redis (veio ${erro.message})`);
    assert.ok(erro.message.startsWith(`${PREFIXO} ${nome}: `), `${desc}: prefixo (veio ${erro.message})`);
    for (const t of trechos) assert.ok(erro.message.includes(t), `${desc}: mensagem com "${t}" (veio ${erro.message})`);
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
    const t = regra.tipo;
    if (emJson) {
        if (t === 'inteiro') {
            out.push(['12', 'numero inteiro no JSON', 'inteiro em texto JSON']);
            out.push([1.5, 'numero inteiro no JSON', 'fracionario']);
            if (regra.min !== undefined) out.push([regra.min - 1, `minimo ${regra.min}`, 'abaixo do minimo']);
            if (regra.max !== undefined) out.push([regra.max + 1, `maximo ${regra.max}`, 'acima do maximo']);
            return out;
        }
        out.push([5, 'texto no JSON', 'numero no lugar de texto']);
    }
    switch (t) {
        case 'texto':
            if (regra.min >= 1) out.push(['', 'vazio', 'vazio']);
            if (regra.max !== undefined) out.push(['é'.repeat(regra.max + 1), `maximo ${regra.max}`, `${regra.max + 1} caracteres multibyte`]);
            for (const c of regra.proibe || '') out.push([`a${c}b`, `nao pode conter "${c}"`, `com "${c}"`]);
            break;
        case 'booleano':
            for (const v of ['True', 'False', '1', '']) out.push([v, '"true" ou "false"', `"${v}"`]);
            break;
        case 'inteiro':
            for (const v of ['x', '1.5', '028', '-0', ' 1']) out.push([v, 'inteiro', `"${v}"`]);
            if (!regra.vazio) out.push(['', 'inteiro', 'vazio']);
            if (regra.min !== undefined) out.push([String(regra.min - 1), `minimo ${regra.min}`, 'abaixo do minimo']);
            if (regra.max !== undefined) out.push([String(regra.max + 1), `maximo ${regra.max}`, 'acima do maximo']);
            break;
        case 'enum':
            out.push(['nao-e-valor', 'deve ser um de', 'fora da lista']);
            if (regra.valores.some(v => v.toLowerCase() !== v)) out.push([regra.valores.find(v => v.toLowerCase() !== v).toLowerCase(), 'deve ser um de', 'caixa trocada']);
            else if (regra.valores.some(v => v.toUpperCase() !== v)) out.push([regra.valores.find(v => v.toUpperCase() !== v).toUpperCase(), 'deve ser um de', 'caixa trocada']);
            break;
        case 'idioma':
            for (const v of ['pt_BR', 'p', '', 'pt-', 'pt--BR', 'português', '123', 'pt-abcdefghi']) out.push([v, 'RFC 5646', `"${v}"`]);
            break;
        case 'uuid': {
            const ok = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
            for (const [v, d] of [
                ['', 'vazio'], ['c3167a18-5dc5', 'UUID truncado'], ['user_1760000000000', 'user_<epoch-ms>'],
                ['p1', 'curto'], [ok.slice(0, -1), 'um digito a menos'], [ok + '0', 'um digito a mais'],
                [ok.replace(/-/g, ''), 'sem hifens'], [`{${ok}}`, 'entre chaves'], [`urn:uuid:${ok}`, 'forma URN'],
                ['g' + ok.slice(1), 'digito nao hexadecimal'], [ok.replace(/-/g, '_'), 'separador errado'],
                [` ${ok}`, 'espaco antes'], [`${ok} `, 'espaco depois'], ['c3167a185-dc5-4a6e-9b1d-3f2a7c5e8d10', 'grupos 9-3'],
            ]) out.push([v, 'RFC 9562', d]);
            out.push([ok.toUpperCase(), 'minusculas', 'maiusculas']);
            out.push(['C' + ok.slice(1), 'minusculas', 'uma maiuscula']);
            // Version 1 to 8 and the variant of RFC 9562; the message gives the
            // part and the digit.
            for (const d of '09f') {
                out.push([comDigito(ok, UUID_VERSAO, d), ['versao do UUID deve ser de 1 a 8', `veio ${d} em`], `versao ${d}`]);
            }
            for (const d of '01234567cdef') {
                out.push([comDigito(ok, UUID_VARIANTE, d), ['variante do UUID deve ser a da RFC 9562', `veio ${d} em`], `variante ${d}`]);
            }
            // Both wrong: the version is checked first.
            out.push([comDigito(comDigito(ok, UUID_VERSAO, '0'), UUID_VARIANTE, 'c'), ['versao do UUID', 'veio 0 em'], 'versao 0 e variante c']);
            out.push(['00000000-0000-0000-0000-000000000000', ['UUID nulo', 'nao serve de id de perfil'], 'nulo (Nil UUID)']);
            out.push(['ffffffff-ffff-ffff-ffff-ffffffffffff', ['UUID maximo', 'nao serve de id de perfil'], 'maximo (Max UUID)']);
            // The case is checked first: a valid variant in uppercase (B).
            out.push([comDigito(ok, UUID_VARIANTE, 'B'), 'minusculas', 'variante B maiuscula']);
            break;
        }
        case 'nomeArquivo': {
            const ext = regra.extensoes[0];
            out.push(['', 'vazio', 'vazio']);
            out.push([SVG_EM_LINHA, 'SVG em linha nao serve', 'SVG em linha']);
            out.push(['<svg onload="alert(1)"/>', 'SVG em linha nao serve', 'SVG com script']);
            for (const v of ['thumbs/a.svg', '../a.svg', '/a.svg', '/user-files/thumbs/a.svg', 'a/../../etc/passwd.svg', 'a\\b.svg', '..\\a.svg', `a.${ext}/`]) {
                out.push([v, ['sem diretorio', 'nao pode conter "/" nem "\\"'], `"${v}"`]);
            }
            for (const v of ['a', 'a.', '..', 'a.SVG', 'a.Png', 'a.gif', 'a.svgz', 'a.jpe', 'a.svg.txt', `a.${ext} `, `a.${ext}\n`, 'svg', 'a_svg']) {
                out.push([v, ['extensao deve ser uma de', regra.extensoes.map(e => `.${e}`).join(', ')], `extensao: ${JSON.stringify(v)}`]);
            }
            for (const v of [`.${ext}`, `..${ext}`, `.oculto.${ext}`, `-a.${ext}`, `_a.${ext}`, `.a.${ext}`]) {
                out.push([v, 'deve comecar por letra ou digito', `comeco: "${v}"`]);
            }
            for (const v of [`a b.${ext}`, `avataré.${ext}`, `a:b.${ext}`, `a%2f.${ext}`, `a?.${ext}`, `a*.${ext}`, `a\u0000b.${ext}`, `a\tb.${ext}`, `a~.${ext}`, `a+b.${ext}`]) {
                out.push([v, 'so com letras sem acento, digitos', `caractere: ${JSON.stringify(v)}`]);
            }
            out.push(['a'.repeat(regra.maxNome + 1) + `.${ext}`, [`${regra.maxNome + 1} caracteres antes da extensao`, `maximo ${regra.maxNome}`], `${regra.maxNome + 1} caracteres antes da extensao`]);
            break;
        }
        case 'origem':
            for (const [v, m] of [
                ['ftp://x.example', 'http:// ou https://'], ['x.example', 'http:// ou https://'],
                ['http://X.example', 'minusculas'], ['http://x.example/', 'sem caminho'], ['http://x.example/app', 'sem caminho'],
                ['http://x.example:80', 'porta padrao'], ['https://x.example:443', 'porta padrao'],
                ['http://x.example:0', 'fora de 1 a 65535'], ['http://x.example:65536', 'fora de 1 a 65535'],
                ['http://x.example:080', 'zero a esquerda'], ['http://', 'http:// ou https://'],
            ]) out.push([v, m, `"${v}"`]);
            break;
        case 'json': {
            out.push(['nao-e-json', 'objeto JSON', 'texto que nao e JSON']);
            out.push(['[1,2]', 'objeto JSON', 'array JSON']);
            out.push(['"texto"', 'objeto JSON', 'string JSON']);
            const valido = jsonValido(regra);
            for (const [nome, r0] of Object.entries(regra.campos)) {
                const rr = resolve(r0);
                if (rr.obrigatorio) {
                    const o = { ...valido }; delete o[nome];
                    out.push([JSON.stringify(o), `membro "${nome}" obrigatorio`, `sem ${nome}`]);
                }
                for (const [v, m, d] of violacoes(rr, true)) {
                    out.push([JSON.stringify({ ...valido, [nome]: v }), [`membro "${nome}": `, m], `${nome}: ${d}`]);
                }
            }
            if (regra.outrosCampos === false) out.push([JSON.stringify({ ...valido, inventado: 1 }), 'membro "inventado" nao previsto', 'membro extra']);
            break;
        }
        default:
            throw new Error(`tipo de regra sem gerador de violacoes neste teste: ${t}`);
    }
    return out;
}

// Values that satisfy the rule, the boundaries included.
function aceitacoes(regra) {
    switch (regra.tipo) {
        case 'texto': {
            const out = [];
            if (regra.max !== undefined) out.push('é'.repeat(regra.max));
            out.push('a'.repeat(Math.max(regra.min || 0, 1)));
            if (!regra.min) out.push('');
            return out;
        }
        case 'booleano': return ['true', 'false'];
        case 'inteiro': {
            const out = ['0' === String(regra.min) ? '0' : String(regra.min ?? 7)];
            if (regra.max !== undefined) out.push(String(regra.max));
            if (regra.vazio) out.push('');
            if (regra.min === undefined) out.push('-3');
            return out;
        }
        case 'enum': return regra.valores.slice();
        case 'idioma': return ['pt-BR', 'en', 'pt', 'zh-Hant-TW', 'x-tv30', 'i-default', 'es-419'];
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
        case 'nomeArquivo': return [
            'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10.svg', `${require('crypto').randomUUID()}.svg`,
            ...regra.extensoes.map(e => `a.${e}`), 'Z.png', '0.jpg',
            'a'.repeat(regra.maxNome) + '.' + regra.extensoes[0], 'Avatar_1-x.webp', 'a.png.svg', 'a..b.svg', 'a-.jpeg', 'a_.svg', 'a.b.c.png',
        ];
        case 'origem': return ['http://users-test.apps.localhost:8080', 'http://a.apps.localhost', 'https://x.example', 'https://x.example:8443', 'http://[::1]:8099', 'http://127.0.0.1:65535'];
        case 'json': return ['HS256', 'HS512', 'RS256', 'RS512'].map(alg => JSON.stringify({ ...jsonValido(regra), alg }));
        default: throw new Error(`tipo de regra sem gerador de aceitacoes neste teste: ${regra.tipo}`);
    }
}

function jsonValido(regra) {
    assert.ok(regra.campos.alg && regra.campos.key && regra.campos.registeredAt, 'o gerador de JSON so conhece a entrada de bind-context');
    return { ...ENTRADA };
}

// --------------------------------------------- calls assembled per slot --
// For each family: the "slots" (places where a rule applies) and how to
// assemble a valid call with the value of the slot replaced. prep runs
// first.
const indexaP1 = () => fcall('tv30_perfil_grava', ...perfilArgs(P1, [], PERFIL));
const SLOTS = {
    'session:current-user': [{ rotulo: 'valor', regra: f => f.valor, chamada: v => ['tv30_sessao_usuario', [v]], trecho: 'valor:' }],
    'session:current-service-id': [{ rotulo: 'valor', regra: f => f.valor, chamada: v => ['tv30_sessao_servico_id', [v]], trecho: 'valor:' }],
    'session:current-service': campoSlots(campo => v => ['tv30_sessao_servico', plano({ ...SVC, [campo]: v })]),
    'users:index': [{ rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_perfil_grava', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} da chave:' }],
    'user:{id}': [
        { rotulo: '{id}', regra: f => f.partes.id, chamada: v => ['tv30_perfil_grava', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} da chave:' },
        // The id field has the same rule as the {id} part (slot above) and must
        // equal it (own test below): left out of the per-field slots.
        ...campoSlots(campo => v => ['tv30_perfil_grava', perfilArgs(P1, [], { ...PERFIL, [campo]: v })], ['id']),
    ],
    'user:{id}:consent': [
        { rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_perfil_grava', perfilArgs(P1, ['urn:tv30:service:ok', v], PERFIL)], trecho: 'membro' },
        { rotulo: '{id}', regra: f => f.partes.id, chamada: v => ['tv30_perfil_grava', perfilArgs(v, ['urn:x'], { ...PERFIL, id: v })], trecho: '{id} da chave:' },
    ],
    'user:{id}:broadcaster-attrs:{scid}': [
        // The profile of the value must exist for the acceptance; with an
        // invalid value the creation fails and the refusal comes from the {id}
        // part of the key.
        { rotulo: '{id}', regra: f => f.partes.id, prep: v => fcall('tv30_perfil_grava', ...perfilArgs(v, [], { ...PERFIL, id: v })).catch(() => undefined), chamada: v => ['tv30_atributos_emissora', [v, 'urn:s:1', 'cor', 'azul']], trecho: '{id} da chave:' },
        { rotulo: '{scid}', regra: f => f.partes.scid, prep: indexaP1, chamada: v => ['tv30_atributos_emissora', [P1, v, 'cor', 'azul']], trecho: '{scid} da chave:' },
        { rotulo: 'nome de campo', regra: f => f.outrosCampos.nome, prep: indexaP1, chamada: v => ['tv30_atributos_emissora', [P1, 'urn:s:1', v, 'azul']], trecho: 'nome de campo:' },
        // outrosCampos.valor (text, min 1): empty REMOVES the field in the
        // function, so no writable value violates the rule; the audit covers it
        // (below).
    ],
    'client:{id}': [
        { rotulo: '{id}', regra: f => f.partes.id, chamada: v => ['tv30_cliente_grava', [v, ...plano(CLIENTE)]], trecho: '{id} da chave:' },
        ...campoSlots(campo => v => ['tv30_cliente_grava', ['c1', ...plano({ ...CLIENTE, [campo]: v })]]),
    ],
    'clients:authorized': [{ rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_cliente_autoriza', [v, ...plano(CLIENTE)]], trecho: '{id} da chave:' }],
    'clients:blocked': [{ rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_cliente_bloqueia', [v]], trecho: '{id} da chave:' }],
    'origins:associated': [
        { rotulo: 'nome de campo', regra: f => f.outrosCampos.nome, chamada: v => ['tv30_origem_registra', [v, 'urn:tv30:service:users-test']], trecho: 'nome de campo:' },
        { rotulo: 'valor', regra: f => f.outrosCampos.valor, chamada: v => ['tv30_origem_registra', [ORIGEM, v]], trecho: 'campo "' },
    ],
    'services:announced': [{ rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_servicos_anunciados', ['urn:tv30:service:ok', v]], trecho: 'services:announced membro' }],
    'bind-context:{serviceId}': [
        { rotulo: '{serviceId}', regra: f => f.partes.serviceId, chamada: v => ['tv30_bind_registra', [v, JSON.stringify(ENTRADA)]], trecho: '{serviceId} da chave:' },
        { rotulo: 'entrada', regra: f => f.itens, chamada: v => ['tv30_bind_registra', ['urn:tv30:service:a', v]], trecho: 'entrada:' },
    ],
};
// One slot per declared field; the family comes when the cases are
// generated.
function campoSlots(montar, exceto = []) {
    return [{ porCampo: true, montar, exceto }];
}
function expandeSlots(f) {
    const out = [];
    for (const s of SLOTS[f.familia] || []) {
        if (!s.porCampo) { out.push(s); continue; }
        for (const campo of Object.keys(f.campos).filter(c => !s.exceto.includes(c))) {
            out.push({ rotulo: `campo ${campo}`, regra: ff => ff.campos[campo], chamada: s.montar(campo), trecho: `campo ${campo}:` });
        }
    }
    return out;
}

// ------------------------------------------------------------------ load --
test('biblioteca carregada: exatamente as funcoes do esquema.json, flags certas', async () => {
    const lista = await r('FUNCTION', 'LIST', 'LIBRARYNAME', ESQ.biblioteca);
    assert.equal(lista.length, 1, 'uma biblioteca tv30');
    const lib = {};
    for (let i = 0; i < lista[0].length; i += 2) lib[lista[0][i]] = lista[0][i + 1];
    assert.equal(lib.library_name, ESQ.biblioteca);
    const carregadas = {};
    for (const fn of lib.functions) {
        const o = {};
        for (let i = 0; i < fn.length; i += 2) o[fn[i]] = fn[i + 1];
        carregadas[o.name] = o.flags;
    }
    assert.deepEqual(Object.keys(carregadas).sort(), ESQ.funcoes.map(f => f.nome).sort());
    for (const f of ESQ.funcoes) {
        const flags = carregadas[f.nome];
        assert.ok(flags.includes('no-cluster'), `${f.nome}: no-cluster`);
        assert.equal(flags.includes('no-writes'), !!f.somenteLeitura, `${f.nome}: no-writes so nas somente leitura`);
    }
    // Each function named in a family exists; each family has a writer.
    const nomes = new Set(ESQ.funcoes.map(f => f.nome));
    for (const fam of ESQ.familias) {
        assert.ok(fam.funcoes.length > 0, `${fam.familia}: funcoes`);
        for (const n of fam.funcoes) assert.ok(nomes.has(n), `${fam.familia} cita ${n}`);
        assert.ok(SLOTS[fam.familia], `${fam.familia}: o teste de recusa conhece a familia`);
    }
    assert.equal(await fcallRo('tv30_versao'), ESQ.versao);
});

test('FCALL_RO so roda as somente leitura; numkeys > 0 e argumentos errados sao recusados', async () => {
    await assert.rejects(fcallRo('tv30_sessao_usuario', 'u'), /write flag/);
    await assert.rejects(r('FCALL', 'tv30_sessao_usuario', 1, 'session:current-user', 'u'), e => e.message.startsWith(`${PREFIXO} tv30_sessao_usuario: chamada com numkeys 1`));
    await recusa('sem argumento', 'tv30_sessao_usuario', [], ['uso:']);
    await recusa('pares incompletos', 'tv30_sessao_servico', ['serviceContextId'], ['pares campo valor incompletos']);
    await recusa('n invalido', 'tv30_perfil_grava', [P1, 'x', 'id', P1], ['n=']);
    await recusa('n alem dos argumentos', 'tv30_perfil_grava', [P1, '9', 'a'], ['n=']);
    await assert.rejects(fcallRo('tv30_auditoria', 'x'), new RegExp(`${PREFIXO} tv30_auditoria: uso:`));
});

// --------------------------------------- refusal of each rule (generated) --
for (const fam of ESQ.familias) {
    test(`recusa de cada regra: ${fam.familia}`, async (t) => {
        let casos = 0;
        let aceitos = 0;
        for (const slot of expandeSlots(fam)) {
            const regra = resolve(slot.regra(fam));
            for (const [valor, trecho, desc] of violacoes(regra)) {
                await limpo();
                if (slot.prep) await slot.prep(valor);
                const [nome, args] = slot.chamada(valor);
                await recusa(`${fam.familia} ${slot.rotulo} ${desc}`, nome, args, [slot.trecho, ...[].concat(trecho)]);
                casos++;
            }
            for (const valor of aceitacoes(regra)) {
                await limpo();
                if (slot.prep) await slot.prep(valor);
                const [nome, args] = slot.chamada(valor);
                await assert.doesNotReject(fcall(nome, ...args), `${fam.familia} ${slot.rotulo} aceita ${JSON.stringify(valor).slice(0, 60)}`);
                assert.deepEqual(await auditoria(), [], `${fam.familia} ${slot.rotulo}: auditoria vazia depois de gravar ${JSON.stringify(valor).slice(0, 40)}`);
                aceitos++;
            }
        }
        // Required, conditional and unexpected fields.
        if (fam.campos) {
            const [base, chamar] = {
                'session:current-service': [SVC, h => ['tv30_sessao_servico', plano(h)]],
                'user:{id}': [PERFIL, h => ['tv30_perfil_grava', perfilArgs(P1, [], h)]],
                'client:{id}': [CLIENTE, h => ['tv30_cliente_grava', ['c1', ...plano(h)]]],
            }[fam.familia];
            for (const [campo, r0] of Object.entries(fam.campos)) {
                const regra = resolve(r0);
                const sem = { ...base }; delete sem[campo];
                await limpo();
                const [nome, args] = chamar(sem);
                if (regra.obrigatorio) {
                    await recusa(`${fam.familia} sem ${campo}`, nome, args, [`campo ${campo}: obrigatorio, ausente`]);
                    casos++;
                } else if (regra.obrigatorioSe) {
                    const c = regra.obrigatorioSe;
                    assert.equal(base[c.campo], c.valor, `base do teste com ${c.campo}=${c.valor}`);
                    await recusa(`${fam.familia} sem ${campo} com ${c.campo}=${c.valor}`, nome, args, [`campo ${campo}: obrigatorio quando ${c.campo}=${c.valor}`]);
                    casos++;
                } else {
                    await assert.doesNotReject(fcall(nome, ...args), `${fam.familia}: ${campo} e opcional`);
                }
            }
            if (fam.outrosCampos === false) {
                await limpo();
                const [nome, args] = chamar({ ...base, inventado: 'x' });
                await recusa(`${fam.familia} campo extra`, nome, args, ['campo "inventado": nao previsto no esquema']);
                casos++;
            }
        }
        assert.ok(casos > 0, `${fam.familia}: algum caso de recusa`);
        t.diagnostic(`${fam.familia}: ${casos} recusa(s) e ${aceitos} aceitacao(oes) de fronteira`);
    });
}

test('user:{id}: condicionais falsos dispensam os campos; id do hash igual ao da chave', async () => {
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, [], PERFIL_MIN));
    assert.deepEqual(await auditoria(), []);
    await recusa('id diferente', 'tv30_perfil_grava', perfilArgs(P1, [], { ...PERFIL, id: P2 }), [`user:${P1} campo id: deve ser igual ao {id} da chave`]);
});

test('broadcaster-attrs: atributos basicos proibidos e teto de 20 MB no resultado', async () => {
    const f = familia('user:{id}:broadcaster-attrs:{scid}');
    await indexaP1();
    for (const nome of f.camposProibidos.nomes) {
        await recusa(`redefinir ${nome}`, 'tv30_atributos_emissora', [P1, 'urn:s:1', nome, 'x'], [`campo ${nome}: atributo basico`]);
        await recusa(`remover ${nome}`, 'tv30_atributos_emissora', [P1, 'urn:s:1', nome, ''], [`campo ${nome}: atributo basico`]);
    }
    // Exactly at the limit: accepted; one byte above: refused (sum of names
    // and values).
    const nome = 'grande';
    await fcall('tv30_atributos_emissora', P1, 'urn:s:1', nome, 'x'.repeat(f.maxBytes - nome.length));
    await recusa('acima do teto', 'tv30_atributos_emissora', [P1, 'urn:s:1', 'b', 'y'], [`acima do teto de ${f.maxBytes}`]);
    // Removing frees room.
    assert.equal(await fcall('tv30_atributos_emissora', P1, 'urn:s:1', nome, '', 'b', 'y'), 2);
    assert.deepEqual(await r('HGETALL', `user:${P1}:broadcaster-attrs:urn:s:1`), ['b', 'y']);
});

// ---------------------------------------------------- effects (acceptance) --
test('sessao: SET dos dois espelhos (vazio vale) e o hash regravado inteiro', async () => {
    assert.equal(await fcall('tv30_sessao_usuario', P1), 1);
    assert.equal(await r('GET', 'session:current-user'), P1);
    await fcall('tv30_sessao_usuario', '');
    assert.equal(await r('GET', 'session:current-user'), '');
    await fcall('tv30_sessao_servico_id', 'urn:tv30:service:users-test');
    assert.equal(await r('GET', 'session:current-service-id'), 'urn:tv30:service:users-test');
    await fcall('tv30_sessao_servico', ...plano({ ...SVC, serviceId: '101', serviceName: 'Canal' }));
    await fcall('tv30_sessao_servico', ...plano(SVC));
    const h = await r('HGETALL', 'session:current-service');
    assert.equal(h.length, 10);
    assert.equal(h[h.indexOf('serviceId') + 1], '');
    assert.deepEqual(await auditoria(), []);
});

test('perfil: grava os tres juntos, visibilidade acumula, ultimo acesso so em perfil indexado, remocao leva tudo', async () => {
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, ['urn:a', 'urn:b'], PERFIL_MIN));
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, ['urn:c'], { ...PERFIL_MIN, nickname: 'Outro' }));
    assert.deepEqual((await r('SMEMBERS', `user:${P1}:consent`)).sort(), ['urn:a', 'urn:b', 'urn:c']);
    assert.equal(await r('HGET', `user:${P1}`, 'nickname'), 'Outro');
    assert.deepEqual(await r('SMEMBERS', 'users:index'), [P1]);

    assert.equal(await fcall('tv30_perfil_ultimo_acesso', P1, '1234'), 1);
    assert.equal(await r('HGET', `user:${P1}`, 'lastAccess'), '1234');
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'desconhecido', '1'), 0);
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', P2, '1'), 0, 'UUID valido fora de users:index: 0');
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'a:b', '1'), 0, 'id que nunca seria perfil: 0, sem erro (espelho de topico)');
    assert.equal(await r('EXISTS', 'user:desconhecido'), 0, 'sem hash fantasma');
    assert.equal(await r('EXISTS', `user:${P2}`), 0, 'sem hash fantasma');
    await recusa('instante invalido', 'tv30_perfil_ultimo_acesso', [P1, 'ontem'], ['campo lastAccess:']);

    await fcall('tv30_perfil_grava', ...perfilArgs(P2, [], { ...PERFIL_MIN, id: P2 }));
    await fcall('tv30_atributos_emissora', P1, 'urn:tv30:service:x', 'cor', 'azul');
    await fcall('tv30_atributos_emissora', P1, 'urn:y', 'k', 'v');
    await fcall('tv30_atributos_emissora', P2, 'urn:y', 'k', 'v');
    assert.deepEqual(await auditoria(), []);
    // 1 (SREM) + user:{P1} + consent + 2 broadcaster-attrs
    assert.equal(await fcall('tv30_perfil_remove', P1), 5);
    assert.deepEqual((await r('KEYS', `user:${P1}*`)), []);
    assert.equal(await r('EXISTS', `user:${P2}:broadcaster-attrs:urn:y`), 1, 'remover P1 nao alcanca P2');
    // The glob of the id is escaped: an id with a wildcard (never a UUID, so
    // never a profile) that, unescaped, would match P2 does not reach P2.
    assert.equal(await fcall('tv30_perfil_remove', P2.slice(0, 9) + '*'), 0);
    assert.equal(await r('EXISTS', `user:${P2}:broadcaster-attrs:urn:y`), 1);
    assert.equal(await r('EXISTS', `user:${P2}`), 1);
    assert.equal(await fcall('tv30_perfil_remove', P2), 3);
    assert.equal(await fcall('tv30_perfil_remove', 'nao-existe'), 0);
    assert.deepEqual(await auditoria(), []);
});

// The limit check is the function's own: no read before the call.
test('perfil: teto de users:index na gravacao; id novo alem dele recusado, regravar existente aceito, remover libera', async () => {
    const f = familia('users:index');
    assert.ok(Number.isInteger(f.maxMembros) && f.maxMembros > 0, 'teto declarado no esquema');
    assert.ok(f.maxMembrosFonte, 'fonte do teto');
    const ids = Array.from({ length: f.maxMembros }, (_, i) => `0b6e3c1a-7f2d-4e8b-9a5c-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids) await fcall('tv30_perfil_grava', ...perfilArgs(id, ['urn:a'], { ...PERFIL_MIN, id }));
    assert.equal(await r('SCARD', 'users:index'), f.maxMembros);

    const novo = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    const msg = await recusa('id novo com o conjunto no teto', 'tv30_perfil_grava', perfilArgs(novo, ['urn:b'], { ...PERFIL_MIN, id: novo }),
        [`users:index teto de ${f.maxMembros} perfis atingido (${f.maxMembrosFonte})`, novo]);
    assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    assert.equal(await r('EXISTS', `user:${novo}`), 0);

    // Rewriting one of those at the limit: accepted, with a set of the same
    // size.
    assert.equal(await fcall('tv30_perfil_grava', ...perfilArgs(ids[2], ['urn:c'], { ...PERFIL_MIN, id: ids[2], nickname: 'Regravado' })), 1);
    assert.equal(await r('HGET', `user:${ids[2]}`, 'nickname'), 'Regravado');
    assert.deepEqual((await r('SMEMBERS', `user:${ids[2]}:consent`)).sort(), ['urn:a', 'urn:c']);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembros);

    // With one removed, the new one goes in; and the next one is refused
    // again.
    await fcall('tv30_perfil_remove', ids[0]);
    assert.equal(await fcall('tv30_perfil_grava', ...perfilArgs(novo, [], { ...PERFIL_MIN, id: novo })), 1);
    assert.equal(await r('SCARD', 'users:index'), f.maxMembros);
    await recusa('de novo no teto', 'tv30_perfil_grava', perfilArgs(ids[0], [], { ...PERFIL_MIN, id: ids[0] }), ['teto de']);
    assert.deepEqual(await auditoria(), []);
});

test('perfil: teto conta o que esta no Redis, inclusive membro gravado a mao', async () => {
    const f = familia('users:index');
    const ids = Array.from({ length: f.maxMembros + 1 }, (_, i) => `5a9d2f4e-1c3b-4d6a-8e7f-${String(i + 1).padStart(12, '0')}`);
    for (const id of ids.slice(0, f.maxMembros)) await fcall('tv30_perfil_grava', ...perfilArgs(id, [], { ...PERFIL_MIN, id }));
    // One more, outside the functions: the audit reports the excess.
    await r('SADD', 'users:index', ids[f.maxMembros]);
    await r('HSET', `user:${ids[f.maxMembros]}`, ...plano({ ...PERFIL_MIN, id: ids[f.maxMembros] }));
    const achados = await auditoria();
    assert.deepEqual(achados, [['users:index', 'users:index', `${f.maxMembros + 1} membros, acima do teto de ${f.maxMembros} (${f.maxMembrosFonte})`]]);
    // Above the limit: an existing one can still be rewritten; a new id cannot.
    assert.equal(await fcall('tv30_perfil_grava', ...perfilArgs(ids[0], [], { ...PERFIL_MIN, id: ids[0], nickname: 'Ainda' })), 1);
    const novo = 'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10';
    await recusa('id novo acima do teto', 'tv30_perfil_grava', perfilArgs(novo, [], { ...PERFIL_MIN, id: novo }), [`teto de ${f.maxMembros} perfis`]);
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
    await recusa('regravar o perfil', 'tv30_perfil_grava', perfilArgs(velho, [], { ...PERFIL_MIN, id: velho }), ['{id} da chave: deve ser UUID']);
    await recusa('atributo de emissora no perfil', 'tv30_atributos_emissora', [velho, 'urn:s', 'cor', 'azul'], ['{id} da chave: deve ser UUID']);
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', velho, '5'), 1);
    assert.equal(await fcall('tv30_perfil_remove', velho), 3);
    assert.deepEqual(await auditoria(), []);
});

// Replacing the avatar with the file name, or removing it, empties the
// audit.
test('avatar em SVG em linha, gravado a mao: auditoria aponta, regravacao recusada, o resto segue', async () => {
    await r('SADD', 'users:index', P1);
    await r('HSET', `user:${P1}`, ...plano({ ...PERFIL_MIN, avatar: SVG_EM_LINHA }));
    const achados = await auditoria();
    assert.deepEqual(achados.map(([k, f]) => [k, f]), [[`user:${P1}`, 'user:{id}']], JSON.stringify(achados));
    assert.ok(achados[0][2].startsWith('campo avatar: SVG em linha nao serve'), achados[0][2]);
    assert.ok(!achados[0][2].includes('\n'), 'motivo numa linha so');
    await recusa('regravar com o SVG', 'tv30_perfil_grava', perfilArgs(P1, [], { ...PERFIL_MIN, avatar: SVG_EM_LINHA }), ['campo avatar: SVG em linha nao serve']);
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', P1, '7'), 1);
    assert.equal(await fcall('tv30_atributos_emissora', P1, 'urn:s', 'cor', 'azul'), 1);
    await r('HSET', `user:${P1}`, 'avatar', `${P1}.svg`);
    assert.deepEqual(await auditoria(), []);
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, [], PERFIL_MIN));
    assert.equal(await r('HEXISTS', `user:${P1}`, 'avatar'), 0);
    assert.deepEqual(await auditoria(), []);
    // 1 (SREM) + user:{P1} + 1 broadcaster-attrs (no consent)
    assert.equal(await fcall('tv30_perfil_remove', P1), 3);
});

// Catches a deviation between the schema and the expression of the contract
// that the cases generated from the schema itself do not catch (an extension
// removed from the list, maxNome changed).
const AVATAR_CONTRATO = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$/;
test('avatar: a regra do esquema decide igual a expressao do contrato', async (t) => {
    const regra = resolve(familia('user:{id}').campos.avatar);
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
        try { await fcall('tv30_perfil_grava', ...perfilArgs(P1, [], { ...PERFIL_MIN, avatar: v })); } catch (e) { erro = e; }
        const esperado = AVATAR_CONTRATO.test(v);
        assert.equal(erro === null, esperado, `${JSON.stringify(v)}: o contrato ${esperado ? 'aceita' : 'recusa'}, a funcao ${erro ? 'recusou: ' + erro.message : 'aceitou'}`);
        if (erro) assert.ok(erro.message.startsWith(`${PREFIXO} tv30_perfil_grava: user:${P1} campo avatar: `), erro.message);
        if (esperado) aceitos++;
    }
    assert.ok(aceitos >= 50 && aceitos < corpus.size - 50, `${aceitos} aceitos de ${corpus.size}`);
    t.diagnostic(`avatar: ${corpus.size} valores, ${aceitos} aceitos pela expressao do contrato e pela funcao`);
});

test('UUID estrito: id da carga e crypto.randomUUID() do AoP aceitos; nulo e maximo recusados', async () => {
    const ids = ['c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10', ...Array.from({ length: 20 }, () => require('crypto').randomUUID())];
    for (const id of ids) {
        await limpo();
        await fcall('tv30_perfil_grava', ...perfilArgs(id, ['urn:s'], { ...PERFIL_MIN, id }));
        assert.deepEqual(await r('SMEMBERS', 'users:index'), [id], id);
        assert.deepEqual(await auditoria(), [], id);
    }
    for (const [id, m] of [['00000000-0000-0000-0000-000000000000', 'UUID nulo'], ['ffffffff-ffff-ffff-ffff-ffffffffffff', 'UUID maximo']]) {
        await limpo();
        const msg = await recusa(m, 'tv30_perfil_grava', perfilArgs(id, [], { ...PERFIL_MIN, id }), [`{id} da chave: ${m}`, 'nao serve de id de perfil']);
        assert.ok(!msg.includes('\n'), 'mensagem numa linha so');
    }
});

test('atributos de emissora: perfil precisa estar indexado; vazio remove', async () => {
    await recusa('perfil inexistente', 'tv30_atributos_emissora', [P1, 'urn:s:1', 'cor', 'azul'], ['fora de users:index (invariante perfil-dependentes)']);
    await indexaP1();
    assert.equal(await fcall('tv30_atributos_emissora', P1, 'urn:s:1', 'cor', 'azul', 'tam', 'G'), 2);
    assert.equal(await fcall('tv30_atributos_emissora', P1, 'urn:s:1', 'cor', ''), 1);
    assert.deepEqual(await r('HGETALL', `user:${P1}:broadcaster-attrs:urn:s:1`), ['tam', 'G']);
});

test('clientes: autorizar e bloquear trocam o id de conjunto (disjuntos); grava acrescenta; inclusao no boot', async () => {
    await fcall('tv30_cliente_bloqueia', 'c1');
    await fcall('tv30_cliente_autoriza', 'c1', 'class', 'local-autonomous', 'refreshToken', 'r1');
    assert.deepEqual(await r('SMEMBERS', 'clients:authorized'), ['c1']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), []);
    await fcall('tv30_cliente_grava', 'c1', 'accessToken', 'a1');
    await fcall('tv30_cliente_grava', 'c1', 'refreshToken', 'r2');
    assert.deepEqual((await r('HGETALL', 'client:c1')), ['class', 'local-autonomous', 'refreshToken', 'r2', 'accessToken', 'a1']);
    await fcall('tv30_cliente_bloqueia', 'c1');
    assert.deepEqual(await r('SMEMBERS', 'clients:authorized'), []);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1']);
    assert.equal(await r('EXISTS', 'client:c1'), 1, 'o registro fica depois do bloqueio');
    assert.deepEqual(await auditoria(), []);

    // Inclusion on boot: a blocked client does not go in; without a record it
    // is refused; with a record it goes in.
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'c1'), 0);
    await recusa('sem client:{id}', 'tv30_cliente_inclui_autorizado', ['c9'], ['invariante autorizado-tem-registro']);
    await r('HSET', 'client:velho', 'class', 'local-autonomous', 'refreshToken', 'r');
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'velho'), 1);
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'velho'), 0);
    assert.deepEqual(await auditoria(), []);
});

test('registro existente: campo novo conferido, o resto ja gravado nao trava (a auditoria mostra)', async () => {
    // Boolean "False" written by hand, outside the schema.
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, [], PERFIL_MIN));
    await r('HSET', `user:${P1}`, 'parentalControl', 'False');
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', P1, '99'), 1);
    assert.deepEqual(await auditoria(), [[`user:${P1}`, 'user:{id}', 'campo parentalControl: deve ser "true" ou "false" (veio "False")']]);
    // A new client checks everything; an existing one only what is written.
    await recusa('cliente novo incompleto', 'tv30_cliente_grava', ['c1', 'accessToken', 'a'], ['campo class: obrigatorio, ausente']);
});

test('origens: registra, remove, limpa', async () => {
    await fcall('tv30_origem_registra', ORIGEM, 'urn:tv30:service:users-test');
    await fcall('tv30_origem_registra', 'http://uff.apps.localhost:8080', 'current-service');
    assert.equal(await fcall('tv30_origem_remove', ORIGEM), 1);
    assert.equal(await fcall('tv30_origem_remove', ORIGEM), 0);
    assert.deepEqual(await r('HGETALL', 'origins:associated'), ['http://uff.apps.localhost:8080', 'current-service']);
    assert.equal(await fcall('tv30_origens_limpa'), 1);
    assert.equal(await fcall('tv30_origens_limpa'), 0);
    assert.equal(await fcall('tv30_origem_remove', ORIGEM), 0);
});

test('servicos anunciados: o conjunto regravado inteiro; repetido conta uma vez; sem argumento, apaga', async () => {
    assert.equal(await fcall('tv30_servicos_anunciados', 'urn:tv30:service:a', 'urn:tv30:service:b', 'urn:tv30:service:a'), 2);
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), ['urn:tv30:service:a', 'urn:tv30:service:b']);
    // What left the BAMT leaves the set.
    assert.equal(await fcall('tv30_servicos_anunciados', 'urn:tv30:service:b', 'urn:tv30:service:c'), 2);
    assert.deepEqual((await r('SMEMBERS', 'services:announced')).sort(), ['urn:tv30:service:b', 'urn:tv30:service:c']);
    assert.deepEqual(await auditoria(), []);
    assert.equal(await fcall('tv30_servicos_anunciados'), 0);
    assert.equal(await r('EXISTS', 'services:announced'), 0);
    assert.equal(await fcall('tv30_servicos_anunciados'), 0);
    // More services than one SADD batch.
    const muitos = Array.from({ length: 1000 }, (_, i) => `urn:tv30:service:s${i}`);
    assert.equal(await fcall('tv30_servicos_anunciados', ...muitos), 1000);
    assert.deepEqual(await auditoria(), []);
});

test('bind-context: RPUSH da entrada exata, LREM de todas as iguais', async () => {
    const e1 = JSON.stringify(ENTRADA);
    const e2 = JSON.stringify({ alg: 'RS256', key: '-----BEGIN PUBLIC KEY-----\nMFww\n-----END PUBLIC KEY-----', registeredAt: 1 });
    assert.equal(await fcall('tv30_bind_registra', 'urn:tv30:service:a', e1), 1);
    assert.equal(await fcall('tv30_bind_registra', 'urn:tv30:service:a', e2), 2);
    assert.equal(await fcall('tv30_bind_registra', 'urn:tv30:service:a', e1), 3);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e1, e2, e1]);
    assert.deepEqual(await auditoria(), []);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', e1), 2);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:a', 'nao-tem'), 0);
    assert.equal(await fcall('tv30_bind_remove', 'urn:tv30:service:b', e1), 0);
    assert.deepEqual(await r('LRANGE', 'bind-context:urn:tv30:service:a', 0, -1), [e2]);
});

// ------------------------------------------------------------- atomicity --
test('atomicidade: tipo errado numa chave posterior ou membro invalido no fim nao deixam gravacao parcial', async () => {
    // Profile: users:index of another type, so user:{id} is not created.
    await r('SET', 'users:index', 'x');
    await recusa('users:index STRING', 'tv30_perfil_grava', perfilArgs(P1, [], PERFIL), ['users:index no Redis e string, o esquema pede set']);
    await limpo();
    // Profile: consent of another type (the last key written).
    await r('SET', `user:${P1}:consent`, 'x');
    await recusa('consent STRING', 'tv30_perfil_grava', perfilArgs(P1, ['urn:a'], PERFIL), [`user:${P1}:consent no Redis e string`]);
    await limpo();
    // Profile: the last invalid service brings the whole operation down.
    await recusa('ultimo servico vazio', 'tv30_perfil_grava', perfilArgs(P1, ['urn:a', 'urn:b', ''], PERFIL), [`user:${P1}:consent membro`]);
    // Rewriting an existing profile with invalid data keeps the previous one
    // intact.
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, ['urn:a'], PERFIL));
    await recusa('regravacao invalida', 'tv30_perfil_grava', perfilArgs(P1, ['urn:b'], { ...PERFIL, nickname: '' }), ['campo nickname:']);
    // Client: clients:blocked of another type, so neither client:{id} nor
    // clients:authorized.
    await limpo();
    await r('LPUSH', 'clients:blocked', 'x');
    await recusa('clients:blocked LIST', 'tv30_cliente_autoriza', ['c1', ...plano(CLIENTE)], ['clients:blocked no Redis e list']);
    await limpo();
    await r('SADD', 'clients:blocked', 'c1');
    await recusa('autorizar com classe invalida', 'tv30_cliente_autoriza', ['c1', ...plano({ ...CLIENTE, class: 'outra' })], ['campo class:']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1'], 'continua bloqueado');
    // Session: the previous hash intact.
    await limpo();
    await fcall('tv30_sessao_servico', ...plano({ ...SVC, serviceName: 'Antes' }));
    await recusa('servico invalido', 'tv30_sessao_servico', plano({ ...SVC, serviceId: 'x' }), ['campo serviceId:']);
    // Attributes: a forbidden name among several valid ones.
    await limpo();
    await indexaP1();
    await recusa('um proibido entre validos', 'tv30_atributos_emissora', [P1, 'urn:s', 'a', '1', 'nickname', 'x', 'z', '2'], ['campo nickname:']);
    // bind-context of another type.
    await limpo();
    await r('SET', 'bind-context:urn:a', 'x');
    await recusa('bind-context STRING', 'tv30_bind_registra', ['urn:a', JSON.stringify(ENTRADA)], ['no Redis e string, o esquema pede list']);
    await recusa('bind remove em STRING', 'tv30_bind_remove', ['urn:a', 'x'], ['no Redis e string']);
    // Announced services: the previous set intact.
    await limpo();
    await fcall('tv30_servicos_anunciados', 'urn:antes');
    await recusa('ultimo servico vazio', 'tv30_servicos_anunciados', ['urn:a', 'urn:b', ''], ['services:announced membro "": vazio']);
    await recusa('servico longo demais', 'tv30_servicos_anunciados', ['urn:a', 'x'.repeat(257)], ['maximo 256']);
    await limpo();
    await r('SET', 'services:announced', 'x');
    await recusa('services:announced STRING', 'tv30_servicos_anunciados', ['urn:a'], ['services:announced no Redis e string, o esquema pede set']);
    await recusa('services:announced STRING, sem argumento', 'tv30_servicos_anunciados', [], ['no Redis e string']);
});

// ----------------------------------------------------------------- audit --
test('auditoria: cada tipo de problema gravado a mao aparece com chave, familia e motivo; limpa, volta vazia', async () => {
    assert.deepEqual(await auditoria(), [], 'banco vazio');
    // Valid state of every family.
    await fcall('tv30_sessao_usuario', P1);
    await fcall('tv30_sessao_servico_id', 'urn:s');
    await fcall('tv30_sessao_servico', ...plano(SVC));
    await fcall('tv30_perfil_grava', ...perfilArgs(P1, ['urn:s'], PERFIL));
    await fcall('tv30_atributos_emissora', P1, 'urn:s', 'cor', 'azul');
    await fcall('tv30_cliente_autoriza', 'c1', ...plano(CLIENTE));
    await fcall('tv30_cliente_bloqueia', 'c2');
    await fcall('tv30_origem_registra', ORIGEM, 'urn:s');
    await fcall('tv30_bind_registra', 'urn:s', JSON.stringify(ENTRADA));
    await fcall('tv30_servicos_anunciados', 'urn:s', 'urn:t');
    assert.deepEqual(await auditoria(), [], 'todas as familias validas');
    const valido = await snapshot();

    // Manual writes (what redis-cli or the commander allow), one of each kind.
    const manuais = [
        [['SET', `user:${P1}:profile`, 'x'], [`user:${P1}:profile`, '-', 'chave sem familia']],
        [['HSET', 'session:x', 'a', '1'], ['session:x', '-', 'chave sem familia']],
        [['SET', 'session:current-service', 'x'], ['session:current-service', 'session:current-service', 'no Redis e string, o esquema pede hash']],
        [['SET', 'session:current-user', 'x'.repeat(65)], ['session:current-user', 'session:current-user', 'valor: tem 65 caracteres, maximo 64']],
        [['HSET', `user:${P1}`, 'parentalControl', 'False'], [`user:${P1}`, 'user:{id}', 'campo parentalControl:']],
        [['HDEL', `user:${P1}`, 'nickname'], [`user:${P1}`, 'user:{id}', 'campo nickname: obrigatorio, ausente']],
        [['HSET', `user:${P1}`, 'extra', '1'], [`user:${P1}`, 'user:{id}', 'campo "extra": nao previsto']],
        // Avatar as an inline SVG, and a path with a directory.
        [['HSET', `user:${P1}`, 'avatar', SVG_EM_LINHA], [`user:${P1}`, 'user:{id}', 'campo avatar: SVG em linha nao serve']],
        [['HSET', `user:${P1}`, 'avatar', '../segredo.svg'], [`user:${P1}`, 'user:{id}', 'campo avatar: so o nome do arquivo, sem diretorio']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'vazio', ''], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'campo "vazio": vazio']],
        [['HSET', `user:${P1}:broadcaster-attrs:urn:s`, 'nickname', 'x'], [`user:${P1}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'campo nickname: atributo basico']],
        [['SADD', `user:${P1}:consent`, ''], [`user:${P1}:consent`, 'user:{id}:consent', 'membro "": vazio']],
        [['HSET', 'client:c1', 'class', 'admin'], ['client:c1', 'client:{id}', 'campo class:']],
        [['HSET', 'origins:associated', 'http://X.example', 'urn:s'], ['origins:associated', 'origins:associated', 'nome de campo: host deve estar em minusculas']],
        [['RPUSH', 'bind-context:urn:s', '{"alg":"none","key":"k","registeredAt":1}'], ['bind-context:urn:s', 'bind-context:{serviceId}', 'item 1: membro "alg"']],
        [['SADD', 'services:announced', ''], ['services:announced', 'services:announced', 'membro "": vazio']],
        [['SET', 'services:announced', 'x'], ['services:announced', 'services:announced', 'no Redis e string, o esquema pede set']],
        [['HSET', 'services:outra', 'a', '1'], ['services:outra', '-', 'chave sem familia']],
        [['SET', 'users:index', 'x'], ['users:index', 'users:index', 'no Redis e string, o esquema pede set']],
        [['SADD', 'users:index', FANTASMA, ORFAO, SEM_HASH, 'f0f0f0f0-0000-4000-8000-000000000004', 'f0f0f0f0-0000-4000-8000-000000000005'], ['users:index', 'users:index', '6 membros, acima do teto de 5']],
        // Ids that are not lowercase UUIDs.
        [['SADD', 'users:index', 'user_1760000000000'], ['users:index', 'users:index', 'membro "user_1760000000000": deve ser UUID no formato da RFC 9562']],
        [['HSET', 'user:c3167a18-5dc5', 'id', 'c3167a18-5dc5'], ['user:c3167a18-5dc5', 'user:{id}', '{id} da chave: deve ser UUID']],
        [['SADD', `user:${P1.toUpperCase()}:consent`, 'urn:s'], [`user:${P1.toUpperCase()}:consent`, 'user:{id}:consent', '{id} da chave: UUID deve estar em minusculas']],
        // Nil UUID, version and variant outside RFC 9562.
        [['SADD', 'users:index', '00000000-0000-0000-0000-000000000000'], ['users:index', 'users:index', 'membro "00000000-0000-0000-0000-000000000000": UUID nulo']],
        [['SADD', `user:${comDigito(P1, UUID_VERSAO, '9')}:consent`, 'urn:s'], [`user:${comDigito(P1, UUID_VERSAO, '9')}:consent`, 'user:{id}:consent', '{id} da chave: versao do UUID deve ser de 1 a 8 (1o digito do 3o grupo; veio 9']],
        [['HSET', `user:${comDigito(P1, UUID_VARIANTE, 'c')}`, 'id', comDigito(P1, UUID_VARIANTE, 'c')], [`user:${comDigito(P1, UUID_VARIANTE, 'c')}`, 'user:{id}', '{id} da chave: variante do UUID deve ser a da RFC 9562 (1o digito do 4o grupo em 8, 9, a ou b; veio c']],
        // Invariants
        [['HSET', `user:${FANTASMA}`, 'id', FANTASMA, 'nickname', 'F', 'parentalControl', 'false', 'closedCaptioning', 'false', 'closedSigning', 'false', 'audioDescription', 'false', 'dialogEnhancement', 'false', 'voiceGuidance', 'false'],
            [`user:${FANTASMA}`, 'user:{id}', 'id fora de users:index (invariante perfil-indexado)']],
        [['SREM', 'users:index', P1], [`user:${P1}`, 'user:{id}', 'invariante perfil-indexado']],
        [['SADD', 'users:index', SEM_HASH], ['users:index', 'users:index', `membro "${SEM_HASH}" sem o hash user:${SEM_HASH} (invariante perfil-indexado)`]],
        [['SADD', `user:${ORFAO}:consent`, 'urn:s'], [`user:${ORFAO}:consent`, 'user:{id}:consent', `perfil "${ORFAO}" fora de users:index (invariante perfil-dependentes)`]],
        [['HSET', `user:${ORFAO}:broadcaster-attrs:urn:s`, 'a', '1'], [`user:${ORFAO}:broadcaster-attrs:urn:s`, 'user:{id}:broadcaster-attrs:{scid}', 'invariante perfil-dependentes']],
        [['SADD', 'clients:blocked', 'c1'], ['clients:authorized', 'clients:authorized', 'membro "c1" tambem em clients:blocked (invariante clientes-disjuntos)']],
        [['SADD', 'clients:authorized', 'c9'], ['clients:authorized', 'clients:authorized', 'membro "c9" sem o hash client:c9 (invariante autorizado-tem-registro)']],
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

test('auditoria nao olha chaves fora do alcance (esquema.json, foraDoAlcance)', async () => {
    await r('SADD', 'remote-devices:index', 'h1');
    await r('HSET', 'remote-device:h1', 'deviceClass', 'x');
    await r('SET', 'seed:done', '1');
    await r('SET', 'tv30:meu-componente:exemplo', 'v');
    assert.deepEqual(await auditoria(), []);
    for (const f of ESQ.foraDoAlcance) {
        assert.ok(!ESQ.prefixosAuditados.some(p => f.chave.startsWith(p)), `${f.chave} fora dos prefixos auditados`);
    }
});
