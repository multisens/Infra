// Minimal RESP2 client, on the node standard library only (runs on a
// node:20-alpine without npm install). One command at a time; a Redis error
// rejects with an Error whose message is the error text and redis = true.
'use strict';
const net = require('net');

function encode(args) {
    const parts = [`*${args.length}\r\n`];
    for (const a of args) {
        const b = Buffer.isBuffer(a) ? a : Buffer.from(String(a), 'utf8');
        parts.push(`$${b.length}\r\n`, b, '\r\n');
    }
    return Buffer.concat(parts.map(p => (Buffer.isBuffer(p) ? p : Buffer.from(p, 'utf8'))));
}

// parse(buf, pos) returns [value, nextPos], or null if bytes are missing.
function parse(buf, pos) {
    const eol = buf.indexOf('\r\n', pos);
    if (eol < 0) return null;
    const type = String.fromCharCode(buf[pos]);
    const line = buf.toString('utf8', pos + 1, eol);
    const next = eol + 2;
    switch (type) {
        case '+': return [line, next];
        case '-': { const e = new Error(line); e.redis = true; return [e, next]; }
        case ':': return [Number(line), next];
        case '$': {
            const n = Number(line);
            if (n < 0) return [null, next];
            if (buf.length < next + n + 2) return null;
            return [buf.toString('utf8', next, next + n), next + n + 2];
        }
        case '*': {
            const n = Number(line);
            if (n < 0) return [null, next];
            const out = [];
            let p = next;
            for (let i = 0; i < n; i++) {
                const r = parse(buf, p);
                if (r === null) return null;
                out.push(r[0]);
                p = r[1];
            }
            return [out, p];
        }
        default: throw new Error(`RESP: tipo desconhecido ${JSON.stringify(type)}`);
    }
}

function connect(host = process.env.REDIS_HOST || '127.0.0.1', port = Number(process.env.REDIS_PORT || 6379)) {
    return new Promise((resolve, reject) => {
        const sock = net.connect(port, host);
        // Accumulated chunks, joined only when there are bytes for the next
        // reply (a large bulk, as in the 20 MB limit test, comes in hundreds of
        // chunks).
        let chunks = [];
        let total = 0;
        let need = 1;
        let buf = Buffer.alloc(0);
        const pending = [];
        sock.on('data', chunk => {
            chunks.push(chunk);
            total += chunk.length;
            if (total < need) return;
            buf = chunks.length === 1 ? chunks[0] : Buffer.concat(chunks, total);
            chunks = [buf];
            for (;;) {
                if (!pending.length || !buf.length) break;
                const r = parse(buf, 0);
                if (r === null) {
                    const m = /^\$(\d+)\r\n/.exec(buf.toString('latin1', 0, Math.min(buf.length, 32)));
                    need = m ? m[0].length + Number(m[1]) + 2 : buf.length + 1;
                    break;
                }
                buf = buf.subarray(r[1]);
                chunks = buf.length ? [buf] : [];
                total = buf.length;
                need = 1;
                const { res, rej } = pending.shift();
                if (r[0] instanceof Error) rej(r[0]); else res(r[0]);
            }
        });
        sock.on('error', err => {
            while (pending.length) pending.shift().rej(err);
            reject(err);
        });
        sock.on('connect', () => {
            const cmd = (...args) => new Promise((res, rej) => {
                pending.push({ res, rej });
                sock.write(encode(args));
            });
            cmd.close = () => new Promise(r => sock.end(r));
            resolve(cmd);
        });
    });
}

module.exports = { connect, encode, parse };
