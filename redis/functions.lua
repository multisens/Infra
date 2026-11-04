-- Body of the "tv30" Redis library; library.sh prepends "#!lua name=tv30"
-- and SCHEMA_JSON (schema.json). Every write of the project to the families
-- of the schema goes through a function here; data outside the schema gives
-- a TV30SCHEMA error, and nothing is written.

-- cjson exists only inside the calls (at load time Redis refuses the
-- global), so the schema is decoded on the first call.
local S

-- Schema error; register() turns it into a TV30SCHEMA reply.
local function fail(msg)
  error({ tv30 = msg }, 0)
end

local function sortedKeys(t)
  local out = {}
  for k in pairs(t) do
    out[#out + 1] = k
  end
  table.sort(out, function(a, b) return tostring(a) < tostring(b) end)
  return out
end

-- UTF-8 code points, not bytes (Table 7: 20 characters in nickname).
local function charCount(s)
  local _, n = string.gsub(s, '[^\128-\191]', '')
  return n
end

-- Short and without control characters: the Redis error reply is a single
-- line.
local function sample(v)
  v = tostring(v)
  if #v > 48 then
    v = string.sub(v, 1, 45) .. '...'
  end
  return '"' .. string.gsub(v, '%c', '?') .. '"'
end

local function escapePattern(s)
  return (string.gsub(s, '[%^%$%(%)%%%.%[%]%*%+%-%?]', '%%%0'))
end

local function escapeGlob(s)
  return (string.gsub(s, '[%*%?%[%]\\]', '\\%0'))
end

local function redisType(key)
  return redis.call('TYPE', key).ok
end

local function hashOf(key)
  local flat = redis.call('HGETALL', key)
  local h = {}
  for i = 1, #flat, 2 do
    h[flat[i]] = flat[i + 1]
  end
  return h
end

local function setOf(list)
  local s = {}
  for _, m in ipairs(list) do
    s[m] = true
  end
  return s
end

-- In batches: unpack has a stack limit.
local function inBatches(cmd, key, list)
  local step = 400
  for i = 1, #list, step do
    local batch = { cmd, key }
    for j = i, math.min(i + step - 1, #list) do
      batch[#batch + 1] = list[j]
    end
    redis.call(unpack(batch))
  end
end

-- "{x}" in the middle of the key matches [^:]+; the last part matches the
-- rest, unless its rule does not admit ':' (so user:{id} does not take
-- user:{id}:consent).
local function compile(template, parts)
  local pattern, names, pos = '^', {}, 1
  while true do
    local a, b, name = string.find(template, '{(%w+)}', pos)
    if not a then
      pattern = pattern .. escapePattern(string.sub(template, pos))
      break
    end
    pattern = pattern .. escapePattern(string.sub(template, pos, a - 1))
    local rule = parts and parts[name]
    local noColon = rule and (rule.type == 'uuid' or (rule.forbids and string.find(rule.forbids, ':', 1, true)))
    if b == #template and not noColon then
      pattern = pattern .. '(.+)'
    else
      pattern = pattern .. '([^:]+)'
    end
    names[#names + 1] = name
    pos = b + 1
  end
  return pattern .. '$', names
end

-- Checked by the audit; schema.json must declare exactly these.
local INVARIANTS = {
  ['profile-indexed'] = true,
  ['profile-dependents'] = true,
  ['disjoint-clients'] = true,
  ['authorized-has-record'] = true,
}

local function prepare()
  if S then
    return S
  end
  local s = cjson.decode(SCHEMA_JSON)
  local defs = s.definitions or {}
  local function resolve(r)
    if type(r) ~= 'table' or r.ref == nil then
      return r
    end
    local base = defs[r.ref]
    if base == nil then
      error('schema.json: unknown definition "' .. tostring(r.ref) .. '"')
    end
    local out = {}
    for k, v in pairs(base) do
      out[k] = v
    end
    for k, v in pairs(r) do
      if k ~= 'ref' then
        out[k] = v
      end
    end
    return out
  end
  s.byName = {}
  for _, f in ipairs(s.families) do
    if f.value then
      f.value = resolve(f.value)
    end
    if f.members then
      f.members = resolve(f.members)
    end
    if f.parts then
      for k, r in pairs(f.parts) do
        f.parts[k] = resolve(r)
      end
    end
    if f.fields then
      for k, r in pairs(f.fields) do
        f.fields[k] = resolve(r)
      end
    end
    if type(f.otherFields) == 'table' then
      f.otherFields.name = resolve(f.otherFields.name)
      f.otherFields.value = resolve(f.otherFields.value)
    end
    if f.items then
      f.items = resolve(f.items)
      if f.items.fields then
        for k, r in pairs(f.items.fields) do
          f.items.fields[k] = resolve(r)
        end
      end
    end
    if f.forbiddenFields then
      f.forbidden = setOf(f.forbiddenFields.names)
    end
    f.pattern, f.partNames = compile(f.key, f.parts)
    s.byName[f.family] = f
  end
  local declared = {}
  for _, inv in ipairs(s.invariants or {}) do
    if not INVARIANTS[inv.id] then
      error('schema.json declares the invariant "' .. tostring(inv.id) .. '", without implementation in functions.lua')
    end
    declared[inv.id] = true
  end
  for id in pairs(INVARIANTS) do
    if not declared[id] then
      error('functions.lua implements the invariant "' .. id .. '", absent from schema.json')
    end
  end
  S = s
  return S
end

local function family(name)
  local f = prepare().byName[name]
  if f == nil then
    error('schema.json without the family "' .. name .. '"')
  end
  return f
end

local function keyOf(fam, parts)
  return (string.gsub(fam.key, '{(%w+)}', function(n) return parts[n] end))
end

local function classify(key)
  for _, f in ipairs(prepare().families) do
    local caps = { string.match(key, f.pattern) }
    if caps[1] ~= nil then
      local parts = {}
      for i, n in ipairs(f.partNames) do
        parts[n] = caps[i]
      end
      return f, parts
    end
  end
  return nil
end

local function languageOk(v)
  local i = 0
  for sub in string.gmatch(v .. '-', '([^-]*)-') do
    i = i + 1
    if #sub < 1 or #sub > 8 or string.find(sub, '[^%w]') then
      return false
    end
    if i == 1 and not (string.find(sub, '^%a%a+$') or sub == 'x' or sub == 'i') then
      return false
    end
  end
  return i > 0
end

-- A literal pattern, not string.rep: at load time Redis does not expose the
-- libraries.
local UUID_FORMAT = '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-%x%x%x%x%-%x%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$'
local UUID_NIL = '00000000-0000-0000-0000-000000000000'
local UUID_MAX = 'ffffffff-ffff-ffff-ffff-ffffffffffff'

local function uuidReason(v)
  if not string.find(v, UUID_FORMAT) then
    return 'must be a UUID in the format of RFC 9562 (8-4-4-4-12 hexadecimal digits, like ' ..
      'c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10; got ' .. sample(v) .. ')'
  end
  if string.find(v, '%u') then
    return 'UUID must be in lowercase (got ' .. sample(v) .. ')'
  end
  -- Nil and max before the version, so that the message says what the value
  -- is.
  if v == UUID_NIL then
    return 'nil UUID (Nil UUID of RFC 9562, all 0) is not a profile id (version 0 and variant 0)'
  end
  if v == UUID_MAX then
    return 'max UUID (Max UUID of RFC 9562, all f) is not a profile id (version f and variant f)'
  end
  local version = string.sub(v, 15, 15)
  if not string.find(version, '^[1-8]$') then
    return 'UUID version must be 1 to 8 (1st digit of the 3rd group; got ' .. version .. ' in ' .. sample(v) .. ')'
  end
  local variant = string.sub(v, 20, 20)
  if not string.find(variant, '^[89ab]$') then
    return 'UUID variant must be the one of RFC 9562 (1st digit of the 4th group in 8, 9, a or b; got ' ..
      variant .. ' in ' .. sample(v) .. ')'
  end
  return nil
end

-- Avatar: name of a file of the thumbnail directory, as
-- ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(svg|png|jpg|jpeg|webp)$. A Lua pattern
-- has no alternation or counted repetition: the check is by parts, and the
-- extension is what comes after the last dot. Only ASCII passes, so #base
-- counts characters.
local AVATAR_EXAMPLE = 'like c3167a18-5dc5-4a6e-9b1d-3f2a7c5e8d10.svg'

local function fileNameReason(rule, v)
  if v == '' then
    return 'empty: the avatar is the name of a file of the thumbnail directory (' .. AVATAR_EXAMPLE ..
      '); without an avatar, the field is absent'
  end
  if string.sub(v, 1, 1) == '<' then
    return 'inline SVG is not accepted: the avatar is the name of a file of the thumbnail directory (Table 7: file path), ' ..
      AVATAR_EXAMPLE .. ' (got ' .. sample(v) .. ')'
  end
  if string.find(v, '[/\\]') then
    return 'only the file name, without a directory: cannot contain "/" or "\\" (got ' .. sample(v) .. ')'
  end
  local extensions = rule.extensions or {}
  local base, ext = string.match(v, '^(.*)%.([^.]*)$')
  local extOk = false
  if ext then
    for _, e in ipairs(extensions) do
      if ext == e then
        extOk = true
      end
    end
  end
  if not extOk then
    return 'extension must be one of .' .. table.concat(extensions, ', .') .. ', in lowercase (got ' .. sample(v) .. ')'
  end
  if not string.find(base, '^[A-Za-z0-9]') then
    return 'file name must start with a letter or digit (no dot at the start; got ' .. sample(v) .. ')'
  end
  if string.find(base, '[^A-Za-z0-9._%-]') then
    return 'file name only with unaccented letters, digits, ".", "_" and "-" (got ' .. sample(v) .. ')'
  end
  if rule.maxName and #base > rule.maxName then
    return 'file name has ' .. #base .. ' characters before the extension, maximum ' .. rule.maxName ..
      ' (got ' .. sample(v) .. ')'
  end
  return nil
end

local function originReason(v)
  local scheme, rest = string.match(v, '^(https?)://(.+)$')
  if not scheme then
    return 'must start with http:// or https://'
  end
  local host, port = string.match(rest, '^(%[[%x:%.]+%])(.*)$')
  if not host then
    host, port = string.match(rest, '^([%w%.%-]+)(.*)$')
  end
  if not host then
    return 'invalid host'
  end
  if string.find(host, '%u') then
    return 'host must be in lowercase (as the browser sends it in Origin)'
  end
  if port ~= '' then
    local p = string.match(port, '^:(%d+)$')
    if not p then
      return 'after the host only :port fits (no path and no trailing slash)'
    end
    if #p > 1 and string.sub(p, 1, 1) == '0' then
      return 'port with a leading zero'
    end
    local n = tonumber(p)
    if n < 1 or n > 65535 then
      return 'port outside 1 to 65535'
    end
    if (scheme == 'http' and n == 80) or (scheme == 'https' and n == 443) then
      return 'default port of the scheme does not appear in the origin the browser sends'
    end
  end
  return nil
end

-- nil if v satisfies the rule, otherwise the reason. inJson: v came from a
-- JSON value (integer and number; the rest, JSON string).
local reason
reason = function(rule, v, inJson)
  local t = rule.type
  if inJson then
    if t == 'integer' then
      if type(v) ~= 'number' or v ~= math.floor(v) then
        return 'must be an integer number in the JSON'
      end
      if rule.min and v < rule.min then
        return 'less than the minimum ' .. rule.min
      end
      if rule.max and v > rule.max then
        return 'greater than the maximum ' .. rule.max
      end
      return nil
    end
    if type(v) ~= 'string' then
      return 'must be text in the JSON'
    end
  end
  if t == 'text' then
    local n = charCount(v)
    if rule.min and n < rule.min then
      if n == 0 then
        return 'empty (minimum ' .. rule.min .. ' character(s))'
      end
      return 'has ' .. n .. ' character(s), minimum ' .. rule.min
    end
    if rule.max and n > rule.max then
      return 'has ' .. n .. ' characters, maximum ' .. rule.max
    end
    if rule.forbids then
      for i = 1, #rule.forbids do
        local c = string.sub(rule.forbids, i, i)
        if string.find(v, c, 1, true) then
          return 'cannot contain "' .. c .. '" (got ' .. sample(v) .. ')'
        end
      end
    end
  elseif t == 'boolean' then
    if v ~= 'true' and v ~= 'false' then
      return 'must be "true" or "false" (got ' .. sample(v) .. ')'
    end
  elseif t == 'integer' then
    if v == '' then
      if rule.allowEmpty then
        return nil
      end
      return 'must be an integer (got empty)'
    end
    if not (v == '0' or string.match(v, '^%-?[1-9]%d*$')) then
      return 'must be a decimal integer, without a leading zero (got ' .. sample(v) .. ')'
    end
    local n = tonumber(v)
    if rule.min and n < rule.min then
      return 'less than the minimum ' .. rule.min .. ' (got ' .. v .. ')'
    end
    if rule.max and n > rule.max then
      return 'greater than the maximum ' .. rule.max .. ' (got ' .. v .. ')'
    end
  elseif t == 'enum' then
    for _, x in ipairs(rule.values) do
      if v == x then
        return nil
      end
    end
    return 'must be one of ' .. table.concat(rule.values, ', ') .. ' (got ' .. sample(v) .. ')'
  elseif t == 'uuid' then
    return uuidReason(v)
  elseif t == 'fileName' then
    return fileNameReason(rule, v)
  elseif t == 'language' then
    if not languageOk(v) then
      return 'must be an IETF RFC 5646 language tag, like pt-BR (got ' .. sample(v) .. ')'
    end
  elseif t == 'origin' then
    local m = originReason(v)
    if m then
      return m .. ' (got ' .. sample(v) .. ')'
    end
  elseif t == 'json' then
    local ok, obj = pcall(cjson.decode, v)
    if not ok or type(obj) ~= 'table' or obj[1] ~= nil then
      return 'must be a JSON object (got ' .. sample(v) .. ')'
    end
    for _, name in ipairs(sortedKeys(rule.fields or {})) do
      local r = rule.fields[name]
      local x = obj[name]
      if x == nil or x == cjson.null then
        if r.required then
          return 'member "' .. name .. '" required, absent'
        end
      else
        local m = reason(r, x, true)
        if m then
          return 'member "' .. name .. '": ' .. m
        end
      end
    end
    if rule.otherFields == false then
      for _, name in ipairs(sortedKeys(obj)) do
        if not (rule.fields and rule.fields[name]) then
          return 'member "' .. tostring(name) .. '" not in the schema'
        end
      end
    end
  elseif t == 'jsonList' then
    -- cjson decodes [] and {} alike: the text must open a list.
    local ok, list = pcall(cjson.decode, v)
    if string.sub(v, 1, 1) ~= '[' or not ok or type(list) ~= 'table' then
      return 'must be a JSON list (got ' .. sample(v) .. ')'
    end
    for i, x in ipairs(list) do
      local m = reason(rule.items, x, true)
      if m then
        return 'item ' .. (i - 1) .. ': ' .. m
      end
    end
  else
    return 'rule of unknown type in schema.json: ' .. tostring(t)
  end
  return nil
end

local function partReasons(fam, parts)
  local out = {}
  for _, name in ipairs(fam.partNames) do
    local r = fam.parts and fam.parts[name]
    if r then
      local m = reason(r, parts[name] or '')
      if m then
        out[#out + 1] = '{' .. name .. '} of the key: ' .. m
      end
    end
  end
  return out
end

-- h = resulting state. written nil checks the whole record; a set checks
-- only those fields (what was already outside the schema does not block the
-- write; the audit shows it). The size limit always applies, to the result.
local function hashReasons(fam, parts, h, written)
  local out = {}
  local function add(m)
    out[#out + 1] = m
  end
  local fields = fam.fields or {}
  if written == nil then
    for _, name in ipairs(sortedKeys(fields)) do
      local r = fields[name]
      if h[name] == nil then
        if r.required then
          add('field ' .. name .. ': required, absent')
        elseif r.requiredIf and h[r.requiredIf.field] == r.requiredIf.value then
          add('field ' .. name .. ': required when ' .. r.requiredIf.field .. '=' .. r.requiredIf.value .. ', absent')
        end
      end
    end
  end
  for _, name in ipairs(sortedKeys(written or h)) do
    local v = h[name]
    if v ~= nil then
      local r = fields[name]
      if r then
        local m = reason(r, v)
        if m then
          add('field ' .. name .. ': ' .. m)
        end
      elseif type(fam.otherFields) == 'table' then
        local m = reason(fam.otherFields.name, name)
        if m then
          add('field name: ' .. m)
        end
        if fam.forbidden and fam.forbidden[name] then
          add('field ' .. name .. ': basic attribute that cannot be redefined here (' .. fam.forbiddenFields.source .. ')')
        end
        m = reason(fam.otherFields.value, v)
        if m then
          add('field ' .. sample(name) .. ': ' .. m)
        end
      else
        add('field ' .. sample(name) .. ': not in the schema')
      end
    end
  end
  local c = fam.fieldEqualsPart
  if c and (written == nil or written[c.field]) and h[c.field] ~= nil and h[c.field] ~= parts[c.part] then
    add('field ' .. c.field .. ': must equal the {' .. c.part .. '} of the key (got ' .. sample(h[c.field]) .. ')')
  end
  if fam.maxBytes then
    local n = 0
    for k, v in pairs(h) do
      n = n + #k + #v
    end
    if n > fam.maxBytes then
      add('names and values add up to ' .. n .. ' bytes, above the limit of ' .. fam.maxBytes .. ' (' .. fam.maxBytesSource .. ')')
    end
  end
  return out
end

local function ensure(key, reasons)
  if #reasons > 0 then
    local extra = ''
    if #reasons > 1 then
      extra = ' (and ' .. (#reasons - 1) .. ' more problem(s) in the same request)'
    end
    fail(key .. ' ' .. reasons[1] .. extra)
  end
end

-- Absent or of the schema type; true if it exists.
local function ensureType(fam, key)
  local t = redisType(key)
  if t ~= 'none' and t ~= fam.type then
    fail(key .. ' in Redis is ' .. t .. ', the schema asks for ' .. fam.type .. ' (nothing written)')
  end
  return t ~= 'none'
end

local function validKey(fam, parts)
  local key = keyOf(fam, parts)
  ensure(key, partReasons(fam, parts))
  return key
end

local function ensureArgs(args, n, usage)
  if #args ~= n then
    fail('usage: ' .. usage .. ' (got ' .. #args .. ' argument(s))')
  end
end

local function ensureNotEmpty(v, name)
  if v == nil or v == '' then
    fail(name .. ' empty')
  end
end

local function fieldPairs(args, first, usage, acceptsNone)
  local n = #args - first + 1
  if n % 2 ~= 0 or (n == 0 and not acceptsNone) then
    fail('usage: ' .. usage .. ' (incomplete field value pairs)')
  end
  local h = {}
  for i = first, #args, 2 do
    h[args[i]] = args[i + 1]
  end
  return h
end

local function flatten(h)
  local out = {}
  for _, k in ipairs(sortedKeys(h)) do
    out[#out + 1] = k
    out[#out + 1] = h[k]
  end
  return out
end

-- Checks new fields on a hash that may exist: a new record checks
-- everything, an existing one only the written fields. Does not write.
local function mergeHash(fam, parts, key, newFields)
  local exists = ensureType(fam, key)
  local h = exists and hashOf(key) or {}
  for k, v in pairs(newFields) do
    h[k] = v
  end
  local written = nil
  if exists then
    written = setOf(sortedKeys(newFields))
  end
  ensure(key, hashReasons(fam, parts, h, written))
end

-- Redis does not roll back what a function already wrote if it fails midway:
-- every operation checks everything, including the type of each key, before
-- the first write.

local OP = {}

-- Session.

local function sessionString(familyName, args, usage)
  ensureArgs(args, 1, usage)
  local fam = family(familyName)
  local m = reason(fam.value, args[1])
  if m then
    fail(fam.key .. ' value: ' .. m)
  end
  ensureType(fam, fam.key)
  redis.call('SET', fam.key, args[1])
  return 1
end

OP.tv30_session_user = function(args)
  return sessionString('session:current-user', args, 'tv30_session_user <profile id or empty>')
end

OP.tv30_session_service_id = function(args)
  return sessionString('session:current-service-id', args, 'tv30_session_service_id <service id or empty>')
end

OP.tv30_session_service = function(args)
  local fam = family('session:current-service')
  local h = fieldPairs(args, 1, 'tv30_session_service <field> <value> [...]')
  ensure(fam.key, hashReasons(fam, {}, h, nil))
  ensureType(fam, fam.key)
  redis.call('DEL', fam.key)
  inBatches('HSET', fam.key, flatten(h))
  return 1
end

-- Profiles.

OP.tv30_profile_write = function(args)
  local usage = 'tv30_profile_write <id> <n> <service 1..n> <field> <value> [...]'
  if #args < 2 then
    fail('usage: ' .. usage)
  end
  local id = args[1]
  local n = tonumber(args[2])
  if n == nil or n < 0 or n ~= math.floor(n) or 2 + n > #args then
    fail('usage: ' .. usage .. ' (n=' .. sample(args[2]) .. ' invalid)')
  end
  local services = {}
  for i = 3, 2 + n do
    services[#services + 1] = args[i]
  end
  local h = fieldPairs(args, 3 + n, usage)

  local fIndex, fProfile, fVis = family('users:index'), family('user:{id}'), family('user:{id}:consent')
  local parts = { id = id }
  local key = validKey(fProfile, parts)
  local visKey = keyOf(fVis, parts)
  ensure(key, hashReasons(fProfile, parts, h, nil))
  for _, s in ipairs(services) do
    local m = reason(fVis.members, s)
    if m then
      fail(visKey .. ' member ' .. sample(s) .. ': ' .. m)
    end
  end
  ensureType(fIndex, fIndex.key)
  ensureType(fProfile, key)
  ensureType(fVis, visKey)
  -- Rewriting an existing profile does not count as new.
  if fIndex.maxMembers and redis.call('SISMEMBER', fIndex.key, id) == 0
      and redis.call('SCARD', fIndex.key) >= fIndex.maxMembers then
    fail(fIndex.key .. ' limit of ' .. fIndex.maxMembers .. ' profiles reached (' .. fIndex.maxMembersSource ..
      '): ' .. sample(id) .. ' not written')
  end

  redis.call('SADD', fIndex.key, id)
  redis.call('DEL', key)
  inBatches('HSET', key, flatten(h))
  if #services > 0 then
    inBatches('SADD', visKey, services)
  end
  return 1
end

OP.tv30_profile_last_access = function(args)
  ensureArgs(args, 2, 'tv30_profile_last_access <id> <instant in ms>')
  local id, ms = args[1], args[2]
  local fIndex, fProfile = family('users:index'), family('user:{id}')
  if redisType(fIndex.key) ~= 'set' or redis.call('SISMEMBER', fIndex.key, id) == 0 then
    -- Profile missing or evicted: no stray hash is created.
    return 0
  end
  local parts = { id = id }
  local key = keyOf(fProfile, parts)
  if redisType(key) == 'none' then
    fail(key .. ' does not exist, but the id is in users:index (invariant profile-indexed broken; see tv30_audit)')
  end
  mergeHash(fProfile, parts, key, { lastAccess = ms })
  redis.call('HSET', key, 'lastAccess', ms)
  return 1
end

OP.tv30_profile_remove = function(args)
  ensureArgs(args, 1, 'tv30_profile_remove <id>')
  local id = args[1]
  ensureNotEmpty(id, 'profile id')
  local fIndex = family('users:index')
  local parts = { id = id }
  local targets = { keyOf(family('user:{id}'), parts), keyOf(family('user:{id}:consent'), parts) }
  local fAttr = family('user:{id}:broadcaster-attrs:{scid}')
  local prefix = string.gsub(fAttr.key, '{scid}', '')
  prefix = string.gsub(prefix, '{id}', function() return id end)
  local cursor = '0'
  repeat
    local r = redis.call('SCAN', cursor, 'MATCH', escapeGlob(prefix) .. '*', 'COUNT', 500)
    cursor = r[1]
    for _, k in ipairs(r[2]) do
      targets[#targets + 1] = k
    end
  until cursor == '0'
  ensureType(fIndex, fIndex.key)

  local n = 0
  if redisType(fIndex.key) == 'set' then
    n = n + redis.call('SREM', fIndex.key, id)
  end
  local seen = {}
  for _, k in ipairs(targets) do
    if not seen[k] then
      seen[k] = true
      n = n + redis.call('DEL', k)
    end
  end
  return n
end

-- Broadcaster attributes.

OP.tv30_broadcaster_attributes = function(args)
  local usage = 'tv30_broadcaster_attributes <id> <scid> <field> <value> [...] (an empty value removes the field)'
  if #args < 4 then
    fail('usage: ' .. usage)
  end
  local id, scid = args[1], args[2]
  local fIndex, fAttr = family('users:index'), family('user:{id}:broadcaster-attrs:{scid}')
  local parts = { id = id, scid = scid }
  local key = validKey(fAttr, parts)
  local requested = fieldPairs(args, 3, usage)
  if redisType(fIndex.key) ~= 'set' or redis.call('SISMEMBER', fIndex.key, id) == 0 then
    fail(key .. ' profile ' .. sample(id) .. ' outside users:index (invariant profile-dependents)')
  end
  local exists = ensureType(fAttr, key)
  local h = exists and hashOf(key) or {}
  local toDelete, toWrite, written = {}, {}, {}
  for _, k in ipairs(sortedKeys(requested)) do
    local v = requested[k]
    if v == '' then
      toDelete[#toDelete + 1] = k
      h[k] = nil
    else
      toWrite[k] = v
      h[k] = v
      written[k] = true
    end
    -- The name is checked even on removal (a basic attribute never belongs to
    -- the broadcaster).
    local m = reason(fAttr.otherFields.name, k)
    if m then
      fail(key .. ' field name: ' .. m)
    end
    if fAttr.forbidden[k] then
      fail(key .. ' field ' .. k .. ': basic attribute that cannot be redefined here (' .. fAttr.forbiddenFields.source .. ')')
    end
  end
  ensure(key, hashReasons(fAttr, parts, h, written))
  if #toDelete > 0 then
    inBatches('HDEL', key, toDelete)
  end
  local flat = flatten(toWrite)
  if #flat > 0 then
    inBatches('HSET', key, flat)
  end
  return #toDelete + #flat / 2
end

-- Clients.

local function validClientId(id)
  local fRecord = family('client:{id}')
  local parts = { id = id }
  return fRecord, parts, validKey(fRecord, parts)
end

OP.tv30_client_write = function(args)
  local usage = 'tv30_client_write <id> <field> <value> [...]'
  if #args < 3 then
    fail('usage: ' .. usage)
  end
  local fRecord, parts, key = validClientId(args[1])
  local newFields = fieldPairs(args, 2, usage)
  mergeHash(fRecord, parts, key, newFields)
  inBatches('HSET', key, flatten(newFields))
  return 1
end

OP.tv30_client_authorize = function(args)
  local usage = 'tv30_client_authorize <id> <field> <value> [...]'
  if #args < 3 then
    fail('usage: ' .. usage)
  end
  local id = args[1]
  local fRecord, parts, key = validClientId(id)
  local newFields = fieldPairs(args, 2, usage)
  local fAuth, fBlocked = family('clients:authorized'), family('clients:blocked')
  mergeHash(fRecord, parts, key, newFields)
  ensureType(fAuth, fAuth.key)
  ensureType(fBlocked, fBlocked.key)
  inBatches('HSET', key, flatten(newFields))
  redis.call('SREM', fBlocked.key, id)
  redis.call('SADD', fAuth.key, id)
  return 1
end

OP.tv30_client_block = function(args)
  ensureArgs(args, 1, 'tv30_client_block <id>')
  local id = args[1]
  validClientId(id)
  local fAuth, fBlocked = family('clients:authorized'), family('clients:blocked')
  ensureType(fAuth, fAuth.key)
  ensureType(fBlocked, fBlocked.key)
  redis.call('SREM', fAuth.key, id)
  redis.call('SADD', fBlocked.key, id)
  return 1
end

OP.tv30_client_add_authorized = function(args)
  ensureArgs(args, 1, 'tv30_client_add_authorized <id>')
  local id = args[1]
  local fRecord, _, key = validClientId(id)
  local fAuth, fBlocked = family('clients:authorized'), family('clients:blocked')
  ensureType(fAuth, fAuth.key)
  ensureType(fBlocked, fBlocked.key)
  if redis.call('SISMEMBER', fBlocked.key, id) == 1 then
    return 0
  end
  local t = redisType(key)
  if t == 'none' then
    fail(key .. ' absent: only who has the record enters clients:authorized (invariant authorized-has-record)')
  elseif t ~= fRecord.type then
    fail(key .. ' in Redis is ' .. t .. ', the schema asks for ' .. fRecord.type .. ' (invariant authorized-has-record; nothing written)')
  end
  return redis.call('SADD', fAuth.key, id)
end

-- Origins of the associated applications.

OP.tv30_origin_register = function(args)
  ensureArgs(args, 2, 'tv30_origin_register <origin> <service id>')
  local fam = family('origins:associated')
  local h = { [args[1]] = args[2] }
  mergeHash(fam, {}, fam.key, h)
  redis.call('HSET', fam.key, args[1], args[2])
  return 1
end

OP.tv30_origin_remove = function(args)
  ensureArgs(args, 1, 'tv30_origin_remove <origin>')
  ensureNotEmpty(args[1], 'origin')
  local fam = family('origins:associated')
  if not ensureType(fam, fam.key) then
    return 0
  end
  return redis.call('HDEL', fam.key, args[1])
end

OP.tv30_origins_clear = function(args)
  ensureArgs(args, 0, 'tv30_origins_clear')
  return redis.call('DEL', family('origins:associated').key)
end

-- Services announced in the LLS.

OP.tv30_announced_services = function(args)
  local fam = family('services:announced')
  for _, s in ipairs(args) do
    local m = reason(fam.members, s)
    if m then
      fail(fam.key .. ' member ' .. sample(s) .. ': ' .. m)
    end
  end
  ensureType(fam, fam.key)
  redis.call('DEL', fam.key)
  if #args > 0 then
    inBatches('SADD', fam.key, args)
  end
  return redis.call('SCARD', fam.key)
end

-- bind-context.

OP.tv30_bind_register = function(args)
  ensureArgs(args, 2, 'tv30_bind_register <service id> <JSON entry>')
  local fam = family('bind-context:{serviceId}')
  local parts = { serviceId = args[1] }
  local key = validKey(fam, parts)
  local m = reason(fam.items, args[2])
  if m then
    fail(key .. ' entry: ' .. m)
  end
  ensureType(fam, key)
  return redis.call('RPUSH', key, args[2])
end

OP.tv30_bind_remove = function(args)
  ensureArgs(args, 2, 'tv30_bind_remove <service id> <exact JSON entry>')
  ensureNotEmpty(args[1], 'service id')
  local fam = family('bind-context:{serviceId}')
  local key = keyOf(fam, { serviceId = args[1] })
  if not ensureType(fam, key) then
    return 0
  end
  return redis.call('LREM', key, 0, args[2])
end

-- Remote devices of API version 100.0: projection written by tv3ws.

local V100_REMOTE_DEVICE = 'v100.0:remote-device:{handle}'

-- Rewrites the whole record of the handle.
OP.tv30_v100_remote_device_write = function(args)
  local usage = 'tv30_v100_remote_device_write <handle> <field> <value> [...]'
  if #args < 3 then
    fail('usage: ' .. usage)
  end
  local fam = family(V100_REMOTE_DEVICE)
  local parts = { handle = args[1] }
  local key = validKey(fam, parts)
  local record = fieldPairs(args, 2, usage)
  ensure(key, hashReasons(fam, parts, record, nil))
  ensureType(fam, key)
  redis.call('DEL', key)
  inBatches('HSET', key, flatten(record))
  return 1
end

OP.tv30_v100_remote_device_remove = function(args)
  ensureArgs(args, 1, 'tv30_v100_remote_device_remove <handle>')
  ensureNotEmpty(args[1], 'handle')
  local key = keyOf(family(V100_REMOTE_DEVICE), { handle = args[1] })
  return redis.call('DEL', key)
end

OP.tv30_v100_remote_devices_clear = function(args)
  ensureArgs(args, 0, 'tv30_v100_remote_devices_clear')
  local prefix = string.gsub(family(V100_REMOTE_DEVICE).key, '{handle}', '')
  local removed = 0
  local cursor = '0'
  repeat
    local r = redis.call('SCAN', cursor, 'MATCH', escapeGlob(prefix) .. '*', 'COUNT', 500)
    cursor = r[1]
    for _, key in ipairs(r[2]) do
      removed = removed + redis.call('DEL', key)
    end
  until cursor == '0'
  return removed
end

local function membersOf(key)
  if redisType(key) ~= 'set' then
    return {}
  end
  return setOf(redis.call('SMEMBERS', key))
end

local function audit()
  local s = prepare()
  local out = {}
  local function add(key, fam, m)
    out[#out + 1] = { key, fam, m }
  end

  local seen, keys = {}, {}
  for _, p in ipairs(s.auditedPrefixes) do
    local cursor = '0'
    repeat
      local r = redis.call('SCAN', cursor, 'MATCH', escapeGlob(p) .. '*', 'COUNT', 500)
      cursor = r[1]
      for _, k in ipairs(r[2]) do
        if not seen[k] then
          seen[k] = true
          keys[#keys + 1] = k
        end
      end
    until cursor == '0'
  end
  table.sort(keys)

  local profiles, dependents = {}, {}
  for _, k in ipairs(keys) do
    local fam, parts = classify(k)
    if fam == nil then
      add(k, '-', 'key without family in the schema (audited prefix; see schema.json)')
    else
      local t = redisType(k)
      if t ~= fam.type then
        add(k, fam.family, 'in Redis is ' .. t .. ', the schema asks for ' .. fam.type)
      else
        for _, m in ipairs(partReasons(fam, parts)) do
          add(k, fam.family, m)
        end
        if t == 'string' then
          local m = reason(fam.value, redis.call('GET', k))
          if m then
            add(k, fam.family, 'value: ' .. m)
          end
        elseif t == 'hash' then
          for _, m in ipairs(hashReasons(fam, parts, hashOf(k), nil)) do
            add(k, fam.family, m)
          end
        elseif t == 'set' then
          local ms = sortedKeys(setOf(redis.call('SMEMBERS', k)))
          for _, x in ipairs(ms) do
            local m = reason(fam.members, x)
            if m then
              add(k, fam.family, 'member ' .. sample(x) .. ': ' .. m)
            end
          end
          if fam.maxMembers and #ms > fam.maxMembers then
            add(k, fam.family, #ms .. ' members, above the limit of ' .. fam.maxMembers .. ' (' .. fam.maxMembersSource .. ')')
          end
        elseif t == 'list' then
          for i, x in ipairs(redis.call('LRANGE', k, 0, -1)) do
            local m = reason(fam.items, x)
            if m then
              add(k, fam.family, 'item ' .. (i - 1) .. ': ' .. m)
            end
          end
        end
      end
      if fam.family == 'user:{id}' then
        profiles[parts.id] = k
      elseif fam.family == 'user:{id}:consent' or fam.family == 'user:{id}:broadcaster-attrs:{scid}' then
        dependents[#dependents + 1] = { k, fam.family, parts.id }
      end
    end
  end

  -- profile-indexed
  local index = membersOf('users:index')
  for _, id in ipairs(sortedKeys(index)) do
    if profiles[id] == nil then
      add('users:index', 'users:index', 'member ' .. sample(id) .. ' without the hash user:' .. id .. ' (invariant profile-indexed)')
    end
  end
  for _, id in ipairs(sortedKeys(profiles)) do
    if not index[id] then
      add(profiles[id], 'user:{id}', 'id outside users:index (invariant profile-indexed)')
    end
  end
  -- profile-dependents
  for _, d in ipairs(dependents) do
    if not index[d[3]] then
      add(d[1], d[2], 'profile ' .. sample(d[3]) .. ' outside users:index (invariant profile-dependents)')
    end
  end
  -- disjoint-clients and authorized-has-record
  local authorized, blocked = membersOf('clients:authorized'), membersOf('clients:blocked')
  for _, id in ipairs(sortedKeys(authorized)) do
    if blocked[id] then
      add('clients:authorized', 'clients:authorized', 'member ' .. sample(id) .. ' also in clients:blocked (invariant disjoint-clients)')
    end
    if redisType(keyOf(family('client:{id}'), { id = id })) ~= 'hash' then
      add('clients:authorized', 'clients:authorized', 'member ' .. sample(id) .. ' without the hash client:' .. id .. ' (invariant authorized-has-record)')
    end
  end
  return out
end

local ERROR_PREFIX = 'TV30SCHEMA'

-- Redis applies the ACL of the caller inside the function (without @write:
-- "ERR ACL failure in script"): the functions do not prevent direct writes,
-- which the audit shows.
local function register(name, fn, flags)
  redis.register_function {
    function_name = name,
    flags = flags,
    callback = function(keys, args)
      local ok, r = pcall(function()
        if #keys > 0 then
          fail('call with numkeys ' .. #keys .. ': the tv30 functions use numkeys 0 and derive the keys from the arguments')
        end
        return fn(args)
      end)
      if ok then
        return r
      end
      if type(r) == 'table' and r.tv30 then
        return redis.error_reply(ERROR_PREFIX .. ' ' .. name .. ': ' .. r.tv30)
      end
      error(r, 0)
    end,
  }
end

-- At load time Redis exposes only redis.register_function (neither pairs nor
-- ipairs): a fixed list and a numeric loop.
local WRITE_FUNCTIONS = {
  'tv30_announced_services',
  'tv30_bind_register',
  'tv30_bind_remove',
  'tv30_broadcaster_attributes',
  'tv30_client_add_authorized',
  'tv30_client_authorize',
  'tv30_client_block',
  'tv30_client_write',
  'tv30_origin_register',
  'tv30_origin_remove',
  'tv30_origins_clear',
  'tv30_profile_last_access',
  'tv30_profile_remove',
  'tv30_profile_write',
  'tv30_session_service',
  'tv30_session_service_id',
  'tv30_session_user',
  'tv30_v100_remote_device_remove',
  'tv30_v100_remote_device_write',
  'tv30_v100_remote_devices_clear',
}
for i = 1, #WRITE_FUNCTIONS do
  register(WRITE_FUNCTIONS[i], OP[WRITE_FUNCTIONS[i]], { 'no-cluster' })
end

register('tv30_audit', function(args)
  ensureArgs(args, 0, 'tv30_audit')
  return audit()
end, { 'no-writes', 'no-cluster' })

register('tv30_version', function(args)
  ensureArgs(args, 0, 'tv30_version')
  return prepare().version
end, { 'no-writes', 'no-cluster' })
