# Modelo de dados — Redis

> Redis, funções e esquema são decisões deste testbed; a ABNT NBR 25608 não fala de armazenamento. O esquema cita a norma só onde uma regra vem dela (por exemplo, a Tabela 7 nos perfis). O modelo com a descrição de cada família também está em `docs/modelo-redis.md` da raiz do TV30, e as funções em [`redis/README.md`](../redis/README.md).

O Redis guarda o estado que os serviços compartilham: sessão (usuário e serviço correntes), perfis, clientes autorizados e bloqueados, origens das apps de emissora e chaves de bind-token. Ele não guarda ACL de tópicos nem nada que o broker consulte.

## Famílias de chave, quem escreve e quem lê

Toda escrita do código nas famílias abaixo passa por uma função da biblioteca `tv30`, que confere o esquema e grava de forma atômica. O dono de cada família é o do campo `dono` do `esquema.json` (um dono por família, com uma exceção, nas notas abaixo).

| Chave | Tipo | Quem escreve (função) | Quem lê |
|---|---|---|---|
| `session:current-user` | string | tv3ws: `tv30_sessao_usuario` (espelho de `aop/currentUser`; C.6.14.4) | tv3ws |
| `session:current-service-id` | string | tv3ws: `tv30_sessao_servico_id` (espelho de `aop/currentService`) | tv3ws, borda (C.6.8 e bind-token), AoP (serviço do perfil criado) |
| `session:current-service` | hash: `serviceContextId`, `serviceId`, `serviceName`, `transportStreamId`, `originalNetworkId` | tv3ws: `tv30_sessao_servico` (troca de serviço; regrava o hash inteiro) | borda (C.6.8.3) |
| `users:index` | set | AoP: `tv30_perfil_grava` (criação); carga inicial | AoP, tv3ws |
| `user:{id}` | hash: atributos da Tabela 7 + `lastAccess` | AoP: `tv30_perfil_grava`, `tv30_perfil_ultimo_acesso`; carga inicial | AoP, tv3ws |
| `user:{id}:consent` | set de ids de serviço | AoP: `tv30_perfil_grava` (acrescenta); carga inicial | tv3ws (C.6.14.1, C.6.14.2, C.6.14.5, C.6.14.6) |
| `user:{id}:broadcaster-attrs:{scid}` | hash | tv3ws: `tv30_atributos_emissora` (C.6.14.5) | tv3ws |
| `client:{id}` | hash: `class`, `refreshToken`, `accessToken` | tv3ws: `tv30_cliente_grava`, `tv30_cliente_autoriza` | tv3ws |
| `clients:authorized` | set | tv3ws: `tv30_cliente_autoriza`, `tv30_cliente_bloqueia` (tira), `tv30_cliente_inclui_autorizado` (boot) | tv3ws, borda |
| `clients:blocked` | set | tv3ws: `tv30_cliente_bloqueia`, `tv30_cliente_autoriza` (tira) | tv3ws, borda |
| `origins:associated` | hash: origem → id do serviço corrente | AoP: `tv30_origem_registra` (lançamento da app), `tv30_origem_remove` (encerramento), `tv30_origens_limpa` (boot) | borda (`HGET`), tv3ws (`HGET`, no `/tv3/authorize`) |
| `bind-context:{serviceId}` | list de JSON `{alg, key, registeredAt}` | borda: `tv30_bind_registra` (C.6.8.2), `tv30_bind_remove` (C.6.8.4) | borda (bind-token e C.6.8.3) |

Notas sobre a tabela:
- **`user:{id}:consent` não é o consentimento da norma.** O conjunto diz em que serviços o perfil é visível às APIs C.6.14 (`esquema.json`: "NAO e o consentimento da secao 8.8 da norma"). O Privacy Manager da seção 8.8 (registros de privacidade, PRRD) não está implementado.
- **Remoção de perfil:** `tv30_perfil_remove` (tira o perfil, o `:consent` e todos os `:broadcaster-attrs:*`) está no `esquema.json` e no adaptador do AoP (`perfilRemove`, em `aop/src/redis-funcoes.js`), mas nenhum código o chama: o AoP aceita no máximo cinco perfis e recusa o sexto, sem remover nenhum (`createProfile`, em `aop/src/modules/profile-manager/service.js`). Por ela, o `esquema.json` declara dois escritores em `user:{id}:broadcaster-attrs:{scid}`: o tv3ws, que grava, e o AoP, que a apagaria junto com o perfil.
- **Id do perfil:** UUID estrito: versão de 1 a 8, variante da RFC 9562 (`8`, `9`, `a` ou `b`), e o UUID nulo e o máximo são recusados. Os ids do AoP (`crypto.randomUUID()`, versão 4) e o da carga (`c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10`, versão 4) passam.
- **Avatar:** o `avatar` de `user:{id}` (tipo `nomeArquivo`), quando existe, é o nome de um arquivo do diretório de miniaturas (`<USER_DATA_PATH>/thumbs` no AoP = `USER_THUMBS` no tv3ws, o volume `./user-files` da raiz), no formato `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$`, como a Tabela 7 pede (*file path*). O AoP grava `<id>.svg` no diretório ao criar o perfil (e o apaga se o perfil for recusado); o tv3ws o entrega pela C.6.14.6. O SVG em linha é recusado ([`redis/README.md`](../redis/README.md)).
- **Origens associadas:** a origem gravada é a própria da app, `http://<rótulo>.<APPS_DOMAIN>:<AOP_PUBLIC_PORT>` (padrão `<rótulo>.apps.localhost:8080`), a que o navegador manda no `Origin`, e o valor é o id do serviço corrente. Sem serviço corrente, a origem não é gravada (`registerAssociatedOrigin`, em `aop/src/core.js`).
- **Carga inicial:** o container `redis` gera no build (`redis/emit_seed.py`, a partir de `infra/user-files-template/userData.json`) uma chamada `tv30_perfil_grava` por perfil e a aplica uma vez, só em banco vazio (sem `seed:done`). O arquivo do avatar do perfil da carga fica em `infra/user-files-template/thumbs/`, e quem o leva a `./user-files/thumbs` é o `userfiles-seed` da raiz, a cada subida, se faltar.

## Fora do esquema

Ficam fora do alcance das funções e da auditoria (`foraDoAlcance` do `esquema.json`), com escrita direta:
- `remote-devices:index`, `remote-devices:class:{classe}` e `remote-device:{handle}`: espelho dos dispositivos remotos conectados, gravado pelo tv3ws (`tv3ws/src/modules/remotedevice-manager/manager.ts`, em `MULTI`), limpo no boot dele e lido só por ele;
- `seed:done`: marca da carga inicial, gravada pelo entrypoint do container e lida pelo healthcheck;
- `tv30:{componente}:exemplo`: chave do exemplo do template de componente, só dos scripts de teste.

## Erro de esquema e auditoria

Dado fora do esquema volta como erro `TV30ESQUEMA` (função, chave, campo e motivo), e nada é gravado. Para os serviços, a recusa é defeito do serviço: a rota HTTP responde 404 `{"error":200}`, e o handler MQTT só registra no log.

A conexão fica sem senha e com escrita: o Redis aplica o ACL de quem chama também dentro da função, então não há como liberar só a escrita pela função. A escrita manual continua possível, e a auditoria a mostra:

```bash
docker exec redis redis-cli FCALL_RO tv30_auditoria 0
```

Vazia quer dizer tudo no esquema. Ela percorre os prefixos `session:`, `users:`, `user:`, `clients:`, `client:`, `origins:` e `bind-context:` e confere tipo, campos, membros e as invariantes `perfil-indexado`, `perfil-dependentes`, `clientes-disjuntos` e `autorizado-tem-registro`.

## Quem não usa o Redis

- **O broker MQTT.** O plugin C não abre conexão com o Redis; só valida esquema e responde ao teste de latência ([03](03-pipeline-mqtt.md)). O `migrate_to_redis.py` fica na imagem do broker só para depuração manual (grava perfis pela função `tv30_perfil_grava`); a partida do broker não o executa.
- **O bcast.** Não lê nem grava o Redis.

