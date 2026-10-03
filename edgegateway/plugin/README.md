# Plugin `tv30-auth` — validação de credenciais na borda

Plugin Go do tipo **http-server** do KrakenD, carregado pelas duas superfícies do `edgegateway` (interna 44642 e externa 44643). Ele implementa a decisão da reunião de 28/09 (D1): **toda a validação de credenciais fica na borda**, e o tv3ws só implementa as APIs.

> **Estado corrente, não a decisão.** O tv3ws ainda valida parte da credencial, nos dois modos: responde 107 a um `Authorization` presente e inválido (`tv3ws/src/middleware/authorization.ts`) e 106 ao cliente não local que chega por HTTP (`validateClientProtocol` em `tv3ws/src/middleware/basic.ts`; a superfície interna repassa ao `http://tv3ws:44652`). A limpeza do tv3ws não foi decidida.

O plugin anterior (`consent-validator`, removido em `infra@f2eb652`) não validava nada: repassava qualquer caminho ao tv3ws. Deste plugin só foi aproveitado o esqueleto de registro.

## O que ele valida

Cada requisição passa por quatro etapas, nesta ordem. A primeira que falha define o código de erro.

| # | Etapa | Erro | Regra |
|---|---|---|---|
| 1 | Rota | 100 | Método e caminho precisam estar declarados em `routes.json` para a superfície. Um caminho não declarado não chega ao roteador, o que elimina o panic do Gin descrito em `KNOWN-ISSUES.md`. O preflight CORS (`OPTIONS` com `Access-Control-Request-Method`) sempre passa e é respondido pelo módulo CORS do KrakenD. `OPTIONS` sem esse cabeçalho não é preflight: num caminho declarado (com qualquer método), o plugin responde 200 com `Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: *` e `Access-Control-Allow-Headers` igual ao `cors.allow_headers` do `routes.json` (C.4.1.9.3); num caminho não declarado, dá 100. |
| 2 | Classe | 106 | A classe vem do claim `class` do access token. Sem `Authorization`, o cliente é tratado como local associado se o `Origin` estiver no hash `origins:associated`. Com `Authorization` presente mas inválido, o `Origin` também é consultado, só para esta checagem: mandar um `Authorization` qualquer não escapa do 106, e o 107 continua valendo. A rota declara as classes permitidas em `classes`. |
| 3 | Access token | 107 | `Authorization: Bearer <jwt>`, com `alg` fixo em HS256, segredo `JWT_SECRET`, `exp` obrigatório e não vencido, `nbf` respeitado se existir e `iss` igual a `JWT_ISSUER`. Cliente cujo `sub` está em `clients:blocked` recebe 107 (C.4.2.2), inclusive com token de classe `local-associated`. |
| 4 | Bind-token | 104 / 108 | Vale só nas rotas `token+bind`. Sem o cabeçalho `bind-token`, o erro é 104. O token tem de ser assinado por uma chave registrada para o **serviço corrente** (ver abaixo); se não for, o erro é 108. |

O local associado não precisa de access token nem de bind-token em nenhuma rota que o admita (C.4.1.1).

Toda resposta leva `Access-Control-Allow-Origin: *` (C.4.1.9.2), também a do cliente que não manda `Origin`: o plugin define o cabeçalho antes de repassar, com o mesmo `Set` do módulo CORS, e ele sai uma vez só.

Falhas de infraestrutura têm código próprio:
- **Redis fora do ar.** Em `warn`, o plugin registra no log e deixa passar. Em `enforce`, responde 404 com `{error: 200}`.
- **Panic no roteador.** O plugin responde 404 com `{error: 200}` em vez de deixar a conexão resetar.

### Bind-token: verificação em quatro frentes (C.4.1.4)

A verificação segue esta ordem:

1. **Formato.** JWS compacto com 3 partes base64url e cabeçalho com `alg`. Token cifrado (JWE) ou com `crit` é recusado.
2. **Assinatura.** O token é aceito se **qualquer** chave da lista `bind-context:{serviceId}` validar a assinatura. `serviceId` é o valor de `session:current-service-id`.
   - O `alg` do token tem de ser igual ao `alg` registrado da chave. Isso impede a confusão de algoritmo, como um HS256 assinado com a chave pública RSA usada como segredo.
   - Só valem HS256, HS512, RS256 e RS512. `none` é sempre inválido.
3. **Tempo: `nbf` e `exp`.** Quando presentes, o token só vale a partir de `nbf` e deixa de valer em `exp`.
4. **Emissão: `iat`.** Quando presente, `iat` não pode estar no futuro.

As claims `iat`, `nbf` e `exp` são opcionais para o bind-token, porque a norma usa *should* (C.4.1.3).

Quem grava as chaves é a API C.6.8 do tv3ws (`POST /tv3/bind-context`). A lista é um LIST no Redis, e cada entrada tem o formato `{"alg","key","registeredAt"}`. Entradas malformadas ou chaves ilegíveis são ignoradas, com registro no log. Uma entrada ruim não invalida as outras chaves da mesma emissora.

**Codificação da chave.** A norma não define. O formato abaixo é decisão de implementação do testbed:
- **HS256 e HS512:** o segredo são os bytes UTF-8 da string.
- **RS256 e RS512:** PEM (`PUBLIC KEY` ou `RSA PUBLIC KEY`) ou DER em base64 (SPKI ou PKCS#1 público). O PEM só vale se o texto aparado começar por `-----BEGIN` e o rótulo for um destes quatro: `PUBLIC KEY`, `RSA PUBLIC KEY`, `RSA PRIVATE KEY`, `PRIVATE KEY`. O base64 vale nas codificações padrão e URL, com ou sem preenchimento.
- **Mesmas regras no registro.** O tv3ws aplica as mesmas regras ao aceitar a chave (`broadcaster-security/bind-token.ts`). Um teste cruzado lê o mesmo `testdata/keyformats.json` dos dois lados (`keys_test.go` e `tv3ws/test/bind-token.test.ts`; cópia em `tv3ws/test/fixtures/`), para que o registro não aceite chave que a borda não lê.
- **Chave privada** (PKCS#1 ou PKCS#8, como no exemplo `MIIBOgIBAAJB...` da norma): é aceita, e o plugin deriva dela a chave pública.
- **Limite das chaves de 512 bits:** como a do exemplo, só servem para RS256. O DigestInfo do SHA-512 não cabe num módulo de 64 bytes. O registro do tv3ws responde 101 a RS512 com módulo menor que 752 bits (e a RS256 com menos de 496).

### Política por rota

A política fica nos campos `auth` e `classes` de cada rota em `../routes.json`:
- **`auth`:** `none`, `token` ou `token+bind`. Se o campo faltar, vale `token`.
- **`classes`:** lista das classes permitidas, com os valores exatos que o tv3ws grava no claim: `local-associated`, `local-autonomous` e `non-local`. Se o campo faltar, todas as classes são permitidas.

O `generate.js` monta o bloco do plugin a partir desses campos. Valor inválido derruba o build. O `generate.js` também acrescenta `bind-token` aos `input_headers` das rotas `token+bind`.

| Rotas | auth | classes |
|---|---|---|
| `GET /health`, `GET /manifest` | none | todas |
| `GET /tv3/authorize`, `GET /tv3/token` | none | `local-autonomous`, `non-local` |
| `POST /tv3/bind-context`, `DELETE /tv3/bind-context` | none | `local-associated` |
| `GET /tv3/bind-context` | token | `local-autonomous`, `non-local` |
| `POST /tv3/current-service/users`; `POST /tv3/{serviceContextId}/users`; `GET` e `POST .../users/current-user`; `GET .../users/files`; `GET .../users/{userid}`; `GET` e `POST /tv3/{serviceContextId}/users/{userid}`; `GET .../apps/{appid}/files`; `POST /tv3/sensory-effect-renderers/{rendererId}` | token+bind | todas |
| demais | token | todas |

Observações sobre a tabela:
- **Critério do `token+bind`.** É o campo "Security requirements" = *shall* da tabela de cada API na norma.
- **`GET /tv3/bind-context`.** O bind-token desta rota é validado pelo próprio tv3ws, porque faz parte da lógica da API. Por isso a rota está como `token`.
- **`POST /tv3/{serviceContextId}/users`.** Não existe na norma: só há `POST /tv3/current-service/users` (C.6.14.1, *shall*). O tv3ws atende os dois caminhos com o mesmo handler, que ignora o scid. A rota começou como `token`, o que deixava a mesma operação passar sem bind-token por esse caminho (e com `Current-Service`, porque o Express não diferencia caixa). Agora está como `token+bind`, e vale a regra provisória da L2: scid diferente de `current-service` e da constante dá 108. É PENDENTE: o Joel decide se a rota fica ou sai da tabela.

## Configuração

O plugin lê o ambiente do container, definido no serviço `edgegateway` de `infra/docker-compose.yml`.

| Variável | Padrão | Uso |
|---|---|---|
| `AUTH_ENFORCE` | `warn` | Com `warn`, nada é bloqueado: o plugin loga `[tv30-auth] WARN ...` e acrescenta `X-TV30-Auth-Warn: <código>` à resposta. Com `enforce`, responde 404 com o corpo C.3.2. |
| `JWT_SECRET` | sem padrão (obrigatória) | Tem de ser **o mesmo** do tv3ws, que emite o access token. O compose usa o mesmo padrão de desenvolvimento nos dois serviços. |
| `JWT_ISSUER` | `GenericIssuer` | Igual ao tv3ws. |
| `REDIS_HOST` / `REDIS_PORT` | `redis` / `6379` | Mesmo Redis do resto da stack. A variante `windows` (dev-host) também usa este Redis. |
| `REDIS_TIMEOUT_MS` | `500` | Timeout de cada comando. |

Exceções que valem nos dois modos: o erro 100 (rota não declarada) e o 200 (panic do roteador). Nenhum cliente depende de uma rota não declarada, porque ela nunca chegou ao tv3ws. O modo `warn` existe porque clientes como o Guaraná ainda não obtêm token.

**Morre-inteiro.** O KrakenD só emite um aviso e sobe **sem** o plugin quando o registro falha. Por isso, configuração inválida derruba o processo com `os.Exit(1)` e uma linha `[tv30-auth] FATAL ...`, e o entrypoint derruba o container. Os casos são:
- `JWT_SECRET` ausente;
- `AUTH_ENFORCE` com valor diferente de `warn` ou `enforce`;
- política de rota inválida.

O build também falha se o `.so` não casar com o binário do KrakenD (`krakend check-plugin` e `krakend test-plugin -s` no Dockerfile).

**Erros.** Todas as respostas de erro seguem a C.3.2:
- status 404;
- corpo `{"error": <n>, "description": "..."}`;
- cabeçalhos `Content-Type: application/json`, `Access-Control-Allow-Origin: *` e `API-Version`.

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
- o cliente Redis (servidor RESP falso, reconexão e timeout).

**Teste de integração com a stack real:** `scripts/test-auth.sh`, na raiz do TV30. Ele roda contra 44642 e 44643. Em `warn`, verifica o aviso 107 e o erro 100. Depois recria só o `edgegateway` em `enforce` e cobre:
- 107, com o token obtido pelo fluxo real `/tv3/authorize` + `/tv3/token`;
- 104, 106 e 108;
- a API C.6.8 nos quatro algoritmos;
- isolamento entre serviços e confusão de algoritmo;
- preflight CORS.

No fim, o script volta a borda ao modo anterior e desfaz o que semeou no Redis.

`testdata/jsonwebtoken.json` traz tokens gerados pelo `jsonwebtoken` (a biblioteca do tv3ws). Eles servem para conferir a interoperabilidade. O arquivo tem só material público e segredos de teste.

## PENDENTE (Joel)

Comportamentos provisórios, cada um marcado no código com `PENDENTE (Joel)`:

- **L1. Como reconhecer o local associado.** Hoje o critério é só o `Origin` presente em `origins:associated`.
  - A C.4.1.7 sugere usar a porta de origem.
  - O `Origin` não distingue as apps de emissora servidas por proxy na origem do AoP.
  - Fora do navegador, o `Origin` pode ser forjado.
- **L2. `{serviceContextId}` no caminho.** O tv3ws usa um `serviceContextId` constante (`tv3ws/src/core.ts`), copiado para `routes.json` em `auth.current_service_context_id`. Nas rotas `token+bind`, só `current-service` e essa constante contam como serviço corrente; qualquer outro valor dá 108.
  - A C.3.5 diz que o `<service-context-id>` segue o `globalServiceId` do SLT. Isso não foi implementado.
- **L3. TLS na borda.** A 44643 ainda é HTTP. O 106 por protocolo (cliente não local usando HTTP) não é aplicado pela borda.
- **L4. 106 ao associado em `/authorize` e `/token`.** Só bloqueia em `enforce`. O tv3ws continua emitindo token para o associado.
- **L5. Relógio do bind-token.** A norma usa o System Time Fragment. O plugin usa o relógio do host, sem tolerância de defasagem.
- **L7. Revogação de chave.** Revogar uma chave não libera os recursos compartilhados (C.4.4). Não foi implementado.
- **Associado em contexto de outra emissora.** A dispensa de bind-token vale para o próprio contexto do associado. A borda não restringe o acesso dele a `/tv3/{serviceContextId}/...` de outra emissora.
- **Sem serviço corrente** (`session:current-service-id` vazio). O resultado é 108; a norma também prevê 300 nessas APIs.
- **Redis sem senha e publicado no host (6379).** Quem alcança a porta pode gravar chaves de bind ou origens associadas. Isso é risco, não implementação: a borda confia no Redis. Precisa de decisão antes de qualquer `enforce` (registrado também em `docs/avaliacao-item9-credenciais.md`). Opções: publicar só em `127.0.0.1` (o dev-host continua funcionando, porque usa `127.0.0.1` e `--network host`) ou exigir senha no Redis.
- **Em `enforce`, o `Origin` é a porta de entrada sem credencial (L1).** Um `Origin` forjado fora do navegador, presente em `origins:associated`, dispensa access token e bind-token em toda rota que admite o associado, inclusive `POST` e `DELETE /tv3/bind-context`.
- **`/tv3/token` sem `Origin` (L4).** O 106 ao associado depende do `Origin`. Um `/tv3/token` chamado fora do navegador passa, e o tv3ws emite o token com a classe gravada do cliente. Um access token válido de outra classe também prevalece sobre o `Origin`.
