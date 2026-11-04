// Static checker: outside the adapter of the functions, no service code
// writes directly to a family in scope. Own tokenizer, without dependencies.
//
// JS/TS: a write method of the Redis client outside the adapter is a
// violation; in the adapter, only call('FCALL'|'FCALL_RO') with a function of
// schema.json. "new Redis(" only in redis-client (a second client would
// bypass the adapter). Go and Python: no string of the code is a Redis write
// command (nor, in Python, a write method of redis-py).
'use strict';
const fs = require('fs');
const path = require('path');

const SCHEMA = path.join(__dirname, '..', 'schema.json');

// Redis commands that write, run scripts or touch functions or
// transactions.
const WRITE_COMMANDS = `APPEND BITFIELD BITOP BLMOVE BLMPOP BLPOP BRPOP BRPOPLPUSH BZMPOP BZPOPMAX BZPOPMIN
 COPY DECR DECRBY DEL EVAL EVALSHA EVAL_RO EVALSHA_RO EXEC EXPIRE EXPIREAT FLUSHALL FLUSHDB FUNCTION GEOADD
 GETDEL GETEX GETSET HDEL HEXPIRE HINCRBY HINCRBYFLOAT HMSET HPERSIST HPEXPIRE HSET HSETNX INCR INCRBY
 INCRBYFLOAT LINSERT LMOVE LMPOP LPOP LPUSH LPUSHX LREM LSET LTRIM MIGRATE MOVE MSET MSETNX MULTI PERSIST
 PEXPIRE PEXPIREAT PFADD PFMERGE PSETEX RENAME RENAMENX RESTORE RPOP RPOPLPUSH RPUSH RPUSHX SADD SCRIPT
 SDIFFSTORE SET SETBIT SETEX SETNX SETRANGE SINTERSTORE SMOVE SORT SPOP SREM SUNIONSTORE SWAPDB UNLINK XADD
 XAUTOCLAIM XCLAIM XDEL XGROUP XTRIM ZADD ZDIFFSTORE ZINCRBY ZINTERSTORE ZMPOP ZPOPMAX ZPOPMIN ZRANGESTORE
 ZREM ZREMRANGEBYLEX ZREMRANGEBYRANK ZREMRANGEBYSCORE ZUNIONSTORE`.split(/\s+/).filter(Boolean);

// ioredis methods that write (the commands above in lowercase) plus the
// generic entry points, which only the adapter may use.
const JS_METHODS = new Set([
    ...WRITE_COMMANDS.map(c => c.toLowerCase()), 'hmset', 'mset', 'multi', 'eval', 'evalsha', 'evalsha_ro', 'eval_ro',
    'call', 'callBuffer', 'sendCommand', 'send_command', 'fcall', 'fcall_ro', 'script', 'function',
]);
// redis-py methods that write, looked for only on objects named as a client
// or pipeline (list.append() is not the Redis APPEND).
const PY_METHODS = new Set([
    ...WRITE_COMMANDS.map(c => c.toLowerCase()), 'delete', 'hmset', 'mset', 'register_script', 'transaction',
]);
const PY_RECEIVERS = /\b(r|redis|pipe|pipeline|client|conn)\s*\.\s*([A-Za-z_]\w*)\s*\(/g;

function schema() {
    return JSON.parse(fs.readFileSync(SCHEMA, 'utf8'));
}

// -------------------------------------------------------------- tokenizer --
// Returns [{t: 'id'|'str'|'tpl'|'p'|'num'|'re', v, line}] without comments.
// 'tpl' (template literal) carries the text up to the first ${ in prefix.
function jsTokens(src) {
    const out = [];
    let i = 0, line = 1;
    const n = src.length;
    const lastToken = () => out[out.length - 1];
    const regexAllowed = () => {
        const u = lastToken();
        if (!u) return true;
        if (u.t === 'p') return !/^[)\]}]$/.test(u.v) || u.v === '}';
        if (u.t === 'id') return ['return', 'typeof', 'case', 'in', 'of', 'new', 'delete', 'void', 'throw', 'else', 'do'].includes(u.v);
        return false;
    };
    while (i < n) {
        const c = src[i];
        if (c === '\n') { line++; i++; continue; }
        if (/\s/.test(c)) { i++; continue; }
        if (c === '/' && src[i + 1] === '/') { while (i < n && src[i] !== '\n') i++; continue; }
        if (c === '/' && src[i + 1] === '*') {
            i += 2;
            while (i < n && !(src[i] === '*' && src[i + 1] === '/')) { if (src[i] === '\n') line++; i++; }
            i += 2; continue;
        }
        if (c === '"' || c === "'") {
            const l0 = line; let j = i + 1, v = '';
            while (j < n && src[j] !== c) { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } if (src[j] === '\n') line++; v += src[j]; j++; }
            out.push({ t: 'str', v, line: l0 }); i = j + 1; continue;
        }
        if (c === '`') {
            const l0 = line; let j = i + 1, v = '', prefix = null, depth = 0;
            while (j < n) {
                if (depth === 0 && src[j] === '`') break;
                if (src[j] === '\\') { v += src[j] + (src[j + 1] || ''); j += 2; continue; }
                if (depth === 0 && src[j] === '$' && src[j + 1] === '{') { if (prefix === null) prefix = v; depth = 1; v += '${'; j += 2; continue; }
                if (depth > 0 && src[j] === '{') depth++;
                if (depth > 0 && src[j] === '}') depth--;
                if (src[j] === '\n') line++;
                v += src[j]; j++;
            }
            out.push({ t: 'tpl', v, prefix: prefix === null ? v : prefix, line: l0 }); i = j + 1; continue;
        }
        if (c === '/' && regexAllowed()) {
            let j = i + 1, inClass = false;
            while (j < n && src[j] !== '\n') {
                if (src[j] === '\\') { j += 2; continue; }
                if (src[j] === '[') inClass = true;
                else if (src[j] === ']') inClass = false;
                else if (src[j] === '/' && !inClass) break;
                j++;
            }
            j++;
            while (j < n && /[a-z]/i.test(src[j])) j++;
            out.push({ t: 're', v: src.slice(i, j), line }); i = j; continue;
        }
        if (/[A-Za-z_$]/.test(c)) {
            let j = i; while (j < n && /[\w$]/.test(src[j])) j++;
            out.push({ t: 'id', v: src.slice(i, j), line }); i = j; continue;
        }
        if (/[0-9]/.test(c)) {
            let j = i; while (j < n && /[\w.]/.test(src[j])) j++;
            out.push({ t: 'num', v: src.slice(i, j), line }); i = j; continue;
        }
        if (c === '.' && src[i + 1] === '.' && src[i + 2] === '.') { out.push({ t: 'p', v: '...', line }); i += 3; continue; }
        if (c === '?' && src[i + 1] === '.') { out.push({ t: 'p', v: '.', line }); i += 2; continue; }
        out.push({ t: 'p', v: c, line }); i++;
    }
    return out;
}

// Skips from the '(' at k to after the matching ')'.
function closeParen(tk, k) {
    let depth = 0;
    for (; k < tk.length; k++) {
        if (tk[k].t !== 'p') continue;
        if (tk[k].v === '(' || tk[k].v === '[' || tk[k].v === '{') depth++;
        else if (tk[k].v === ')' || tk[k].v === ']' || tk[k].v === '}') { depth--; if (depth === 0) return k + 1; }
    }
    return k;
}

// ----------------------------------------------------------------- JS/TS --
function analyzeJs(file, src) {
    const tk = jsTokens(src);
    const clients = new Set();
    // import redis from '../../redis-client' / import redis, {..} from ... /
    // const redis = require('./redis-client')
    for (let k = 0; k < tk.length; k++) {
        if (tk[k].t === 'id' && tk[k].v === 'import' && tk[k + 1] && tk[k + 1].t === 'id') {
            let j = k + 1; while (j < tk.length && !(tk[j].t === 'id' && tk[j].v === 'from') && j < k + 40) j++;
            if (tk[j + 1] && tk[j + 1].t === 'str' && /(^|\/)redis-client(\.[jt]s)?$/.test(tk[j + 1].v)) clients.add(tk[k + 1].v);
        }
        if (tk[k].t === 'id' && tk[k].v === 'require' && tk[k + 1]?.v === '(' && tk[k + 2]?.t === 'str'
            && /(^|\/)redis-client(\.[jt]s)?$/.test(tk[k + 2].v) && tk[k - 1]?.v === '=' && tk[k - 2]?.t === 'id') {
            clients.add(tk[k - 2].v);
        }
    }
    const roots = new Set(clients);
    // const p = redis.pipeline() / redis.multi(); alias: const r = redis
    for (let pass = 0; pass < 2; pass++) {
        for (let k = 0; k + 3 < tk.length; k++) {
            if (!(tk[k].t === 'id' && tk[k + 1].v === '=' && roots.has(tk[k + 2].v))) continue;
            if (tk[k + 3].v === '.' && ['pipeline', 'multi'].includes(tk[k + 4]?.v)) roots.add(tk[k].v);
            else if ([';', ',', ')'].includes(tk[k + 3].v) || tk[k + 3].line > tk[k + 2].line) roots.add(tk[k].v);
        }
    }
    // {method, line, arg1}
    const writes = [];
    // Bracket access (redis['set'](...)) hides the method: only in the
    // adapter.
    for (let k = 0; k + 1 < tk.length; k++) {
        if (tk[k].t === 'id' && roots.has(tk[k].v) && tk[k + 1].v === '[' && tk[k - 1]?.v !== '.') {
            writes.push({ method: '[...]', line: tk[k].line, arg1: tk[k + 2] });
        }
    }
    for (let k = 0; k < tk.length; k++) {
        if (!(tk[k].t === 'id' && roots.has(tk[k].v) && tk[k + 1]?.v === '.')) continue;
        // x.redis.y: not the root.
        if (tk[k - 1]?.v === '.') continue;
        let j = k + 1;
        while (tk[j]?.v === '.' && tk[j + 1]?.t === 'id') {
            const method = tk[j + 1].v;
            j += 2;
            if (tk[j]?.v === '(') {
                const arg1 = tk[j + 1];
                if (JS_METHODS.has(method)) writes.push({ method, line: tk[j - 1].line, arg1 });
                j = closeParen(tk, j);
            }
        }
    }
    const newClients = [];
    for (let k = 0; k + 1 < tk.length; k++) {
        if (tk[k].t === 'id' && tk[k].v === 'new' && tk[k + 1].t === 'id' && /^(Redis|Cluster|IORedis)$/.test(tk[k + 1].v)) newClients.push(tk[k + 1].line);
    }
    const texts = tk.filter(t => t.t === 'str' || t.t === 'tpl').map(t => ({ v: t.t === 'tpl' ? t.prefix : t.v, line: t.line }));
    return { file, clients: [...clients], writes, newClients, texts };
}

function listFiles(dir, ext) {
    const out = [];
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (e.name !== 'node_modules') out.push(...listFiles(p, ext)); }
        else if (ext.some(x => e.name.endsWith(x)) && !e.name.endsWith('.d.ts')) out.push(p);
    }
    return out.sort();
}

const inScope = (esq, v) => esq.auditedPrefixes.some(p => v.startsWith(p));

// opts: { root (module folder), sources ('src'), adapter
//   ('src/redis-functions.ts'), redisClient ('src/redis-client.ts'),
//   directOutOfScope: { 'src/x.ts': 'reason' } }
// Returns { violations: [text], writesPerFile: {file: n},
//   adapterFunctions: [names] }
function checkJsModule(opts) {
    const esq = schema();
    const functions = new Set(esq.functions.map(f => f.name));
    const rel = p => path.relative(opts.root, p).split(path.sep).join('/');
    const files = listFiles(path.join(opts.root, opts.sources), ['.ts', '.js']);
    const violations = [];
    const writesPerFile = {};
    const adapterFunctions = new Set();
    let sawAdapter = false;
    for (const f of files) {
        const r = rel(f);
        const a = analyzeJs(r, fs.readFileSync(f, 'utf8'));
        if (a.writes.length) writesPerFile[r] = a.writes.length;
        if (r !== opts.redisClient) for (const l of a.newClients) violations.push(`${r}:${l}: second Redis client (new Redis) outside ${opts.redisClient}`);
        if (r === opts.adapter) {
            sawAdapter = true;
            for (const e of a.writes) {
                if (e.method !== 'call' || !(e.arg1 && e.arg1.t === 'str' && ['FCALL', 'FCALL_RO'].includes(e.arg1.v))) {
                    violations.push(`${r}:${e.line}: the adapter may only call redis.call('FCALL', ...), got .${e.method}(${e.arg1 ? e.arg1.v : ''})`);
                }
            }
            for (const t of a.texts) {
                if (/^tv30_[a-z0-9_]+$/.test(t.v)) {
                    adapterFunctions.add(t.v);
                    if (!functions.has(t.v)) violations.push(`${r}:${t.line}: function ${t.v} does not exist in schema.json`);
                }
            }
            continue;
        }
        const allowed = opts.directOutOfScope && opts.directOutOfScope[r];
        if (allowed) {
            for (const t of a.texts) {
                if (inScope(esq, t.v)) violations.push(`${r}:${t.line}: file with a direct write allowed only out of scope (${allowed}) cites a key in scope "${t.v}"`);
            }
            continue;
        }
        for (const e of a.writes) {
            violations.push(`${r}:${e.line}: direct write in Redis (.${e.method}) outside the adapter ${opts.adapter}; use the function of the schema (infra/redis/schema.json)`);
        }
    }
    if (!sawAdapter) violations.push(`adapter ${opts.adapter} not found`);
    for (const r of Object.keys(opts.directOutOfScope || {})) {
        if (!files.some(f => rel(f) === r)) violations.push(`allowed file ${r} no longer exists (update the list)`);
    }
    return { violations, writesPerFile, adapterFunctions: [...adapterFunctions].sort(), files: files.length };
}

// ---------------------------------------------------------- Go and Python --
// Strings of the code, without comments. Go: "..." and `...` (outside the
// import declarations, whose path "sort" would be taken for the SORT
// command); Python: '...', "...", '''...''', """...""" (with r/b/f prefixes).
function goTexts(src) {
    const out = [];
    let i = 0, line = 1, inImport = false, afterImport = false;
    while (i < src.length) {
        const c = src[i];
        if (/[A-Za-z_]/.test(c) && !/\w/.test(src[i - 1] || '')) {
            let j = i;
            while (j < src.length && /\w/.test(src[j])) j++;
            afterImport = src.slice(i, j) === 'import';
            i = j; continue;
        }
        if (afterImport && c === '(') { inImport = true; afterImport = false; i++; continue; }
        if (inImport && c === ')') { inImport = false; i++; continue; }
        if (c === '\n') { line++; i++; continue; }
        if (c === '/' && src[i + 1] === '/') { while (i < src.length && src[i] !== '\n') i++; continue; }
        if (c === '/' && src[i + 1] === '*') { i += 2; while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) { if (src[i] === '\n') line++; i++; } i += 2; continue; }
        if (c === '"' || c === "'") {
            let j = i + 1, v = '';
            while (j < src.length && src[j] !== c && src[j] !== '\n') { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } v += src[j]; j++; }
            if (c === '"' && !inImport && !afterImport) out.push({ v, line });
            afterImport = false;
            i = j + 1; continue;
        }
        if (c === '`') {
            const l0 = line; let j = i + 1;
            while (j < src.length && src[j] !== '`') { if (src[j] === '\n') line++; j++; }
            out.push({ v: src.slice(i + 1, j), line: l0 }); i = j + 1; continue;
        }
        i++;
    }
    return out;
}

function pyTokens(src) {
    const texts = [], code = [];
    let i = 0, line = 1, buf = '';
    const flush = () => { if (buf) code.push({ v: buf, line }); buf = ''; };
    while (i < src.length) {
        const c = src[i];
        if (c === '\n') { flush(); line++; i++; continue; }
        if (c === '#') { while (i < src.length && src[i] !== '\n') i++; continue; }
        if (c === '"' || c === "'") {
            const triple = src.slice(i, i + 3) === c.repeat(3);
            const end = triple ? c.repeat(3) : c;
            const l0 = line; let j = i + end.length, v = '';
            while (j < src.length && src.slice(j, j + end.length) !== end) { if (src[j] === '\\') { v += src[j + 1]; j += 2; continue; } if (src[j] === '\n') line++; v += src[j]; j++; }
            texts.push({ v, line: l0 }); buf += ' ';
            i = j + end.length; continue;
        }
        buf += c; i++;
    }
    flush();
    return { texts, code };
}

function checkGo(dir) {
    const violations = [];
    const cmds = new Set(WRITE_COMMANDS);
    const files = fs.readdirSync(dir).filter(f => f.endsWith('.go') && !f.endsWith('_test.go')).sort();
    for (const f of files) {
        for (const t of goTexts(fs.readFileSync(path.join(dir, f), 'utf8'))) {
            if (cmds.has(t.v.toUpperCase())) violations.push(`${f}:${t.line}: write command "${t.v}" in the code of the edge; writes go only through FCALL (schema.json)`);
        }
    }
    return { violations, files: files.length };
}

function checkPy(files) {
    const violations = [];
    const cmds = new Set(WRITE_COMMANDS);
    for (const f of files) {
        const { texts, code } = pyTokens(fs.readFileSync(f, 'utf8'));
        const name = path.basename(f);
        for (const t of texts) {
            if (cmds.has(t.v.toUpperCase())) violations.push(`${name}:${t.line}: write command "${t.v}"; the load writes only through FCALL tv30_profile_write`);
        }
        for (const l of code) {
            for (const m of l.v.matchAll(PY_RECEIVERS)) {
                if (PY_METHODS.has(m[2])) violations.push(`${name}:${l.line}: write method of redis-py ${m[1]}.${m[2]}(); the load writes only through FCALL tv30_profile_write`);
            }
        }
    }
    return { violations, files: files.length };
}

// Configuration of the TV30 modules (root = TV30 folder). A direct write is
// allowed only where the family is out of scope (schema.json,
// outOfScope).
function tv30Modules(root) {
    return {
        tv3ws: {
            root: path.join(root, 'tv3ws'), sources: 'src', adapter: 'src/redis-functions.ts', redisClient: 'src/redis-client.ts',
            directOutOfScope: {
                'src/modules/remotedevice-manager/manager.ts': 'remote-devices:*, remote-device:* (mirror of the remote devices)',
            },
        },
        aop: {
            root: path.join(root, 'aop'), sources: 'src', adapter: 'src/redis-functions.js', redisClient: 'src/redis-client.js',
            directOutOfScope: {},
        },
    };
}

module.exports = {
    WRITE_COMMANDS, JS_METHODS, jsTokens, analyzeJs, checkJsModule, checkGo, checkPy, tv30Modules, schema,
};
