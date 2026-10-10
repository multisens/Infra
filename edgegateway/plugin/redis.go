package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Mini cliente Redis (protocolo RESP2) so com net da biblioteca padrao —
// leitura (GET, SISMEMBER, HEXISTS, LRANGE, HGETALL, SCAN) e, para a C.6.8
// respondida pela borda, escrita (RPUSH, LREM). Pool pequeno de conexoes,
// timeout curto por comando e reconexao: conexao com erro de rede eh
// descartada e o comando eh repetido UMA vez numa conexao nova — menos o
// RPUSH quando a falha veio DEPOIS de o comando sair (resposta perdida de um
// comando talvez executado: repetir duplicaria a entrada).

type redisError string

func (e redisError) Error() string { return "redis: " + string(e) }

type redisConn struct {
	c net.Conn
	r *bufio.Reader
}

type redisClient struct {
	addr    string
	timeout time.Duration
	pool    chan *redisConn
}

const (
	redisPoolSize = 8
	redisMaxBulk  = 4 << 20 // 4 MiB: nenhuma chave lida aqui chega perto disso
)

func newRedisClient(addr string, timeout time.Duration) *redisClient {
	return &redisClient{addr: addr, timeout: timeout, pool: make(chan *redisConn, redisPoolSize)}
}

func (rc *redisClient) dial() (*redisConn, error) {
	c, err := net.DialTimeout("tcp", rc.addr, rc.timeout)
	if err != nil {
		return nil, err
	}
	return &redisConn{c: c, r: bufio.NewReader(c)}, nil
}

func (rc *redisClient) take() (*redisConn, bool, error) {
	select {
	case cn := <-rc.pool:
		return cn, false, nil
	default:
		cn, err := rc.dial()
		return cn, true, err
	}
}

func (rc *redisClient) give(cn *redisConn) {
	select {
	case rc.pool <- cn:
	default:
		cn.c.Close()
	}
}

// drain fecha as conexoes paradas no pool (provavelmente mortas tambem).
func (rc *redisClient) drain() {
	for {
		select {
		case cn := <-rc.pool:
			cn.c.Close()
		default:
			return
		}
	}
}

// do executa um comando idempotente. Erro de rede numa conexao reaproveitada
// (ex.: o Redis reiniciou e fechou as conexoes) => esvazia o pool e tenta UMA
// vez numa conexao nova.
func (rc *redisClient) do(args ...string) (interface{}, error) {
	return rc.exec(true, args)
}

// doWrite executa um comando que NAO pode ser repetido as cegas (RPUSH): so
// repete se a falha foi antes de o comando terminar de sair.
func (rc *redisClient) doWrite(args ...string) (interface{}, error) {
	return rc.exec(false, args)
}

func (rc *redisClient) exec(idempotent bool, args []string) (interface{}, error) {
	cn, fresh, err := rc.take()
	if err != nil {
		return nil, err
	}
	v, sent, err := cn.roundTrip(rc.timeout, args)
	if err == nil {
		rc.give(cn)
		return v, nil
	}
	var re redisError
	if errors.As(err, &re) {
		// resposta de erro do servidor: a conexao continua integra
		rc.give(cn)
		return nil, err
	}
	cn.c.Close()
	if fresh || (sent && !idempotent) {
		return nil, err
	}
	rc.drain()
	if cn, err = rc.dial(); err != nil {
		return nil, err
	}
	if v, _, err = cn.roundTrip(rc.timeout, args); err != nil {
		var re redisError
		if errors.As(err, &re) {
			rc.give(cn)
		} else {
			cn.c.Close()
		}
		return nil, err
	}
	rc.give(cn)
	return v, nil
}

// roundTrip envia o comando e le a resposta. sent=true: o comando saiu
// inteiro (a falha, se houver, foi na leitura da resposta).
func (cn *redisConn) roundTrip(timeout time.Duration, args []string) (v interface{}, sent bool, err error) {
	if err := cn.c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, false, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(cn.c, b.String()); err != nil {
		return nil, false, err
	}
	v, err = readReply(cn.r)
	return v, true, err
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("resposta RESP malformada")
	}
	return line[:len(line)-2], nil
}

// readReply decodifica uma resposta RESP2: simple string -> string, erro ->
// redisError, inteiro -> int64, bulk -> string (nulo -> nil), array ->
// []interface{} (nulo -> nil).
func readReply(r *bufio.Reader) (interface{}, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, errors.New("resposta RESP vazia")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, redisError(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return nil, errors.New("inteiro RESP malformado")
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > redisMaxBulk {
			return nil, errors.New("bulk RESP malformado")
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return nil, errors.New("bulk RESP sem CRLF")
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 1<<20 {
			return nil, errors.New("array RESP malformado")
		}
		if n == -1 {
			return nil, nil
		}
		out := make([]interface{}, n)
		for i := range out {
			if out[i], err = readReply(r); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("tipo RESP desconhecido %q", line[0])
}

func (rc *redisClient) get(key string) (string, bool, error) {
	v, err := rc.do("GET", key)
	if err != nil {
		return "", false, err
	}
	if v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, errors.New("GET: resposta inesperada")
	}
	return s, true, nil
}

func (rc *redisClient) intCmd(args ...string) (int64, error) {
	v, err := rc.do(args...)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("%s: resposta inesperada", args[0])
	}
	return n, nil
}

func (rc *redisClient) lrange(key string) ([]string, error) {
	v, err := rc.do("LRANGE", key, "0", "-1")
	if err != nil {
		return nil, err
	}
	return bulkStrings(v), nil
}

// bulkStrings: elementos texto de uma resposta array (nulo => vazio).
func bulkStrings(v interface{}) []string {
	arr, _ := v.([]interface{})
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// hgetall: o array campo, valor, campo, valor... vira mapa (chave ausente
// => mapa vazio).
func (rc *redisClient) hgetall(key string) (map[string]string, error) {
	v, err := rc.do("HGETALL", key)
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]interface{})
	if !ok || len(arr)%2 != 0 {
		return nil, errors.New("HGETALL: resposta inesperada")
	}
	out := make(map[string]string, len(arr)/2)
	for i := 0; i+1 < len(arr); i += 2 {
		f, ok1 := arr[i].(string)
		val, ok2 := arr[i+1].(string)
		if !ok1 || !ok2 {
			return nil, errors.New("HGETALL: resposta inesperada")
		}
		out[f] = val
	}
	return out, nil
}

// Teto de iteracoes do SCAN: o cursor sempre volta a 0, mas um servidor
// defeituoso nao pode prender a requisicao num laco.
const scanMaxRounds = 10000

// scanKeys: todas as chaves que casam o padrao (SCAN com cursor, nao KEYS,
// que bloquearia o Redis), sem repeticao (o SCAN pode repetir chave).
func (rc *redisClient) scanKeys(match string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	cursor := "0"
	for round := 0; ; round++ {
		if round >= scanMaxRounds {
			return nil, errors.New("SCAN: cursor nao terminou")
		}
		v, err := rc.do("SCAN", cursor, "MATCH", match, "COUNT", "100")
		if err != nil {
			return nil, err
		}
		arr, ok := v.([]interface{})
		if !ok || len(arr) != 2 {
			return nil, errors.New("SCAN: resposta inesperada")
		}
		next, ok := arr[0].(string)
		if !ok {
			return nil, errors.New("SCAN: cursor inesperado")
		}
		for _, k := range bulkStrings(arr[1]) {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
		if next == "0" {
			return out, nil
		}
		cursor = next
	}
}

// Chaves Redis da borda:
//
//	clients:blocked              SET    le    — tv3ws (auth-manager): clientes bloqueados (C.4.2.2)
//	origins:associated           HASH   le    — AoP (core.js): origem -> scid das apps associadas
//	session:current-service-id   STRING le    — tv3ws (api/user/service.ts): servico corrente
//	session:current-service      HASH   le    — tv3ws (core.ts): serviceName/serviceId do corrente (C.6.8.3)
//	bind-context:{serviceId}     LIST   le e ESCREVE — a propria borda (C.6.8.2/C.6.8.4, bindcontext.go):
//	                                    {alg,key,registeredAt}
const (
	keyBlocked        = "clients:blocked"
	keyAssociated     = "origins:associated"
	keyCurrentService = "session:current-service-id"
	keyCurrentSvcHash = "session:current-service"
	keyBindPrefix     = "bind-context:"
)

// store eh o que a borda precisa do armazenamento (interface p/ testes).
type store interface {
	IsBlocked(clientID string) (bool, error)
	IsAssociatedOrigin(origin string) (bool, error)
	CurrentServiceID() (string, error)
	BindKeys(serviceID string) ([]string, error)

	// C.6.8 respondida pela borda
	AddBindKey(serviceID, raw string) error             // RPUSH
	RemoveBindKey(serviceID, raw string) (int64, error) // LREM 0 (todas as iguais)
	BindServices() ([]string, error)                    // serviceIds com bind-context:* (SCAN)
	CurrentService() (map[string]string, error)         // HGETALL session:current-service
}

type redisStore struct{ c *redisClient }

func (s *redisStore) IsBlocked(id string) (bool, error) {
	n, err := s.c.intCmd("SISMEMBER", keyBlocked, id)
	return n == 1, err
}

// origins:associated eh HASH (o AoP grava com HSET origem scid e o tv3ws
// le com HGET), nao SET — por isso HEXISTS.
func (s *redisStore) IsAssociatedOrigin(origin string) (bool, error) {
	n, err := s.c.intCmd("HEXISTS", keyAssociated, origin)
	return n == 1, err
}

func (s *redisStore) CurrentServiceID() (string, error) {
	v, _, err := s.c.get(keyCurrentService)
	return v, err
}

func (s *redisStore) BindKeys(serviceID string) ([]string, error) {
	return s.c.lrange(keyBindPrefix + serviceID)
}

func (s *redisStore) AddBindKey(serviceID, raw string) error {
	v, err := s.c.doWrite("RPUSH", keyBindPrefix+serviceID, raw)
	if err != nil {
		return err
	}
	if _, ok := v.(int64); !ok {
		return errors.New("RPUSH: resposta inesperada")
	}
	return nil
}

func (s *redisStore) RemoveBindKey(serviceID, raw string) (int64, error) {
	return s.c.intCmd("LREM", keyBindPrefix+serviceID, "0", raw)
}

func (s *redisStore) BindServices() ([]string, error) {
	keys, err := s.c.scanKeys(keyBindPrefix + "*")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if sid := strings.TrimPrefix(k, keyBindPrefix); sid != "" && sid != k {
			out = append(out, sid)
		}
	}
	return out, nil
}

func (s *redisStore) CurrentService() (map[string]string, error) {
	return s.c.hgetall(keyCurrentSvcHash)
}
