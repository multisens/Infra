#!/usr/bin/env python3
# Segundo leitor de tests/test_json_payload.c: confere cada veredito do plugin
# contra o json do Python (RFC 8259; NaN e Infinity recusados por parse_constant).
# Divergencia conhecida do json-c tem de ser recusada pelo Python; o resto, mesmo veredito.
#
#   python3 check_json_payload.py /tmp/casos.tsv
import json
import sys


def no_constant(name):
    raise ValueError('constante ' + name)


def python_accepts(raw):
    try:
        json.loads(raw.decode('utf-8'), parse_constant=no_constant)
        return 1
    except ValueError:  # UnicodeDecodeError e JSONDecodeError incluidos
        return 0


cases = diverged = 0
with open(sys.argv[1], encoding='ascii') as f:
    for line in f:
        hx, verdict, known = line.rstrip('\n').split('\t')
        raw = bytes.fromhex(hx)
        cases += 1
        py = python_accepts(raw)
        if (known == '1' and py != 0) or (known == '0' and py != int(verdict)):
            diverged += 1
            print('DIVERGE plugin=%s python=%d conhecida=%s payload=%r' % (verdict, py, known, raw[:80]))
print('python3 json: %d casos, %d divergencia(s)' % (cases, diverged))
sys.exit(1 if diverged else 0)
