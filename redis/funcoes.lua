-- Biblioteca de funcoes do Redis "tv30" (P4, decisao do Luis em 10/10:
-- "Funcoes + auditoria"). Toda escrita que o codigo do projeto faz nas
-- familias do alcance (session:*, users:index, user:*, client:*, clients:*,
-- origins:associated, bind-context:*) passa por uma funcao daqui, que confere
-- o esquema e grava de forma atomica. Esquema invalido -> erro do Redis com o
-- prefixo TV30ESQUEMA, a funcao, a chave, o campo e o motivo, e NADA gravado.
--
-- Este arquivo e so o CORPO. O infra/redis/biblioteca.sh monta a biblioteca
-- que o Redis carrega:
--   #!lua name=tv30
--   local ESQUEMA_JSON = [==[ <conteudo de infra/redis/esquema.json> ]==]
--   <este arquivo>
-- O esquema (familias, regras, invariantes) mora no esquema.json; aqui ficam o
-- interpretador das regras, as operacoes e a auditoria.
--
-- Regras de construcao (por que o codigo e assim):
-- - cjson so existe DENTRO das chamadas (na carga, o Redis recusa: "Script
--   attempted to access nonexistent global variable 'cjson'"), por isso o
--   esquema e decodificado na primeira chamada e guardado em E.
-- - O Redis NAO desfaz o que uma funcao ja gravou quando ela da erro no meio
--   (conferido no redis 7.4.11: SET seguido de SADD com WRONGTYPE deixa o SET
--   gravado). Por isso toda funcao primeiro le, monta o estado resultante e
--   confere tudo (inclusive o tipo de cada chave no Redis) e so depois grava.
-- - As funcoes sao chamadas com numkeys 0 e derivam os nomes das chaves dos
--   argumentos (flag no-cluster: o testbed usa um Redis so, sem cluster).
-- - O Redis aplica o ACL de quem chama DENTRO da funcao (achado de 10/10: o
--   default sem @write recebe "ERR ACL failure in script"); as funcoes nao
--   bloqueiam escrita direta. Escrita manual pelo redis-cli ou pelo commander
--   continua possivel, e a auditoria (tv30_auditoria) a mostra.

local E -- esquema decodificado e preparado (preguicoso, ver acima)

-- Erro de esquema: levantado como tabela e convertido em resposta de erro
-- pelo registra() (qualquer outro erro sobe como o Redis o daria).
local function falha(msg)
  error({ tv30 = msg }, 0)
end

-- ------------------------------------------------------------- utilidades --

local function ordenadas(t)
  local out = {}
  for k in pairs(t) do out[#out + 1] = k end
  table.sort(out, function(a, b) return tostring(a) < tostring(b) end)
  return out
end

-- caracteres (pontos de codigo UTF-8), nao bytes: a Tabela 7 fala em 20
-- caracteres no nickname
local function nchars(s)
  local _, n = string.gsub(s, '[^\128-\191]', '')
  return n
end

-- amostra do valor para a mensagem: curta e sem caractere de controle (a
-- resposta de erro do Redis e uma linha so)
local function amostra(v)
  v = tostring(v)
  if #v > 48 then v = string.sub(v, 1, 45) .. '...' end
  return '"' .. string.gsub(v, '%c', '?') .. '"'
end

local function escapaPadrao(s)
  return (string.gsub(s, '[%^%$%(%)%%%.%[%]%*%+%-%?]', '%%%0'))
end

local function escapaGlob(s)
  return (string.gsub(s, '[%*%?%[%]\\]', '\\%0'))
end

local function tipoNoRedis(chave)
  return redis.call('TYPE', chave).ok
end

local function hashDe(chave)
  local flat = redis.call('HGETALL', chave)
  local h = {}
  for i = 1, #flat, 2 do h[flat[i]] = flat[i + 1] end
  return h
end

local function conjuntoDe(lista)
  local s = {}
  for _, m in ipairs(lista) do s[m] = true end
  return s
end

-- comando com muitos argumentos em lotes (unpack tem limite de pilha)
local function emLotes(cmd, chave, lista)
  local passo = 400
  for i = 1, #lista, passo do
    local lote = { cmd, chave }
    for j = i, math.min(i + passo - 1, #lista) do lote[#lote + 1] = lista[j] end
    redis.call(unpack(lote))
  end
end

-- ------------------------------------------------------ preparo do esquema --

-- "{id}" no meio da chave casa [^:]+; a ultima parte casa o resto, a nao ser
-- que a regra dela nao admita ':' (proibe ':', ou o tipo uuid do idPerfil):
-- assim user:{id} nao engole user:{id}:consent.
local function compila(tpl, partes)
  local pat, nomes, pos = '^', {}, 1
  while true do
    local a, b, nome = string.find(tpl, '{(%w+)}', pos)
    if not a then
      pat = pat .. escapaPadrao(string.sub(tpl, pos))
      break
    end
    pat = pat .. escapaPadrao(string.sub(tpl, pos, a - 1))
    local regra = partes and partes[nome]
    local semDoisPontos = regra and (regra.tipo == 'uuid' or (regra.proibe and string.find(regra.proibe, ':', 1, true)))
    if b == #tpl and not semDoisPontos then pat = pat .. '(.+)' else pat = pat .. '([^:]+)' end
    nomes[#nomes + 1] = nome
    pos = b + 1
  end
  return pat .. '$', nomes
end

-- invariantes que este arquivo implementa (auditoria); o esquema.json tem de
-- declarar exatamente estas
local INVARIANTES = {
  ['perfil-indexado'] = true,
  ['perfil-dependentes'] = true,
  ['clientes-disjuntos'] = true,
  ['autorizado-tem-registro'] = true,
}

local function prepara()
  if E then return E end
  local e = cjson.decode(ESQUEMA_JSON)
  local defs = e.definicoes or {}
  local function resolve(r)
    if type(r) ~= 'table' or r.ref == nil then return r end
    local base = defs[r.ref]
    if base == nil then error('esquema.json: definicao desconhecida "' .. tostring(r.ref) .. '"') end
    local out = {}
    for k, v in pairs(base) do out[k] = v end
    for k, v in pairs(r) do if k ~= 'ref' then out[k] = v end end
    return out
  end
  e.porNome = {}
  for _, f in ipairs(e.familias) do
    if f.valor then f.valor = resolve(f.valor) end
    if f.membros then f.membros = resolve(f.membros) end
    if f.partes then for k, r in pairs(f.partes) do f.partes[k] = resolve(r) end end
    if f.campos then for k, r in pairs(f.campos) do f.campos[k] = resolve(r) end end
    if type(f.outrosCampos) == 'table' then
      f.outrosCampos.nome = resolve(f.outrosCampos.nome)
      f.outrosCampos.valor = resolve(f.outrosCampos.valor)
    end
    if f.itens then
      f.itens = resolve(f.itens)
      if f.itens.campos then for k, r in pairs(f.itens.campos) do f.itens.campos[k] = resolve(r) end end
    end
    if f.camposProibidos then f.proibidos = conjuntoDe(f.camposProibidos.nomes) end
    f.padrao, f.nomesPartes = compila(f.chave, f.partes)
    e.porNome[f.familia] = f
  end
  local declaradas = {}
  for _, inv in ipairs(e.invariantes or {}) do
    if not INVARIANTES[inv.id] then error('esquema.json declara a invariante "' .. tostring(inv.id) .. '", sem implementacao em funcoes.lua') end
    declaradas[inv.id] = true
  end
  for id in pairs(INVARIANTES) do
    if not declaradas[id] then error('funcoes.lua implementa a invariante "' .. id .. '", ausente do esquema.json') end
  end
  E = e
  return E
end

local function familia(nome)
  local f = prepara().porNome[nome]
  if f == nil then error('esquema.json sem a familia "' .. nome .. '"') end
  return f
end

local function chaveDe(fam, partes)
  return (string.gsub(fam.chave, '{(%w+)}', function(n) return partes[n] end))
end

local function classifica(chave)
  for _, f in ipairs(prepara().familias) do
    local caps = { string.match(chave, f.padrao) }
    if caps[1] ~= nil then
      local partes = {}
      for i, n in ipairs(f.nomesPartes) do partes[n] = caps[i] end
      return f, partes
    end
  end
  return nil
end

-- ---------------------------------------------------------------- regras --

local function idiomaOk(v)
  local i = 0
  for sub in string.gmatch(v .. '-', '([^-]*)-') do
    i = i + 1
    if #sub < 1 or #sub > 8 or string.find(sub, '[^%w]') then return false end
    if i == 1 and not (string.find(sub, '^%a%a+$') or sub == 'x' or sub == 'i') then return false end
  end
  return i > 0
end

-- F-3 (decisao do Luis em 11/10): id do perfil em UUID, no formato textual da
-- RFC 9562 (8-4-4-4-12 digitos hexadecimais), em minusculas. Literal (e nao
-- string.rep): na carga da biblioteca o Redis nao expoe as bibliotecas.
-- UUID estrito (decisao do Luis em 12/10, versao 3 do esquema): alem do
-- formato, a versao (1o digito do 3o grupo) tem de ser de 1 a 8 e a variante
-- (1o digito do 4o grupo) a da RFC 9562 (8, 9, a ou b). O UUID nulo (todo 0)
-- e o maximo (todo f) ficam de fora: nao servem de id de perfil. Ate 12/10
-- (versao 2) versao e variante nao eram conferidas.
local UUID_FORMATO = '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-%x%x%x%x%-%x%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$'
local UUID_NULO = '00000000-0000-0000-0000-000000000000'
local UUID_MAXIMO = 'ffffffff-ffff-ffff-ffff-ffffffffffff'

local function motivoUuid(v)
  if not string.find(v, UUID_FORMATO) then
    return 'deve ser UUID no formato da RFC 9562 (8-4-4-4-12 digitos hexadecimais, como ' ..
      'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10; veio ' .. amostra(v) .. ')'
  end
  if string.find(v, '%u') then return 'UUID deve estar em minusculas (veio ' .. amostra(v) .. ')' end
  -- nulo e maximo antes da versao: a mensagem diz o que o valor e
  if v == UUID_NULO then
    return 'UUID nulo (Nil UUID da RFC 9562, todo 0) nao serve de id de perfil (versao 0 e variante 0)'
  end
  if v == UUID_MAXIMO then
    return 'UUID maximo (Max UUID da RFC 9562, todo f) nao serve de id de perfil (versao f e variante f)'
  end
  local versao = string.sub(v, 15, 15)
  if not string.find(versao, '^[1-8]$') then
    return 'versao do UUID deve ser de 1 a 8 (1o digito do 3o grupo; veio ' .. versao .. ' em ' .. amostra(v) .. ')'
  end
  local variante = string.sub(v, 20, 20)
  if not string.find(variante, '^[89ab]$') then
    return 'variante do UUID deve ser a da RFC 9562 (1o digito do 4o grupo em 8, 9, a ou b; veio ' ..
      variante .. ' em ' .. amostra(v) .. ')'
  end
  return nil
end

-- G8 (decisao do Luis em 12/10, versao 4 do esquema): avatar = NOME de um
-- arquivo dentro do diretorio de miniaturas (<USER_DATA_PATH>/thumbs no AoP =
-- USER_THUMBS no tv3ws), no formato do contrato
--   ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$
-- O padrao Lua nao tem alternativa nem repeticao contada, por isso a regra e
-- conferida por partes, na ordem em que a mensagem ajuda mais: SVG em linha
-- (o formato de ate 12/10, que um volume antigo ainda tem), diretorio, a
-- extensao (o que vem depois do ULTIMO ponto: nenhuma extensao da lista tem
-- ponto, entao o corte e o mesmo da expressao), o primeiro caractere, os
-- demais e o tamanho do nome antes da extensao (regra.maxNome, 128 = 1 +
-- {0,127}). Caracteres so ASCII: depois da conferencia deles, bytes =
-- caracteres. Ate 12/10 (versao 3) o avatar era texto livre.
local EXEMPLO_AVATAR = 'como c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10.svg'

local function motivoNomeArquivo(regra, v)
  if v == '' then
    return 'vazio: o avatar e o nome de um arquivo do diretorio de miniaturas (' .. EXEMPLO_AVATAR ..
      '); sem avatar, o campo fica ausente'
  end
  if string.sub(v, 1, 1) == '<' then
    return 'SVG em linha nao serve: o avatar e o nome de um arquivo do diretorio de miniaturas (Tabela 7: file path; G8), ' ..
      EXEMPLO_AVATAR .. ' (veio ' .. amostra(v) .. ')'
  end
  if string.find(v, '[/\\]') then
    return 'so o nome do arquivo, sem diretorio: nao pode conter "/" nem "\\" (veio ' .. amostra(v) .. ')'
  end
  local exts = regra.extensoes or {}
  local base, ext = string.match(v, '^(.*)%.([^.]*)$')
  local extOk = false
  if ext then
    for _, e in ipairs(exts) do if ext == e then extOk = true end end
  end
  if not extOk then
    return 'extensao deve ser uma de .' .. table.concat(exts, ', .') .. ', em minusculas (veio ' .. amostra(v) .. ')'
  end
  if not string.find(base, '^[A-Za-z0-9]') then
    return 'nome do arquivo deve comecar por letra ou digito (sem ponto no comeco; veio ' .. amostra(v) .. ')'
  end
  if string.find(base, '[^A-Za-z0-9._%-]') then
    return 'nome do arquivo so com letras sem acento, digitos, ".", "_" e "-" (veio ' .. amostra(v) .. ')'
  end
  if regra.maxNome and #base > regra.maxNome then
    return 'nome do arquivo tem ' .. #base .. ' caracteres antes da extensao, maximo ' .. regra.maxNome ..
      ' (veio ' .. amostra(v) .. ')'
  end
  return nil
end

local function motivoOrigem(v)
  local esq, resto = string.match(v, '^(https?)://(.+)$')
  if not esq then return 'deve comecar por http:// ou https://' end
  local host, porta = string.match(resto, '^(%[[%x:%.]+%])(.*)$')
  if not host then host, porta = string.match(resto, '^([%w%.%-]+)(.*)$') end
  if not host then return 'host invalido' end
  if string.find(host, '%u') then return 'host deve estar em minusculas (como o navegador o manda no Origin)' end
  if porta ~= '' then
    local p = string.match(porta, '^:(%d+)$')
    if not p then return 'depois do host so cabe :porta (sem caminho nem barra final)' end
    if #p > 1 and string.sub(p, 1, 1) == '0' then return 'porta com zero a esquerda' end
    local n = tonumber(p)
    if n < 1 or n > 65535 then return 'porta fora de 1 a 65535' end
    if (esq == 'http' and n == 80) or (esq == 'https' and n == 443) then
      return 'porta padrao do esquema nao aparece na origem que o navegador manda'
    end
  end
  return nil
end

-- motivo(regra, valor[, emJson]) -> nil se o valor cumpre a regra, ou o
-- motivo. emJson: valor vindo de um objeto JSON (inteiro e numero, o resto e
-- texto JSON).
local motivo
motivo = function(regra, v, emJson)
  local t = regra.tipo
  if emJson then
    if t == 'inteiro' then
      if type(v) ~= 'number' or v ~= math.floor(v) then return 'deve ser numero inteiro no JSON' end
      if regra.min and v < regra.min then return 'menor que o minimo ' .. regra.min end
      if regra.max and v > regra.max then return 'maior que o maximo ' .. regra.max end
      return nil
    end
    if type(v) ~= 'string' then return 'deve ser texto no JSON' end
  end
  if t == 'texto' then
    local n = nchars(v)
    if regra.min and n < regra.min then
      if n == 0 then return 'vazio (minimo ' .. regra.min .. ' caractere(s))' end
      return 'tem ' .. n .. ' caractere(s), minimo ' .. regra.min
    end
    if regra.max and n > regra.max then return 'tem ' .. n .. ' caracteres, maximo ' .. regra.max end
    if regra.proibe then
      for i = 1, #regra.proibe do
        local c = string.sub(regra.proibe, i, i)
        if string.find(v, c, 1, true) then return 'nao pode conter "' .. c .. '" (veio ' .. amostra(v) .. ')' end
      end
    end
  elseif t == 'booleano' then
    if v ~= 'true' and v ~= 'false' then return 'deve ser "true" ou "false" (veio ' .. amostra(v) .. ')' end
  elseif t == 'inteiro' then
    if v == '' then
      if regra.vazio then return nil end
      return 'deve ser inteiro (veio vazio)'
    end
    if not (v == '0' or string.match(v, '^%-?[1-9]%d*$')) then
      return 'deve ser inteiro em decimal, sem zero a esquerda (veio ' .. amostra(v) .. ')'
    end
    local n = tonumber(v)
    if regra.min and n < regra.min then return 'menor que o minimo ' .. regra.min .. ' (veio ' .. v .. ')' end
    if regra.max and n > regra.max then return 'maior que o maximo ' .. regra.max .. ' (veio ' .. v .. ')' end
  elseif t == 'enum' then
    for _, x in ipairs(regra.valores) do if v == x then return nil end end
    return 'deve ser um de ' .. table.concat(regra.valores, ', ') .. ' (veio ' .. amostra(v) .. ')'
  elseif t == 'uuid' then
    return motivoUuid(v)
  elseif t == 'nomeArquivo' then
    return motivoNomeArquivo(regra, v)
  elseif t == 'idioma' then
    if not idiomaOk(v) then return 'deve ser etiqueta de idioma IETF RFC 5646, como pt-BR (veio ' .. amostra(v) .. ')' end
  elseif t == 'origem' then
    local m = motivoOrigem(v)
    if m then return m .. ' (veio ' .. amostra(v) .. ')' end
  elseif t == 'json' then
    local ok, obj = pcall(cjson.decode, v)
    if not ok or type(obj) ~= 'table' or obj[1] ~= nil then return 'deve ser um objeto JSON (veio ' .. amostra(v) .. ')' end
    for _, nome in ipairs(ordenadas(regra.campos or {})) do
      local r = regra.campos[nome]
      local x = obj[nome]
      if x == nil or x == cjson.null then
        if r.obrigatorio then return 'membro "' .. nome .. '" obrigatorio, ausente' end
      else
        local m = motivo(r, x, true)
        if m then return 'membro "' .. nome .. '": ' .. m end
      end
    end
    if regra.outrosCampos == false then
      for _, nome in ipairs(ordenadas(obj)) do
        if not (regra.campos and regra.campos[nome]) then return 'membro "' .. tostring(nome) .. '" nao previsto no esquema' end
      end
    end
  else
    return 'regra de tipo desconhecido no esquema.json: ' .. tostring(t)
  end
  return nil
end

local function motivosPartes(fam, partes)
  local out = {}
  for _, nome in ipairs(fam.nomesPartes) do
    local r = fam.partes and fam.partes[nome]
    if r then
      local m = motivo(r, partes[nome] or '')
      if m then out[#out + 1] = '{' .. nome .. '} da chave: ' .. m end
    end
  end
  return out
end

-- Motivos de um hash. h = estado resultante (campo -> valor). escritos = nil
-- confere o registro inteiro (novo ou regravado: obrigatorios, campo igual a
-- parte da chave, teto); escritos = conjunto de campos confere so esses (campo
-- acrescentado a registro que ja existe: o que ja estava gravado fora do
-- esquema nao trava a escrita - a auditoria o mostra).
local function motivosHash(fam, partes, h, escritos)
  local out = {}
  local function add(m) out[#out + 1] = m end
  local campos = fam.campos or {}
  if escritos == nil then
    for _, nome in ipairs(ordenadas(campos)) do
      local r = campos[nome]
      if h[nome] == nil then
        if r.obrigatorio then
          add('campo ' .. nome .. ': obrigatorio, ausente')
        elseif r.obrigatorioSe and h[r.obrigatorioSe.campo] == r.obrigatorioSe.valor then
          add('campo ' .. nome .. ': obrigatorio quando ' .. r.obrigatorioSe.campo .. '=' .. r.obrigatorioSe.valor .. ', ausente')
        end
      end
    end
  end
  for _, nome in ipairs(ordenadas(escritos or h)) do
    local v = h[nome]
    if v ~= nil then
      local r = campos[nome]
      if r then
        local m = motivo(r, v)
        if m then add('campo ' .. nome .. ': ' .. m) end
      elseif type(fam.outrosCampos) == 'table' then
        local m = motivo(fam.outrosCampos.nome, nome)
        if m then add('nome de campo: ' .. m) end
        if fam.proibidos and fam.proibidos[nome] then
          add('campo ' .. nome .. ': atributo basico que nao pode ser redefinido aqui (' .. fam.camposProibidos.fonte .. ')')
        end
        m = motivo(fam.outrosCampos.valor, v)
        if m then add('campo ' .. amostra(nome) .. ': ' .. m) end
      else
        add('campo ' .. amostra(nome) .. ': nao previsto no esquema')
      end
    end
  end
  local c = fam.campoIgualAParte
  if c and (escritos == nil or escritos[c.campo]) and h[c.campo] ~= nil and h[c.campo] ~= partes[c.parte] then
    add('campo ' .. c.campo .. ': deve ser igual ao {' .. c.parte .. '} da chave (veio ' .. amostra(h[c.campo]) .. ')')
  end
  if fam.maxBytes then
    local n = 0
    for k, v in pairs(h) do n = n + #k + #v end
    if n > fam.maxBytes then add('nomes e valores somam ' .. n .. ' bytes, acima do teto de ' .. fam.maxBytes .. ' (' .. fam.maxBytesFonte .. ')') end
  end
  return out
end

-- ------------------------------------------------- apoio das operacoes --

local function exige(chave, motivos)
  if #motivos > 0 then
    local extra = ''
    if #motivos > 1 then extra = ' (e mais ' .. (#motivos - 1) .. ' problema(s) no mesmo pedido)' end
    falha(chave .. ' ' .. motivos[1] .. extra)
  end
end

-- tipo da chave no Redis: ausente ou o do esquema; devolve true se existe
local function exigeTipo(fam, chave)
  local t = tipoNoRedis(chave)
  if t ~= 'none' and t ~= fam.tipo then
    falha(chave .. ' no Redis e ' .. t .. ', o esquema pede ' .. fam.tipo .. ' (nada gravado)')
  end
  return t ~= 'none'
end

-- partes da chave conferidas; devolve a chave
local function chaveValida(fam, partes)
  local chave = chaveDe(fam, partes)
  exige(chave, motivosPartes(fam, partes))
  return chave
end

local function exigeArgs(args, n, uso)
  if #args ~= n then falha('uso: ' .. uso .. ' (recebeu ' .. #args .. ' argumento(s))') end
end

local function exigeNaoVazio(v, nome)
  if v == nil or v == '' then falha(nome .. ' vazio') end
end

-- pares campo valor de args[inicio..]; devolve o hash e a lista plana
local function pares(args, inicio, uso, aceitaNenhum)
  local n = #args - inicio + 1
  if n % 2 ~= 0 or (n == 0 and not aceitaNenhum) then
    falha('uso: ' .. uso .. ' (pares campo valor incompletos)')
  end
  local h = {}
  for i = inicio, #args, 2 do h[args[i]] = args[i + 1] end
  return h
end

local function plano(h)
  local out = {}
  for _, k in ipairs(ordenadas(h)) do
    out[#out + 1] = k
    out[#out + 1] = h[k]
  end
  return out
end

-- grava (HSET) campos num hash que pode existir: registro novo confere tudo,
-- existente so os campos escritos
local function mergeHash(fam, partes, chave, novos)
  local existe = exigeTipo(fam, chave)
  local h = existe and hashDe(chave) or {}
  for k, v in pairs(novos) do h[k] = v end
  local escritos = nil
  if existe then escritos = conjuntoDe(ordenadas(novos)) end
  exige(chave, motivosHash(fam, partes, h, escritos))
end

-- ------------------------------------------------------------- operacoes --

local OP = {}

-- sessao (dono: tv3ws) -----------------------------------------------------

local function sessaoString(nomeFamilia, args, uso)
  exigeArgs(args, 1, uso)
  local fam = familia(nomeFamilia)
  local m = motivo(fam.valor, args[1])
  if m then falha(fam.chave .. ' valor: ' .. m) end
  exigeTipo(fam, fam.chave)
  redis.call('SET', fam.chave, args[1])
  return 1
end

OP.tv30_sessao_usuario = function(args)
  return sessaoString('session:current-user', args, 'tv30_sessao_usuario <id do perfil ou vazio>')
end

OP.tv30_sessao_servico_id = function(args)
  return sessaoString('session:current-service-id', args, 'tv30_sessao_servico_id <id do servico ou vazio>')
end

OP.tv30_sessao_servico = function(args)
  local fam = familia('session:current-service')
  local h = pares(args, 1, 'tv30_sessao_servico <campo> <valor> [...]')
  exige(fam.chave, motivosHash(fam, {}, h, nil))
  exigeTipo(fam, fam.chave)
  redis.call('DEL', fam.chave)
  emLotes('HSET', fam.chave, plano(h))
  return 1
end

-- perfis (dono: AoP) --------------------------------------------------------

OP.tv30_perfil_grava = function(args)
  local uso = 'tv30_perfil_grava <id> <n> <servico 1..n> <campo> <valor> [...]'
  if #args < 2 then falha('uso: ' .. uso) end
  local id = args[1]
  local n = tonumber(args[2])
  if n == nil or n < 0 or n ~= math.floor(n) or 2 + n > #args then
    falha('uso: ' .. uso .. ' (n=' .. amostra(args[2]) .. ' invalido)')
  end
  local servicos = {}
  for i = 3, 2 + n do servicos[#servicos + 1] = args[i] end
  local h = pares(args, 3 + n, uso)

  local fIndice, fPerfil, fVis = familia('users:index'), familia('user:{id}'), familia('user:{id}:consent')
  local partes = { id = id }
  local chave = chaveValida(fPerfil, partes)
  local chaveVis = chaveDe(fVis, partes)
  exige(chave, motivosHash(fPerfil, partes, h, nil))
  for _, s in ipairs(servicos) do
    local m = motivo(fVis.membros, s)
    if m then falha(chaveVis .. ' membro ' .. amostra(s) .. ': ' .. m) end
  end
  exigeTipo(fIndice, fIndice.chave)
  exigeTipo(fPerfil, chave)
  exigeTipo(fVis, chaveVis)

  redis.call('SADD', fIndice.chave, id)
  redis.call('DEL', chave)
  emLotes('HSET', chave, plano(h))
  if #servicos > 0 then emLotes('SADD', chaveVis, servicos) end
  return 1
end

OP.tv30_perfil_ultimo_acesso = function(args)
  exigeArgs(args, 2, 'tv30_perfil_ultimo_acesso <id> <instante em ms>')
  local id, ms = args[1], args[2]
  local fIndice, fPerfil = familia('users:index'), familia('user:{id}')
  if tipoNoRedis(fIndice.chave) ~= 'set' or redis.call('SISMEMBER', fIndice.chave, id) == 0 then
    return 0 -- perfil inexistente ou despejado: nao cria hash avulso
  end
  local partes = { id = id }
  local chave = chaveDe(fPerfil, partes)
  if tipoNoRedis(chave) == 'none' then
    falha(chave .. ' nao existe, mas o id esta em users:index (invariante perfil-indexado quebrada; ver tv30_auditoria)')
  end
  mergeHash(fPerfil, partes, chave, { lastAccess = ms })
  redis.call('HSET', chave, 'lastAccess', ms)
  return 1
end

OP.tv30_perfil_remove = function(args)
  exigeArgs(args, 1, 'tv30_perfil_remove <id>')
  local id = args[1]
  exigeNaoVazio(id, 'id do perfil')
  local fIndice = familia('users:index')
  local partes = { id = id }
  local alvos = { chaveDe(familia('user:{id}'), partes), chaveDe(familia('user:{id}:consent'), partes) }
  local fAttr = familia('user:{id}:broadcaster-attrs:{scid}')
  local prefixo = string.gsub(fAttr.chave, '{scid}', '')
  prefixo = string.gsub(prefixo, '{id}', function() return id end)
  local cur = '0'
  repeat
    local r = redis.call('SCAN', cur, 'MATCH', escapaGlob(prefixo) .. '*', 'COUNT', 500)
    cur = r[1]
    for _, k in ipairs(r[2]) do alvos[#alvos + 1] = k end
  until cur == '0'
  exigeTipo(fIndice, fIndice.chave)

  local n = 0
  if tipoNoRedis(fIndice.chave) == 'set' then n = n + redis.call('SREM', fIndice.chave, id) end
  local vistos = {}
  for _, k in ipairs(alvos) do
    if not vistos[k] then
      vistos[k] = true
      n = n + redis.call('DEL', k)
    end
  end
  return n
end

-- atributos de emissora (E4, Joel 15/10: grava o tv3ws; remove o AoP, em tv30_perfil_remove) --

OP.tv30_atributos_emissora = function(args)
  local uso = 'tv30_atributos_emissora <id> <scid> <campo> <valor> [...] (valor vazio remove o campo)'
  if #args < 4 then falha('uso: ' .. uso) end
  local id, scid = args[1], args[2]
  local fIndice, fAttr = familia('users:index'), familia('user:{id}:broadcaster-attrs:{scid}')
  local partes = { id = id, scid = scid }
  local chave = chaveValida(fAttr, partes)
  local pedidos = pares(args, 3, uso)
  if tipoNoRedis(fIndice.chave) ~= 'set' or redis.call('SISMEMBER', fIndice.chave, id) == 0 then
    falha(chave .. ' perfil ' .. amostra(id) .. ' fora de users:index (invariante perfil-dependentes)')
  end
  local existe = exigeTipo(fAttr, chave)
  local h = existe and hashDe(chave) or {}
  local apagar, gravar, escritos = {}, {}, {}
  for _, k in ipairs(ordenadas(pedidos)) do
    local v = pedidos[k]
    if v == '' then
      apagar[#apagar + 1] = k
      h[k] = nil
    else
      gravar[k] = v
      h[k] = v
      escritos[k] = true
    end
    -- nome conferido mesmo na remocao (atributo basico nunca e da emissora)
    local m = motivo(fAttr.outrosCampos.nome, k)
    if m then falha(chave .. ' nome de campo: ' .. m) end
    if fAttr.proibidos[k] then
      falha(chave .. ' campo ' .. k .. ': atributo basico que nao pode ser redefinido aqui (' .. fAttr.camposProibidos.fonte .. ')')
    end
  end
  local motivos = motivosHash(fAttr, partes, h, escritos)
  exige(chave, motivos)
  -- o teto vale no resultado mesmo quando so se apaga (motivosHash com
  -- escritos so confere os gravados; o teto e conferido sempre)
  if #apagar > 0 then emLotes('HDEL', chave, apagar) end
  local flat = plano(gravar)
  if #flat > 0 then emLotes('HSET', chave, flat) end
  return #apagar + #flat / 2
end

-- clientes (dono: tv3ws) ----------------------------------------------------

local function idClienteValido(id)
  local fReg = familia('client:{id}')
  local partes = { id = id }
  return fReg, partes, chaveValida(fReg, partes)
end

OP.tv30_cliente_grava = function(args)
  local uso = 'tv30_cliente_grava <id> <campo> <valor> [...]'
  if #args < 3 then falha('uso: ' .. uso) end
  local fReg, partes, chave = idClienteValido(args[1])
  local novos = pares(args, 2, uso)
  mergeHash(fReg, partes, chave, novos)
  emLotes('HSET', chave, plano(novos))
  return 1
end

OP.tv30_cliente_autoriza = function(args)
  local uso = 'tv30_cliente_autoriza <id> <campo> <valor> [...]'
  if #args < 3 then falha('uso: ' .. uso) end
  local id = args[1]
  local fReg, partes, chave = idClienteValido(id)
  local novos = pares(args, 2, uso)
  local fAut, fBlq = familia('clients:authorized'), familia('clients:blocked')
  mergeHash(fReg, partes, chave, novos)
  exigeTipo(fAut, fAut.chave)
  exigeTipo(fBlq, fBlq.chave)
  emLotes('HSET', chave, plano(novos))
  redis.call('SREM', fBlq.chave, id)
  redis.call('SADD', fAut.chave, id)
  return 1
end

OP.tv30_cliente_bloqueia = function(args)
  exigeArgs(args, 1, 'tv30_cliente_bloqueia <id>')
  local id = args[1]
  idClienteValido(id)
  local fAut, fBlq = familia('clients:authorized'), familia('clients:blocked')
  exigeTipo(fAut, fAut.chave)
  exigeTipo(fBlq, fBlq.chave)
  redis.call('SREM', fAut.chave, id)
  redis.call('SADD', fBlq.chave, id)
  return 1
end

OP.tv30_cliente_inclui_autorizado = function(args)
  exigeArgs(args, 1, 'tv30_cliente_inclui_autorizado <id>')
  local id = args[1]
  local fReg, _, chave = idClienteValido(id)
  local fAut, fBlq = familia('clients:authorized'), familia('clients:blocked')
  exigeTipo(fAut, fAut.chave)
  exigeTipo(fBlq, fBlq.chave)
  if redis.call('SISMEMBER', fBlq.chave, id) == 1 then return 0 end
  local t = tipoNoRedis(chave)
  if t == 'none' then
    falha(chave .. ' ausente: so entra em clients:authorized quem tem o registro (invariante autorizado-tem-registro)')
  elseif t ~= fReg.tipo then
    falha(chave .. ' no Redis e ' .. t .. ', o esquema pede ' .. fReg.tipo .. ' (invariante autorizado-tem-registro; nada gravado)')
  end
  return redis.call('SADD', fAut.chave, id)
end

-- origens das aplicacoes associadas (dono: AoP) ---------------------------

OP.tv30_origem_registra = function(args)
  exigeArgs(args, 2, 'tv30_origem_registra <origem> <id do servico>')
  local fam = familia('origins:associated')
  local h = { [args[1]] = args[2] }
  mergeHash(fam, {}, fam.chave, h)
  redis.call('HSET', fam.chave, args[1], args[2])
  return 1
end

OP.tv30_origem_remove = function(args)
  exigeArgs(args, 1, 'tv30_origem_remove <origem>')
  exigeNaoVazio(args[1], 'origem')
  local fam = familia('origins:associated')
  if not exigeTipo(fam, fam.chave) then return 0 end
  return redis.call('HDEL', fam.chave, args[1])
end

OP.tv30_origens_limpa = function(args)
  exigeArgs(args, 0, 'tv30_origens_limpa')
  return redis.call('DEL', familia('origins:associated').chave)
end

-- bind-context (dono: borda) ------------------------------------------------

OP.tv30_bind_registra = function(args)
  exigeArgs(args, 2, 'tv30_bind_registra <id do servico> <entrada JSON>')
  local fam = familia('bind-context:{serviceId}')
  local partes = { serviceId = args[1] }
  local chave = chaveValida(fam, partes)
  local m = motivo(fam.itens, args[2])
  if m then falha(chave .. ' entrada: ' .. m) end
  exigeTipo(fam, chave)
  return redis.call('RPUSH', chave, args[2])
end

OP.tv30_bind_remove = function(args)
  exigeArgs(args, 2, 'tv30_bind_remove <id do servico> <entrada JSON exata>')
  exigeNaoVazio(args[1], 'id do servico')
  local fam = familia('bind-context:{serviceId}')
  local chave = chaveDe(fam, { serviceId = args[1] })
  if not exigeTipo(fam, chave) then return 0 end
  return redis.call('LREM', chave, 0, args[2])
end

-- --------------------------------------------------------------- auditoria --

local function membros(chave)
  if tipoNoRedis(chave) ~= 'set' then return {} end
  return conjuntoDe(redis.call('SMEMBERS', chave))
end

local function auditoria()
  local e = prepara()
  local out = {}
  local function add(chave, fam, m) out[#out + 1] = { chave, fam, m } end

  local vistos, chaves = {}, {}
  for _, p in ipairs(e.prefixosAuditados) do
    local cur = '0'
    repeat
      local r = redis.call('SCAN', cur, 'MATCH', escapaGlob(p) .. '*', 'COUNT', 500)
      cur = r[1]
      for _, k in ipairs(r[2]) do
        if not vistos[k] then
          vistos[k] = true
          chaves[#chaves + 1] = k
        end
      end
    until cur == '0'
  end
  table.sort(chaves)

  local perfis, dependentes = {}, {}
  for _, k in ipairs(chaves) do
    local fam, partes = classifica(k)
    if fam == nil then
      add(k, '-', 'chave sem familia no esquema (prefixo auditado; ver esquema.json)')
    else
      local t = tipoNoRedis(k)
      if t ~= fam.tipo then
        add(k, fam.familia, 'no Redis e ' .. t .. ', o esquema pede ' .. fam.tipo)
      else
        for _, m in ipairs(motivosPartes(fam, partes)) do add(k, fam.familia, m) end
        if t == 'string' then
          local m = motivo(fam.valor, redis.call('GET', k))
          if m then add(k, fam.familia, 'valor: ' .. m) end
        elseif t == 'hash' then
          for _, m in ipairs(motivosHash(fam, partes, hashDe(k), nil)) do add(k, fam.familia, m) end
        elseif t == 'set' then
          for _, x in ipairs(ordenadas(conjuntoDe(redis.call('SMEMBERS', k)))) do
            local m = motivo(fam.membros, x)
            if m then add(k, fam.familia, 'membro ' .. amostra(x) .. ': ' .. m) end
          end
        elseif t == 'list' then
          for i, x in ipairs(redis.call('LRANGE', k, 0, -1)) do
            local m = motivo(fam.itens, x)
            if m then add(k, fam.familia, 'item ' .. (i - 1) .. ': ' .. m) end
          end
        end
      end
      if fam.familia == 'user:{id}' then
        perfis[partes.id] = k
      elseif fam.familia == 'user:{id}:consent' or fam.familia == 'user:{id}:broadcaster-attrs:{scid}' then
        dependentes[#dependentes + 1] = { k, fam.familia, partes.id }
      end
    end
  end

  -- perfil-indexado
  local indice = membros('users:index')
  for _, id in ipairs(ordenadas(indice)) do
    if perfis[id] == nil then
      add('users:index', 'users:index', 'membro ' .. amostra(id) .. ' sem o hash user:' .. id .. ' (invariante perfil-indexado)')
    end
  end
  for _, id in ipairs(ordenadas(perfis)) do
    if not indice[id] then
      add(perfis[id], 'user:{id}', 'id fora de users:index (invariante perfil-indexado)')
    end
  end
  -- perfil-dependentes
  for _, d in ipairs(dependentes) do
    if not indice[d[3]] then
      add(d[1], d[2], 'perfil ' .. amostra(d[3]) .. ' fora de users:index (invariante perfil-dependentes)')
    end
  end
  -- clientes-disjuntos e autorizado-tem-registro
  local aut, blq = membros('clients:authorized'), membros('clients:blocked')
  for _, id in ipairs(ordenadas(aut)) do
    if blq[id] then
      add('clients:authorized', 'clients:authorized', 'membro ' .. amostra(id) .. ' tambem em clients:blocked (invariante clientes-disjuntos)')
    end
    if tipoNoRedis(chaveDe(familia('client:{id}'), { id = id })) ~= 'hash' then
      add('clients:authorized', 'clients:authorized', 'membro ' .. amostra(id) .. ' sem o hash client:' .. id .. ' (invariante autorizado-tem-registro)')
    end
  end
  return out
end

-- -------------------------------------------------------------- registro --

local PREFIXO = 'TV30ESQUEMA'

local function registra(nome, fn, flags)
  redis.register_function {
    function_name = nome,
    flags = flags,
    callback = function(keys, args)
      local ok, r = pcall(function()
        if #keys > 0 then
          falha('chamada com numkeys ' .. #keys .. ': as funcoes tv30 usam numkeys 0 e derivam as chaves dos argumentos')
        end
        return fn(args)
      end)
      if ok then return r end
      if type(r) == 'table' and r.tv30 then
        return redis.error_reply(PREFIXO .. ' ' .. nome .. ': ' .. r.tv30)
      end
      error(r, 0)
    end,
  }
end

-- Na carga o Redis so expoe redis.register_function (nem ipairs nem pairs:
-- 'Script attempted to access nonexistent global variable ipairs'), por isso
-- a lista e fixa e o laco e numerico. Os testes conferem que ela casa com as
-- funcoes do esquema.json.
local ESCRITA = {
  'tv30_atributos_emissora',
  'tv30_bind_registra',
  'tv30_bind_remove',
  'tv30_cliente_autoriza',
  'tv30_cliente_bloqueia',
  'tv30_cliente_grava',
  'tv30_cliente_inclui_autorizado',
  'tv30_origem_registra',
  'tv30_origem_remove',
  'tv30_origens_limpa',
  'tv30_perfil_grava',
  'tv30_perfil_remove',
  'tv30_perfil_ultimo_acesso',
  'tv30_sessao_servico',
  'tv30_sessao_servico_id',
  'tv30_sessao_usuario',
}
for i = 1, #ESCRITA do
  registra(ESCRITA[i], OP[ESCRITA[i]], { 'no-cluster' })
end

registra('tv30_auditoria', function(args)
  exigeArgs(args, 0, 'tv30_auditoria')
  return auditoria()
end, { 'no-writes', 'no-cluster' })

registra('tv30_versao', function(args)
  exigeArgs(args, 0, 'tv30_versao')
  return prepara().versao
end, { 'no-writes', 'no-cluster' })
