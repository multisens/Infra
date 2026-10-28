#!/usr/bin/env python3
"""
Generates the Redis seed in RESP, for `redis-cli --pipe`: one FCALL
tv30_profile_write per profile, with the arguments of profile_fcall_args
(migrate_to_redis.py).

Usage: emit_seed.py <userData.json> <output.resp>
"""
import json
import sys

from migrate_to_redis import profile_fcall_args


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
        args = profile_fcall_args(user)
        if args:
            cmds.append(resp(*args))

    with open(dst, "wb") as f:
        f.writelines(cmds)
    print(f"seed: {len(users)} users, {len(cmds)} calls of tv30_profile_write -> {dst}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
