# Plugin `tv30-auth`: decisão de credencial e versão na borda

Plugin Go do tipo **http-server** do KrakenD, carregado pelas duas superfícies do `edgegateway`: a interna (44642, HTTP) e a externa (44643, TLS). Ele decide sobre credencial, tipo de cliente e versão de cada requisição. Depois disso, responde ele mesmo as APIs da borda ou repassa a requisição ao tv3ws.

A borda e o plugin são decisões deste testbed. A norma define as regras (C.3.1, C.3.2, C.3.6, C.4.1, C.6.1, C.6.7.8, C.6.7.9, C.6.8), mas não diz qual processo as aplica. A visão geral, para quem usa a borda, está em `docs/borda-credenciais.md`, na raiz do TV30.

## A decisão

`ServeHTTP` e `evaluate` (`handler.go`). A primeira etapa que falha define o erro.

| # | Etapa | Erro | Regra |
|---|---|---|---|
| 1 | Rota | 100 | Método e caminho declarados para a superfície (`routes.json`). Caminho não declarado não chega ao roteador do KrakenD |
| 2 | Superfície | 106 | Rota com `surface_error` na superfície: na externa, as de uso exclusivo do cliente local e o `/tv3/authorize` (C.4.1.8) |
| 3 | Associado excluído | 106 | Nas rotas que não admitem o associado (C.6.1.2, C.6.1.3, C.6.8.3), um `Origin` em `origins:associated` dá 106 também com token válido de outra classe |
| 4 | Classe | 106 | A classe vem do claim `class` do access token. Sem `Authorization`, um `Origin` em `origins:associated` faz do cliente um local associado. Com `Authorization` inválido, o `Origin` também é consultado nas rotas que restringem `classes`, para o 106 não ser contornado |
| 5 | Não local por HTTP | 106 | Na interna (`nonlocal_https_only`), o token `non-local` fora das rotas `nonlocal_http`, a C.6.1.2 e a C.6.1.3 |
| 6 | Access token | 107 | `Authorization: Bearer <jwt>`, HS256 com `JWT_SECRET`, `exp` obrigatório e válido, `nbf` respeitado, `iss` = `JWT_ISSUER`, `sub` em `clients:authorized` e fora de `clients:blocked` (C.4.2.2) |
| 7 | Serviço selecionado | 300 | Rota com `no_service: 300` (C.6.4.7, C.6.14.1 a C.6.14.6) sem serviço DTV selecionado. Vem antes do bind-token, porque sem serviço não há chave registrada |
| 8 | Bind-token | 104 / 108 | Nas rotas `token+bind`. Sem o cabeçalho `bind-token`, 104. O token precisa de assinatura válida por uma chave registrada para o **contexto de serviço da rota**: o `{serviceContextId}` do caminho, ou o serviço corrente em `current-service` e nas rotas sem o parâmetro. Sem isso, 108. Sem serviço selecionado na C.6.16.3, 108 |
| 9 | Versão | 100 / 101 | `negotiateVersion` (`edge.go`), contra o `versions` da rota. Sem `Accept-Version`, vale a 2.0; fora do formato X.Y, 101; versão que a rota não serve, 100 |

- **O local associado** usa sem access token as rotas que o admitem. No **próprio** contexto de serviço (o valor de `origins:associated` para a origem dele), dispensa também o bind-token. Fora dele, segue a regra geral: 104, 108 ou atendimento com bind-token válido daquele contexto (C.4.1.1).
- **O que segue o modo.** Só as recusas de credencial seguem `AUTH_ENFORCE` (`followsMode`): 104, 107 e 108. Em `warn`, viram o log `[tv30-auth] WARN` e o cabeçalho `X-TV30-Auth-Warn: <código>`, e a requisição segue. Os erros 100, 101, 106, 300 e 200 saem nos dois modos.
- **O que vai ao serviço.** O plugin passa a versão negociada em `X-TV30-API-Version` e o `Host` original do cliente em `X-TV30-Host`, usado pelo `/manifest`. Os dois apagam o que vier do cliente. O tv3ws responde na versão recebida.
- **Rotas da borda.** A rota com campo `edge` é respondida pelo plugin (`edge.go`). As demais seguem para o roteador do KrakenD, que as repassa ao tv3ws.

### Bind-token (C.4.1.3, C.4.1.4)

1. **Formato:** JWS compacto com cabeçalho `alg`. JWE e `crit` são recusados.
2. **Assinatura:** basta qualquer chave da lista `bind-context:{serviceId}` do contexto da rota validá-la. O `alg` do token tem de ser igual ao `alg` registrado, o que impede a confusão de algoritmo. Só valem HS256, HS512, RS256 e RS512; `none` nunca vale.
3. **Tempo:** `nbf` e `exp`, quando presentes.
4. **Emissão:** `iat`, quando presente, não pode estar no futuro.
5. **Relógio:** o do host, sem tolerância. A emissora simulada e o receptor rodam na mesma máquina.

**Codificação da chave**, decisão do testbed (a norma não a define; `keys.go`, a mesma função no registro e na validação):
- HS: os bytes UTF-8 da string.
- RS: PEM (`PUBLIC KEY`, `RSA PUBLIC KEY`, `RSA PRIVATE KEY`, `PRIVATE KEY`) ou DER em base64 (SPKI ou PKCS#1). Da chave privada, o plugin deriva a pública.
- O registro responde 101 a RS512 com módulo menor que 752 bits e a RS256 com menos de 496.

### Infraestrutura

- **Redis fora do ar:** 404 `{"error":200}`.
- **Resposta 5xx do KrakenD** (backend lento, fora do ar ou que não resolve): o KrakenD manda 500 sem corpo. O plugin retém essa resposta e devolve 404 `{"error":200}`, com a duração em ms na `description`. O timeout é o padrão de 2 s (decisão do projeto); o `/tv3/authorize` tem 15 s, por causa do pop-up.
- **Panic no roteador:** 404 `{"error":200}` se o cabeçalho ainda não saiu; se já saiu, a conexão é abortada.
- **CORS** (C.4.1.9), todo no plugin:
  - `Access-Control-Allow-Origin: *` em toda resposta;
  - `Access-Control-Expose-Headers` de `cors.expose_headers`;
  - o preflight respondido em qualquer caminho, qualquer que seja a lista pedida.

## APIs respondidas pela borda

### C.6.8, bind-context (`bindcontext.go`)

As chaves ficam em `bind-context:{serviceId}`, um LIST de JSON `{"alg","key","registeredAt"}`, gravado pelas funções `tv30_bind_registra` e `tv30_bind_remove` do Redis. A borda é a única escritora dessa chave.

| Rota | Sucesso | Erros |
|---|---|---|
| `POST /tv3/bind-context`, corpo `{alg, key}` | `{"serviceContextId": <globalServiceId do serviço corrente>}` | 101: corpo ou chave inválidos. 105: falta `alg` ou `key`. 300: sem serviço corrente |
| `GET /tv3/bind-context`, cabeçalho `bind-token` | `{"boundServices": [{serviceContextId, serviceName?, serviceId?}]}`, um item por serviço cuja chave valida o token | 104: sem cabeçalho. 108: não é JWT, ou a assinatura confere fora do prazo. 101: nenhuma chave valida a assinatura |
| `DELETE /tv3/bind-context`, cabeçalho `key` | `{}` | 105: sem cabeçalho |

- **Sem duplicata.** Para RSA, a mesma chave pública em outra codificação também conta como duplicata.
- **Escrita.** A borda só escreve por `FCALL`; o `escritas_test.go` falha se aparecer outro comando de escrita.
- **Esquema.** Uma recusa do esquema é defeito e responde 200.

### C.6.7.8 e C.6.7.9, api-info (`apiinfo.go`)

- **A lista.** São os ids da Tabela C.2 marcados no campo `api` das rotas, na ordem da tabela: as 20 APIs implementadas, iguais nas duas superfícies. Cada uma vem com a versão mais recente que serve: 100.0 para a `tv3ws-remotedevice-communication` e 2.0 para as demais.
- **Erros.** Um id não implementado dá 101, e um subsistema desconhecido também. Sem `subsystem`, a lista traz todas. `ncl` e `nclua` dão lista vazia.

## Configuração

O bloco do plugin é gerado pelo `../generate.js`, a partir do `../routes.json`, no build da imagem:
- a superfície;
- as rotas, com `auth`, `classes`, `edge`, `surface_error`, `nonlocal_http`, `no_service` e `versions`;
- `nonlocal_https_only`;
- as APIs;
- os cabeçalhos CORS.

Ambiente do container (`infra/docker-compose.yml`):

| Variável | Padrão | Uso |
|---|---|---|
| `AUTH_ENFORCE` | `enforce` | `warn` só avisa nas recusas de credencial (104, 107, 108) |
| `JWT_SECRET` | obrigatória | A mesma do tv3ws, que emite o access token |
| `JWT_ISSUER` | `GenericIssuer` | Igual ao tv3ws |
| `REDIS_HOST` / `REDIS_PORT` | `redis` / `6379` | O Redis da stack |
| `REDIS_TIMEOUT_MS` | `500` | Timeout de cada comando |

**Configuração inválida derruba o processo** (`[tv30-auth] FATAL`, `os.Exit(1)`), porque o KrakenD subiria sem o plugin. São configuração inválida (`loadConfig`, em `config.go`):
- `JWT_SECRET` ausente;
- `AUTH_ENFORCE` diferente de `warn` e `enforce` (vazio vale `enforce`);
- porta ou timeout inválidos;
- `surface` ausente, ou lista de rotas vazia;
- rota inválida;
- `surface_error` diferente de 106;
- `no_service` diferente de 300, ou fora de rota `token+bind`;
- `versions` vazio, fora do formato X.Y ou com versão repetida;
- `current_service_context_id` presente (o `serviceContextId` é o `globalServiceId` do serviço);
- CORS ou lista de APIs inválidos.

## Build e testes

O plugin só usa a biblioteca padrão. O `.so` precisa do mesmo Go, da mesma libc (musl) e da mesma arquitetura do KrakenD 2.7.2. O Dockerfile escolhe o Go pela arquitetura (`TARGETARCH`):
- **amd64:** `krakend/builder:2.7.2`;
- **arm64:** `golang:1.22.7-alpine3.19` com `binutils-gold`.

Depois, o Dockerfile roda `go vet`, `go test`, o build e `krakend check-plugin`/`test-plugin`.

```bash
# fora do build, numa máquina amd64
docker run --rm -v "$PWD":/src -w /src krakend/builder:2.7.2 \
  sh -c 'go vet ./... && go test ./... && go build -buildmode=plugin -o /tmp/tv30-auth.so .'
```

**Testes de unidade (`*_test.go`):**
- `jwt_test.go`, `keys_test.go`: JWT e formatos de chave;
- `routes_test.go`: casamento de rota;
- `handler_test.go`: a decisão por classe, rota e modo; o associado excluído, o não local por HTTP, o bind-token no contexto da rota e o serviço selecionado; versão negociada por rota; cabeçalhos internos;
- `surface_test.go`: o 106 da superfície externa;
- `bindcontext_test.go`, `apiinfo_test.go`: as APIs da borda;
- `redis_test.go`: o cliente RESP;
- `escritas_test.go`: escritas só por `FCALL`;
- `contrato_test.go`: contra as funções reais, pelo `infra/redis/test/run.sh contrato`.

O gerador tem testes próprios: `node --test ../generate.test.js`.

**Na stack:** `scripts/test-auth.sh`, na raiz do TV30.

## Limitações

- **Associado pelo `Origin`:** fora do navegador, o `Origin` é forjável; o risco é aceito (decisão do projeto). A chamada sem `Origin` fica fora do 106 do associado excluído (etapa 3).
- **Não implementado:**
  - liberar os recursos compartilhados (C.4.4) ao revogar uma chave, ao remover a autorização ou ao fechar a app associada;
  - o 304 a serviço fora do Application Catalog, porque o catálogo não tem publicador;
  - a escolha do serviço de destino pela versão, porque hoje toda rota tem um backend só.
