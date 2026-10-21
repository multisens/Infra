// Verificador estatico: fora do adaptador das funcoes, nenhum codigo de servico
// grava direto numa familia do alcance. Usado por escritas.test.js e pelos testes
// de escrita do tv3ws e do aop. Tokenizador proprio, sem dependencia.
//
// JS/TS: metodo de escrita do cliente Redis fora do adaptador e violacao; no
// adaptador, so call('FCALL'|'FCALL_RO') com funcao do esquema.json. "new Redis("
// so no redis-client (um segundo cliente furaria o adaptador). Go e Python:
// nenhum texto do codigo e comando de escrita do Redis (nem, em Python, metodo
// de escrita do redis-py).
'use strict';
const fs = require('fs');
const path = require('path');

const ESQUEMA = path.join(__dirname, '..', 'esquema.json');

// comandos do Redis que gravam, rodam script ou mexem em funcoes/transacao
const COMANDOS_ESCRITA = `APPEND BITFIELD BITOP BLMOVE BLMPOP BLPOP BRPOP BRPOPLPUSH BZMPOP BZPOPMAX BZPOPMIN
 COPY DECR DECRBY DEL EVAL EVALSHA EVAL_RO EVALSHA_RO EXEC EXPIRE EXPIREAT FLUSHALL FLUSHDB FUNCTION GEOADD
 GETDEL GETEX GETSET HDEL HEXPIRE HINCRBY HINCRBYFLOAT HMSET HPERSIST HPEXPIRE HSET HSETNX INCR INCRBY
 INCRBYFLOAT LINSERT LMOVE LMPOP LPOP LPUSH LPUSHX LREM LSET LTRIM MIGRATE MOVE MSET MSETNX MULTI PERSIST
 PEXPIRE PEXPIREAT PFADD PFMERGE PSETEX RENAME RENAMENX RESTORE RPOP RPOPLPUSH RPUSH RPUSHX SADD SCRIPT
 SDIFFSTORE SET SETBIT SETEX SETNX SETRANGE SINTERSTORE SMOVE SORT SPOP SREM SUNIONSTORE SWAPDB UNLINK XADD
 XAUTOCLAIM XCLAIM XDEL XGROUP XTRIM ZADD ZDIFFSTORE ZINCRBY ZINTERSTORE ZMPOP ZPOPMAX ZPOPMIN ZRANGESTORE
 ZREM ZREMRANGEBYLEX ZREMRANGEBYRANK ZREMRANGEBYSCORE ZUNIONSTORE`.split(/\s+/).filter(Boolean);

// metodos do ioredis que gravam (comandos acima em minusculas) mais as portas
// genericas, que so o adaptador pode usar
const METODOS_JS = new Set([
    ...COMANDOS_ESCRITA.map(c => c.toLowerCase()), 'hmset', 'mset', 'multi', 'eval', 'evalsha', 'evalsha_ro', 'eval_ro',
    'call', 'callBuffer', 'sendCommand', 'send_command', 'fcall', 'fcall_ro', 'script', 'function',
]);
// metodos do redis-py que gravam, procurados so em objetos com nome de
// cliente ou pipeline (lista.append() nao e o APPEND do Redis)
const METODOS_PY = new Set([
    ...COMANDOS_ESCRITA.map(c => c.toLowerCase()), 'delete', 'hmset', 'mset', 'register_script', 'transaction',
]);
const RECEPTORES_PY = /\b(r|redis|pipe|pipeline|client|conn|cliente)\s*\.\s*([A-Za-z_]\w*)\s*\(/g;

function esquema() {
    return JSON.parse(fs.readFileSync(ESQUEMA, 'utf8'));
}

// ------------------------------------------------------------ tokenizador --
// Devolve [{t: 'id'|'str'|'tpl'|'p'|'num'|'re', v, linha}] sem comentarios.
// 'tpl' (template literal) leva o texto ate o primeiro ${ em prefixo.
function tokensJs(src) {
    const out = [];
    let i = 0, linha = 1;
    const n = src.length;
    const ultimoSignificativo = () => out[out.length - 1];
    const regexPode = () => {
        const u = ultimoSignificativo();
        if (!u) return true;
        if (u.t === 'p') return !/^[)\]}]$/.test(u.v) || u.v === '}';
        if (u.t === 'id') return ['return', 'typeof', 'case', 'in', 'of', 'new', 'delete', 'void', 'throw', 'else', 'do'].includes(u.v);
        return false;
    };
    while (i < n) {
        const c = src[i];
        if (c === '\n') { linha++; i++; continue; }
        if (/\s/.test(c)) { i++; continue; }
        if (c === '/' && src[i + 1] === '/') { while (i < n && src[i] !== '\n') i++; continue; }
        if (c === '/' && src[i + 1] === '*') {
            i += 2;
            while (i < n && !(src[i] === '*' && src[i + 1] === '/')) { if (src[i] === '\n') linha++; i++; }
            i += 2; continue;
        }
        if (c === '"' || c === "'") {
            const l0 = linha; let j = i + 1, v = '';
            while (j < n && src[j] !== c) { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } if (src[j] === '\n') linha++; v += src[j]; j++; }
            out.push({ t: 'str', v, linha: l0 }); i = j + 1; continue;
        }
        if (c === '`') {
            const l0 = linha; let j = i + 1, v = '', prefixo = null, prof = 0;
            while (j < n) {
                if (prof === 0 && src[j] === '`') break;
                if (src[j] === '\\') { v += src[j] + (src[j + 1] || ''); j += 2; continue; }
                if (prof === 0 && src[j] === '$' && src[j + 1] === '{') { if (prefixo === null) prefixo = v; prof = 1; v += '${'; j += 2; continue; }
                if (prof > 0 && src[j] === '{') prof++;
                if (prof > 0 && src[j] === '}') prof--;
                if (src[j] === '\n') linha++;
                v += src[j]; j++;
            }
            out.push({ t: 'tpl', v, prefixo: prefixo === null ? v : prefixo, linha: l0 }); i = j + 1; continue;
        }
        if (c === '/' && regexPode()) {
            let j = i + 1, classe = false;
            while (j < n && src[j] !== '\n') {
                if (src[j] === '\\') { j += 2; continue; }
                if (src[j] === '[') classe = true;
                else if (src[j] === ']') classe = false;
                else if (src[j] === '/' && !classe) break;
                j++;
            }
            j++;
            while (j < n && /[a-z]/i.test(src[j])) j++;
            out.push({ t: 're', v: src.slice(i, j), linha }); i = j; continue;
        }
        if (/[A-Za-z_$]/.test(c)) {
            let j = i; while (j < n && /[\w$]/.test(src[j])) j++;
            out.push({ t: 'id', v: src.slice(i, j), linha }); i = j; continue;
        }
        if (/[0-9]/.test(c)) {
            let j = i; while (j < n && /[\w.]/.test(src[j])) j++;
            out.push({ t: 'num', v: src.slice(i, j), linha }); i = j; continue;
        }
        if (c === '.' && src[i + 1] === '.' && src[i + 2] === '.') { out.push({ t: 'p', v: '...', linha }); i += 3; continue; }
        if (c === '?' && src[i + 1] === '.') { out.push({ t: 'p', v: '.', linha }); i += 2; continue; }
        out.push({ t: 'p', v: c, linha }); i++;
    }
    return out;
}

// pula do '(' em k ate depois do ')' correspondente
function fechaParen(tk, k) {
    let prof = 0;
    for (; k < tk.length; k++) {
        if (tk[k].t !== 'p') continue;
        if (tk[k].v === '(' || tk[k].v === '[' || tk[k].v === '{') prof++;
        else if (tk[k].v === ')' || tk[k].v === ']' || tk[k].v === '}') { prof--; if (prof === 0) return k + 1; }
    }
    return k;
}

// ----------------------------------------------------------------- JS/TS --
function analisaJs(arquivo, src) {
    const tk = tokensJs(src);
    const clientes = new Set();
    // import redis from '../../redis-client' / import redis, {..} from ... /
    // const redis = require('./redis-client')
    for (let k = 0; k < tk.length; k++) {
        if (tk[k].t === 'id' && tk[k].v === 'import' && tk[k + 1] && tk[k + 1].t === 'id') {
            let j = k + 1; while (j < tk.length && !(tk[j].t === 'id' && tk[j].v === 'from') && j < k + 40) j++;
            if (tk[j + 1] && tk[j + 1].t === 'str' && /(^|\/)redis-client(\.[jt]s)?$/.test(tk[j + 1].v)) clientes.add(tk[k + 1].v);
        }
        if (tk[k].t === 'id' && tk[k].v === 'require' && tk[k + 1]?.v === '(' && tk[k + 2]?.t === 'str'
            && /(^|\/)redis-client(\.[jt]s)?$/.test(tk[k + 2].v) && tk[k - 1]?.v === '=' && tk[k - 2]?.t === 'id') {
            clientes.add(tk[k - 2].v);
        }
    }
    const raizes = new Set(clientes);
    // const p = redis.pipeline() / redis.multi(); apelido: const r = redis
    for (let pass = 0; pass < 2; pass++) {
        for (let k = 0; k + 3 < tk.length; k++) {
            if (!(tk[k].t === 'id' && tk[k + 1].v === '=' && raizes.has(tk[k + 2].v))) continue;
            if (tk[k + 3].v === '.' && ['pipeline', 'multi'].includes(tk[k + 4]?.v)) raizes.add(tk[k].v);
            else if ([';', ',', ')'].includes(tk[k + 3].v) || tk[k + 3].linha > tk[k + 2].linha) raizes.add(tk[k].v);
        }
    }
    const escritas = [];   // {metodo, linha, arg1}
    // acesso por colchete (redis['set'](...)) esconde o metodo: so no adaptador
    for (let k = 0; k + 1 < tk.length; k++) {
        if (tk[k].t === 'id' && raizes.has(tk[k].v) && tk[k + 1].v === '[' && tk[k - 1]?.v !== '.') {
            escritas.push({ metodo: '[...]', linha: tk[k].linha, arg1: tk[k + 2] });
        }
    }
    for (let k = 0; k < tk.length; k++) {
        if (!(tk[k].t === 'id' && raizes.has(tk[k].v) && tk[k + 1]?.v === '.')) continue;
        if (tk[k - 1]?.v === '.') continue; // x.redis.y: nao e a raiz
        let j = k + 1;
        while (tk[j]?.v === '.' && tk[j + 1]?.t === 'id') {
            const metodo = tk[j + 1].v;
            j += 2;
            if (tk[j]?.v === '(') {
                const arg1 = tk[j + 1];
                if (METODOS_JS.has(metodo)) escritas.push({ metodo, linha: tk[j - 1].linha, arg1 });
                j = fechaParen(tk, j);
            }
        }
    }
    const novosClientes = [];
    for (let k = 0; k + 1 < tk.length; k++) {
        if (tk[k].t === 'id' && tk[k].v === 'new' && tk[k + 1].t === 'id' && /^(Redis|Cluster|IORedis)$/.test(tk[k + 1].v)) novosClientes.push(tk[k + 1].linha);
    }
    const textos = tk.filter(t => t.t === 'str' || t.t === 'tpl').map(t => ({ v: t.t === 'tpl' ? t.prefixo : t.v, linha: t.linha }));
    return { arquivo, clientes: [...clientes], escritas, novosClientes, textos };
}

function listaArquivos(dir, ext) {
    const out = [];
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (e.name !== 'node_modules') out.push(...listaArquivos(p, ext)); }
        else if (ext.some(x => e.name.endsWith(x)) && !e.name.endsWith('.d.ts')) out.push(p);
    }
    return out.sort();
}

const doAlcance = (esq, v) => esq.prefixosAuditados.some(p => v.startsWith(p));

// opts: { raiz (pasta do modulo), fontes ('src'), adaptador ('src/redis-funcoes.ts'),
//         clienteRedis ('src/redis-client.ts'), diretasForaDoAlcance: { 'src/x.ts': 'motivo' } }
// Devolve { violacoes: [texto], escritasPorArquivo: {arquivo: n}, adaptadorFuncoes: [nomes] }
function verificaModuloJs(opts) {
    const esq = esquema();
    const funcoes = new Set(esq.funcoes.map(f => f.nome));
    const rel = p => path.relative(opts.raiz, p).split(path.sep).join('/');
    const arquivos = listaArquivos(path.join(opts.raiz, opts.fontes), ['.ts', '.js']);
    const violacoes = [];
    const escritasPorArquivo = {};
    const adaptadorFuncoes = new Set();
    let viuAdaptador = false;
    for (const f of arquivos) {
        const r = rel(f);
        const a = analisaJs(r, fs.readFileSync(f, 'utf8'));
        if (a.escritas.length) escritasPorArquivo[r] = a.escritas.length;
        if (r !== opts.clienteRedis) for (const l of a.novosClientes) violacoes.push(`${r}:${l}: segundo cliente Redis (new Redis) fora de ${opts.clienteRedis}`);
        if (r === opts.adaptador) {
            viuAdaptador = true;
            for (const e of a.escritas) {
                if (e.metodo !== 'call' || !(e.arg1 && e.arg1.t === 'str' && ['FCALL', 'FCALL_RO'].includes(e.arg1.v))) {
                    violacoes.push(`${r}:${e.linha}: o adaptador so pode chamar redis.call('FCALL', ...), veio .${e.metodo}(${e.arg1 ? e.arg1.v : ''})`);
                }
            }
            for (const t of a.textos) {
                if (/^tv30_[a-z_]+$/.test(t.v)) {
                    adaptadorFuncoes.add(t.v);
                    if (!funcoes.has(t.v)) violacoes.push(`${r}:${t.linha}: funcao ${t.v} nao existe no esquema.json`);
                }
            }
            continue;
        }
        const permitido = opts.diretasForaDoAlcance && opts.diretasForaDoAlcance[r];
        if (permitido) {
            for (const t of a.textos) {
                if (doAlcance(esq, t.v)) violacoes.push(`${r}:${t.linha}: arquivo com escrita direta permitida so fora do alcance (${permitido}) cita chave do alcance "${t.v}"`);
            }
            continue;
        }
        for (const e of a.escritas) {
            violacoes.push(`${r}:${e.linha}: escrita direta no Redis (.${e.metodo}) fora do adaptador ${opts.adaptador}; use a funcao do esquema (infra/redis/esquema.json)`);
        }
    }
    if (!viuAdaptador) violacoes.push(`adaptador ${opts.adaptador} nao encontrado`);
    for (const r of Object.keys(opts.diretasForaDoAlcance || {})) {
        if (!arquivos.some(f => rel(f) === r)) violacoes.push(`arquivo permitido ${r} nao existe mais (atualize a lista)`);
    }
    return { violacoes, escritasPorArquivo, adaptadorFuncoes: [...adaptadorFuncoes].sort(), arquivos: arquivos.length };
}

// ------------------------------------------------------------- Go e Python --
// Textos do codigo (sem comentarios). Go: "..." e `...` (fora das declaracoes
// import, cujo caminho "sort" seria confundido com o comando SORT); Python:
// '...', "...", '''...''', """...""" (com prefixos r/b/f).
function textosGo(src) {
    const out = [];
    let i = 0, linha = 1, emImport = false, depoisImport = false;
    while (i < src.length) {
        const c = src[i];
        if (/[A-Za-z_]/.test(c) && !/\w/.test(src[i - 1] || '')) {
            let j = i;
            while (j < src.length && /\w/.test(src[j])) j++;
            depoisImport = src.slice(i, j) === 'import';
            i = j; continue;
        }
        if (depoisImport && c === '(') { emImport = true; depoisImport = false; i++; continue; }
        if (emImport && c === ')') { emImport = false; i++; continue; }
        if (c === '\n') { linha++; i++; continue; }
        if (c === '/' && src[i + 1] === '/') { while (i < src.length && src[i] !== '\n') i++; continue; }
        if (c === '/' && src[i + 1] === '*') { i += 2; while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) { if (src[i] === '\n') linha++; i++; } i += 2; continue; }
        if (c === '"' || c === "'") {
            let j = i + 1, v = '';
            while (j < src.length && src[j] !== c && src[j] !== '\n') { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } v += src[j]; j++; }
            if (c === '"' && !emImport && !depoisImport) out.push({ v, linha });
            depoisImport = false;
            i = j + 1; continue;
        }
        if (c === '`') {
            const l0 = linha; let j = i + 1;
            while (j < src.length && src[j] !== '`') { if (src[j] === '\n') linha++; j++; }
            out.push({ v: src.slice(i + 1, j), linha: l0 }); i = j + 1; continue;
        }
        i++;
    }
    return out;
}

function tokensPy(src) {
    const textos = [], codigo = [];
    let i = 0, linha = 1, buf = '';
    const flush = () => { if (buf) codigo.push({ v: buf, linha }); buf = ''; };
    while (i < src.length) {
        const c = src[i];
        if (c === '\n') { flush(); linha++; i++; continue; }
        if (c === '#') { while (i < src.length && src[i] !== '\n') i++; continue; }
        if (c === '"' || c === "'") {
            const triplo = src.slice(i, i + 3) === c.repeat(3);
            const fim = triplo ? c.repeat(3) : c;
            const l0 = linha; let j = i + fim.length, v = '';
            while (j < src.length && src.slice(j, j + fim.length) !== fim) { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } if (src[j] === '\n') linha++; v += src[j]; j++; }
            textos.push({ v, linha: l0 }); buf += ' ';
            i = j + fim.length; continue;
        }
        buf += c; i++;
    }
    flush();
    return { textos, codigo };
}

function verificaGo(dir) {
    const violacoes = [];
    const cmds = new Set(COMANDOS_ESCRITA);
    const arquivos = fs.readdirSync(dir).filter(f => f.endsWith('.go') && !f.endsWith('_test.go')).sort();
    for (const f of arquivos) {
        for (const t of textosGo(fs.readFileSync(path.join(dir, f), 'utf8'))) {
            if (cmds.has(t.v.toUpperCase())) violacoes.push(`${f}:${t.linha}: comando de escrita "${t.v}" no codigo da borda; a escrita sai so por FCALL (esquema.json)`);
        }
    }
    return { violacoes, arquivos: arquivos.length };
}

function verificaPy(arquivos) {
    const violacoes = [];
    const cmds = new Set(COMANDOS_ESCRITA);
    for (const f of arquivos) {
        const { textos, codigo } = tokensPy(fs.readFileSync(f, 'utf8'));
        const nome = path.basename(f);
        for (const t of textos) {
            if (cmds.has(t.v.toUpperCase())) violacoes.push(`${nome}:${t.linha}: comando de escrita "${t.v}"; a carga grava so por FCALL tv30_perfil_grava`);
        }
        for (const l of codigo) {
            for (const m of l.v.matchAll(RECEPTORES_PY)) {
                if (METODOS_PY.has(m[2])) violacoes.push(`${nome}:${l.linha}: metodo de escrita do redis-py ${m[1]}.${m[2]}(); a carga grava so por FCALL tv30_perfil_grava`);
            }
        }
    }
    return { violacoes, arquivos: arquivos.length };
}

// Configuracao dos modulos do TV30 (raiz = pasta do TV30). Escrita direta
// permitida so onde a familia esta fora do alcance (esquema.json, foraDoAlcance).
function modulosTv30(raiz) {
    return {
        tv3ws: {
            raiz: path.join(raiz, 'tv3ws'), fontes: 'src', adaptador: 'src/redis-funcoes.ts', clienteRedis: 'src/redis-client.ts',
            diretasForaDoAlcance: {
                'src/modules/remotedevice-manager/manager.ts': 'remote-devices:*, remote-device:* (espelho dos dispositivos remotos)',
            },
        },
        aop: {
            raiz: path.join(raiz, 'aop'), fontes: 'src', adaptador: 'src/redis-funcoes.js', clienteRedis: 'src/redis-client.js',
            diretasForaDoAlcance: {},
        },
    };
}

module.exports = {
    COMANDOS_ESCRITA, METODOS_JS, tokensJs, analisaJs, verificaModuloJs, verificaGo, verificaPy, modulosTv30, esquema,
};
