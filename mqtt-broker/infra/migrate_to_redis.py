#!/usr/bin/env python3
"""
Migration of the profiles of a file to Redis. Writes, by FCALL
tv30_perfil_grava, which checks the schema, the keys:
  - users:index (SET): every user id;
  - user:{userId} (HASH): the profile attributes (Table 7);
  - user:{userId}:consent (SET): services to which the profile is visible.
"""

import json

import sys

# Legacy field names mapped to the attribute names of Table 7.
FIELD_MAP = {
    'name':               'nickname',
    'captions':           'closedCaptioning',
    # Alias; closedCaptioning prevails.
    'subtitle':           'closedCaptioning',
    'signLanguageWindow': 'closedSigning',
    'dialogueEnhancement':'dialogEnhancement',
    'language':           'audioLanguage',
}

# Fields that do not go in the hash.
EXCLUDED_FIELDS = {'accessConsent', 'consent'}


def normalize_user(user: dict) -> dict:
    """
    Renames the legacy fields to the attribute names of Table 7. A new field
    prevails over a legacy one.
    """
    result = {}

    for old_key, new_key in FIELD_MAP.items():
        if old_key in user and new_key not in user:
            result[new_key] = user[old_key]

    for key, val in user.items():
        if key in EXCLUDED_FIELDS:
            continue
        target_key = key if key not in FIELD_MAP else FIELD_MAP[key]
        if target_key not in result or key == target_key:
            result[target_key] = val

    if 'id' in user:
        result['id'] = user['id']

    return result


def redis_text(v) -> str:
    """Value as text in Redis. A JSON boolean becomes 'true'/'false', as the
    schema requires."""
    if isinstance(v, bool):
        return 'true' if v else 'false'
    return str(v)


def perfil_fcall_args(user: dict):
    """Arguments of FCALL tv30_perfil_grava for a profile of userData.json, or
    None if the profile has no id. Format of the function:
      FCALL tv30_perfil_grava 0 <id> <n> <service 1..n> <field> <value> ...
    users:index gets the id, user:{id} is rewritten whole and the visibility
    (accessConsent) is added to user:{id}:consent, without removing what was
    granted outside the JSON."""
    user_id = user.get('id')
    if not user_id:
        return None
    fields = {k: redis_text(v) for k, v in normalize_user(user).items() if v is not None}
    consent = [redis_text(s) for s in (user.get('accessConsent') or user.get('consent') or [])]
    flat = [x for kv in fields.items() for x in kv]
    return ['FCALL', 'tv30_perfil_grava', '0', redis_text(user_id), str(len(consent)), *consent, *flat]


def migrate(r, user_data_path: str) -> None:
    with open(user_data_path, 'r') as f:
        raw = json.load(f)

    # Either {users: [...]} or [...].
    users = raw.get('users', raw)

    # One function call per profile, atomic over users:index, user:{id} and
    # user:{id}:consent. The visibility is merged, without DEL.
    pipe = r.pipeline()
    for user in users:
        args = perfil_fcall_args(user)
        if args:
            pipe.execute_command(*args)

    pipe.execute()

    print(f"[Users]   Migrated {len(users)} user entries")
    print("[Redis]   Migration completed successfully")


if __name__ == '__main__':
    # Imported here, so that normalize_user can be imported without it.
    import redis
    r = redis.Redis(host='redis', port=6379, decode_responses=True)

    user_data_path = '/mosquitto/config/userData.json'

    try:
        migrate(r, user_data_path)
    except Exception as e:
        print(f"[Redis]   Migration failed: {e}", file=sys.stderr)
        sys.exit(1)
