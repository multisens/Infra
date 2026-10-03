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
// GET, SISMEMBER, HEXISTS e LRANGE bastam. Pool pequeno de conexoes,
// timeout curto por comando e reconexao: conexao com erro de rede eh
// descartada e o comando eh repetido UMA vez numa conexao nova.

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

// do executa um comando. Erro de rede numa conexao reaproveitada (ex.: o
// Redis reiniciou e fechou as conexoes) => esvazia o pool e tenta UMA vez
// numa conexao nova.
func (rc *redisClient) do(args ...string) (interface{}, error) {
	cn, fresh, err := rc.take()
	if err != nil {
		return nil, err
	}
	v, err := cn.roundTrip(rc.timeout, args)
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
	if fresh {
		return nil, err
	}
	rc.drain()
	if cn, err = rc.dial(); err != nil {
		return nil, err
	}
	if v, err = cn.roundTrip(rc.timeout, args); err != nil {
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

func (cn *redisConn) roundTrip(timeout time.Duration, args []string) (interface{}, error) {
	if err := cn.c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(cn.c, b.String()); err != nil {
		return nil, err
	}
	return readReply(cn.r)
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
	arr, _ := v.([]interface{})
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// Chaves Redis lidas pela borda (todas escritas por outros componentes):
//
//	clients:blocked              SET   tv3ws (auth-manager) — clientes bloqueados (C.4.2.2)
//	origins:associated           HASH  AoP (core.js) — origem -> scid das apps associadas
//	session:current-service-id   STRING tv3ws (api/user/service.ts) — servico corrente
//	bind-context:{serviceId}     LIST  tv3ws (API C.6.8.2) — {alg,key,registeredAt}
const (
	keyBlocked        = "clients:blocked"
	keyAssociated     = "origins:associated"
	keyCurrentService = "session:current-service-id"
	keyBindPrefix     = "bind-context:"
)

// store eh o que a decisao precisa do armazenamento (interface p/ testes).
type store interface {
	IsBlocked(clientID string) (bool, error)
	IsAssociatedOrigin(origin string) (bool, error)
	CurrentServiceID() (string, error)
	BindKeys(serviceID string) ([]string, error)
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
