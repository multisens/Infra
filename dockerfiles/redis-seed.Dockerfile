# redis-seed — seeds Redis from userData.json + acl.json.
# The `redis` python dep is baked at BUILD time (was `pip install` at runtime,
# which needed internet on every `up`). Now it runs offline once built.
FROM python:3.11-alpine
RUN pip install --no-cache-dir redis
