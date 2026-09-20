#!/usr/bin/env python3
"""
Gera, EM TEMPO DE BUILD, o seed do Redis em protocolo RESP (consumido por
`redis-cli --pipe` no entrypoint). Reusa a normalizacao de campos do
migrate_to_redis.py (fonte unica da regra nickname/closedCaptioning/etc.),
eliminando a dependencia de rede na partida (antigo defeito 1).

Uso: emit_seed.py <userData.json> <saida.resp>
"""
import json
import sys

from migrate_to_redis import normalize_user  # mesma regra do seed legado


def resp(*args) -> bytes:
    out = [f"*{len(args)}\r\n".encode()]
    for a in args:
        b = a if isinstance(a, bytes) else str(a).encode("utf-8")
        out.append(f"${len(b)}\r\n".encode())
        out.append(b)
        out.append(b"\r\n")
    return b"".join(out)


def main(src: str, dst: str) -> None:
    raw = json.load(open(src, encoding="utf-8"))
    users = raw.get("users", raw)

    cmds = []
    for user in users:
        uid = user.get("id")
        if not uid:
            continue
        cmds.append(resp("SADD", "users:index", uid))

        fields = {k: str(v) for k, v in normalize_user(user).items() if v is not None}
        if fields:
            cmds.append(resp("DEL", f"user:{uid}"))
            flat = [x for kv in fields.items() for x in kv]
            cmds.append(resp("HSET", f"user:{uid}", *flat))

        consent = user.get("accessConsent") or user.get("consent") or []
        if consent:
            # merge (sem DEL): visibilidade concedida fora do JSON sobrevive
            cmds.append(resp("SADD", f"user:{uid}:consent", *consent))

    with open(dst, "wb") as f:
        f.writelines(cmds)
    print(f"seed: {len(users)} usuarios, {len(cmds)} comandos -> {dst}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
