# Plugin `tv30-auth` — validação de credenciais na borda

Plugin Go do tipo **http-server** do KrakenD, carregado pelas duas superfícies do `edgegateway` (interna 44642 e externa 44643). Ele implementa a decisão da reunião de 28/09 (D1): **toda a validação de credenciais fica na borda**, e o tv3ws só implementa as APIs.

Na reunião de 05/10 com o Joel, isso foi levado até o fim, e a borda passou a **responder** três grupos de API em vez de repassá-los ao tv3ws:
- **D-0510-1.** Toda a validação de credencial fica na borda, e o tv3ws fica "anônimo": só recebe e responde. A retirada do middleware de credencial do tv3ws (`src/middleware/authorization.ts` e `validateClientProtocol` em `src/middleware/basic.ts`) é feita no próprio tv3ws, fora desta pasta. O tv3ws continua negociando a versão (`Accept-Version`) nas rotas dele e emitindo o token (`/tv3/authorize`, `/tv3/token`).
- **D-0510-2.** A API C.6.8 (`POST`, `GET` e `DELETE /tv3/bind-context`) passa para a borda. Ver [APIs respondidas pela borda](#apis-respondidas-pela-borda).
- **D-0510-3.** As APIs de informação de API (C.6.7.8 e C.6.7.9, `GET /tv3/api-info[/<api-id>]`) ficam na borda e são respondidas a partir da tabela de rotas.

O plugin anterior (`consent-validator`, removido em `infra@f2eb652`) não validava nada: repassava qualquer caminho ao tv3ws. Deste plugin só foi aproveitado o esqueleto de registro.

## O que ele valida

Cada requisição passa por quatro etapas, nesta ordem. A primeira que falha define o código de erro.

| # | Etapa | Erro | Regra |
|---|---|---|---|
| 1 | Rota | 100 | Método e caminho precisam estar declarados em `routes.json` para a superfície. Um caminho não declarado não chega ao roteador, o que elimina o panic do Gin descrito em `KNOWN-ISSUES.md`. O preflight CORS (`OPTIONS` com `Access-Control-Request-Method`) sempre passa e é respondido pelo módulo CORS do KrakenD. `OPTIONS` sem esse cabeçalho não é preflight: num caminho declarado (com qualquer método), o plugin responde 200 com `Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: *` e `Access-Control-Allow-Headers` igual ao `cors.allow_headers` do `routes.json` (C.4.1.9.3); num caminho não declarado, dá 100. |
| 2 | Classe | 106 | A classe vem do claim `class` do access token. Sem `Authorization`, o cliente é tratado como local associado se o `Origin` estiver no hash `origins:associated`. Com `Authorization` presente mas inválido, o `Origin` também é consultado, só para esta checagem: mandar um `Authorization` qualquer não escapa do 106, e o 107 continua valendo. A rota declara as classes permitidas em `classes`. |
| 3 | Access token | 107 | `Authorization: Bearer <jwt>`, com `alg` fixo em HS256, segredo `JWT_SECRET`, `exp` obrigatório e não vencido, `nbf` respeitado se existir e `iss` igual a `JWT_ISSUER`. Cliente cujo `sub` está em `clients:blocked` recebe 107 (C.4.2.2), inclusive com token de classe `local-associated`. O plugin não consulta `clients:authorized`, o conjunto que o tv3ws passou a gravar na rodada de 05/10 (D-0510-4); exigir a presença nele é ponto em aberto (E6 de `docs/decisoes-pendentes.md`, na raiz do TV30). |
| 4 | Bind-token | 104 / 108 | Vale só nas rotas `token+bind`. Sem o cabeçalho `bind-token`, o erro é 104. O token tem de ser assinado por uma chave registrada para o **serviço corrente** (ver abaixo); se não for, o erro é 108. |

O local associado não precisa de access token nem de bind-token em nenhuma rota que o admita (C.4.1.1).

Liberada a requisição (ou só avisada, em `warn`), a rota com o campo `edge` no `routes.json` é respondida pelo próprio plugin. As demais seguem para o roteador do KrakenD, que as repassa ao tv3ws.

Toda resposta leva `Access-Control-Allow-Origin: *` (C.4.1.9.2), também a do cliente que não manda `Origin`: o plugin define o cabeçalho antes de repassar, com o mesmo `Set` do módulo CORS, e ele sai uma vez só.

Falhas de infraestrutura têm código próprio:
- **Redis fora do ar.** Na decisão de credencial, em `warn`, o plugin registra no log e deixa passar; em `enforce`, responde 404 com `{error: 200}`. Na C.6.8, que o plugin responde, a falha do Redis dá 404 com `{error: 200}` nos dois modos.
- **Backend lento ou fora do ar (resposta 5xx do KrakenD).** O KrakenD responde 500 **sem corpo** (`Content-Length: 0`) quando o backend não responde dentro do timeout do endpoint (padrão de 2 s; só as rotas com `timeout` no `routes.json` têm outro), recusa a conexão ou tem um nome que não resolve. O plugin envolve o `ResponseWriter` do roteador e retém qualquer status 5xx, sem repassar cabeçalho nem corpo. No lugar, responde 404 com `{"error": 200, "description": "Platform resource unavailable: 500 do gateway em <n> ms (backend lento ou fora do ar)"}` (C.3.2.1; Tabela C.1, erro 200: "dependence on an unavailable resource"). Os timeouts não mudam.
  - O tempo na descrição separa os dois casos: perto do timeout do endpoint, o backend está lento; perto de zero, está fora do ar. O log registra `[tv30-auth] BACKEND ... status_gateway=500 ms=<n>`, e o motivo exato fica na linha `KRAKEND ERROR: [ENDPOINT: ...]` logo antes.
  - O tv3ws nunca responde 5xx: a camada comum de erro (`tv3ws/src/util/error.ts`) só emite 404, e nenhum handler escreve 5xx. Todo 5xx que chega ao plugin vem, portanto, do próprio KrakenD. Como as rotas são `no-op`, um 5xx de backend também seria trocado. Respostas que não são 5xx passam intactas, inclusive o erro C.3.2 do tv3ws.
  - Os cabeçalhos da resposta retida saem, porque não descrevem o corpo novo (`Content-Encoding`, `Content-Range`, `X-Powered-By` etc.). Ficam os de CORS (`Access-Control-*`, `Vary`), os do KrakenD (`X-Krakend*`) e, em `warn`, o `X-TV30-Auth-Warn`: ele diz o que o `enforce` faria com a requisição, e isso não muda porque o backend falhou.
  - Medido em 03/10 num KrakenD 2.7.2 isolado: sem o plugin, timeout, conexão recusada e nome que não resolve davam 500 com `Content-Length: 0`; com o plugin, os três viram 404 + `{error: 200}` com `Content-Type: application/json`, `Access-Control-Allow-Origin: *` e `API-Version`, nos dois modos.
- **Panic no roteador.** Se o cabeçalho ainda não saiu, o plugin responde 404 com `{error: 200}` em vez de deixar a conexão resetar. Se já saiu, a resposta não tem como virar erro: o plugin aborta a conexão (`http.ErrAbortHandler`) em vez de emendar um corpo de erro no meio da resposta.

### Bind-token: verificação em quatro frentes (C.4.1.4)

A verificação segue esta ordem:

1. **Formato.** JWS compacto com 3 partes base64url e cabeçalho com `alg`. Token cifrado (JWE) ou com `crit` é recusado.
2. **Assinatura.** O token é aceito se **qualquer** chave da lista `bind-context:{serviceId}` validar a assinatura. `serviceId` é o valor de `session:current-service-id`.
   - O `alg` do token tem de ser igual ao `alg` registrado da chave. Isso impede a confusão de algoritmo, como um HS256 assinado com a chave pública RSA usada como segredo.
   - Só valem HS256, HS512, RS256 e RS512. `none` é sempre inválido.
3. **Tempo: `nbf` e `exp`.** Quando presentes, o token só vale a partir de `nbf` e deixa de valer em `exp`.
4. **Emissão: `iat`.** Quando presente, `iat` não pode estar no futuro.

As claims `iat`, `nbf` e `exp` são opcionais para o bind-token, porque a norma usa *should* (C.4.1.3).

Quem grava as chaves é a API C.6.8, respondida pelo próprio plugin desde a reunião de 05/10 (`POST /tv3/bind-context`, `bindcontext.go`). A lista é um LIST no Redis, e cada entrada tem o formato `{"alg","key","registeredAt"}`. Entradas malformadas ou chaves ilegíveis são ignoradas, com registro no log. Uma entrada ruim não invalida as outras chaves da mesma emissora.

**Codificação da chave.** A norma não define. O formato abaixo é decisão de implementação do testbed:
- **HS256 e HS512:** o segredo são os bytes UTF-8 da string.
- **RS256 e RS512:** PEM (`PUBLIC KEY` ou `RSA PUBLIC KEY`) ou DER em base64 (SPKI ou PKCS#1 público). O PEM só vale se o texto aparado começar por `-----BEGIN` e o rótulo for um destes quatro: `PUBLIC KEY`, `RSA PUBLIC KEY`, `RSA PRIVATE KEY`, `PRIVATE KEY`. O base64 vale nas codificações padrão e URL, com ou sem preenchimento.
- **Mesmas regras no registro.** O registro (C.6.8.2) e a validação do bind-token usam a mesma função de leitura de chave (`keys.go`), então o registro não aceita chave que a validação não lê. Até a reunião de 05/10, o registro ficava no tv3ws, com as regras copiadas desta função. Os casos de `testdata/keyformats.json` vêm do teste cruzado daquela época e continuam valendo como teste de regressão (`keys_test.go`).
- **Chave privada** (PKCS#1 ou PKCS#8, como no exemplo `MIIBOgIBAAJB...` da norma): é aceita, e o plugin deriva dela a chave pública.
- **Limite das chaves de 512 bits:** como a do exemplo, só servem para RS256. O DigestInfo do SHA-512 não cabe num módulo de 64 bytes. O registro responde 101 a RS512 com módulo menor que 752 bits (e a RS256 com menos de 496).

### Política por rota

A política fica nos campos `auth` e `classes` de cada rota em `../routes.json`:
- **`auth`:** `none`, `token` ou `token+bind`. Se o campo faltar, vale `token`.
- **`classes`:** lista das classes permitidas, com os valores exatos que o tv3ws grava no claim: `local-associated`, `local-autonomous` e `non-local`. Se o campo faltar, todas as classes são permitidas.

Dois campos opcionais completam a rota:
- **`api`:** `{id, section, version}`, a linha da Tabela C.2 da norma que a rota implementa (p. 220-221 do PDF). As rotas do testbed fora da norma não têm o campo. A lista de APIs da C.6.7.8 e da C.6.7.9 sai daqui.
- **`edge`:** nome do handler do plugin que **responde** a rota (`bind-context-register`, `bind-context-list`, `bind-context-remove`, `api-info`, `api-list`).

O `generate.js` monta o bloco do plugin a partir desses campos. Valor inválido derruba o build. O `generate.js` também acrescenta `bind-token` aos `input_headers` das rotas `token+bind`.

| Rotas | auth | classes |
|---|---|---|
| `GET /health`, `GET /manifest` | none | todas |
| `GET /tv3/authorize`, `GET /tv3/token` | none | `local-autonomous`, `non-local` |
| `POST /tv3/bind-context`, `DELETE /tv3/bind-context` (borda) | none | `local-associated` |
| `GET /tv3/bind-context` (borda) | token | `local-autonomous`, `non-local` |
| `GET /tv3/api-info`, `GET /tv3/api-info/{apiId}` (borda) | token | todas |
| `POST /tv3/current-service/users`; `POST /tv3/{serviceContextId}/users`; `GET` e `POST .../users/current-user`; `GET .../users/files`; `GET .../users/{userid}`; `GET` e `POST /tv3/{serviceContextId}/users/{userid}`; `GET .../apps/{appid}/files`; `POST /tv3/sensory-effect-renderers/{rendererId}` | token+bind | todas |
| demais | token | todas |

Observações sobre a tabela:
- **Critério do `token+bind`.** É o campo "Security requirements" = *shall* da tabela de cada API na norma.
- **`GET /tv3/bind-context`.** O bind-token desta rota é argumento da própria API C.6.8.3, validado pelo handler dela contra as chaves de **todos** os serviços (não só do corrente). Por isso a rota está como `token`, e não `token+bind`.
- **`GET /tv3/api-info[/{apiId}]`.** As Tabelas C.45 e C.46 têm "Restrictions" e "Security requirements" vazios e preveem 107 só para o cliente não local e o autônomo (p. 300-301 do PDF). Daí `token` com todas as classes: o associado dispensa o access token, como nas demais rotas.
- **`POST /tv3/{serviceContextId}/users`.** Não existe na norma: só há `POST /tv3/current-service/users` (C.6.14.1, *shall*). O tv3ws atende os dois caminhos com o mesmo handler, que ignora o scid. A rota começou como `token`, o que deixava a mesma operação passar sem bind-token por esse caminho (e com `Current-Service`, porque o Express não diferencia caixa). Agora está como `token+bind`, e vale a regra provisória da L2: scid diferente de `current-service` e da constante dá 108. É PENDENTE: o Joel decide se a rota fica ou sai da tabela.

## APIs respondidas pela borda

Decisões da reunião de 05/10 com o Joel (D-0510-2 e D-0510-3). A requisição passa antes pela mesma decisão de credencial das demais rotas: 100, 106 e 107 pela política da rota; em `warn`, só o aviso. Depois chega ao handler do plugin (`edge.go`), sem repasse ao tv3ws. O handler aplica a mesma negociação de versão do tv3ws: sem `Accept-Version` vale a 2.0; fora do formato `X.Y`, erro 101; fora de `2.0` e `2.1`, erro 100. Erros saem no formato C.3.2, e a resposta de sucesso leva `Content-Type: application/json`, `API-Version` e `Access-Control-Allow-Origin: *`. Falha do Redis dá 200. Um panic no handler também dá 200, em vez de conexão fechada.

**Escolha sobre o KrakenD.** O `generate.js` continua gerando o endpoint dessas rotas no KrakenD, com o mesmo backend das demais, mas o plugin responde antes, e o endpoint nunca é alcançado. Ele só mantém o caminho registrado no roteador, como o de qualquer outra rota. Assim, o preflight CORS (que passa ao módulo CORS do KrakenD) segue o mesmo caminho já testado, e a árvore de rotas do Gin não muda. Caminho que não está registrado ali pode cair no panic de `KNOWN-ISSUES.md`. Achado nos testes desta rodada, que vale para todas as rotas e não só para estas: o módulo CORS do KrakenD 2.7.2 não concede o preflight quando o `Access-Control-Request-Headers` vem fora de ordem lexicográfica (registrado no `KNOWN-ISSUES.md` da raiz do TV30).

### C.6.8 — bind-context (Tabelas C.47 a C.49)

Mesmo contrato que o tv3ws implementava em `src/api/broadcaster-security` até a reunião de 05/10, inclusive o armazenamento: `bind-context:{serviceId}` é um LIST de JSON `{"alg","key","registeredAt"}` (ms), e `serviceId` é o valor de `session:current-service-id` no momento da chamada.

| Rota | Sucesso | Erros |
|---|---|---|
| `POST /tv3/bind-context`, corpo `{alg, key}` | `{"serviceContextId": <auth.current_service_context_id>}` | 101: corpo que não é objeto JSON (inclusive `Content-Type` diferente de `application/json`), `alg` fora de HS256/HS512/RS256/RS512, chave que não parseia ou módulo RSA pequeno demais para o `alg`. 105: falta `alg` ou `key` num objeto JSON (corpo vazio com `Content-Type` JSON vale `{}`). 300: sem serviço corrente. |
| `GET /tv3/bind-context`, cabeçalho `bind-token` | `{"boundServices": [{serviceContextId, serviceName?, serviceId?}]}` | 104: cabeçalho ausente. 108: não é JWT, ou a assinatura confere mas o token está fora do prazo (`nbf`, `exp`, `iat`). 101: nenhuma chave registrada, de nenhum serviço, valida a assinatura. A assinatura é conferida antes do prazo (C.4.1.4). |
| `DELETE /tv3/bind-context`, cabeçalho `key` | `{}`, também sem serviço corrente ou com chave inexistente | 105: cabeçalho ausente. |

- **Sem duplicata no registro.** Para HS, só um segredo idêntico é duplicata, porque "s" e "s " são segredos diferentes na verificação. Para RSA, a mesma chave pública em qualquer codificação também é duplicata. O mesmo segredo com outro `alg` vira outra entrada. Leitura e escrita não são atômicas (LRANGE seguido de RPUSH, como era no tv3ws): dois registros simultâneos da mesma chave podem gravar uma duplicata, o que não muda o resultado.
- **`GET`.** O handler varre `bind-context:*` com SCAN (não KEYS). Lista de outro tipo e entrada malformada são ignoradas, com log. `serviceName` e `serviceId` só aparecem para o serviço corrente, lidos do HASH `session:current-service` (gravado pelo tv3ws); o `serviceId` volta a inteiro quando é numérico (Tabela C.8).
- **`DELETE`.** Remove do serviço corrente toda entrada com a mesma chave, porque o cabeçalho não traz `alg`: texto igual sem espaços nas pontas ou, para RSA, a mesma chave pública.
- **Classe.** 106 pela política da rota: `POST` e `DELETE` só para o associado, `GET` só para o autônomo e o não local. O `GET` também exige o access token (107).
- **Escrita no Redis.** É a única escrita da borda: RPUSH e LREM em `bind-context:{serviceId}`. O cliente RESP só repete um comando uma vez, numa conexão nova, quando a conexão do pool morreu. O RPUSH não é repetido se a falha veio depois de o comando sair, porque uma resposta perdida de um comando já executado duplicaria a entrada.
- **Divergência do tv3ws.** O tv3ws também aceitava, por acidente, o corpo do `POST` em `application/x-www-form-urlencoded` (o `express.urlencoded` estava montado no app). A borda só aceita JSON, como a Tabela C.47 descreve.

### C.6.7.8 e C.6.7.9 — api-info (Tabelas C.45 e C.46)

| Rota | Resposta | Erros |
|---|---|---|
| `GET /tv3/api-info/<api-id>` | `{"receiverApi": {"id", "version"}}` | 101: `<api-id>` não é API implementada. |
| `GET /tv3/api-info[?subsystem=ncl\|nclua\|tv3ws]` | `{"receiverApis": [{"id", "version"}, ...]}` | 101: subsistema desconhecido (inclusive vazio). |

- **Fonte da lista.** A lista é a de ids da Tabela C.2 marcados no campo `api` das rotas de cada superfície do `routes.json`. O `generate.js` a entrega ao plugin sem repetição e na ordem da tabela (hoje são 20 APIs). O id da C.6.7.8 é `tv3ws-api-info`; a C.6.7.9 não tem id próprio na Tabela C.2.
- **Subsistemas.** O subsistema é o prefixo do id (`tv3ws-...`). `ncl` e `nclua` são conhecidos, e o testbed não implementa nenhuma API deles: a lista sai vazia, e não com erro 101.

## Configuração

O plugin lê o ambiente do container, definido no serviço `edgegateway` de `infra/docker-compose.yml`.

| Variável | Padrão | Uso |
|---|---|---|
| `AUTH_ENFORCE` | `warn` | Com `warn`, nada é bloqueado: o plugin loga `[tv30-auth] WARN ...` e acrescenta `X-TV30-Auth-Warn: <código>` à resposta. Com `enforce`, responde 404 com o corpo C.3.2. |
| `JWT_SECRET` | sem padrão (obrigatória) | Tem de ser **o mesmo** do tv3ws, que emite o access token. O compose usa o mesmo padrão de desenvolvimento nos dois serviços. |
| `JWT_ISSUER` | `GenericIssuer` | Igual ao tv3ws. |
| `REDIS_HOST` / `REDIS_PORT` | `redis` / `6379` | Mesmo Redis do resto da stack. A variante `windows` (dev-host) também usa este Redis. Com a borda em rede do host (variante `host`, override `docker-compose.ssdp.yml` da raiz do TV30), `REDIS_HOST=127.0.0.1`: o mesmo Redis, pela 6379 publicada. |
| `REDIS_TIMEOUT_MS` | `500` | Timeout de cada comando. |

Exceções que valem nos dois modos: o erro 100 (rota não declarada) e o 200 (panic do roteador ou resposta 5xx do KrakenD). Nenhum cliente depende de uma rota não declarada, porque ela nunca chegou ao tv3ws, nem de um 500 sem corpo, que o tv3ws nunca emite. O modo `warn` existe porque clientes como o Guaraná ainda não obtêm token.

**Morre-inteiro.** O KrakenD só emite um aviso e sobe **sem** o plugin quando o registro falha. Por isso, configuração inválida derruba o processo com `os.Exit(1)` e uma linha `[tv30-auth] FATAL ...`, e o entrypoint derruba o container. Os casos são:
- `JWT_SECRET` ausente;
- `AUTH_ENFORCE` com valor diferente de `warn` ou `enforce`;
- política de rota inválida;
- `edge` com nome de handler desconhecido;
- lista `apis` inválida (id fora do formato `<subsistema>-<nome>`, versão fora de `X.Y`, id repetido) ou ausente com rota `api-info`/`api-list` declarada;
- rota `bind-context-*` da borda sem `current_service_context_id`.

O build também falha se o `.so` não casar com o binário do KrakenD (`krakend check-plugin` e `krakend test-plugin -s` no Dockerfile).

**Erros.** Todas as respostas de erro seguem a C.3.2:
- status 404;
- corpo `{"error": <n>, "description": "..."}`;
- cabeçalhos `Content-Type: application/json`, `Access-Control-Allow-Origin: *` e `API-Version`.

O `API-Version` desses erros (e do `OPTIONS` que não é preflight) sai da mesma negociação das APIs da borda (`apiVersion`, em `handler.go`): a versão pedida, se for `2.0` ou `2.1` (`2.0` sem `Accept-Version`); a mais recente suportada, `2.1`, quando a versão pedida está fora do conjunto, o caso do erro 100 da negociação (C.3.6.6: "the latest version supported by the server"); e `2.0` com `Accept-Version` fora do formato `X.Y`, o caso do erro 101, como antes. Este último é leitura do projeto: a exceção da C.3.6.6 fala de versão pedida, e um cabeçalho malformado não pede versão nenhuma.

## Build e testes

O plugin usa **somente a biblioteca padrão**: o `go.mod` não tem `require`. O `.so` precisa do mesmo Go, da mesma libc (musl) e da mesma arquitetura do binário do KrakenD. Por isso o builder e a imagem final usam a mesma versão fixa, `2.7.2`.

```bash
# vet + testes + .so, no builder oficial (Go 1.22.7 / musl, igual ao KrakenD 2.7.2)
docker run --rm -v "$PWD":/src -w /src krakend/builder:2.7.2 \
  sh -c 'go vet ./... && go test ./... && go build -buildmode=plugin -o /tmp/tv30-auth.so .'
```

O Dockerfile do `edgegateway` repete os mesmos passos no estágio `plugin`.

Os testes (`*_test.go`) cobrem:
- JWT nos quatro algoritmos;
- confusão de algoritmo e `alg: none`;
- `exp`, `nbf` e `iat`;
- formatos de chave;
- casamento de rota (literal antes de `{param}`);
- a decisão completa por classe e rota;
- `warn` e `enforce`, preflight `OPTIONS`, `OPTIONS` sem preflight e panic;
- a troca do 5xx do KrakenD por 404 + `{error: 200}`: cabeçalhos que saem e que ficam, aviso do `warn` mantido, respostas não 5xx intactas, `Flush`, `Hijack` e `CloseNotify` (o `gin.ResponseWriter` do gin v1.9.1, embutido no KrakenD 2.7.2, faz type assertion sem `ok` para os três), panic antes e depois do cabeçalho, e um teste ponta a ponta com `net/http` que imita o endpoint `no-op` do KrakenD diante de um backend lento e de um fora do ar;
- o cliente Redis (servidor RESP falso, reconexão e timeout), inclusive RPUSH, LREM, SCAN com várias páginas, HGETALL e o RPUSH que não se repete depois de uma resposta perdida;
- a C.6.8 respondida pela borda (`bindcontext_test.go`): os casos dos testes do tv3ws que a implementava (`test/broadcaster-security.test.ts` e o registro de `test/bind-token.test.ts`), passando pelo `ServeHTTP` inteiro, mais classe, versão, `warn`, Redis fora, panic e um teste ponta a ponta com o cliente RESP real;
- a C.6.7.8 e a C.6.7.9 (`apiinfo_test.go`): id conhecido, não implementado e inventado, subsistemas, política (107 e associado) e `warn`.

**Teste de integração com a stack real:** `scripts/test-auth.sh`, na raiz do TV30. Ele roda contra 44642 e 44643. Em `warn`, verifica o aviso 107 e o erro 100. Depois recria só o `edgegateway` em `enforce` e cobre:
- 107, com o token obtido pelo fluxo real `/tv3/authorize` + `/tv3/token`;
- 104, 106 e 108;
- a API C.6.8 nos quatro algoritmos;
- isolamento entre serviços e confusão de algoritmo;
- preflight CORS.

No fim, o script volta a borda ao modo anterior e desfaz o que semeou no Redis.

> Depois da reunião de 05/10, os erros da C.6.8 que o script esperava do tv3ws (105, 101 e 104, identificados pelo `X-Powered-By: Express`) passam a vir da borda, e a C.6.7.8/C.6.7.9 ainda não tem casos no script. A atualização do script fica para o teste de integração desta rodada.

`testdata/jsonwebtoken.json` traz tokens gerados pelo `jsonwebtoken` (a biblioteca do tv3ws). Eles servem para conferir a interoperabilidade. O arquivo tem só material público e segredos de teste.

## Decidido (Luís, 03/10)

- **L1. Reconhecimento do local associado pelo `Origin`: risco aceito.** O critério continua sendo só o `Origin` presente em `origins:associated`, sem mudança de comportamento. Comentário no código: `DECIDIDO (Luis, 03/10): risco aceito`, em `handler.go`.
  - **Risco aceito.** Fora do navegador, o `Origin` pode ser forjado. Em `enforce`, um `Origin` forjado presente em `origins:associated` passa como associado e dispensa access token e bind-token em toda rota que admite o associado, inclusive `POST` e `DELETE /tv3/bind-context`.
  - O `Origin` também não distingue as apps de emissora servidas por proxy na origem do AoP. Como o AoP grava em `origins:associated` a origem própria da app (alvo do proxy), e não a dele, essas apps **não** são reconhecidas como associadas: em `warn` levam `X-TV30-Auth-Warn: 107`, e em `enforce` seriam bloqueadas. Isso **não** foi decidido em 03/10: a origem própria por app (P1.3) continua aberta e é pré-requisito do `enforce`.
  - A norma (C.4.1.7, p. 206; p. 224 do PDF) manda o associado usar uma porta de origem atribuída pelo gerenciador de componentes ("A source port shall be used") e diz, na mesma seção, que o mecanismo de diferenciação é decisão de implementação. O reconhecimento pelo `Origin` é decisão de implementação deste testbed.

## PENDENTE (Joel)

Comportamentos provisórios, cada um marcado no código com `PENDENTE (Joel)`:

- **L2. `{serviceContextId}` no caminho.** O tv3ws usa um `serviceContextId` constante (`tv3ws/src/core.ts`), copiado para `routes.json` em `auth.current_service_context_id`. Nas rotas `token+bind`, só `current-service` e essa constante contam como serviço corrente; qualquer outro valor dá 108.
  - A C.3.5 diz que o `<service-context-id>` segue o `globalServiceId` do SLT. Isso não foi implementado.
- **L3. TLS na borda.** A 44643 ainda é HTTP. O 106 por protocolo (cliente não local usando HTTP) não é aplicado pela borda. Até a reunião de 05/10, quem o aplicava era o tv3ws (`validateClientProtocol`); com a retirada dele (D-0510-1), o 106 por protocolo deixa de existir no testbed até a borda ter TLS.
- **L4. 106 ao associado em `/authorize` e `/token`.** Só bloqueia em `enforce`. O tv3ws continua emitindo token para o associado.
- **L5. Relógio do bind-token.** A norma usa o System Time Fragment. O plugin usa o relógio do host, sem tolerância de defasagem.
- **L7. Revogação de chave.** Revogar uma chave não libera os recursos compartilhados (C.4.4). Não foi implementado.
- **Associado em contexto de outra emissora.** A dispensa de bind-token vale para o próprio contexto do associado. A borda não restringe o acesso dele a `/tv3/{serviceContextId}/...` de outra emissora.
- **Sem serviço corrente** (`session:current-service-id` vazio). Nas rotas `token+bind`, o resultado é 108; a norma também prevê 300 nessas APIs. No registro da C.6.8 (`POST /tv3/bind-context`), o resultado é 300, como era no tv3ws: a Tabela C.47 não lista erro para esse caso.
- **C.6.7.8 com id da Tabela C.2 que o testbed não implementa.** Responde 101, como um id inválido. A Tabela C.45 não prevê outro erro, e não há "última versão suportada" de uma API que o receptor não tem.
- **Versão de cada API na C.6.7.8 e na C.6.7.9.** É a "Current version" da Tabela C.2, 2.0 em todas (C.3.6.3), no campo `api.version` do `routes.json`. O tv3ws também aceita `Accept-Version: 2.1`, a proposta do Fórum que muda `GET /tv3/remote-device/devices/{classId}`. Não foi decidido se essa API deve aparecer como 2.1.
- **C.6.7.9 sem `subsystem`.** A norma deixa o parâmetro opcional na URL e não diz o que listar sem ele. O provisório é listar todas as APIs implementadas.
- **Redis sem senha e publicado no host (6379). Decidido pelo Luís em 03/10:** a conexão com o banco fica como está, sem senha; só a interface administrativa (redis-commander) passou a exigir login. O risco continua: quem alcança a porta pode gravar chaves de bind ou origens associadas, e a borda confia no Redis (registrado em `docs/avaliacao-item9-credenciais.md`, `docs/decisoes-pendentes.md` A1 e `KNOWN-ISSUES.md`, na raiz). Não é comportamento provisório do plugin e não tem marcador no código.
- **`/tv3/token` sem `Origin` (L4).** O 106 ao associado depende do `Origin`. Um `/tv3/token` chamado fora do navegador passa, e o tv3ws emite o token com a classe gravada do cliente. Um access token válido de outra classe também prevalece sobre o `Origin`.
