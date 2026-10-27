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

// Minimal Redis client (RESP2) on the standard library. It writes only by
// FCALL of tv30_bind_register and tv30_bind_remove, which check the schema
// of the entry.

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
	// Largest bulk string read, 4 MiB.
	redisMaxBulk = 4 << 20
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

// drain closes the idle connections of the pool.
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

// do runs an idempotent command. On a network error on a reused connection,
// as after a Redis restart, it empties the pool and retries once on a new
// connection.
func (rc *redisClient) do(args ...string) (interface{}, error) {
	return rc.exec(true, args)
}

// fcall calls a function of the tv30 Redis library with numkeys 0; the
// functions derive the keys from the arguments. When not idempotent, it
// retries only if the failure happened before the command was fully sent.
func (rc *redisClient) fcall(idempotente bool, nome string, args ...string) (interface{}, error) {
	full := append([]string{"FCALL", nome, "0"}, args...)
	return rc.exec(idempotente, full)
}

// Prefix of the error replies of the tv30 functions for data outside the
// schema.
const schemaErrorPrefix = "TV30SCHEMA "

// schemaRefusal reports whether err is a refusal by the schema, and returns
// its message, which gives function, key, field and reason.
func schemaRefusal(err error) (string, bool) {
	var re redisError
	if errors.As(err, &re) && strings.HasPrefix(string(re), schemaErrorPrefix) {
		return string(re), true
	}
	return "", false
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
		// An error reply of the server leaves the connection usable.
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

// roundTrip sends the command and reads the reply. sent is true if the
// command was fully sent, so that a failure happened reading the reply.
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

// readReply decodes a RESP2 reply: simple string to string, error to
// redisError, integer to int64, bulk to string, array to []interface{}; a
// null bulk or array to nil.
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

// bulkStrings returns the string elements of an array reply; empty for null.
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

// hgetall returns the hash as a map; empty if the key does not exist.
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

// Maximum number of SCAN rounds before giving up on the cursor.
const scanMaxRounds = 10000

// scanKeys returns every key that matches the pattern, by SCAN, without
// repetitions.
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

// Redis keys used by the edge, all read and only bind-context:{serviceId}
// written:
//   - clients:blocked (SET): clients blocked by the user (C.4.2.2);
//   - clients:authorized (SET): clients authorized by the viewer;
//   - origins:associated (HASH): origin to the service that launched the
//     application;
//   - session:current-service-id (STRING): the current service;
//   - session:current-service (HASH): serviceName and serviceId of the
//     current service;
//   - services:announced (SET): the services of the Application Catalog;
//   - bind-context:{serviceId} (LIST): {alg,key,registeredAt}.
const (
	keyBlocked        = "clients:blocked"
	keyAuthorized     = "clients:authorized"
	keyAssociated     = "origins:associated"
	keyCurrentService = "session:current-service-id"
	keyCurrentSvcHash = "session:current-service"
	keyAnnounced      = "services:announced"
	keyBindPrefix     = "bind-context:"
)

// store is the storage access of the edge.
type store interface {
	IsBlocked(clientID string) (bool, error)
	IsAuthorized(clientID string) (bool, error)
	// AssociatedOrigin returns the service of the origin in
	// origins:associated; ok is false if the origin is not in the hash.
	AssociatedOrigin(origin string) (service string, ok bool, err error)
	CurrentServiceID() (string, error)
	ServiceInCatalog(serviceID string) (bool, error)
	BindKeys(serviceID string) ([]string, error)

	// C.6.8, answered by the edge.
	AddBindKey(serviceID, raw string) error
	// RemoveBindKey removes every entry equal to raw, and returns how many.
	RemoveBindKey(serviceID, raw string) (int64, error)
	// BindServices returns the serviceIds that have a bind-context key.
	BindServices() ([]string, error)
	CurrentService() (map[string]string, error)
}

type redisStore struct{ c *redisClient }

func (s *redisStore) IsBlocked(id string) (bool, error) {
	n, err := s.c.intCmd("SISMEMBER", keyBlocked, id)
	return n == 1, err
}

func (s *redisStore) IsAuthorized(id string) (bool, error) {
	n, err := s.c.intCmd("SISMEMBER", keyAuthorized, id)
	return n == 1, err
}

func (s *redisStore) AssociatedOrigin(origin string) (string, bool, error) {
	v, err := s.c.do("HGET", keyAssociated, origin)
	if err != nil || v == nil {
		return "", false, err
	}
	sv, ok := v.(string)
	if !ok {
		return "", false, errors.New("HGET: resposta inesperada")
	}
	return sv, true, nil
}

func (s *redisStore) CurrentServiceID() (string, error) {
	v, _, err := s.c.get(keyCurrentService)
	return v, err
}

func (s *redisStore) ServiceInCatalog(id string) (bool, error) {
	n, err := s.c.intCmd("SISMEMBER", keyAnnounced, id)
	return n == 1, err
}

func (s *redisStore) BindKeys(serviceID string) ([]string, error) {
	return s.c.lrange(keyBindPrefix + serviceID)
}

// AddBindKey appends the entry to bind-context:{serviceId} by
// tv30_bind_register, which checks the schema of the entry and of the
// serviceId; data outside the schema gives a TV30SCHEMA error
// (schemaRefusal), and nothing is written. The call is not retried after
// the command was sent.
func (s *redisStore) AddBindKey(serviceID, raw string) error {
	v, err := s.c.fcall(false, "tv30_bind_register", serviceID, raw)
	if err != nil {
		return err
	}
	if _, ok := v.(int64); !ok {
		return errors.New("tv30_bind_register: resposta inesperada")
	}
	return nil
}

// RemoveBindKey removes every equal entry by tv30_bind_remove; the call may
// be retried on a new connection.
func (s *redisStore) RemoveBindKey(serviceID, raw string) (int64, error) {
	v, err := s.c.fcall(true, "tv30_bind_remove", serviceID, raw)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, errors.New("tv30_bind_remove: resposta inesperada")
	}
	return n, nil
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
