#!/usr/bin/env python3
"""
Migração de dados de arquivo para Redis — TV 3.0
Popula as chaves Redis conforme o schema definido na estratégia de persistência:
  - users:index                               SET  — todos os IDs de usuário
  - user:{userId}                             HASH — atributos do perfil (ABNT NBR 25608)
  - user:{userId}:consent                     SET  — IDs de serviço com consentimento

Não roda sozinho: fica embarcado na imagem do broker só para depuração manual
(mqtt-broker/infra/entrypoint.sh). A carga inicial de verdade é do container
redis (infra/redis/emit_seed.py, que reaproveita normalize_user e
perfil_fcall_args daqui).

P4 (decisão do Luis em 10/10, "Funções + auditoria"): a escrita passa pela
função tv30_perfil_grava da biblioteca de funções do Redis (infra/redis), que
confere o esquema (infra/redis/esquema.json) e grava as três chaves juntas.
Perfil fora do esquema é recusado com erro TV30ESQUEMA e nada é gravado.
"""

import json

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


def redis_text(v) -> str:
    """Valor como texto no Redis. Booleano do JSON vira 'true'/'false', como o
    AoP grava e o esquema exige; o str() do Python daria 'True'/'False' (era o
    que a carga gravava até o P4)."""
    if isinstance(v, bool):
        return 'true' if v else 'false'
    return str(v)


def perfil_fcall_args(user: dict):
    """Argumentos de FCALL tv30_perfil_grava para um perfil do userData.json, ou
    None se o perfil não tem id. Formato da função (infra/redis/esquema.json):
      FCALL tv30_perfil_grava 0 <id> <n> <serviço 1..n> <campo> <valor> ...
    Semântica igual à da carga antiga: users:index recebe o id, user:{id} é
    regravado inteiro e a visibilidade (accessConsent) é ACRESCENTADA a
    user:{id}:consent (sem apagar a concedida fora do JSON)."""
    user_id = user.get('id')
    if not user_id:
        return None
    fields = {k: redis_text(v) for k, v in normalize_user(user).items() if v is not None}
    consent = [redis_text(s) for s in (user.get('accessConsent') or user.get('consent') or [])]
    flat = [x for kv in fields.items() for x in kv]
    return ['FCALL', 'tv30_perfil_grava', '0', redis_text(user_id), str(len(consent)), *consent, *flat]


def migrate(r, user_data_path: str) -> None:
    # --------------------------------------------------------------- Users --
    with open(user_data_path, 'r') as f:
        raw = json.load(f)

    users = raw.get('users', raw)  # suporta tanto {users:[...]} quanto [...]

    # Uma chamada da função por perfil (users:index, user:{id} e
    # user:{id}:consent juntos e atômicos). Merge da visibilidade (sem DEL):
    # a concedida fora do JSON sobrevive ao re-seed.
    pipe = r.pipeline()
    for user in users:
        args = perfil_fcall_args(user)
        if args:
            pipe.execute_command(*args)

    pipe.execute()

    print(f"[Users]   Migrated {len(users)} user entries")
    print("[Redis]   Migration completed successfully")


if __name__ == '__main__':
    import redis  # so o executor precisa da lib; normalize_user importavel sem ela
    r = redis.Redis(host='redis', port=6379, decode_responses=True)

    user_data_path = '/mosquitto/config/userData.json'

    try:
        migrate(r, user_data_path)
    except Exception as e:
        print(f"[Redis]   Migration failed: {e}", file=sys.stderr)
        sys.exit(1)
