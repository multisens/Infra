#!/usr/bin/env python3
"""
Migração de dados de arquivo para Redis — TV 3.0
Popula as chaves Redis conforme o schema definido na estratégia de persistência:
  - acl:{userId}                              SET  — padrões MQTT permitidos
  - users:index                               SET  — todos os IDs de usuário
  - user:{userId}                             HASH — atributos do perfil (ABNT NBR 25608)
  - user:{userId}:consent                     SET  — IDs de serviço com consentimento

Executa no boot do container Mosquitto (entrypoint.sh).
"""

import json
import redis
import sys

# Mapeamento de campos legados para o schema ABNT NBR 25608
FIELD_MAP = {
    'name':               'nickname',
    'captions':           'closedCaptioning',
    'subtitle':           'closedCaptioning',     # alias — closedCaptioning prevalece
    'signLanguageWindow': 'closedSigning',
    'dialogueEnhancement':'dialogEnhancement',
    'language':           'audioLanguage',
}

# Campos que não entram no Hash (tratados separadamente ou ignorados)
EXCLUDED_FIELDS = {'accessConsent', 'consent'}


def normalize_user(user: dict) -> dict:
    """
    Normaliza campos legados para o schema NBR 25608.
    Campos novos têm precedência sobre campos legados.
    """
    result = {}

    # Primeiro passo: converte campos legados
    for old_key, new_key in FIELD_MAP.items():
        if old_key in user and new_key not in user:
            result[new_key] = user[old_key]

    # Segundo passo: aplica campos nativos (sobrescrevem legados)
    for key, val in user.items():
        if key in EXCLUDED_FIELDS:
            continue
        # Mantém o nome original se não há mapeamento ou se é um campo novo
        target_key = key if key not in FIELD_MAP else FIELD_MAP[key]
        if target_key not in result or key == target_key:
            result[target_key] = val

    # Garante que o id está no hash
    if 'id' in user:
        result['id'] = user['id']

    return result


def migrate(r: redis.Redis, acl_path: str, user_data_path: str) -> None:
    # ------------------------------------------------------------------ ACL --
    with open(acl_path, 'r') as f:
        acl_data = json.load(f)

    pipe = r.pipeline()
    for user_id, patterns in acl_data['acl'].items():
        key = f'acl:{user_id}'
        pipe.delete(key)
        if patterns:
            pipe.sadd(key, *patterns)
    pipe.execute()

    print(f"[ACL]     Migrated {len(acl_data['acl'])} entries")

    # --------------------------------------------------------------- Users --
    with open(user_data_path, 'r') as f:
        raw = json.load(f)

    users = raw.get('users', raw)  # suporta tanto {users:[...]} quanto [...]

    pipe = r.pipeline()
    for user in users:
        user_id = user.get('id')
        if not user_id:
            continue

        # users:index
        pipe.sadd('users:index', user_id)

        # user:{id} — Hash com atributos normalizados
        fields = normalize_user(user)
        hash_fields = {k: str(v) for k, v in fields.items() if v is not None}
        if hash_fields:
            pipe.delete(f'user:{user_id}')
            pipe.hset(f'user:{user_id}', mapping=hash_fields)

        # user:{id}:consent — Set de IDs de serviço
        # Merge (SADD sem DEL): consents concedidos fora do JSON sobrevivem
        # ao re-seed. Mesmo comportamento do CCWS syncUsersFromFile.
        consent = user.get('accessConsent') or user.get('consent') or []
        if consent:
            pipe.sadd(f'user:{user_id}:consent', *consent)

    pipe.execute()

    print(f"[Users]   Migrated {len(users)} user entries")
    print("[Redis]   Migration completed successfully")


if __name__ == '__main__':
    r = redis.Redis(host='redis', port=6379, decode_responses=True)

    acl_path       = '/mosquitto/config/acl.json'
    user_data_path = '/mosquitto/config/userData.json'

    try:
        migrate(r, acl_path, user_data_path)
    except Exception as e:
        print(f"[Redis]   Migration failed: {e}", file=sys.stderr)
        sys.exit(1)
