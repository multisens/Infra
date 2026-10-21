#!/usr/bin/env python3
# Driver de test_errors_json.sh, num container da imagem do broker, na rede do
# broker descartavel:  python3 errors_json_driver.py <host-do-broker>
# Saida: uma linha PASS/FAIL por conferencia e o resumo; codigo 0 so sem FAIL.
import json
import os
import queue
import subprocess
import sys
import tempfile
import threading
import time

HOST = sys.argv[1] if len(sys.argv) > 1 else 'localhost'
LIMIT = 2047  # ERROR_JSON_CAP - 1 (plugin/include/error_json.h)
TOPIC_MAX, CLIENT_MAX = 512, 256  # tetos de error_json.h (bytes escapados)

passes, fails = 0, 0

# Teste de latencia (response_time_tester.c): (nome, id, iteracao, topico da
# resposta). O topico da resposta e PluginResponseTime<id>/iteration<N>, com o
# id lido como C string (para no NUL).
RT_CASES = [
    ('latencia: id com aspas e barra invertida', 'rt"q\\b', 1, 'PluginResponseTimert"q\\b/iteration1'),
    ('latencia: id com NUL escapado no meio', 'rt\x00x', 2, 'PluginResponseTimert/iteration2'),
    ('latencia: id comum, como o do brokertimetest', 'perftest1cn1', 3, 'PluginResponseTimeperftest1cn1/iteration3'),
]


def check(cond, name, detail=''):
    global passes, fails
    if cond:
        passes += 1
        print('PASS ' + name, flush=True)
    else:
        fails += 1
        print('FAIL ' + name + ('  -- ' + detail if detail else ''), flush=True)


# ---------------------------------------------------------------- assinante --
# Topico, TAB (caractere real no formato) e payload em hexadecimal (%x): os
# bytes exatos de cada mensagem, sem depender de quebra de linha no payload.
sub = subprocess.Popen(
    ['mosquitto_sub', '-h', HOST, '-q', '0',
     '-t', 'errors/#', '-t', 'sensor/room1/temperature', '-t', 'aop/display/layers/popup/#',
     '-t', 'aop/currentUser', '-t', 'zz/teste-errors-json/#', '-t', 'tlm/lls/svc1/bamt']
    + [a for c in RT_CASES for a in ('-t', c[3])]
    + ['-F', '%t\t%x'],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
inbox = queue.Queue()


def reader():
    for raw in sub.stdout:
        line = raw.rstrip(b'\n')
        if b'\t' not in line:
            continue
        t, hx = line.rsplit(b'\t', 1)
        try:
            inbox.put((t.decode('utf-8', 'surrogateescape'), bytes.fromhex(hx.decode('ascii'))))
        except ValueError:
            inbox.put((t.decode('utf-8', 'surrogateescape'), None))


threading.Thread(target=reader, daemon=True).start()


def publish(topic, payload, client='tv30-teste-pub'):
    with tempfile.NamedTemporaryFile(delete=False) as f:
        f.write(payload)
        name = f.name
    try:
        r = subprocess.run(['mosquitto_pub', '-h', HOST, '-q', '0', '-i', client, '-t', topic, '-f', name],
                           capture_output=True, timeout=15)
        return r.returncode, r.stderr.decode('utf-8', 'replace').strip()
    finally:
        os.unlink(name)


def collect(until_topic, timeout=6.0):
    """Le mensagens ate chegar uma em until_topic (ou estourar o prazo)."""
    got, end = [], time.time() + timeout
    while time.time() < end:
        try:
            t, p = inbox.get(timeout=max(0.05, end - time.time()))
        except queue.Empty:
            break
        got.append((t, p))
        if t == until_topic:
            break
    return got


# espera o assinante ficar ativo
ready = False
for i in range(60):
    publish('zz/teste-errors-json/pronto', b'%d' % i)
    for t, _ in collect('zz/teste-errors-json/pronto', 0.5):
        if t == 'zz/teste-errors-json/pronto':
            ready = True
    if ready:
        break
check(ready, 'assinante ativo em errors/# e nos topicos de teste')
if not ready:
    print('RESUMO: %d PASS, %d FAIL' % (passes, fails))
    sys.exit(1)
while not inbox.empty():
    inbox.get()

FFFD = '\ufffd'
BAMT_BAD = ('[{"globalServiceId":"g","appVersion":1,"appName":"n","appIcon":"i","bannerIcon":"b",'
            '"backgroundColor":"x\\"y\\\\z\\n","foregroundColor":"#000000"}]').encode()
BAMT_DETAILS = ("Item at index 0: Property 'backgroundColor': String 'x\"y\\z\n' "
                "does not match pattern '^#[0-9A-Fa-f]{6}$'")
BAMT_OK = ('[{"globalServiceId":"g","appVersion":1,"appName":"n","appIcon":"i","bannerIcon":"b",'
           '"backgroundColor":"#000000","foregroundColor":"#ffffff"}]').encode()

# (nome, topico, client_id, payload, esperado). esperado: dict com os campos
# a conferir na mensagem de erro, ou None = publicacao aceita (sem erro).
CASES = [
    ('aspas, barra e controles no payload', 'sensor/room1/temperature', 'tv30-t-aspas',
     b'he said "hi" \\ back\n\ttab\r\x01\x1f\x7f fim',
     dict(error_type='INVALID_JSON', payload='he said "hi" \\ back\n\ttab\r\x01\x1f\x7f fim')),
    ('aspas e barra no details (chave de objeto)', 'sensor/room1/temperature', 'tv30-t-details',
     b'{"value":1,"a\\"b\\\\c":2}',
     dict(error_type='SCHEMA_VALIDATION_FAILED', details="Additional property 'a\"b\\c' not allowed",
          payload='{"value":1,"a\\"b\\\\c":2}')),
    ('aspas, barra e quebra de linha no details (pattern)', 'tlm/lls/svc1/bamt', 'tv30-t-pattern',
     BAMT_BAD, dict(error_type='SCHEMA_VALIDATION_FAILED', details=BAMT_DETAILS, payload=BAMT_BAD.decode())),
    ('NUL no meio do payload', 'sensor/room1/temperature', 'tv30-t-nul',
     b'abc\x00def', dict(error_type='INVALID_JSON', payload='abc\x00def')),
    ('UTF-8 invalido no payload', 'sensor/room1/temperature', 'tv30-t-utf8inv',
     b'\xff"q\xc3(', dict(error_type='INVALID_JSON', payload=FFFD + '"q' + FFFD + '(')),
    ('UTF-8 valido no payload', 'sensor/room1/temperature', 'tv30-t-utf8',
     'a\u00e7\u00e3o \u20ac \U0001F600 "x"'.encode(),
     dict(error_type='INVALID_JSON', payload='a\u00e7\u00e3o \u20ac \U0001F600 "x"')),
    ('aspas e barra no topico', 'tlm/sls/x"y\\z/esg', 'tv30-t-topico',
     b'nao e json "q"', dict(error_type='INVALID_JSON', topic='tlm/sls/x"y\\z/esg', payload='nao e json "q"')),
    ('aspas e barra no client_id', 'sensor/room1/temperature', 'cli"q\\b',
     b'nope', dict(error_type='INVALID_JSON', client_id='cli"q\\b', payload='nope')),
    ('texto puro com aspas fora do maxLength', 'aop/currentUser', 'tv30-t-texto',
     b'u' * 65 + b'"\\', dict(error_type='SCHEMA_VALIDATION_FAILED',
                             details='String length 67 is greater than maxLength 64', payload='u' * 65 + '"\\')),
    ('payload de 1800 bytes cabe inteiro', 'sensor/room1/temperature', 'tv30-t-1800',
     b'a' * 1800, dict(error_type='INVALID_JSON', payload='a' * 1800)),
    ('payload de 2000 bytes: mensagem passaria de 2047', 'sensor/room1/temperature', 'tv30-t-2000',
     b'a' * 2000, dict(error_type='INVALID_JSON', cut_unit='a')),
    # o '[' na frente deixa o payload invalido de saida: so aspas comecariam
    # com "" (um texto JSON) seguido de sobra
    ('3000 aspas (escape dobra o tamanho)', 'sensor/room1/temperature', 'tv30-t-aspas3000',
     b'[' + b'"' * 3000, dict(error_type='INVALID_JSON', cut_unit='"', cut_prefix='[')),
    ('1000 bytes 0x01 (escape de 6 bytes)', 'sensor/room1/temperature', 'tv30-t-ctl1000',
     b'\x01' * 1000, dict(error_type='INVALID_JSON', cut_unit='\x01')),
    ('800 emojis (4 bytes cada)', 'sensor/room1/temperature', 'tv30-t-emoji',
     '\U0001F600'.encode() * 800, dict(error_type='INVALID_JSON', cut_unit='\U0001F600')),
    ('topico de 612 bytes', 'tlm/sls/' + 'x' * 600 + '/esg', 'tv30-t-topicolongo',
     b'nope', dict(error_type='INVALID_JSON', payload='nope', topic_cut=True)),
    ('client_id de 300 bytes', 'sensor/room1/temperature', 'c' * 300,
     b'nope', dict(error_type='INVALID_JSON', payload='nope', client_cut=True)),
    # payload lido inteiro: sobra depois do valor e INVALID_JSON
    ('JSON com texto depois', 'sensor/room1/temperature', 'tv30-t-lixo',
     b'{"value":1}lixo', dict(error_type='INVALID_JSON', payload='{"value":1}lixo')),
    ('JSON com NUL e texto depois', 'sensor/room1/temperature', 'tv30-t-nul-lixo',
     b'{"value":1}\x00lixo', dict(error_type='INVALID_JSON', payload='{"value":1}\x00lixo')),
    ('JSON com comentario depois', 'sensor/room1/temperature', 'tv30-t-comentario',
     b'{"value":1}/*lixo*/', dict(error_type='INVALID_JSON', payload='{"value":1}/*lixo*/')),
    ('JSON com \\f depois', 'sensor/room1/temperature', 'tv30-t-ff',
     b'{"value":1}\x0c', dict(error_type='INVALID_JSON', payload='{"value":1}\x0c')),
    ('dois valores JSON', 'sensor/room1/temperature', 'tv30-t-dois',
     b'{"value":1}{"value":99}', dict(error_type='INVALID_JSON', payload='{"value":1}{"value":99}')),
    ('texto vazio "" seguido de texto', 'sensor/room1/temperature', 'tv30-t-vazio-lixo',
     b'""qualquer coisa', dict(error_type='INVALID_JSON', payload='""qualquer coisa')),
    ('bamt valido com texto depois', 'tlm/lls/svc1/bamt', 'tv30-t-bamt-lixo',
     BAMT_OK + b'lixo', dict(error_type='INVALID_JSON', payload=BAMT_OK.decode() + 'lixo')),
    ('texto puro com NUL no meio', 'aop/currentUser', 'tv30-t-user-nul',
     b'u\x00' + b'x' * 100, dict(error_type='INVALID_JSON', payload='u\x00' + 'x' * 100)),
    ('texto puro que comeca como JSON passa do maxLength', 'aop/currentUser', 'tv30-t-user-prefixo',
     b'"abc"' + b'x' * 70, dict(error_type='SCHEMA_VALIDATION_FAILED',
                                details='String length 75 is greater than maxLength 64', payload='"abc"' + 'x' * 70)),
    ('JSON com espaco, tab, CR e LF em volta passa', 'sensor/room1/temperature', 'tv30-t-ws',
     b' \t\r\n{"value":21} \t\r\n', None),
    ('UUID com digito na frente passa em aop/currentUser', 'aop/currentUser', 'tv30-t-uuid',
     b'3f2a1b9c-0000-4000-8000-000000000000', None),
    ('bamt de JSON.stringify passa', 'tlm/lls/svc1/bamt', 'tv30-t-bamt-ok', BAMT_OK, None),
    ('publicacao valida e entregue', 'sensor/room1/temperature', 'tv30-t-valida', b'{"value":20}', None),
    ('fechamento do pop-up do QR code passa', 'aop/display/layers/popup/qrcode', 'tv30-t-fecha-qr', b'{"close":true}', None),
    ('fechamento do pop-up do PIN passa', 'aop/display/layers/popup/pin', 'tv30-t-fecha-pin', b'{"close":true}', None),
]


def validate(name, raw, want, topic, client):
    check(raw is not None, name + ': payload hexadecimal legivel')
    if raw is None:
        return
    check(len(raw) <= LIMIT, name + ': mensagem com %d bytes (<= %d)' % (len(raw), LIMIT))
    try:
        text = raw.decode('utf-8')  # estrito
    except UnicodeDecodeError as e:
        check(False, name + ': UTF-8 valido', str(e))
        return
    check(True, name + ': UTF-8 valido')
    try:
        msg = json.loads(text)  # estrito: recusa controle cru dentro de texto
    except ValueError as e:
        check(False, name + ': JSON valido (json.loads)', '%s; inicio: %r' % (e, raw[:160]))
        return
    check(True, name + ': JSON valido (json.loads)')
    tool = subprocess.run([sys.executable, '-m', 'json.tool'], input=raw, capture_output=True)
    check(tool.returncode == 0, name + ': JSON valido (python3 -m json.tool)', tool.stderr.decode('utf-8', 'replace')[:200])
    check(isinstance(msg, dict), name + ': objeto JSON')
    if not isinstance(msg, dict):
        return
    check(set(msg) >= {'timestamp', 'topic', 'error_type', 'details', 'client_id', 'payload'},
          name + ': campos presentes', repr(sorted(msg)))
    check(msg.get('error_type') == want['error_type'], name + ': error_type ' + want['error_type'], repr(msg.get('error_type')))
    if want.get('topic_cut'):
        check(len(json.dumps(msg.get('topic', ''), ensure_ascii=False)) - 2 <= TOPIC_MAX and topic.startswith(msg.get('topic', '#')),
              name + ': topico cortado no teto, como prefixo', repr(msg.get('topic', ''))[:80])
    else:
        check(msg.get('topic') == want.get('topic', topic), name + ': topic decodificado igual', repr(msg.get('topic'))[:120])
    if want.get('client_cut'):
        check(len(msg.get('client_id', '')) == CLIENT_MAX and client.startswith(msg.get('client_id', '#')),
              name + ': client_id cortado no teto, como prefixo', repr(msg.get('client_id', ''))[:80])
    else:
        check(msg.get('client_id') == want.get('client_id', client), name + ': client_id decodificado igual', repr(msg.get('client_id')))
    if 'details' in want:
        check(msg.get('details') == want['details'], name + ': details decodificado igual', repr(msg.get('details')))
    cut = bool(want.get('cut_unit') or want.get('topic_cut') or want.get('client_cut'))
    check(msg.get('truncated', False) is cut, name + ': truncated=%s' % cut, repr(msg.get('truncated')))
    if 'payload' in want:
        check(msg.get('payload') == want['payload'], name + ': payload decodificado igual',
              '%d chars: %r' % (len(msg.get('payload', '')), msg.get('payload', '')[:80]))
    if want.get('cut_unit'):
        u, pre, pl = want['cut_unit'], want.get('cut_prefix', ''), msg.get('payload', '')
        rest = pl[len(pre):]
        check(pl.startswith(pre) and rest and rest == u * (len(rest) // len(u)),
              name + ': payload cortado so entre unidades inteiras', repr(pl[-12:]))
        check(len(raw) >= LIMIT - 6, name + ': o corte usa o espaco (%d bytes)' % len(raw))


for name, topic, client, payload, want in CASES:
    marker = 'zz/teste-errors-json/fim/%d' % CASES.index((name, topic, client, payload, want))
    rc, err = publish(topic, payload, client)
    check(rc == 0, name + ': mosquitto_pub rc=0', err)
    publish(marker, b'.')
    got = collect(marker)
    err_topic = 'errors/' + client
    errs = [p for t, p in got if t.startswith('errors/')]
    if want is None:
        check(not errs, name + ': nenhuma mensagem em errors/', repr(errs[:1])[:200])
        check(any(t == topic and p == payload for t, p in got), name + ': entregue ao assinante de ' + topic)
        continue
    check(len(errs) == 1, name + ': exatamente uma mensagem em errors/ (%d)' % len(errs))
    check(any(t == err_topic[:255] for t, p in got if t.startswith('errors/')), name + ': no topico errors/<client_id>',
          repr([t for t, _ in got][:3])[:200])
    check(not any(t == topic for t, _ in got), name + ': publicacao recusada (nao entregue)')
    if errs:
        validate(name, errs[0], want, topic, client)

# ----------------------------------------------------- teste de latencia --
# A resposta em PluginResponseTime<id>/iteration<N> tem de ser JSON valido com
# o id inteiro.
for i, (name, rid, it, rtopic) in enumerate(RT_CASES):
    body = json.dumps({'testResponsetime': True, 'localtime': '2026-10-12 10:00:00.000', 'id': rid, 'iteration': it})
    rc, err = publish('PublisherResponseTimezz/iteration%d' % it, body.encode(), 'tv30-t-latencia-%d' % i)
    check(rc == 0, name + ': mosquitto_pub rc=0', err)
    marker = 'zz/teste-errors-json/latencia/%d' % i
    publish(marker, b'.')
    got = collect(marker)
    resp = [p for t, p in got if t == rtopic]
    check(len(resp) == 1, name + ': exatamente uma resposta em ' + rtopic + ' (%d)' % len(resp),
          repr([t for t, _ in got][:4])[:200])
    if not resp:
        continue
    try:
        msg = json.loads(resp[0].decode('utf-8'))
    except ValueError as e:
        check(False, name + ': resposta e JSON valido', '%s; %r' % (e, resp[0][:160]))
        continue
    check(True, name + ': resposta e JSON valido')
    check(isinstance(msg, dict) and set(msg) == {'localtime', 'id', 'iteration', 'diffTime'},
          name + ': campos localtime, id, iteration, diffTime', repr(msg)[:160])
    check(isinstance(msg, dict) and msg.get('id') == rid, name + ': id decodificado igual', repr(msg)[:160])
    check(isinstance(msg, dict) and msg.get('iteration') == it and isinstance(msg.get('diffTime'), int),
          name + ': iteration e diffTime', repr(msg)[:160])

sub.terminate()

# ------------------------------------------------ topico errors/<client_id> --
# +, # e / viram _; o corte em 255 bytes nao parte caractere UTF-8.
# Um assinante por caso: topico com UTF-8 invalido derruba o cliente que o
# recebe (libmosquitto: MOSQ_ERR_MALFORMED_UTF8), e o caso seguinte nao
# depende do anterior.
E_ACUTE, EMOJI = chr(0xE9), chr(0x1F600)
TOPIC_CASES = [
    ('client_id com +', 'a+b', 'errors/a_b'),
    ('client_id com #', 'x#y', 'errors/x_y'),
    ('client_id com /', 'p/q', 'errors/p_q'),
    ('client_id so +', '+', 'errors/_'),
    ('client_id com caractere de 2 bytes no limite', 'c' * 247 + E_ACUTE, 'errors/' + 'c' * 247),
    ('client_id com caractere de 4 bytes no limite', 'c' * 246 + EMOJI, 'errors/' + 'c' * 246),
    ('client_id de 248 bytes cabe inteiro', 'c' * 247 + 'd', 'errors/' + 'c' * 247 + 'd'),
]


def valid_topic_name(raw):
    try:
        raw.decode('utf-8')
    except UnicodeDecodeError:
        return False
    return 0 < len(raw) <= 65535 and b'+' not in raw and b'#' not in raw


for i, (name, client, want_topic) in enumerate(TOPIC_CASES):
    ready_topic = 'zz/teste-errors-json/topico/%d' % i
    s2 = subprocess.Popen(['mosquitto_sub', '-h', HOST, '-q', '0', '-t', 'errors/#', '-t', ready_topic, '-F', '%t\t%x'],
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    box = queue.Queue()

    def reader2(stream=s2.stdout, box=box):
        for raw in stream:
            line = raw.rstrip(b'\n')
            if b'\t' in line:
                t, hx = line.rsplit(b'\t', 1)
                box.put((t, hx))

    threading.Thread(target=reader2, daemon=True).start()
    ok = False
    for k in range(40):
        publish(ready_topic, b'%d' % k)
        end = time.time() + 0.25
        while time.time() < end and not ok:
            try:
                t, _ = box.get(timeout=0.05)
                ok = t == ready_topic.encode()
            except queue.Empty:
                pass
        if ok:
            break
    check(ok, name + ': assinante proprio ativo')
    rc, err = publish('sensor/room1/temperature', b'nope', client)
    check(rc == 0, name + ': mosquitto_pub rc=0', err)
    got, end = [], time.time() + 4.0
    while time.time() < end:
        try:
            t, hx = box.get(timeout=0.1)
        except queue.Empty:
            if s2.poll() is not None:
                break
            continue
        if t.startswith(b'errors/'):
            got.append((t, hx))
            break
    alive = s2.poll() is None
    s2.terminate()
    try:
        _, serr = s2.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        s2.kill()
        serr = b''
    check(alive and len(got) == 1, name + ': o assinante de errors/# recebeu a mensagem e continua conectado',
          'vivo=%s, mensagens=%d, stderr=%r' % (alive, len(got), serr.decode('utf-8', 'replace')[:160]))
    if not got:
        continue
    raw_topic = got[0][0]
    check(valid_topic_name(raw_topic), name + ': nome de topico valido (UTF-8, sem + e #)', repr(raw_topic[-12:]))
    check(len(raw_topic) <= 255, name + ': topico com %d bytes (<= 255)' % len(raw_topic))
    check(raw_topic == want_topic.encode('utf-8'), name + ': topico ' + want_topic[:40] + ('...' if len(want_topic) > 40 else ''),
          repr(raw_topic[:60]) + ('...' + repr(raw_topic[-8:]) if len(raw_topic) > 60 else ''))
    try:
        msg = json.loads(bytes.fromhex(got[0][1].decode('ascii')).decode('utf-8'))
        check(msg.get('client_id') == client, name + ': campo client_id leva o client_id original', repr(msg.get('client_id'))[:80])
    except ValueError as e:
        check(False, name + ': mensagem e JSON valido', str(e))

print('RESUMO: %d PASS, %d FAIL' % (passes, fails), flush=True)
sys.exit(1 if fails else 0)
