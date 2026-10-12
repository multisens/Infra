# Modelo de dados — Redis

> **Estado do código em 12/10/2026**, conferido em `infra/redis/esquema.json` (versão 4, desde a noite de 12/10), `infra/redis/funcoes.lua` e no código que lê e grava cada família (`tv3ws/src`, `aop/src`, `infra/edgegateway/plugin`). Até 12/10 este documento trazia, abaixo de uma nota, o modelo de antes da consolidação; o que mudou está resumido no fim, em [Até 02/10 (histórico)](#até-0210-histórico), e os diagramas daquele modelo saíram (o diagrama da época continua em [00](00-diagrama-unificado.md#até-0210-histórico)).
>
> Redis, funções e esquema são decisões deste testbed; a ABNT NBR 25608 não fala de armazenamento. O esquema cita a norma só onde uma regra vem dela (por exemplo, a Tabela 7 nos perfis). O modelo com a descrição de cada família também está em `docs/modelo-redis.md` da raiz do TV30, e as funções em [`redis/README.md`](../redis/README.md).

O Redis guarda o estado que os serviços compartilham: sessão (usuário e serviço correntes), perfis, clientes autorizados e bloqueados, origens das apps de emissora e chaves de bind-token. Ele não guarda ACL de tópicos nem nada que o broker consulte.

## Famílias de chave, quem escreve e quem lê

Toda escrita do código nas famílias abaixo passa por uma função da biblioteca `tv30` (P4, decisão do Luís em 10/10, "Funções + auditoria"), que confere o esquema e grava de forma atômica. O dono de cada família é o do campo `dono` do `esquema.json` (P5, "um dono por família", aplicado na reunião de 05/10 com o Joel, D-0510-5).

| Chave | Tipo | Quem escreve (função) | Quem lê |
|---|---|---|---|
| `session:current-user` | string | tv3ws: `tv30_sessao_usuario` (espelho de `aop/currentUser`; C.6.14.4) | tv3ws |
| `session:current-service-id` | string | tv3ws: `tv30_sessao_servico_id` (espelho de `aop/currentService`) | tv3ws, borda (C.6.8 e bind-token), AoP (serviço do perfil criado) |
| `session:current-service` | hash: `serviceContextId`, `serviceId`, `serviceName`, `transportStreamId`, `originalNetworkId` | tv3ws: `tv30_sessao_servico` (troca de serviço; regrava o hash inteiro) | borda (C.6.8.3) |
| `users:index` | set | AoP: `tv30_perfil_grava` (criação), `tv30_perfil_remove` (despejo P3); carga inicial | AoP, tv3ws |
| `user:{id}` | hash: atributos da Tabela 7 + `lastAccess` | AoP: `tv30_perfil_grava`, `tv30_perfil_ultimo_acesso`, `tv30_perfil_remove`; carga inicial | AoP, tv3ws |
| `user:{id}:consent` | set de ids de serviço | AoP: `tv30_perfil_grava` (acrescenta), `tv30_perfil_remove`; carga inicial | tv3ws (C.6.14.1, C.6.14.2, C.6.14.5, C.6.14.6) |
| `user:{id}:broadcaster-attrs:{scid}` | hash | tv3ws: `tv30_atributos_emissora` (C.6.14.5); AoP apaga no despejo (`tv30_perfil_remove`) | tv3ws |
| `client:{id}` | hash: `class`, `refreshToken`, `accessToken` | tv3ws: `tv30_cliente_grava`, `tv30_cliente_autoriza` | tv3ws |
| `clients:authorized` | set | tv3ws: `tv30_cliente_autoriza`, `tv30_cliente_bloqueia` (tira), `tv30_cliente_inclui_autorizado` (boot) | tv3ws, borda (E6) |
| `clients:blocked` | set | tv3ws: `tv30_cliente_bloqueia`, `tv30_cliente_autoriza` (tira) | tv3ws, borda |
| `origins:associated` | hash: origem → id do serviço corrente | AoP: `tv30_origem_registra` (lançamento da app), `tv30_origem_remove` (encerramento), `tv30_origens_limpa` (boot) | borda (`HEXISTS`), tv3ws (`HGET`, no `/tv3/authorize`) |
| `bind-context:{serviceId}` | list de JSON `{alg, key, registeredAt}` | borda: `tv30_bind_registra` (C.6.8.2), `tv30_bind_remove` (C.6.8.4) | borda (bind-token e C.6.8.3) |

Notas sobre a tabela:
- **`user:{id}:consent` não é o consentimento da norma.** O conjunto diz em que serviços o perfil é visível às APIs C.6.14 (`esquema.json`: "NAO e o consentimento da secao 8.8 da norma"). O Privacy Manager da seção 8.8 (registros de privacidade, PRRD) não está implementado.
- **Exceção ao P5 sem decisão:** `user:{id}:broadcaster-attrs:{scid}` tem dois escritores, o tv3ws, que grava, e o AoP, que apaga no despejo (E4, `PENDENTE (Joel)` no código).
- **Id do perfil:** UUID (F-3, decisão do Luís em 11/10, esquema versão 2), estrito desde 12/10 (decisão do Luís, esquema versão 3): versão de 1 a 8, variante da RFC 9562 (`8`, `9`, `a` ou `b`), e o UUID nulo e o máximo são recusados. Os ids do AoP (`crypto.randomUUID()`, versão 4) e o da carga (`c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10`, versão 4) passam.
- **Avatar:** desde a G8 (decisão do Luís em 12/10, à noite, esquema versão 4, tipo `nomeArquivo`), o `avatar` de `user:{id}`, quando existe, é o nome de um arquivo do diretório de miniaturas (`<USER_DATA_PATH>/thumbs` no AoP = `USER_THUMBS` no tv3ws, o volume `./user-files` da raiz), no formato `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$`, como a Tabela 7 pede (*file path*). O AoP grava `<id>.svg` no diretório ao criar o perfil e o apaga no despejo; o tv3ws o entrega pela C.6.14.6. Até ali o campo aceitava qualquer texto, e o AoP e a carga gravavam o SVG em linha, que a versão 4 recusa: num volume de antes, a auditoria aponta esses perfis ([`redis/README.md`](../redis/README.md)).
- **Origens associadas:** desde o P1.3 (decisão do Luís em 10/10), a origem gravada é a própria da app, `http://<rótulo>.<APPS_DOMAIN>:<AOP_PUBLIC_PORT>` (padrão `<rótulo>.apps.localhost:8080`), a que o navegador manda no `Origin`. Sem serviço corrente, o valor gravado é `current-service`.
- **Carga inicial:** o container `redis` gera no build (`redis/emit_seed.py`, a partir de `infra/user-files-template/userData.json`) uma chamada `tv30_perfil_grava` por perfil e a aplica uma vez, só em banco vazio (sem `seed:done`). O arquivo do avatar do perfil da carga fica em `infra/user-files-template/thumbs/`, e quem o leva a `./user-files/thumbs` é o `userfiles-seed` da raiz, a cada subida, se faltar.

## Fora do esquema

Ficam fora do alcance das funções e da auditoria (`foraDoAlcance` do `esquema.json`), com escrita direta:
- `remote-devices:index`, `remote-devices:class:{classe}` e `remote-device:{handle}`: espelho dos dispositivos remotos conectados, gravado pelo tv3ws (`tv3ws/src/modules/remotedevice-manager/manager.ts`, em `MULTI`), limpo no boot dele e lido só por ele;
- `seed:done`: marca da carga inicial, gravada pelo entrypoint do container e lida pelo healthcheck;
- `tv30:{componente}:exemplo`: chave do exemplo do template de componente, só dos scripts de teste.

## Erro de esquema e auditoria

Dado fora do esquema volta como erro `TV30ESQUEMA` (função, chave, campo e motivo), e nada é gravado. Para os serviços, a recusa é defeito do serviço: a rota HTTP responde 404 `{"error":200}`, e o handler MQTT só registra no log.

A conexão segue sem senha e com escrita (A1 e D-L1, decisões do Luís em 03/10): o Redis aplica o ACL de quem chama também dentro da função, então não há como liberar só a escrita pela função (achado de 10/10). A escrita manual continua possível, e a auditoria a mostra:

```bash
docker exec redis redis-cli FCALL_RO tv30_auditoria 0
```

Vazia quer dizer tudo no esquema. Ela percorre os prefixos `session:`, `users:`, `user:`, `clients:`, `client:`, `origins:` e `bind-context:` e confere tipo, campos, membros e as invariantes `perfil-indexado`, `perfil-dependentes`, `clientes-disjuntos` e `autorizado-tem-registro`.

## Quem não usa o Redis

- **O broker MQTT.** O plugin C não abre conexão com o Redis; só valida esquema e responde ao teste de latência ([03](03-pipeline-mqtt.md)). O `migrate_to_redis.py` fica na imagem do broker só para depuração manual (grava perfis pela função `tv30_perfil_grava`); a partida do broker não o executa.
- **O bcast.** Não lê nem grava o Redis.

---

## Até 02/10 (histórico)

> **Registro, não estado atual.** Até a consolidação, este documento descrevia chaves `acl:<user_id>` (SET de padrões de tópico MQTT), `user:<user_id>:consent` lido pelo plugin do broker com `SISMEMBER` para autorizar publicações em `aop/<serviceId>/...`, `user:<user_id>:profile` (HASH do perfil) e o `migrate_to_redis.py` populando tudo isso no boot do Mosquitto a partir de `acl.json` e `userData.json`, além de um middleware Node que "consultaria" o Redis no futuro. Nada disso existe no código: as chaves `acl:*` e a leitura de ACL e consentimento pelo broker saíram no item 3, o middleware Node foi removido, o perfil fica em `user:{id}`, e a carga inicial é do container `redis`. O estado atual está nas seções acima.
