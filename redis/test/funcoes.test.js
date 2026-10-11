// Testes da biblioteca de funcoes "tv30" num Redis REAL (P4, decisao do Luis
// em 10/10: "Funcoes + auditoria"). Rode por infra/redis/test/run.sh, que
// sobe um redis:7-alpine descartavel, carrega a biblioteca montada por
// infra/redis/biblioteca.sh e roda este arquivo num node:20-alpine.
//
// O que se prova:
// - a biblioteca carregada tem exatamente as funcoes do esquema.json (e as
//   somente leitura com a flag no-writes);
// - ACEITACAO: cada operacao com dado valido grava o efeito esperado, inclusive
//   os valores de fronteira de cada regra (ex.: 20 caracteres multibyte no
//   nickname);
// - RECUSA DE CADA REGRA: os casos sao GERADOS a partir do esquema.json — para
//   cada familia, cada regra (valor, campo, membro, parte da chave, item, nome
//   de campo) recebe valores que a violam; a funcao tem de responder erro
//   TV30ESQUEMA com a funcao, a chave e o motivo, e o banco tem de ficar
//   identico (nada gravado). Regra nova no esquema.json ganha caso aqui sem
//   editar este arquivo (se o tipo de regra for conhecido);
// - ATOMICIDADE: recusa no meio de uma operacao de varias chaves (tipo errado
//   numa chave posterior, membro invalido no fim) nao deixa gravacao parcial;
// - INVARIANTES nas operacoes (clientes disjuntos, perfil indexado,
//   autorizado com registro);
// - AUDITORIA: vazia num banco valido; aponta cada tipo de problema gravado a
//   mao (chave sem familia, tipo errado, campo invalido, cada invariante) e
//   volta vazia depois da limpeza; FCALL_RO aceita so as somente leitura.
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

// ------------------------------------------------------------ esquema JS --
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

// ----------------------------------------------------------------- dados --
const PERFIL = {
    id: 'p1', nickname: 'Viewer 1', avatar: '<svg viewBox="0 0 1 1"/>',
    parentalControl: 'true', maxContentRating: '12',
    audioLanguage: 'pt-BR', closedCaptioningLanguage: 'pt-BR', userInterfaceLanguage: 'en',
    closedCaptioning: 'true', closedSigning: 'true', closedSigningSide: 'right', closedSigningWidth: '28',
    audioDescription: 'false', dialogEnhancement: 'false', voiceGuidance: 'false', lastAccess: '1760000000000',
};
// perfil minimo: so os obrigatorios (condicoes falsas)
const PERFIL_MIN = {
    id: 'p1', nickname: 'Viewer 1', parentalControl: 'false', closedCaptioning: 'false', closedSigning: 'false',
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

// recusa: erro TV30ESQUEMA <funcao>: com os trechos esperados, e nada gravado
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

// ----------------------------------------------- violacoes de cada regra --
// [valor, trecho esperado no motivo, descricao]
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

// valores que cumprem a regra, inclusive as fronteiras
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
        case 'origem': return ['http://users-test.apps.localhost:8080', 'http://a.apps.localhost', 'https://x.example', 'https://x.example:8443', 'http://[::1]:8099', 'http://127.0.0.1:65535'];
        case 'json': return ['HS256', 'HS512', 'RS256', 'RS512'].map(alg => JSON.stringify({ ...jsonValido(regra), alg }));
        default: throw new Error(`tipo de regra sem gerador de aceitacoes neste teste: ${regra.tipo}`);
    }
}

function jsonValido(regra) {
    assert.ok(regra.campos.alg && regra.campos.key && regra.campos.registeredAt, 'o gerador de JSON so conhece a entrada de bind-context');
    return { ...ENTRADA };
}

// ------------------------------------------- montagem das chamadas por slot --
// Para cada familia: os "slots" (lugares onde uma regra se aplica) e como
// montar uma chamada valida com o valor do slot trocado. prep roda antes.
const indexaP1 = () => fcall('tv30_perfil_grava', ...perfilArgs('p1', [], PERFIL));
const SLOTS = {
    'session:current-user': [{ rotulo: 'valor', regra: f => f.valor, chamada: v => ['tv30_sessao_usuario', [v]], trecho: 'valor:' }],
    'session:current-service-id': [{ rotulo: 'valor', regra: f => f.valor, chamada: v => ['tv30_sessao_servico_id', [v]], trecho: 'valor:' }],
    'session:current-service': campoSlots(campo => v => ['tv30_sessao_servico', plano({ ...SVC, [campo]: v })]),
    'users:index': [{ rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_perfil_grava', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} da chave:' }],
    'user:{id}': [
        { rotulo: '{id}', regra: f => f.partes.id, chamada: v => ['tv30_perfil_grava', perfilArgs(v, [], { ...PERFIL, id: v })], trecho: '{id} da chave:' },
        // o campo id tem a mesma regra da parte {id} (slot acima) e tem de ser
        // igual a ela (teste proprio abaixo): fica fora dos slots por campo
        ...campoSlots(campo => v => ['tv30_perfil_grava', perfilArgs('p1', [], { ...PERFIL, [campo]: v })], ['id']),
    ],
    'user:{id}:consent': [
        { rotulo: 'membro', regra: f => f.membros, chamada: v => ['tv30_perfil_grava', perfilArgs('p1', ['urn:tv30:service:ok', v], PERFIL)], trecho: 'membro' },
        { rotulo: '{id}', regra: f => f.partes.id, chamada: v => ['tv30_perfil_grava', perfilArgs(v, ['urn:x'], { ...PERFIL, id: v })], trecho: '{id} da chave:' },
    ],
    'user:{id}:broadcaster-attrs:{scid}': [
        // o perfil do valor tem de existir para a aceitacao; com valor invalido a
        // criacao falha e a recusa vem da parte {id} da chave
        { rotulo: '{id}', regra: f => f.partes.id, prep: v => fcall('tv30_perfil_grava', ...perfilArgs(v, [], { ...PERFIL, id: v })).catch(() => undefined), chamada: v => ['tv30_atributos_emissora', [v, 'urn:s:1', 'cor', 'azul']], trecho: '{id} da chave:' },
        { rotulo: '{scid}', regra: f => f.partes.scid, prep: indexaP1, chamada: v => ['tv30_atributos_emissora', ['p1', v, 'cor', 'azul']], trecho: '{scid} da chave:' },
        { rotulo: 'nome de campo', regra: f => f.outrosCampos.nome, prep: indexaP1, chamada: v => ['tv30_atributos_emissora', ['p1', 'urn:s:1', v, 'azul']], trecho: 'nome de campo:' },
        // outrosCampos.valor (texto, min 1): vazio na funcao REMOVE o campo, entao
        // nao ha valor gravavel que viole a regra; a auditoria cobre (abaixo)
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
    'bind-context:{serviceId}': [
        { rotulo: '{serviceId}', regra: f => f.partes.serviceId, chamada: v => ['tv30_bind_registra', [v, JSON.stringify(ENTRADA)]], trecho: '{serviceId} da chave:' },
        { rotulo: 'entrada', regra: f => f.itens, chamada: v => ['tv30_bind_registra', ['urn:tv30:service:a', v]], trecho: 'entrada:' },
    ],
};
// um slot por campo declarado; a familia vem na hora de gerar
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

// ------------------------------------------------------------------ carga --
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
    // cada funcao citada numa familia existe; cada familia tem quem a grave
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
    await recusa('n invalido', 'tv30_perfil_grava', ['p1', 'x', 'id', 'p1'], ['n=']);
    await recusa('n alem dos argumentos', 'tv30_perfil_grava', ['p1', '9', 'a'], ['n=']);
    await assert.rejects(fcallRo('tv30_auditoria', 'x'), new RegExp(`${PREFIXO} tv30_auditoria: uso:`));
});

// -------------------------------------------- recusa de cada regra (gerado) --
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
        // campos obrigatorios, condicionais e nao previstos
        if (fam.campos) {
            const [base, chamar] = {
                'session:current-service': [SVC, h => ['tv30_sessao_servico', plano(h)]],
                'user:{id}': [PERFIL, h => ['tv30_perfil_grava', perfilArgs('p1', [], h)]],
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
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', [], PERFIL_MIN));
    assert.deepEqual(await auditoria(), []);
    await recusa('id diferente', 'tv30_perfil_grava', perfilArgs('p1', [], { ...PERFIL, id: 'p2' }), ['user:p1 campo id: deve ser igual ao {id} da chave']);
});

test('broadcaster-attrs: atributos basicos proibidos e teto de 20 MB no resultado', async () => {
    const f = familia('user:{id}:broadcaster-attrs:{scid}');
    await indexaP1();
    for (const nome of f.camposProibidos.nomes) {
        await recusa(`redefinir ${nome}`, 'tv30_atributos_emissora', ['p1', 'urn:s:1', nome, 'x'], [`campo ${nome}: atributo basico`]);
        await recusa(`remover ${nome}`, 'tv30_atributos_emissora', ['p1', 'urn:s:1', nome, ''], [`campo ${nome}: atributo basico`]);
    }
    // exatamente no teto: aceito; um byte acima: recusado (soma de nomes e valores)
    const nome = 'grande';
    await fcall('tv30_atributos_emissora', 'p1', 'urn:s:1', nome, 'x'.repeat(f.maxBytes - nome.length));
    await recusa('acima do teto', 'tv30_atributos_emissora', ['p1', 'urn:s:1', 'b', 'y'], [`acima do teto de ${f.maxBytes}`]);
    // remover libera
    assert.equal(await fcall('tv30_atributos_emissora', 'p1', 'urn:s:1', nome, '', 'b', 'y'), 2);
    assert.deepEqual(await r('HGETALL', 'user:p1:broadcaster-attrs:urn:s:1'), ['b', 'y']);
});

// ----------------------------------------------------- efeitos (aceitacao) --
test('sessao: SET dos dois espelhos (vazio vale) e o hash regravado inteiro', async () => {
    assert.equal(await fcall('tv30_sessao_usuario', 'p1'), 1);
    assert.equal(await r('GET', 'session:current-user'), 'p1');
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
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', ['urn:a', 'urn:b'], PERFIL_MIN));
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', ['urn:c'], { ...PERFIL_MIN, nickname: 'Outro' }));
    assert.deepEqual((await r('SMEMBERS', 'user:p1:consent')).sort(), ['urn:a', 'urn:b', 'urn:c']);
    assert.equal(await r('HGET', 'user:p1', 'nickname'), 'Outro');
    assert.deepEqual(await r('SMEMBERS', 'users:index'), ['p1']);

    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'p1', '1234'), 1);
    assert.equal(await r('HGET', 'user:p1', 'lastAccess'), '1234');
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'desconhecido', '1'), 0);
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'a:b', '1'), 0, 'id que nunca seria perfil: 0, sem erro (espelho de topico)');
    assert.equal(await r('EXISTS', 'user:desconhecido'), 0, 'sem hash fantasma');
    await recusa('instante invalido', 'tv30_perfil_ultimo_acesso', ['p1', 'ontem'], ['campo lastAccess:']);

    await fcall('tv30_perfil_grava', ...perfilArgs('a*b', [], { ...PERFIL_MIN, id: 'a*b' }));
    await fcall('tv30_atributos_emissora', 'p1', 'urn:tv30:service:x', 'cor', 'azul');
    await fcall('tv30_atributos_emissora', 'p1', 'urn:y', 'k', 'v');
    await fcall('tv30_atributos_emissora', 'a*b', 'urn:y', 'k', 'v');
    assert.deepEqual(await auditoria(), []);
    // 1 (SREM) + user:p1 + consent + 2 broadcaster-attrs
    assert.equal(await fcall('tv30_perfil_remove', 'p1'), 5);
    assert.deepEqual((await r('KEYS', 'user:p1*')), []);
    // o glob do id e escapado: remover 'a*b' nao alcanca outro perfil, e vice-versa
    await fcall('tv30_perfil_grava', ...perfilArgs('aXb', [], { ...PERFIL_MIN, id: 'aXb' }));
    await fcall('tv30_atributos_emissora', 'aXb', 'urn:y', 'k', 'v');
    assert.equal(await fcall('tv30_perfil_remove', 'a*b'), 3);
    assert.equal(await r('EXISTS', 'user:aXb:broadcaster-attrs:urn:y'), 1);
    assert.equal(await fcall('tv30_perfil_remove', 'nao-existe'), 0);
    assert.deepEqual(await auditoria(), []);
});

test('atributos de emissora: perfil precisa estar indexado; vazio remove', async () => {
    await recusa('perfil inexistente', 'tv30_atributos_emissora', ['p1', 'urn:s:1', 'cor', 'azul'], ['fora de users:index (invariante perfil-dependentes)']);
    await indexaP1();
    assert.equal(await fcall('tv30_atributos_emissora', 'p1', 'urn:s:1', 'cor', 'azul', 'tam', 'G'), 2);
    assert.equal(await fcall('tv30_atributos_emissora', 'p1', 'urn:s:1', 'cor', ''), 1);
    assert.deepEqual(await r('HGETALL', 'user:p1:broadcaster-attrs:urn:s:1'), ['tam', 'G']);
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

    // inclusao no boot: bloqueado nao entra; sem registro e recusado; com registro entra
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'c1'), 0);
    await recusa('sem client:{id}', 'tv30_cliente_inclui_autorizado', ['c9'], ['invariante autorizado-tem-registro']);
    await r('HSET', 'client:velho', 'class', 'local-autonomous', 'refreshToken', 'r');
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'velho'), 1);
    assert.equal(await fcall('tv30_cliente_inclui_autorizado', 'velho'), 0);
    assert.deepEqual(await auditoria(), []);
});

test('registro existente: campo novo conferido, o resto antigo nao trava (a auditoria mostra)', async () => {
    // gravado a mao fora do esquema (booleano "False" da carga antiga)
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', [], PERFIL_MIN));
    await r('HSET', 'user:p1', 'parentalControl', 'False');
    assert.equal(await fcall('tv30_perfil_ultimo_acesso', 'p1', '99'), 1);
    assert.deepEqual(await auditoria(), [['user:p1', 'user:{id}', 'campo parentalControl: deve ser "true" ou "false" (veio "False")']]);
    // cliente novo confere tudo; existente so o escrito
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

// ------------------------------------------------------------- atomicidade --
test('atomicidade: tipo errado numa chave posterior ou membro invalido no fim nao deixam gravacao parcial', async () => {
    // perfil: users:index de outro tipo -> user:{id} nao nasce
    await r('SET', 'users:index', 'x');
    await recusa('users:index STRING', 'tv30_perfil_grava', perfilArgs('p1', [], PERFIL), ['users:index no Redis e string, o esquema pede set']);
    await limpo();
    // perfil: consent de outro tipo (a ultima chave gravada)
    await r('SET', 'user:p1:consent', 'x');
    await recusa('consent STRING', 'tv30_perfil_grava', perfilArgs('p1', ['urn:a'], PERFIL), ['user:p1:consent no Redis e string']);
    await limpo();
    // perfil: o ultimo servico invalido derruba a operacao inteira
    await recusa('ultimo servico vazio', 'tv30_perfil_grava', perfilArgs('p1', ['urn:a', 'urn:b', ''], PERFIL), ['user:p1:consent membro']);
    // regravar perfil existente com dado invalido mantem o anterior intacto
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', ['urn:a'], PERFIL));
    await recusa('regravacao invalida', 'tv30_perfil_grava', perfilArgs('p1', ['urn:b'], { ...PERFIL, nickname: '' }), ['campo nickname:']);
    // cliente: clients:blocked de outro tipo -> client:{id} nem clients:authorized
    await limpo();
    await r('LPUSH', 'clients:blocked', 'x');
    await recusa('clients:blocked LIST', 'tv30_cliente_autoriza', ['c1', ...plano(CLIENTE)], ['clients:blocked no Redis e list']);
    await limpo();
    await r('SADD', 'clients:blocked', 'c1');
    await recusa('autorizar com classe invalida', 'tv30_cliente_autoriza', ['c1', ...plano({ ...CLIENTE, class: 'outra' })], ['campo class:']);
    assert.deepEqual(await r('SMEMBERS', 'clients:blocked'), ['c1'], 'continua bloqueado');
    // sessao: hash anterior intacto
    await limpo();
    await fcall('tv30_sessao_servico', ...plano({ ...SVC, serviceName: 'Antes' }));
    await recusa('servico invalido', 'tv30_sessao_servico', plano({ ...SVC, serviceId: 'x' }), ['campo serviceId:']);
    // atributos: um nome proibido no meio de varios validos
    await limpo();
    await indexaP1();
    await recusa('um proibido entre validos', 'tv30_atributos_emissora', ['p1', 'urn:s', 'a', '1', 'nickname', 'x', 'z', '2'], ['campo nickname:']);
    // bind-context de outro tipo
    await limpo();
    await r('SET', 'bind-context:urn:a', 'x');
    await recusa('bind-context STRING', 'tv30_bind_registra', ['urn:a', JSON.stringify(ENTRADA)], ['no Redis e string, o esquema pede list']);
    await recusa('bind remove em STRING', 'tv30_bind_remove', ['urn:a', 'x'], ['no Redis e string']);
});

// --------------------------------------------------------------- auditoria --
test('auditoria: cada tipo de problema gravado a mao aparece com chave, familia e motivo; limpa, volta vazia', async () => {
    assert.deepEqual(await auditoria(), [], 'banco vazio');
    // estado valido de todas as familias
    await fcall('tv30_sessao_usuario', 'p1');
    await fcall('tv30_sessao_servico_id', 'urn:s');
    await fcall('tv30_sessao_servico', ...plano(SVC));
    await fcall('tv30_perfil_grava', ...perfilArgs('p1', ['urn:s'], PERFIL));
    await fcall('tv30_atributos_emissora', 'p1', 'urn:s', 'cor', 'azul');
    await fcall('tv30_cliente_autoriza', 'c1', ...plano(CLIENTE));
    await fcall('tv30_cliente_bloqueia', 'c2');
    await fcall('tv30_origem_registra', ORIGEM, 'urn:s');
    await fcall('tv30_bind_registra', 'urn:s', JSON.stringify(ENTRADA));
    assert.deepEqual(await auditoria(), [], 'todas as familias validas');
    const valido = await snapshot();

    // escritas manuais (o que o redis-cli/commander permitem), uma de cada tipo
    const manuais = [
        [['SET', 'user:p1:profile', 'x'], ['user:p1:profile', '-', 'chave sem familia']],
        [['HSET', 'session:x', 'a', '1'], ['session:x', '-', 'chave sem familia']],
        [['SET', 'session:current-service', 'x'], ['session:current-service', 'session:current-service', 'no Redis e string, o esquema pede hash']],
        [['SET', 'session:current-user', 'x'.repeat(65)], ['session:current-user', 'session:current-user', 'valor: tem 65 caracteres, maximo 64']],
        [['HSET', 'user:p1', 'parentalControl', 'False'], ['user:p1', 'user:{id}', 'campo parentalControl:']],
        [['HDEL', 'user:p1', 'nickname'], ['user:p1', 'user:{id}', 'campo nickname: obrigatorio, ausente']],
        [['HSET', 'user:p1', 'extra', '1'], ['user:p1', 'user:{id}', 'campo "extra": nao previsto']],
        [['HSET', 'user:p1:broadcaster-attrs:urn:s', 'vazio', ''], ['user:p1:broadcaster-attrs:urn:s', 'user:{id}:broadcaster-attrs:{scid}', 'campo "vazio": vazio']],
        [['HSET', 'user:p1:broadcaster-attrs:urn:s', 'nickname', 'x'], ['user:p1:broadcaster-attrs:urn:s', 'user:{id}:broadcaster-attrs:{scid}', 'campo nickname: atributo basico']],
        [['SADD', 'user:p1:consent', ''], ['user:p1:consent', 'user:{id}:consent', 'membro "": vazio']],
        [['HSET', 'client:c1', 'class', 'admin'], ['client:c1', 'client:{id}', 'campo class:']],
        [['HSET', 'origins:associated', 'http://X.example', 'urn:s'], ['origins:associated', 'origins:associated', 'nome de campo: host deve estar em minusculas']],
        [['RPUSH', 'bind-context:urn:s', '{"alg":"none","key":"k","registeredAt":1}'], ['bind-context:urn:s', 'bind-context:{serviceId}', 'item 1: membro "alg"']],
        [['SET', 'users:index', 'x'], ['users:index', 'users:index', 'no Redis e string, o esquema pede set']],
        // invariantes
        [['HSET', 'user:fantasma', 'id', 'fantasma', 'nickname', 'F', 'parentalControl', 'false', 'closedCaptioning', 'false', 'closedSigning', 'false', 'audioDescription', 'false', 'dialogEnhancement', 'false', 'voiceGuidance', 'false'],
            ['user:fantasma', 'user:{id}', 'id fora de users:index (invariante perfil-indexado)']],
        [['SREM', 'users:index', 'p1'], ['user:p1', 'user:{id}', 'invariante perfil-indexado']],
        [['SADD', 'users:index', 'sem-hash'], ['users:index', 'users:index', 'membro "sem-hash" sem o hash user:sem-hash (invariante perfil-indexado)']],
        [['SADD', 'user:orfao:consent', 'urn:s'], ['user:orfao:consent', 'user:{id}:consent', 'perfil "orfao" fora de users:index (invariante perfil-dependentes)']],
        [['HSET', 'user:orfao:broadcaster-attrs:urn:s', 'a', '1'], ['user:orfao:broadcaster-attrs:urn:s', 'user:{id}:broadcaster-attrs:{scid}', 'invariante perfil-dependentes']],
        [['SADD', 'clients:blocked', 'c1'], ['clients:authorized', 'clients:authorized', 'membro "c1" tambem em clients:blocked (invariante clientes-disjuntos)']],
        [['SADD', 'clients:authorized', 'c9'], ['clients:authorized', 'clients:authorized', 'membro "c9" sem o hash client:c9 (invariante autorizado-tem-registro)']],
    ];
    for (const [cmd, [chave, fam, trecho]] of manuais) {
        await r('FLUSHALL');
        // restaura o estado valido e aplica a escrita manual
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
    // limpeza: volta vazia
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
