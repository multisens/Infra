package main

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRedis: servidor RESP minimo em memoria (so os comandos usados).
type fakeRedis struct {
	ln      net.Listener
	mu      sync.Mutex
	strs    map[string]string
	sets    map[string]map[string]bool
	hashes  map[string]map[string]string
	lists   map[string][]string
	conns   []net.Conn
	accepts int
}

func startFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{ln: ln, strs: map[string]string{}, sets: map[string]map[string]bool{},
		hashes: map[string]map[string]string{}, lists: map[string][]string{}}
	go f.loop()
	t.Cleanup(func() { ln.Close(); f.dropAll() })
	return f
}

func (f *fakeRedis) loop() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.accepts++
		f.mu.Unlock()
		go f.serve(c)
	}
}

func (f *fakeRedis) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// dropAll fecha as conexoes abertas (simula reinicio do Redis).
func (f *fakeRedis) dropAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
}

func (f *fakeRedis) serve(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		v, err := readReply(r)
		if err != nil {
			c.Close()
			return
		}
		arr, _ := v.([]interface{})
		args := make([]string, len(arr))
		for i, a := range arr {
			args[i], _ = a.(string)
		}
		c.Write([]byte(f.exec(args)))
	}
}

func bulk(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }

func (f *fakeRedis) exec(a []string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := func(ok bool) string {
		if ok {
			return ":1\r\n"
		}
		return ":0\r\n"
	}
	switch strings.ToUpper(a[0]) {
	case "GET":
		if v, ok := f.strs[a[1]]; ok {
			return bulk(v)
		}
		return "$-1\r\n"
	case "SISMEMBER":
		return b(f.sets[a[1]][a[2]])
	case "HEXISTS":
		if _, isStr := f.strs[a[1]]; isStr {
			return "-WRONGTYPE Operation against a key holding the wrong kind of value\r\n"
		}
		_, ok := f.hashes[a[1]][a[2]]
		return b(ok)
	case "LRANGE":
		l := f.lists[a[1]]
		out := "*" + strconv.Itoa(len(l)) + "\r\n"
		for _, e := range l {
			out += bulk(e)
		}
		return out
	}
	return "-ERR unknown command\r\n"
}

func TestRedisComandos(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() {
		f.strs[keyCurrentService] = tSvc
		f.sets[keyBlocked] = map[string]bool{"cli-bloq": true}
		f.hashes[keyAssociated] = map[string]string{tAssoc: tSvc}
		// segundo elemento com CRLF dentro: o bulk tem que ser lido pelo tamanho
		f.lists[keyBindPrefix+tSvc] = []string{storedJSON("HS256", "a"), "x\r\ny"}
	})

	s := &redisStore{c: newRedisClient(f.ln.Addr().String(), time.Second)}
	if v, err := s.CurrentServiceID(); err != nil || v != tSvc {
		t.Fatalf("GET: %q %v", v, err)
	}
	if b, err := s.IsBlocked("cli-bloq"); err != nil || !b {
		t.Fatalf("SISMEMBER bloqueado: %v %v", b, err)
	}
	if b, err := s.IsBlocked("cli-ok"); err != nil || b {
		t.Fatalf("SISMEMBER livre: %v %v", b, err)
	}
	if b, err := s.IsAssociatedOrigin(tAssoc); err != nil || !b {
		t.Fatalf("HEXISTS: %v %v", b, err)
	}
	if b, err := s.IsAssociatedOrigin("http://x"); err != nil || b {
		t.Fatalf("HEXISTS ausente: %v %v", b, err)
	}
	l, err := s.BindKeys(tSvc)
	if err != nil || len(l) != 2 || l[1] != "x\r\ny" {
		t.Fatalf("LRANGE: %q %v", l, err)
	}
	if l, err := s.BindKeys("urn:sem-chaves"); err != nil || len(l) != 0 {
		t.Fatalf("LRANGE vazio: %q %v", l, err)
	}
	f.with(func() { delete(f.strs, keyCurrentService) })
	if v, err := s.CurrentServiceID(); err != nil || v != "" {
		t.Fatalf("GET nulo: %q %v", v, err)
	}
	// erro do servidor (WRONGTYPE) nao derruba a conexao
	f.with(func() { f.strs[keyAssociated] = "x" })
	if _, err := s.IsAssociatedOrigin(tAssoc); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("esperado WRONGTYPE, veio %v", err)
	}
	f.with(func() { delete(f.strs, keyAssociated) })
	if _, err := s.IsAssociatedOrigin(tAssoc); err != nil {
		t.Fatalf("conexao ficou ruim depois de -ERR: %v", err)
	}
}

func TestRedisReconecta(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() { f.strs[keyCurrentService] = tSvc })
	s := &redisStore{c: newRedisClient(f.ln.Addr().String(), time.Second)}
	if _, err := s.CurrentServiceID(); err != nil {
		t.Fatal(err)
	}
	f.dropAll() // "reinicio" do Redis: conexao do pool morre
	time.Sleep(20 * time.Millisecond)
	if v, err := s.CurrentServiceID(); err != nil || v != tSvc {
		t.Fatalf("nao reconectou: %q %v", v, err)
	}
	f.mu.Lock()
	n := f.accepts
	f.mu.Unlock()
	if n < 2 {
		t.Fatalf("esperada conexao nova, accepts=%d", n)
	}
}

func TestRedisForaTimeoutCurto(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // ninguem escutando
	s := &redisStore{c: newRedisClient(addr, 200*time.Millisecond)}
	start := time.Now()
	if _, err := s.IsBlocked("x"); err == nil {
		t.Fatal("esperado erro com Redis fora")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("demorou demais: %v", d)
	}
}

func TestRedisServidorMudo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan net.Conn, 1)
	defer func() {
		ln.Close()
		select {
		case c := <-held:
			c.Close()
		case <-time.After(time.Second):
		}
	}()
	go func() {
		c, err := ln.Accept() // aceita e nunca responde
		if err == nil {
			held <- c
		}
	}()
	s := &redisStore{c: newRedisClient(ln.Addr().String(), 150*time.Millisecond)}
	start := time.Now()
	if _, err := s.CurrentServiceID(); err == nil {
		t.Fatal("esperado timeout")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("timeout nao respeitado: %v", d)
	}
}
