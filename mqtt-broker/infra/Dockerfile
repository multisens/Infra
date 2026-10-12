FROM ubuntu:22.04

RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    libssl-dev \
    libc-ares-dev \
    uuid-dev \
    libwebsockets-dev \
    libcjson-dev \
    libjson-c-dev \
    python3 \
    python3-pip \
    redis-tools \
    wget \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install redis

WORKDIR /tmp

RUN wget https://mosquitto.org/files/source/mosquitto-2.0.22.tar.gz && \
    tar -xzf mosquitto-2.0.22.tar.gz && \
    cd mosquitto-2.0.22 && \
    make WITH_WEBSOCKETS=yes && \
    make install && \
    ldconfig

COPY plugin/src/ /tmp/src/
COPY plugin/include/ /tmp/include/
COPY plugin/tests/test_error_json.c plugin/tests/test_json_payload.c plugin/tests/check_json_payload.py /tmp/tests/

# Teste da mensagem de errors/<client_id> (correcao de 12/10: JSON sempre
# valido, src/error_json.c; tambem o topico errors/<client_id> e o escape do
# id do response_time_tester.c) antes de compilar o plugin: escape quebrado
# nao vira imagem. O json do Python (python3, instalado acima) confere de novo
# cada mensagem que o teste gerou, uma por linha.
RUN gcc -std=gnu11 -Wall -Wextra -I/tmp/include -o /tmp/test_error_json \
        /tmp/tests/test_error_json.c /tmp/src/error_json.c -ljson-c \
 && /tmp/test_error_json /tmp/mensagens.jsonl \
 && python3 -c "import json,sys; ls=open(sys.argv[1],encoding='utf-8').read().split('\n')[:-1]; [json.loads(l) for l in ls]; print('python3 json: %d mensagens validas' % len(ls))" /tmp/mensagens.jsonl \
 && rm -f /tmp/test_error_json /tmp/mensagens.jsonl

# Teste da leitura do payload na validacao (correcao de 12/10, revisao do
# plugin: payload inteiro, exatamente um texto JSON, src/json_payload.c). O
# json do Python confere de novo o veredito de cada caso.
RUN gcc -std=gnu11 -Wall -Wextra -I/tmp/include -o /tmp/test_json_payload \
        /tmp/tests/test_json_payload.c /tmp/src/json_payload.c -ljson-c \
 && /tmp/test_json_payload /tmp/casos.tsv \
 && python3 /tmp/tests/check_json_payload.py /tmp/casos.tsv \
 && rm -f /tmp/test_json_payload /tmp/casos.tsv

RUN gcc -fPIC -shared -o /usr/local/lib/mosquitto_plugin.so \
    /tmp/src/mosquitto_plugin.c \
    /tmp/src/schema_validator.c \
    /tmp/src/response_time_tester.c \
    /tmp/src/error_json.c \
    /tmp/src/json_payload.c \
    -ljson-c -I/tmp/mosquitto-2.0.22/include -I/tmp/include

RUN useradd -r -s /bin/false mosquitto && \
    mkdir -p /mosquitto/config /mosquitto/data /mosquitto/log && \
    chown -R mosquitto:mosquitto /mosquitto

COPY plugin/config/mosquitto.conf plugin/config/schemas.json /mosquitto/config/
COPY infra/migrate_to_redis.py /usr/local/bin/migrate_to_redis.py
COPY infra/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 1883

USER mosquitto

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
