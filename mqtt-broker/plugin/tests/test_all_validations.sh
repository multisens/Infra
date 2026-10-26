#!/bin/bash
# Battery of the sensor/comprehensive/test schema: one valid publication and
# 19 violations, one per keyword of schema_validator.c; no violation is
# delivered, and each gives a message on errors/test-client with the
# expected details. Disposable image and broker, as in test_errors_json.sh;
# the stack is not touched.
#
#   bash infra/mqtt-broker/plugin/tests/test_all_validations.sh
#   IMG=<image> NOBUILD=1 bash ...
#       uses an image already built, and does not remove it
#   KEEP_IMG=1 bash ...
#       does not remove the built image
set -u
AQUI=$(cd "$(dirname "$0")" && pwd)
# infra/mqtt-broker
BROKER_DIR=$(cd "$AQUI/../.." && pwd)
IMG=${IMG:-tv30-mosquitto:teste-validacoes}
P=${PREFIXO:-tv30-teste-validacoes}
NET=$P-net
BRK=$P-broker
LOG=$(mktemp)

cleanup() {
  docker rm -f "$BRK" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  if [ -z "${NOBUILD:-}" ] && [ -z "${KEEP_IMG:-}" ]; then docker image rm "$IMG" >/dev/null 2>&1; fi
  rm -f "$LOG"
}
trap cleanup EXIT
docker rm -f "$BRK" >/dev/null 2>&1
docker network rm "$NET" >/dev/null 2>&1

if [ -z "${NOBUILD:-}" ]; then
  echo "== build $IMG"
  if ! DOCKER_BUILDKIT=1 docker build --progress=plain -t "$IMG" -f "$BROKER_DIR/infra/Dockerfile" "$BROKER_DIR" >"$LOG" 2>&1; then
    tail -40 "$LOG"
    echo "FAIL build da imagem"
    exit 1
  fi
fi

docker network create "$NET" >/dev/null
docker run -d --name "$BRK" --network "$NET" "$IMG" >/dev/null
for _ in $(seq 1 50); do
  docker logs "$BRK" 2>&1 | grep -q "mosquitto version .* running" && break
  sleep 0.2
done
docker logs "$BRK" 2>&1 | grep -E "Schemas loaded|plugin loaded|running" | head -3

# The driver runs in a second container of the same image (python3 and the
# mosquitto_pub/mosquitto_sub clients are there), on the network of the
# disposable broker.
docker run --rm -i --network "$NET" --entrypoint python3 "$IMG" - "$BRK" <<'PY'
import json, queue, subprocess, sys, threading, time

HOST = sys.argv[1]
TOPIC = 'sensor/comprehensive/test'
CLIENT = 'test-client'
ERR_TOPIC = 'errors/' + CLIENT
BASE = ('"string_field": "hello", "number_field": 50, "integer_field": 25, "boolean_field": true, '
        '"enum_field": "option1", "const_field": "fixed_value"')

# (name, payload, expected details; None for a valid, delivered publication)
CASES = [
    ('TESTE 1: Mensagem válida completa',
     '{\n  "string_field": "hello",\n  "number_field": 50,\n  "integer_field": 25,\n  "boolean_field": true,\n'
     '  "enum_field": "option2",\n  "const_field": "fixed_value",\n  "array_field": [1, 2, 3],\n'
     '  "nested_object": {\n    "nested_string": "test",\n    "nested_number": 5\n  }\n}', None),
    ('TESTE 2: string_field - minLength violation', BASE.replace('"hello"', '"ab"'),
     "Property 'string_field': String length 2 is less than minLength 3"),
    ('TESTE 3: string_field - maxLength violation', BASE.replace('"hello"', '"verylongstringthatexceedslimit"'),
     "Property 'string_field': String length 30 is greater than maxLength 20"),
    ('TESTE 4: string_field - pattern violation', BASE.replace('"hello"', '"Hello123"'),
     "Property 'string_field': String 'Hello123' does not match pattern '^[a-z]+$'"),
    ('TESTE 5: number_field - minimum violation', BASE.replace('"number_field": 50', '"number_field": 5'),
     "Property 'number_field': Value 5.00 is less than minimum 10.00"),
    ('TESTE 6: number_field - maximum violation', BASE.replace('"number_field": 50', '"number_field": 150'),
     "Property 'number_field': Value 150.00 is greater than maximum 100.00"),
    ('TESTE 7: number_field - multipleOf violation', BASE.replace('"number_field": 50', '"number_field": 47'),
     "Property 'number_field': Value 47.00 is not a multiple of 5.00"),
    ('TESTE 8: integer_field - exclusiveMinimum violation', BASE.replace('"integer_field": 25', '"integer_field": 0'),
     "Property 'integer_field': Value 0.00 must be greater than 0.00"),
    ('TESTE 9: integer_field - exclusiveMaximum violation', BASE.replace('"integer_field": 25', '"integer_field": 50'),
     "Property 'integer_field': Value 50.00 must be less than 50.00"),
    ('TESTE 10: boolean_field - type violation', BASE.replace('"boolean_field": true', '"boolean_field": "not_a_bool"'),
     "Property 'boolean_field': Expected type 'boolean', got 'string'"),
    ('TESTE 11: enum_field - invalid value', BASE.replace('"option1"', '"invalid_option"'),
     "Property 'enum_field': Value is not in enum"),
    ('TESTE 12: const_field - wrong value', BASE.replace('"fixed_value"', '"wrong_value"'),
     "Property 'const_field': Value does not match const"),
    ('TESTE 13: array_field - minItems violation', BASE + ', "array_field": [1]',
     "Property 'array_field': Array length 1 is less than minItems 2"),
    ('TESTE 14: array_field - maxItems violation', BASE + ', "array_field": [1,2,3,4,5,6]',
     "Property 'array_field': Array length 6 is greater than maxItems 5"),
    ('TESTE 15: array_field - uniqueItems violation', BASE + ', "array_field": [1,2,2,3]',
     "Property 'array_field': Array contains duplicate items"),
    ('TESTE 16: array_field - item validation (value > 10)', BASE + ', "array_field": [1,2,15]',
     "Property 'array_field': Item at index 2: Value 15.00 is greater than maximum 10.00"),
    ('TESTE 17: required field missing', BASE.replace('"string_field": "hello", ', ''),
     "Missing required property 'string_field'"),
    ('TESTE 18: additionalProperties violation', BASE + ', "extra_field": "not_allowed"',
     "Additional property 'extra_field' not allowed"),
    ('TESTE 19: nested_object - missing required field', BASE + ', "nested_object": {"nested_number": 5}',
     "Property 'nested_object': Missing required property 'nested_string'"),
    ('TESTE 20: nested_object - maxProperties violation',
     BASE + ', "nested_object": {"nested_string": "test", "nested_number": 5, "extra": "field"}',
     "Property 'nested_object': Object has 3 properties, maximum is 2"),
]
CASES = [(n, p if p.startswith('{') else '{' + p + '}', d) for n, p, d in CASES]

passes = fails = 0


def check(cond, name, detail=''):
    global passes, fails
    if cond:
        passes += 1
        print('PASS ' + name, flush=True)
    else:
        fails += 1
        print('FAIL ' + name + ('  -- ' + detail if detail else ''), flush=True)


sub = subprocess.Popen(['mosquitto_sub', '-h', HOST, '-q', '0', '-t', ERR_TOPIC, '-t', TOPIC,
                        '-t', 'zz/teste-validacoes/#', '-F', '%t\t%x'],
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
inbox = queue.Queue()


def reader():
    for raw in sub.stdout:
        line = raw.rstrip(b'\n')
        if b'\t' in line:
            t, hx = line.rsplit(b'\t', 1)
            inbox.put((t.decode('utf-8', 'replace'), bytes.fromhex(hx.decode('ascii'))))


threading.Thread(target=reader, daemon=True).start()


def pub(topic, payload, client=CLIENT):
    r = subprocess.run(['mosquitto_pub', '-h', HOST, '-q', '0', '-i', client, '-t', topic, '-m', payload],
                       capture_output=True, timeout=15)
    return r.returncode


def collect(until, timeout=6.0):
    got, end = [], time.time() + timeout
    while time.time() < end:
        try:
            t, p = inbox.get(timeout=max(0.05, end - time.time()))
        except queue.Empty:
            break
        got.append((t, p))
        if t == until:
            break
    return got


ready = False
for i in range(60):
    pub('zz/teste-validacoes/pronto', str(i), 'tv30-validacoes-pronto')
    if any(t == 'zz/teste-validacoes/pronto' for t, _ in collect('zz/teste-validacoes/pronto', 0.5)):
        ready = True
        break
check(ready, 'assinante ativo em ' + ERR_TOPIC + ' e ' + TOPIC)
if not ready:
    print('RESUMO: %d PASS, %d FAIL' % (passes, fails))
    sys.exit(1)
while not inbox.empty():
    inbox.get()

for i, (name, payload, details) in enumerate(CASES):
    print('=== ' + name, flush=True)
    check(pub(TOPIC, payload) == 0, name + ': mosquitto_pub rc=0')
    marker = 'zz/teste-validacoes/fim/%d' % i
    pub(marker, '.', 'tv30-validacoes-marcador')
    got = collect(marker)
    errs = [p for t, p in got if t == ERR_TOPIC]
    delivered = any(t == TOPIC and p == payload.encode() for t, p in got)
    if details is None:
        check(delivered, name + ': entregue ao assinante')
        check(not errs, name + ': nenhuma mensagem em ' + ERR_TOPIC, repr(errs[:1])[:200])
        continue
    check(not delivered, name + ': recusada (nao entregue)')
    check(len(errs) == 1, name + ': exatamente uma mensagem em ' + ERR_TOPIC + ' (%d)' % len(errs))
    if not errs:
        continue
    try:
        msg = json.loads(errs[0].decode('utf-8'))
    except ValueError as e:
        check(False, name + ': mensagem e JSON valido', '%s; %r' % (e, errs[0][:160]))
        continue
    check(msg.get('error_type') == 'SCHEMA_VALIDATION_FAILED', name + ': error_type SCHEMA_VALIDATION_FAILED', repr(msg.get('error_type')))
    check(msg.get('details') == details, name + ': details', repr(msg.get('details')))
    check(msg.get('topic') == TOPIC and msg.get('client_id') == CLIENT and msg.get('payload') == payload,
          name + ': topic, client_id e payload', repr(msg)[:200])

sub.terminate()
print('RESUMO: %d PASS, %d FAIL' % (passes, fails), flush=True)
sys.exit(1 if fails else 0)
PY
rc=$?
echo "recusas registradas no log do broker: $(docker logs "$BRK" 2>&1 | grep -c 'Validation FAILED for topic')"
echo "=== Testes concluídos (rc=$rc) ==="
exit $rc
