#!/usr/bin/env python3
"""
Gera, EM TEMPO DE BUILD, o seed do Redis em protocolo RESP (consumido por
`redis-cli --pipe` no entrypoint). Reusa a normalizacao de campos do
migrate_to_redis.py (fonte unica da regra nickname/closedCaptioning/etc.),
eliminando a dependencia de rede na partida (antigo defeito 1).

P4 (decisao do Luis em 10/10, "Funcoes + auditoria"): cada perfil vira UMA
chamada FCALL tv30_perfil_grava (biblioteca de funcoes do Redis, ver
infra/redis/README.md), que confere o esquema e grava users:index, user:{id}
e user:{id}:consent juntos. Antes eram SADD/DEL/HSET/SADD avulsos. Os
argumentos saem de perfil_fcall_args (migrate_to_redis.py), o mesmo que a
migracao manual usa. Perfil fora do esquema e recusado pela funcao; o build
da imagem confere a carga (confere-imagem.sh) e falha nesse caso.

Uso: emit_seed.py <userData.json> <saida.resp>
"""
import json
import sys

from migrate_to_redis import perfil_fcall_args  # mesma regra da migracao manual


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
        args = perfil_fcall_args(user)
        if args:
            cmds.append(resp(*args))

    with open(dst, "wb") as f:
        f.writelines(cmds)
    print(f"seed: {len(users)} usuarios, {len(cmds)} chamadas tv30_perfil_grava -> {dst}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
