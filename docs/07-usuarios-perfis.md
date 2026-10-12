# Usuários e perfis — TV 3.0

> **Estado do código em 12/10/2026**, conferido em `aop/src/modules/profile-manager/service.js`, `aop/src/core.js`, `tv3ws/src/api/user/`, `infra/redis/esquema.json` (versão 3) e no texto da ABNT NBR 25608 (seção 8.7, Tabela 7, e C.6.14). Até 12/10 este documento trazia, abaixo de uma nota, o desenho anterior (perfis sincronizados pelo tv3ws a partir do `userData.json`, consentimento consultado pelo broker, `403` no tv3ws); esses trechos saíram, e o resumo está no fim, em [Até 02/10 (histórico)](#até-0210-histórico).
>
> Onde o texto cita a norma, a seção e a página são da ABNT NBR 25608. Redis, MQTT e o gestor de perfis no AoP são decisões deste testbed.

## Base normativa

A seção 8.7 (Profile Manager, p. 61-62; p. 79-80 do PDF) organiza os atributos do perfil do telespectador em três níveis (Figura 5):

| Nível | O que a norma diz | No testbed |
|---|---|---|
| **Atributos básicos** | Padronizados (Tabela 7); acessíveis pelo fabricante e pelo serviço de TV digital habilitado pelo espectador | `user:{id}` no Redis |
| **Atributos do dispositivo** | Definidos pelo fabricante (ou pelo desenvolvedor do AoP), que gerencia o acesso a eles | não há |
| **Atributos de emissora** | Definidos por cada serviço de TV digital, guardados no contexto daquele serviço e acessíveis só de dentro dele | `user:{id}:broadcaster-attrs:{scid}`, gravados pela C.6.14.5 |

### Tabela 7 (p. 63-64; p. 81-82 do PDF) e o que o testbed grava

| Atributo | Norma: tipo e uso | Testbed: regra do `esquema.json` e padrão do AoP na criação |
|---|---|---|
| `id` | UUID string, required; atribuído pelo AoP, não editável | UUID estrito (F-3, 11/10; versão 1 a 8 e variante RFC 9562 desde 12/10); o AoP usa `crypto.randomUUID()` |
| `nickname` | string até 20 caracteres, required (se o espectador não define, o AoP dá um valor único, por exemplo "Viewer 1") | texto de 1 a 20 caracteres, obrigatório; o formulário do AoP exige o campo |
| `parentalControl` | `true` ou `false`, required (padrão `false`) | `"true"` ou `"false"`, obrigatório |
| `maxContentRating` | valores predefinidos do país ou território; required só se `parentalControl = true` | `L`, `10`, `12`, `14`, `16` ou `18` (classificação indicativa do Brasil, escolha do testbed), obrigatório se `parentalControl` for `"true"` |
| `avatar` | file path, optional | qualquer texto; o AoP e a carga gravam o SVG em linha (divergência registrada no `esquema.json`) |
| `audioLanguage` | string IETF RFC 5646, optional | formato RFC 5646; o AoP grava `pt-BR` se o campo vier vazio |
| `closedCaptioningLanguage` | string IETF RFC 5646, optional; aplicável só se `closedCaptioning = true` | formato RFC 5646; o AoP só grava com `closedCaptioning` ligado (padrão `pt-BR`) |
| `userInterfaceLanguage` | string IETF RFC 5646, optional | formato RFC 5646; o AoP grava `pt-BR` se o campo vier vazio |
| `closedCaptioning` | `true` ou `false`, required (padrão `false`) | `"true"` ou `"false"`, obrigatório |
| `closedSigning` | `true` ou `false`, required (padrão `false`) | `"true"` ou `"false"`, obrigatório |
| `closedSigningSide` | `left` ou `right`, required (padrão `right`); aplicável só se `closedSigning = true` | obrigatório se `closedSigning` for `"true"`; o AoP grava `right` se o campo vier vazio |
| `closedSigningWidth` | inteiro de 14 a 28, required (padrão 28); aplicável só se `closedSigning = true` | inteiro de 14 a 28, obrigatório se `closedSigning` for `"true"`; o AoP grava 28 se o campo vier vazio |
| `audioDescription`, `dialogEnhancement`, `voiceGuidance` | `true` ou `false`, required (padrão `false`) | `"true"` ou `"false"`, obrigatórios |

Fora da Tabela 7, o testbed grava `lastAccess` (instante em ms), usado pelo despejo do perfil de último acesso mais antigo (P3: no máximo cinco perfis, `MAX_PROFILES` no gestor de perfis do AoP).

## Quem cuida dos perfis

A dona de `users:index`, `user:{id}` e `user:{id}:consent` é a plataforma, o AoP (P5, aplicado na reunião de 05/10 com o Joel, D-0510-5). O tv3ws só lê essas chaves. Toda escrita passa pelas funções do Redis (P4, decisão do Luís em 10/10), que conferem o esquema da Tabela 7:

| Operação | Quem | Função do Redis |
|---|---|---|
| Criar perfil (formulário do AoP; com cinco perfis, despeja antes o de último acesso mais antigo) | AoP, `createProfile` | `tv30_perfil_grava` |
| Carga inicial (perfil "Viewer 1" de `infra/user-files-template/userData.json`, só em banco vazio) | container `redis`, `emit_seed.py` | `tv30_perfil_grava` |
| Gravar `lastAccess` na troca de usuário corrente (só se o id está em `users:index`) | AoP, `touchLastAccess` | `tv30_perfil_ultimo_acesso` |
| Despejar perfil (tira o perfil, o `:consent` e todos os `:broadcaster-attrs:*`) | AoP, `evictLeastRecentlyUsed` | `tv30_perfil_remove` |
| Gravar atributos de emissora (C.6.14.5; valor vazio remove o atributo; teto de 20 MB por perfil e contexto) | tv3ws, `writeBroadcasterAttrs` | `tv30_atributos_emissora` |
| Gravar o usuário corrente em `session:current-user` | tv3ws (C.6.14.4 e espelho de `aop/currentUser`) | `tv30_sessao_usuario` |

`user:{id}:broadcaster-attrs:{scid}` tem dois escritores (o tv3ws grava, o AoP apaga no despejo): exceção ao P5 ainda sem decisão (E4).

O `userData.json` é só a fonte da carga inicial. Ninguém o lê nem grava em tempo de execução para manter perfis: o AoP não toca nele ao criar perfil, e o tv3ws não o sincroniza mais.

### `user:{id}:consent` não é o consentimento da norma

O conjunto `user:{id}:consent` guarda os serviços para os quais o perfil é visível. É como o testbed aplica a restrição das APIs C.6.14 ("A user's identifier is only reported if the user has granted access to the broadcaster corresponding to the current DTV service", Tabela C.68, p. 328; p. 346 do PDF). Não é o Privacy Manager da seção 8.8 (registros de privacidade, PRRD), que não está implementado. Na criação pelo AoP, o conjunto recebe os serviços que vierem no formulário e o serviço corrente (`session:current-service-id`), se houver. O perfil da carga tem os quatro serviços do bcast (`urn:tv30:service:webmedia`, `uff`, `eduplay` e `users-test`).

O broker MQTT não consulta esse conjunto nem nenhuma outra chave do Redis ([03](03-pipeline-mqtt.md)).

## APIs da C.6.14 no testbed

Todas passam pela borda, que exige access token e bind-token (`token+bind`) do cliente autônomo e do não local; o associado dispensa os dois (C.4.1.1). O tv3ws não confere credencial (D-0510-1) e responde os erros no formato C.3.2 (status 404 + `{error, description}`).

| Rota | Seção | O que o tv3ws faz |
|---|---|---|
| `POST /tv3/current-service/users` | C.6.14.1 | Lista os ids visíveis no serviço corrente e filtra pela expressão do corpo; 300 sem serviço corrente; 101 com expressão malformada |
| `GET /tv3/current-service/users/{userid}` e `GET /tv3/{serviceContextId}/users/{userid}` | C.6.14.2 | Com atributo na query, devolve o valor em texto puro (atributo básico ou, se não houver, de emissora); sem atributo, o JSON com os básicos e os de emissora daquele contexto; 305 se o perfil ou o atributo não existe, 405 se o perfil não é visível no serviço |
| `GET /tv3/current-service/users/current-user` | C.6.14.3 | Devolve `{"id": <session:current-user>}` |
| `POST /tv3/current-service/users/current-user` | C.6.14.4 | Grava `session:current-user` e publica `aop/currentUser` retido; 105 sem `id` no corpo |
| `POST /tv3/{serviceContextId}/users/{userid}` | C.6.14.5 | Grava atributos de emissora; 101 sem atributos ou com `id`, `nickname`, `parentalControl` ou `maxContentRating` (básicos que não se redefinem no contexto da emissora) |
| `GET /tv3/current-service/users/files?path=...` | C.6.14.6 | Devolve o arquivo de `USER_THUMBS` se ele é o `avatar` de algum perfil visível no serviço corrente; 305 se não |

Na borda, `{serviceContextId}` só vale como `current-service` ou como a constante do tv3ws (L2, `PENDENTE (Joel)`): outro valor dá 108 na etapa do bind-token, que o associado não percorre (ele chega ao tv3ws com qualquer valor, e o tv3ws usa o valor recebido como contexto). `POST /tv3/{serviceContextId}/users`, fora da norma, saiu da tabela de rotas em 11/10 (A4) e dá 100 na borda.

### Filtro da C.6.14.1

O corpo segue a estrutura da Tabela C.68: expressão simples `{"attribute", "comparator", "value"}`, com `eq`, `neq`, `lt`, `lte`, `gt` e `gte`, ou combinações `{"and": [...]}` e `{"or": [...]}`, aninháveis. Corpo ausente ou `{}` devolve todos os visíveis. Os valores são comparados como número quando os dois lados são numéricos e como texto nos demais casos (`tv3ws/src/api/user/service.ts`, `compareValues`).

```mermaid
flowchart TD
    REQ["POST /tv3/current-service/users<br/>{ expressão }"]
    SVC{"serviço corrente?<br/>(session:current-service-id)"}
    E300["300"]
    IDX["SMEMBERS users:index"]
    VIS["para cada id: SMEMBERS user:{id}:consent<br/>→ contém o serviço corrente?"]
    EXP["para cada visível: HGETALL user:{id}<br/>→ casa a expressão?"]
    RESP["200 { users: [{ id }] }"]

    REQ --> SVC
    SVC -->|"não"| E300
    SVC -->|"sim"| IDX --> VIS --> EXP --> RESP
```

### Troca do usuário corrente (C.6.14.4)

```mermaid
sequenceDiagram
    participant C as Cliente
    participant E as edgegateway
    participant T as tv3ws
    participant R as Redis
    participant M as Broker MQTT
    participant A as AoP

    C->>E: POST /tv3/current-service/users/current-user<br/>{ "id": "<uuid>" }
    E->>T: repassa (token+bind conferidos, ou warn)
    T->>R: FCALL tv30_sessao_usuario <uuid>
    T->>M: PUBLISH aop/currentUser <uuid> (retido)
    T-->>C: 200
    M->>A: aop/currentUser
    A->>R: FCALL tv30_perfil_ultimo_acesso <uuid> <ms><br/>(só se o id está em users:index)
```

O tv3ws não confere se o id existe em `users:index` antes de trocar; a função só confere o formato (texto de até 64 caracteres, o mesmo teto do tópico no broker). O tv3ws e o AoP assinam `aop/currentUser` com `noLocal` (`nl` no tv3ws), então nenhum dos dois recebe a própria publicação. O AoP também troca o usuário corrente pela própria tela (seletor de perfil): grava o `lastAccess` antes e publica `aop/currentUser` retido; o tv3ws recebe e grava `session:current-user` pela mesma função.

---

## Até 02/10 (histórico)

> **Registro, não estado atual.** Até a consolidação, este documento descrevia: a criação de perfil gravando o `userData.json` e publicando `aop/users` para o tv3ws sincronizar o arquivo no Redis (`syncUsersFromFile`); o plugin do broker consultando `user:{id}:consent` para autorizar publicações; o tv3ws validando o token JWT e respondendo `403`; e um resumo de comandos com "Sync do userData.json". Nada disso existe no código: a sincronização saiu do tv3ws na reunião de 05/10 (D-0510-5), o tópico `aop/users` não tem publicador nem assinante desde 10/10, o broker só valida esquema, e a credencial é conferida na borda. A seção de privacidade citava a "seção 8.7.2" da norma e o LGPD; a norma trata de privacidade na seção 8.8, que o testbed não implementa. O estado atual está nas seções acima.
