#!/usr/bin/env python3
# Segundo leitor do teste de unidade de json_payload_parse (correcao de 12/10,
# revisao do plugin): confere, caso a caso, o veredito do plugin contra o json
# do Python, independente do json-c. Le o arquivo que
# tests/test_json_payload.c grava (payload em hexadecimal, veredito 0/1,
# divergencia conhecida 0/1). O json.loads do Python segue a RFC 8259 no que
# importa aqui: so espaco, tab, LF e CR em volta do valor, nada depois dele,
# controle cru dentro de string recusado, texto em UTF-8; NaN e Infinity sao
# recusados com parse_constant. Caso marcado como divergencia conhecida (o
# json-c 0.15 estrito aceita, json_payload.h registra) tem de ser recusado
# pelo Python; os demais, o mesmo veredito. Roda no build (infra/Dockerfile).
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
