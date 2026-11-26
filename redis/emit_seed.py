#!/usr/bin/env python3
"""
Generates the Redis loads in RESP, for `redis-cli --pipe`:
  - the avatar set: one FCALL tv30_avatar_write per image of AVATAR_PALETTE,
    applied on every start of redis;
  - the initial load: one FCALL tv30_profile_write per profile, with the
    arguments of profile_fcall_args (migrate_to_redis.py), applied only on an
    empty database.

Usage: emit_seed.py <user-data.json> <avatars.resp> <seed.resp>
"""
import json
import sys

from migrate_to_redis import profile_fcall_args

SVG_TYPE = 'image/svg+xml'

# The avatar set: name of the image, label in the profile form and color.
AVATAR_PALETTE = [
    ('green.svg', 'Verde', '#4d7c5b'),
    ('blue.svg', 'Azul', '#4d5b7c'),
    ('red.svg', 'Vermelho', '#7c4d4d'),
    ('mustard.svg', 'Mostarda', '#7c704d'),
    ('purple.svg', 'Roxo', '#5b4d7c'),
    ('teal.svg', 'Verde-azulado', '#4d7c70'),
    ('pink.svg', 'Rosa', '#7c4d6e'),
    ('brown.svg', 'Marrom', '#7c6e4d'),
]


def avatar_svg(color: str) -> str:
    """SVG document of an avatar of the set: a figure over the color."""
    return (
        '<svg viewBox="0 0 100 100" preserveAspectRatio="xMidYMid meet" '
        'xmlns="http://www.w3.org/2000/svg">'
        f'<rect width="100" height="100" rx="30" fill="{color}"/>'
        '<circle cx="50" cy="40" r="14" fill="none" stroke="#ffffff" '
        'stroke-width="5"/>'
        '<path d="M 14 90 a 36 36 0 0 1 72 0" fill="none" stroke="#ffffff" '
        'stroke-width="5" stroke-linecap="round"/>'
        '</svg>'
    )


def resp(*args) -> bytes:
    out = [f'*{len(args)}\r\n'.encode()]
    for a in args:
        b = a if isinstance(a, bytes) else str(a).encode('utf-8')
        out.append(f'${len(b)}\r\n'.encode())
        out.append(b)
        out.append(b'\r\n')
    return b''.join(out)


def avatar_commands() -> list:
    commands = []
    for name, label, color in AVATAR_PALETTE:
        command = resp('FCALL', 'tv30_avatar_write', '0', name, label,
                       SVG_TYPE, avatar_svg(color))
        commands.append(command)
    return commands


def profile_commands(user_data_path: str) -> list:
    with open(user_data_path, encoding='utf-8') as f:
        raw = json.load(f)
    users = raw.get('users', raw)

    commands = []
    for user in users:
        args = profile_fcall_args(user)
        if args:
            commands.append(resp(*args))
    return commands


def write_load(path: str, commands: list) -> None:
    with open(path, 'wb') as f:
        f.writelines(commands)


def main(user_data_path: str, avatars_path: str, seed_path: str) -> None:
    avatars = avatar_commands()
    write_load(avatars_path, avatars)
    print(f'avatars: {len(avatars)} calls of tv30_avatar_write -> {avatars_path}')

    profiles = profile_commands(user_data_path)
    write_load(seed_path, profiles)
    print(f'seed: {len(profiles)} calls of tv30_profile_write -> {seed_path}')


if __name__ == '__main__':
    main(sys.argv[1], sys.argv[2], sys.argv[3])
