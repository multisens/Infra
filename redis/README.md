# infra/redis — Redis do testbed, com esquema nas escritas

Imagem `tv30-redis`: o Redis do testbed TV 3.0, com a carga inicial dos perfis, a **biblioteca de funções que confere o esquema de toda escrita** feita pelo código do projeto (P4, decisão do Luís em 10/10, "Funções + auditoria") e o Redis Commander para inspeção.

Redis, funções e esquema são decisões de implementação deste testbed. A ABNT NBR 25608 não fala de armazenamento; o esquema só cita a norma onde uma regra vem dela (por exemplo, a Tabela 7 nos perfis). O modelo completo (quem escreve e quem lê cada família) está em [`docs/modelo-redis.md`](../../docs/modelo-redis.md), na raiz do TV30.

## O que tem nesta pasta

| Arquivo | Papel |
|---|---|
| `esquema.json` | **Arquivo único** dos esquemas: famílias de chave, tipo, regra de cada campo, invariantes entre chaves, funções e o que fica fora do alcance. A biblioteca o embute (`biblioteca.sh`) e os testes o leem. As invariantes são declaradas aqui e implementadas no `funcoes.lua`. |
| `funcoes.lua` | Corpo da biblioteca de funções `tv30`: o interpretador das regras do `esquema.json`, uma função por operação de escrita e a auditoria. |
| `biblioteca.sh` | Monta a biblioteca que o Redis carrega: cabeçalho `#!lua name=tv30` + o `esquema.json` embutido numa string Lua + o `funcoes.lua`. |
| `entrypoint.sh` | Partida do container: sobe o `redis-server`, carrega a biblioteca, aplica a carga inicial (só em banco vazio) e sobe o commander. |
| `confere-imagem.sh` | Conferência no build: biblioteca que não carrega, carga inicial fora do esquema ou auditoria com achados não viram imagem. |
| `emit_seed.py` | Gera no build o `seed.resp` a partir de `infra/user-files-template/userData.json`: uma chamada `FCALL tv30_perfil_grava` por perfil. |
| `Dockerfile`, `docker-compose.yml` | Imagem e serviço `redis` (o healthcheck só fica saudável depois da biblioteca e da carga). |
| `test/` | Testes num Redis real descartável (`run.sh`), teste estático das escritas e contrato dos adaptadores. |

## Como o container sobe

1. `redis-server` com AOF (`appendonly yes`).
2. **Biblioteca de funções**, a cada partida, com `FUNCTION LOAD REPLACE`. O Redis guarda as funções no AOF; o `REPLACE` garante que vale a versão desta imagem, não a que ficou no volume. Se a biblioteca não carregar, o container encerra com a mensagem do Redis no log (sem ela, toda escrita dos serviços falharia com `ERR Function not found`).
3. **Carga inicial**, só se não existir a chave `seed:done`: `redis-cli --pipe < /seed.resp`, que chama `tv30_perfil_grava` para cada perfil do `userData.json`. Se alguma chamada for recusada pelo esquema, `seed:done` não é gravado e o container fica *unhealthy*, com os erros no log. Na prática isso não acontece com a imagem do repositório: o build já aplicou a mesma carga e falharia antes.
4. Marca `/tmp/tv30-redis-pronto` e commander na porta 18081, com login (D-L1).
5. **Vigia (morre-inteiro, C2, decisão do Luís em 11/10).** O entrypoint confere os dois processos a cada segundo. Se o `redis-server` ou o commander cair, ele encerra o outro, espera o `redis-server` terminar de desligar (AOF gravado) e sai com 1; o `restart: unless-stopped` do compose traz o container de volta. Até 11/10 o commander era exceção (decisão V8/V9): a morte dele não derrubava o banco. No `docker stop`, o entrypoint para o commander e espera o `redis-server` desligar antes de sair.

O healthcheck exige a marca e `seed:done`. A marca é apagada no começo de cada partida, então um `docker restart` só volta a ficar saudável depois de recarregar a biblioteca.

Logs esperados:

```
[redis] biblioteca de funcoes tv30 carregada (esquema versao 4)
[redis] aplicando carga inicial pelas funcoes (redis-cli --pipe)...
[redis] carga concluida (1 perfil(is))
[redis] commander (debug) na porta 18081, com login (usuario: admin)
```

Se um dos dois processos cair, o log termina com `[redis] commander (pid <n>) morreu — derrubando o container inteiro (morre-inteiro, C2)` (ou `redis-server`, no lugar de `commander`).

## As funções

Toda escrita do código do projeto nas famílias do alcance passa por uma função. A função lê o que precisa, monta o estado que vai resultar, confere tudo (o valor de cada campo, os campos obrigatórios, o tipo de cada chave no Redis e as invariantes entre chaves) e **só então** grava. Isso importa porque o Redis não desfaz o que uma função já gravou quando ela dá erro no meio. Conferido em 10/10 no redis 7.4.11: um `SET` seguido de um `SADD` com `WRONGTYPE` deixa o `SET` gravado. Como nenhum outro comando roda no meio de uma função, a operação é atômica.

As funções são chamadas com `numkeys 0`: elas montam os nomes das chaves a partir dos argumentos (flag `no-cluster`; o testbed usa um Redis só).

| Função | Argumentos | Efeito | Quem chama |
|---|---|---|---|
| `tv30_sessao_usuario` | `<id ou vazio>` | `SET session:current-user` | tv3ws (espelho de `aop/currentUser`; C.6.14.4) |
| `tv30_sessao_servico_id` | `<id ou vazio>` | `SET session:current-service-id` | tv3ws (espelho de `aop/currentService`) |
| `tv30_sessao_servico` | `<campo> <valor> ...` | regrava `session:current-service` inteiro | tv3ws (troca de serviço) |
| `tv30_perfil_grava` | `<id> <n> <serviço 1..n> <campo> <valor> ...` | `users:index` + `user:{id}` regravado + serviços acrescentados a `user:{id}:consent` | AoP (criação), carga inicial, `migrate_to_redis.py` |
| `tv30_perfil_ultimo_acesso` | `<id> <ms>` | `lastAccess`, só de perfil em `users:index` (devolve 1 ou 0) | AoP (troca de usuário corrente) |
| `tv30_perfil_remove` | `<id>` | tira o perfil e as quatro famílias dele | AoP (despejo P3) |
| `tv30_atributos_emissora` | `<id> <scid> <campo> <valor> ...` | `HDEL` dos vazios e `HSET` dos demais, teto de 20 MB | tv3ws (C.6.14.5) |
| `tv30_cliente_grava` | `<id> <campo> <valor> ...` | `HSET client:{id}` | tv3ws (emissão e rotação de token) |
| `tv30_cliente_autoriza` | `<id> <campo> <valor> ...` | `client:{id}` + sai de `clients:blocked` + entra em `clients:authorized` | tv3ws (pop-up aceito) |
| `tv30_cliente_bloqueia` | `<id>` | sai de `clients:authorized` + entra em `clients:blocked` | tv3ws (pop-up recusado) |
| `tv30_cliente_inclui_autorizado` | `<id>` | entra em `clients:authorized` se tem `client:{id}` e não está bloqueado | tv3ws (boot) |
| `tv30_origem_registra` / `tv30_origem_remove` / `tv30_origens_limpa` | `<origem> <serviço>` / `<origem>` / nada | `origins:associated` | AoP (lançamento e encerramento de app; boot) |
| `tv30_bind_registra` / `tv30_bind_remove` | `<serviceId> <entrada JSON>` | `RPUSH` / `LREM 0` em `bind-context:{serviceId}` | borda (C.6.8.2, C.6.8.4) |
| `tv30_auditoria` (só leitura) | nada | lista o que está fora do esquema | testes e desenvolvedor |
| `tv30_versao` (só leitura) | nada | versão do `esquema.json` | desenvolvedor |

Os adaptadores que fazem essas chamadas são `tv3ws/src/redis-funcoes.ts`, `aop/src/redis-funcoes.js` e `infra/edgegateway/plugin/redis.go` (`fcall`), além da carga inicial (`emit_seed.py`).

### Erro de esquema

Dado fora do esquema volta como erro do Redis com prefixo próprio, a função, a chave, o campo e o motivo. Nesse caso, nada é gravado:

```
$ docker exec redis redis-cli FCALL tv30_perfil_grava 0 0b6e3c1a-7f2d-4e8b-9a5c-3d1f2e4a6b8c 0 id 0b6e3c1a-7f2d-4e8b-9a5c-3d1f2e4a6b8c nickname X parentalControl False closedCaptioning false closedSigning false audioDescription false dialogEnhancement false voiceGuidance false
TV30ESQUEMA tv30_perfil_grava: user:0b6e3c1a-7f2d-4e8b-9a5c-3d1f2e4a6b8c campo parentalControl: deve ser "true" ou "false" (veio "False")
```

Para os serviços, a recusa é **defeito do serviço**. O erro sobe como qualquer falha do Redis: a rota HTTP responde `404 {"error":200}` e loga a mensagem; um handler MQTT só loga. Na borda, o `description` diz `escrita recusada pelo esquema do Redis (...)`.

## O esquema (`esquema.json`)

Cada família tem `chave` (com partes como `{id}`), `tipo` (`string`, `hash`, `set`, `list`), `dono` e as regras:

- `valor` (string), `campos` e `outrosCampos` (hash), `membros` (set), `itens` (list), `partes` (as partes da chave);
- cada regra tem um `tipo`: `texto` (`min`/`max` em caracteres, `proibe`), `booleano` (`"true"`/`"false"`), `inteiro` (decimal sem zero à esquerda, `min`/`max`, `vazio`), `enum`, `idioma` (formato IETF RFC 5646), `uuid` (formato textual da IETF RFC 9562, em minúsculas, com versão de 1 a 8 e variante da RFC 9562, `8`, `9`, `a` ou `b`, o que deixa de fora o UUID nulo e o máximo; o id do perfil, F-3, estrito desde 12/10), `nomeArquivo` (nome de arquivo sem diretório: começa por letra ou dígito, só letras sem acento, dígitos, `.`, `_` e `-`, até `maxNome` caracteres antes da extensão, e a extensão, o que vem depois do último ponto, numa das `extensoes`, em minúsculas; o avatar do perfil, desde a G8), `origem` (como o navegador manda no `Origin`) e `json` (objeto com `campos`);
- `obrigatorio`, `obrigatorioSe` (`{campo, valor}`), `campoIgualAParte`, `camposProibidos`, `maxBytes`;
- `ref` reaproveita uma regra de `definicoes` (por exemplo, `idPerfil`);
- `fonte` diz de onde a regra veio. Uma lista `divergencias` registrava onde o código não seguia a norma, e desde a G8 (decisão do Luís em 12/10, à noite) não há nenhuma: o último item, o avatar gravado como SVG em linha, e não como caminho de arquivo (Tabela 7), saiu quando o avatar passou a nome de arquivo (tipo `nomeArquivo`, formato `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$`), e o `tv30_versao` passou a `4`. A recusa diz o que falhou, na ordem em que a mensagem ajuda mais: `SVG em linha nao serve`, diretório (`/` ou `\`), extensão, primeiro caractere, demais caracteres e tamanho (`motivoNomeArquivo`, em `funcoes.lua`). O id do perfil saiu das divergências em 11/10 (F-3, decisão do Luís): o esquema exige UUID, como a Tabela 7, e o `tv30_versao` passou a `2`. Em 12/10 (decisão do Luís), o UUID ficou estrito e o `tv30_versao` passou a `3`: além do formato, a versão (1º dígito do 3º grupo) tem de ser de 1 a 8 e a variante (1º dígito do 4º grupo), `8`, `9`, `a` ou `b`. O UUID nulo (`00000000-…`) e o máximo (`ffffffff-…`) não servem de id de perfil. A recusa diz qual parte falhou (por exemplo, `versao do UUID deve ser de 1 a 8 (1o digito do 3o grupo; veio 9 em ...)` ou `UUID nulo (...) nao serve de id de perfil`). O id da carga (`c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10`, versão 4, variante `9`) e os do AoP (`crypto.randomUUID()`, versão 4) continuam válidos.

As invariantes entre chaves (`perfil-indexado`, `perfil-dependentes`, `clientes-disjuntos`, `autorizado-tem-registro`) são declaradas no `esquema.json` e implementadas no `funcoes.lua`. A biblioteca recusa rodar se as duas listas não baterem.

**Para mudar uma regra:** edite o `esquema.json`; os testes de `test/` geram casos para ela. Rode `bash infra/redis/test/run.sh tudo` e reconstrua a imagem (`docker compose build redis`, na raiz do TV30): o arquivo é copiado para a imagem no build (`Dockerfile`), e a regra nova passa a valer quando o container é recriado com ela, porque o entrypoint recarrega a biblioteca a cada partida.

**Para uma operação de escrita nova:** escreva a função no `funcoes.lua`, acrescente o nome em `ESCRITA` (fim do arquivo) e em `funcoes` do `esquema.json`, chame-a pelo adaptador do serviço e acrescente testes. O teste estático falha se o serviço gravar direto.

## Auditoria

```bash
docker exec redis redis-cli FCALL_RO tv30_auditoria 0
```

A auditoria percorre as chaves dos prefixos do alcance (`session:`, `users:`, `user:`, `clients:`, `client:`, `origins:`, `bind-context:`) e devolve uma entrada `[chave, familia, motivo]` por problema: chave sem família, tipo errado, campo fora da regra, membro inválido e cada invariante quebrada. **Vazia = tudo no esquema.** `scripts/test-auth.sh` exige a auditoria vazia no fim, e `scripts/test-consolidacao.sh` a mostra. Exemplo:

```
1) 1) "user:9d1e2f3a-4b5c-4d6e-8f70-81a2b3c4d5e6"
   2) "user:{id}"
   3) "id fora de users:index (invariante perfil-indexado)"
```

Chaves fora do alcance não são auditadas: `remote-devices:*` e `remote-device:*` (espelho do tv3ws), `seed:done` e a chave de exemplo do template de componente (`foraDoAlcance` no `esquema.json`).

**Volume de antes do P4:** dados gravados antes das funções podem estar fora do esquema. Os casos conhecidos são os booleanos `"False"`/`"True"` da carga antiga e hashes `user:{id}` só com `lastAccess`, de antes de 05/10. A auditoria os mostra. Para começar limpo, use `docker compose down -v`, que apaga o volume e com ele os perfis criados. A outra saída é corrigir as chaves apontadas.

**Volume de antes da F-3 (11/10):** perfis gravados com o id antigo (`user_<epoch-ms>`, do AoP, e `c3167a18-5dc5`, da carga) ficam fora do esquema, sem compatibilidade (decisão do Luís). A auditoria aponta `users:index`, `user:{id}`, `user:{id}:consent` e `user:{id}:broadcaster-attrs:*` de cada um (`{id} da chave: deve ser UUID ...`). Com esse id, `tv30_perfil_grava` e `tv30_atributos_emissora` recusam a escrita; `tv30_perfil_ultimo_acesso` ainda grava o `lastAccess` (num registro que já existe, a função confere só o campo escrito) e `tv30_perfil_remove` ainda remove (não confere o id). Como a carga só roda em banco vazio (`seed:done`), um volume antigo também não recebe o perfil novo da carga (`c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10`). Para começar limpo, recrie o volume, como acima.

**Volume de 11/10 a 12/10 (esquema versão 2):** os ids gravados pelo AoP (`crypto.randomUUID()`, versão 4) e pela carga (versão 4, variante `9`) continuam válidos com o UUID estrito. Só um id gravado por fora deles (à mão ou por `FCALL` direto) com versão fora de 1 a 8, variante fora de `8`, `9`, `a` e `b`, nulo ou máximo passa a aparecer na auditoria, com as mesmas consequências do parágrafo acima.

**Volume de antes da G8 (esquema versão 3, até a noite de 12/10):** os ids continuam válidos, mas o `avatar` em SVG em linha que o AoP e a carga gravavam fica fora do esquema (decisão do Luís, sem compatibilidade). A auditoria aponta o `user:{id}` de cada um (`campo avatar: SVG em linha nao serve: ...`). `tv30_perfil_ultimo_acesso`, `tv30_atributos_emissora` e `tv30_perfil_remove` continuam funcionando (conferem só o que gravam), e `tv30_perfil_grava` com o SVG em linha é recusada. O perfil da carga se conserta com `docker exec redis redis-cli HSET user:c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10 avatar c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10.svg` (o arquivo vem de `user-files-template/thumbs/` pelo `userfiles-seed` da raiz do TV30); os perfis do AoP de antes não têm arquivo, e o campo pode sair (`HDEL user:<id> avatar`) ou o volume pode ser recriado, como acima.

## Gravar à mão para depurar

A escrita direta pelo `redis-cli` ou pelo commander **continua possível** e não passa pelo esquema. Para gravar como um serviço gravaria, use a função:

```bash
docker exec redis redis-cli FCALL tv30_sessao_servico_id 0 urn:tv30:service:users-test
docker exec redis redis-cli FCALL tv30_origem_registra 0 http://users-test.apps.localhost:8080 urn:tv30:service:users-test
```

Se gravar direto (`HSET`, `SADD`...), rode a auditoria depois: ela aponta o que ficou fora do esquema.

### Por que não há bloqueio por ACL

A ideia inicial era deixar o usuário `default` só com leitura e liberar a escrita apenas pelas funções. Não funciona: o Redis aplica o ACL de quem chama **dentro** da função. Teste de 10/10 no redis:7-alpine (7.4.11), com `default` sem `@write` e com `+fcall`:

```
ERR ACL failure in script: User default has no permissions to run the 'set' command
```

Não há como, no Redis puro, bloquear a escrita direta e liberar só a feita pela função. A decisão do Luís (10/10) foi manter a conexão como está (`default` sem senha e com escrita, decisão A1 de 03/10), sem usuário admin e sem ACL novo. As garantias passam a ser o teste estático, que impede o código de gravar direto, e a auditoria, que mostra o que entrou por fora.

## Testes

```bash
bash infra/redis/test/run.sh tudo        # ou: funcoes | estatico | contrato | imagem
```

| Modo | O que prova |
|---|---|
| `funcoes` | Biblioteca num `redis:7-alpine` descartável. Cobre aceitação; **recusa de cada regra**, com os casos gerados a partir do `esquema.json` e conferência de que nada foi gravado; valores de fronteira; atomicidade (tipo errado ou membro inválido no fim não deixam gravação parcial); invariantes; e auditoria, que aponta cada tipo de problema gravado à mão e volta vazia. Desde a G8, a regra do avatar (`nomeArquivo`) decide igual à expressão do contrato, nos valores de fronteira e em valores gerados, e o volume com o avatar em SVG em linha tem o comportamento descrito acima. |
| `estatico` | Nenhum serviço grava direto numa família do alcance. Cobre tv3ws e AoP (cadeias de comandos no cliente Redis fora do adaptador), plugin Go (texto de comando de escrita) e carga inicial (Python). O mesmo teste roda no `npm test` do tv3ws e do AoP e no `go test` da borda (`escritas_test.go`, também no build da imagem dela). |
| `contrato` | Os adaptadores e os gestores de verdade (auth-manager do tv3ws, gestor de perfis e `app-hosts` do AoP, `redisStore` da borda) contra as funções num Redis real. Desde a G8, o gestor de perfis grava o avatar em `thumbs/` de um `USER_DATA_PATH` temporário: o `avatar` é `<id>.svg`, o arquivo tem o SVG do formulário, o despejo o apaga, e no fim só restam os arquivos dos perfis existentes. Termina com a auditoria vazia. |
| `imagem` | Build da imagem (com a conferência do `confere-imagem.sh`), healthcheck igual ao do compose, carga pelas funções (id do perfil em UUID, lido do template; desde a G8, o avatar da carga igual ao nome do arquivo que o template traz em `thumbs/`), booleanos `"false"`, a versão do esquema na imagem igual à do `esquema.json`, auditoria vazia, recarga da biblioteca numa nova partida e o morre-inteiro do commander (C2: a morte dele derruba o container com exit 1). |

Os containers, a rede e a imagem de teste têm nomes próprios (`tv30-redis-teste-<pid>`) e são removidos no fim. A stack não é tocada.
