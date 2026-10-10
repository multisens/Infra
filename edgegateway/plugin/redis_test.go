package main

import (
	"bufio"
	"errors"
	"net"
	"sort"
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
	cmds    map[string]int // comandos recebidos, por nome
	// dropNext: le o proximo comando, NAO o executa e fecha a conexao sem
	// responder (resposta perdida)
	dropNext bool
}

func startFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{ln: ln, strs: map[string]string{}, sets: map[string]map[string]bool{},
		hashes: map[string]map[string]string{}, lists: map[string][]string{}, cmds: map[string]int{}}
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
		f.mu.Lock()
		drop := f.dropNext
		f.dropNext = false
		if len(args) > 0 {
			f.cmds[strings.ToUpper(args[0])]++
		}
		f.mu.Unlock()
		if drop {
			c.Close()
			return
		}
		c.Write([]byte(f.exec(args)))
	}
}

// keysMatching: chaves de qualquer tipo com o prefixo do padrao "<prefixo>*".
func (f *fakeRedis) keysMatching(pattern string) []string {
	prefix := strings.TrimSuffix(pattern, "*")
	var out []string
	add := func(k string) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	for k := range f.strs {
		add(k)
	}
	for k := range f.lists {
		add(k)
	}
	for k := range f.hashes {
		add(k)
	}
	for k := range f.sets {
		add(k)
	}
	sort.Strings(out)
	return out
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
		if _, isStr := f.strs[a[1]]; isStr {
			return "-WRONGTYPE Operation against a key holding the wrong kind of value\r\n"
		}
		l := f.lists[a[1]]
		out := "*" + strconv.Itoa(len(l)) + "\r\n"
		for _, e := range l {
			out += bulk(e)
		}
		return out
	case "RPUSH":
		f.lists[a[1]] = append(f.lists[a[1]], a[2:]...)
		return ":" + strconv.Itoa(len(f.lists[a[1]])) + "\r\n"
	case "LREM": // so count 0 (todas as ocorrencias)
		var kept []string
		n := 0
		for _, e := range f.lists[a[1]] {
			if e == a[3] {
				n++
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(f.lists, a[1]) // como no Redis: lista vazia some
		} else {
			f.lists[a[1]] = kept
		}
		return ":" + strconv.Itoa(n) + "\r\n"
	case "HGETALL":
		h := f.hashes[a[1]]
		fields := make([]string, 0, len(h))
		for k := range h {
			fields = append(fields, k)
		}
		sort.Strings(fields)
		out := "*" + strconv.Itoa(2*len(fields)) + "\r\n"
		for _, k := range fields {
			out += bulk(k) + bulk(h[k])
		}
		return out
	case "SCAN": // SCAN cursor MATCH padrao COUNT n — UMA chave por pagina
		all := f.keysMatching(a[3])
		cur, _ := strconv.Atoi(a[1])
		if cur >= len(all) {
			return "*2\r\n" + bulk("0") + "*0\r\n"
		}
		next := cur + 1
		if next >= len(all) {
			next = 0
		}
		return "*2\r\n" + bulk(strconv.Itoa(next)) + "*1\r\n" + bulk(all[cur])
	}
	return "-ERR unknown command\r\n"
}

// Comandos da C.6.8 respondida pela borda: RPUSH, LREM, SCAN (varias
// paginas), HGETALL; LRANGE de chave de outro tipo eh erro do servidor.
func TestRedisComandosC68(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() {
		f.lists[keyBindPrefix+"urn:b"] = []string{"x"}
		f.strs[keyBindPrefix+"urn:texto"] = "nao eh lista"
		f.strs["outra-chave"] = "fora do padrao"
		f.hashes[keyCurrentSvcHash] = map[string]string{"serviceName": "Canal A", "serviceId": "7"}
	})
	s := &redisStore{c: newRedisClient(f.ln.Addr().String(), time.Second)}

	if err := s.AddBindKey("urn:a", "e1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBindKey("urn:a", "e2"); err != nil {
		t.Fatal(err)
	}
	s.AddBindKey("urn:a", "e1")
	if l, err := s.BindKeys("urn:a"); err != nil || strings.Join(l, ",") != "e1,e2,e1" {
		t.Fatalf("RPUSH/LRANGE: %q %v", l, err)
	}
	if n, err := s.RemoveBindKey("urn:a", "e1"); err != nil || n != 2 {
		t.Fatalf("LREM: %d %v", n, err)
	}
	if n, err := s.RemoveBindKey("urn:a", "nao-tem"); err != nil || n != 0 {
		t.Fatalf("LREM ausente: %d %v", n, err)
	}
	sids, err := s.BindServices()
	if err != nil || strings.Join(sids, ",") != "urn:a,urn:b,urn:texto" {
		t.Fatalf("SCAN: %q %v", sids, err)
	}
	f.with(func() {
		if f.cmds["SCAN"] < 3 {
			t.Errorf("SCAN deveria ter percorrido o cursor (chamadas=%d)", f.cmds["SCAN"])
		}
	})
	var re redisError
	if _, err := s.BindKeys("urn:texto"); !errors.As(err, &re) {
		t.Fatalf("LRANGE de STRING: esperado erro do servidor, veio %v", err)
	}
	m, err := s.CurrentService()
	if err != nil || m["serviceName"] != "Canal A" || m["serviceId"] != "7" || len(m) != 2 {
		t.Fatalf("HGETALL: %v %v", m, err)
	}
	f.with(func() { delete(f.hashes, keyCurrentSvcHash) })
	if m, err := s.CurrentService(); err != nil || len(m) != 0 {
		t.Fatalf("HGETALL ausente: %v %v", m, err)
	}
	// SCAN sem nenhuma chave
	f.with(func() { f.lists = map[string][]string{}; f.strs = map[string]string{} })
	if sids, err := s.BindServices(); err != nil || len(sids) != 0 {
		t.Fatalf("SCAN vazio: %q %v", sids, err)
	}
}

// Resposta perdida depois de o comando sair: RPUSH NAO eh repetido (poderia
// duplicar a entrada); leitura numa conexao do pool que morreu eh repetida
// (TestRedisReconecta).
func TestRedisRPUSHNaoRepete(t *testing.T) {
	f := startFakeRedis(t)
	f.with(func() { f.strs[keyCurrentService] = tSvc })
	s := &redisStore{c: newRedisClient(f.ln.Addr().String(), time.Second)}
	if _, err := s.CurrentServiceID(); err != nil { // deixa uma conexao no pool
		t.Fatal(err)
	}
	f.with(func() { f.dropNext = true })
	if err := s.AddBindKey("urn:a", "e1"); err == nil {
		t.Fatal("esperado erro com a resposta perdida")
	}
	f.with(func() {
		if f.cmds["RPUSH"] != 1 {
			t.Errorf("RPUSH enviado %d vezes, esperado 1", f.cmds["RPUSH"])
		}
	})
	// o cliente segue usavel
	if err := s.AddBindKey("urn:a", "e2"); err != nil {
		t.Fatalf("depois da falha: %v", err)
	}
	if l, _ := s.BindKeys("urn:a"); strings.Join(l, ",") != "e2" {
		t.Fatalf("lista %q", l)
	}
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
