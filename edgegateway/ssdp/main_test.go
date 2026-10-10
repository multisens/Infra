package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Politica de falha do anunciante (decisao do Luis, 10/10), com rede falsa:
// erro de configuracao encerra (exit 1); falta de rede espera e tenta de
// novo, com um aviso ao entrar e outro ao sair.

type readMsg struct {
	msg  string
	from *net.UDPAddr
	err  error
}

type fakeConn struct {
	ch      choice
	mu      sync.Mutex
	sent    []string
	replies []string
	sendErr error
	onSend  func(n int) // depois do n-esimo envio bem-sucedido
	onReply func()
	in      chan readMsg
	closed  chan struct{}
	once    sync.Once
}

func (c *fakeConn) Send(msg []byte) error {
	c.mu.Lock()
	if c.sendErr != nil {
		err := c.sendErr
		c.mu.Unlock()
		return err
	}
	c.sent = append(c.sent, string(msg))
	n, hook := len(c.sent), c.onSend
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return nil
}

func (c *fakeConn) Reply(msg []byte, to *net.UDPAddr) error {
	c.mu.Lock()
	c.replies = append(c.replies, string(msg))
	hook := c.onReply
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (c *fakeConn) Read(buf []byte) (int, *net.UDPAddr, error) {
	select {
	case m := <-c.in:
		if m.err != nil {
			return 0, nil, m.err
		}
		return copy(buf, m.msg), m.from, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeConn) Close() { c.once.Do(func() { close(c.closed) }) }

func (c *fakeConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *fakeConn) setSendErr(err error) { c.mu.Lock(); c.sendErr = err; c.mu.Unlock() }

func (c *fakeConn) count(nts string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.sent {
		if strings.Contains(m, "NTS: "+nts+"\r\n") {
			n++
		}
	}
	return n
}

type fakeNet struct {
	h       *harness
	mu      sync.Mutex
	ifaces  []ifaceInfo
	route   string
	names   map[string]bool // existem na maquina (Lookup), com ou sem IPv4
	openErr []error         // erros dos proximos Open, um por chamada
	opened  []*fakeConn
}

func (f *fakeNet) set(route string, ifaces ...ifaceInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.route, f.ifaces = route, ifaces
	for _, i := range ifaces {
		f.names[i.Name] = true
	}
}

func (f *fakeNet) Interfaces() []ifaceInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ifaceInfo(nil), f.ifaces...)
}

func (f *fakeNet) DefaultRoute() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.route
}

func (f *fakeNet) Lookup(name string) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.names[name] || name == "lo", name == "lo"
}

func (f *fakeNet) Open(ch choice) (ssdpConn, error) {
	f.mu.Lock()
	if len(f.openErr) > 0 {
		err := f.openErr[0]
		f.openErr = f.openErr[1:]
		f.mu.Unlock()
		return nil, err
	}
	c := &fakeConn{ch: ch, in: make(chan readMsg, 4), closed: make(chan struct{})}
	f.opened = append(f.opened, c)
	n := len(f.opened)
	f.mu.Unlock()
	if f.h.onOpen != nil {
		f.h.onOpen(n, c)
	}
	return c, nil
}

func (f *fakeNet) conns() []*fakeConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeConn(nil), f.opened...)
}

type harness struct {
	t      *testing.T
	net    *fakeNet
	env    map[string]string
	stop   chan os.Signal
	mu     sync.Mutex
	logs   []string
	waits  []time.Duration
	clock  time.Time
	ticks  int                      // ticks prontos em cada sessao
	onWait func(n int)              // depois da n-esima espera
	onOpen func(n int, c *fakeConn) // n-esima conexao aberta
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, env: map[string]string{}, stop: make(chan os.Signal, 4),
		clock: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	h.net = &fakeNet{h: h, names: map[string]bool{}}
	return h
}

func (h *harness) sigterm() { h.stop <- syscall.SIGTERM }

// stopAfterFirstNotify: SIGTERM logo depois do primeiro par de NOTIFY da
// n-esima conexao.
func (h *harness) stopAfterFirstNotify(want int) {
	h.onOpen = func(n int, c *fakeConn) {
		if n == want {
			c.onSend = func(k int) {
				if k == 2 {
					h.sigterm()
				}
			}
		}
	}
}

func (h *harness) run() int {
	h.t.Helper()
	a := &announcer{
		net:    h.net,
		getenv: func(k string) string { return h.env[k] },
		logf: func(f string, args ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(f, args...))
			h.mu.Unlock()
		},
		now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.clock
		},
		after: func(d time.Duration) <-chan time.Time {
			h.mu.Lock()
			h.waits = append(h.waits, d)
			h.clock = h.clock.Add(d)
			n := len(h.waits)
			h.mu.Unlock()
			if h.onWait != nil {
				h.onWait(n)
			}
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return c
		},
		ticker: func(d time.Duration) (<-chan time.Time, func()) {
			if d != notifyInterval {
				h.t.Errorf("intervalo do NOTIFY %s, esperado %s", d, notifyInterval)
			}
			c := make(chan time.Time, h.ticks+1)
			for i := 0; i < h.ticks; i++ {
				c <- time.Time{}
			}
			return c, func() {}
		},
		stop:     h.stop,
		interval: notifyInterval,
	}
	done := make(chan int, 1)
	go func() { done <- a.run() }()
	select {
	case code := <-done:
		return code
	case <-time.After(5 * time.Second):
		h.t.Fatalf("o anunciante nao terminou; log:\n%s", h.log())
		return -1
	}
}

func (h *harness) log() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func (h *harness) count(sub string) int { return strings.Count(h.log(), sub) }

func (h *harness) wantWaits(want ...time.Duration) {
	h.t.Helper()
	h.mu.Lock()
	got := fmt.Sprint(h.waits)
	h.mu.Unlock()
	if got != fmt.Sprint(want) {
		h.t.Errorf("esperas %s, esperado %s", got, fmt.Sprint(want))
	}
}

func (h *harness) wantLog(sub string, n int) {
	h.t.Helper()
	if c := h.count(sub); c != n {
		h.t.Errorf("%q aparece %d vez(es) no log, esperado %d:\n%s", sub, c, n, h.log())
	}
}

func sockErr(op, call string, errno syscall.Errno) error {
	return &net.OpError{Op: op, Net: "udp4", Err: os.NewSyscallError(call, errno)}
}

var (
	wifi2 = ifaceInfo{"wlp2s0", []string{"192.168.2.7"}}
	wifi0 = ifaceInfo{"wlp2s0", []string{"192.168.0.23"}}
	dock  = ifaceInfo{"docker0", []string{"172.17.0.1"}}
)

const (
	semRede     = "AVISO sem rede para o anuncio"
	deVolta     = "rede de volta"
	falha       = "FALHA em"
	anunciando  = "anunciando " + ssdpST
	semByebye   = "saindo sem ssdp:byebye"
	enviaByebye = "enviando ssdp:byebye"
)

// Teste 44 de docs/ssdp-verificacao.md: o Wi-Fi ainda sem IPv4 na partida.
// A borda nao cai: o anunciante espera (1, 2, 4, 8 s), avisa uma vez e, com
// a rede de volta, anuncia (com o IP local da rede de agora).
func TestSemRedeNaPartidaEsperaEVolta(t *testing.T) {
	h := newHarness(t)
	h.net.names["wlp2s0"] = true // existe, ainda sem IPv4
	h.ticks = 3                  // SIGTERM pendente vence o tick: sem alive depois do byebye
	h.onWait = func(n int) {
		if n == 4 {
			h.net.set("wlp2s0", wifi2)
		}
	}
	h.stopAfterFirstNotify(1)
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d, esperado 0:\n%s", code, h.log())
	}
	h.wantWaits(1*time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
	h.wantLog(semRede, 1)
	h.wantLog("nenhuma interface de pe com IPv4 externo", 1)
	h.wantLog(deVolta+": anunciando de novo depois de 15s sem anunciar (4 tentativas sem rede)", 1)
	h.wantLog(anunciando+" em UDP 1900 pela interface wlp2s0 (192.168.2.7, via host-ip); "+
		"LOCATION http://192.168.2.7:44642/manifest (host via local-ip)", 1)
	h.wantLog(falha, 0)
	cs := h.net.conns()
	if len(cs) != 1 || cs[0].count(ssdpAlive) != 2 || cs[0].count(ssdpByebye) != 2 || !cs[0].isClosed() {
		t.Fatalf("conexoes: %d", len(cs))
	}
}

// Espera crescente ate 30 s, sem repetir o aviso; SIGTERM sem rede sai com
// 0 e sem byebye (nao ha socket).
func TestEsperaCrescenteAte30s(t *testing.T) {
	h := newHarness(t)
	h.env["SSDP_ADVERTISE_HOST"] = "192.168.2.7"
	h.onWait = func(n int) {
		if n == 8 {
			h.sigterm()
		}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	s := time.Second
	h.wantWaits(1*s, 2*s, 4*s, 8*s, 16*s, 30*s, 30*s, 30*s)
	h.wantLog(semRede, 1)
	h.wantLog(semByebye, 1)
	h.wantLog(falha, 0)
	if len(h.net.conns()) != 0 {
		t.Fatal("abriu socket sem rede")
	}
}

// Queda de rede no meio do anuncio (troca de Wi-Fi, docker network
// disconnect): fecha os sockets, espera e volta na rede nova, com a interface
// e o IP escolhidos de novo. O erro de envio vale como falta de rede pelo
// errno ou porque o IPv4 da sessao sumiu; com o IPv4 ainda la e errno fora da
// lista, e erro de configuracao.
func TestQuedaDeRedeDuranteOAnuncio(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		netGone bool
		fatal   bool
	}{
		{"ENETUNREACH", sockErr("write", "sendto", syscall.ENETUNREACH), true, false},
		{"EADDRNOTAVAIL com o IPv4 ainda la", sockErr("write", "sendto", syscall.EADDRNOTAVAIL), false, false},
		{"ENODEV", sockErr("write", "sendto", syscall.ENODEV), true, false},
		{"EINVAL com o IPv4 sumido", sockErr("write", "sendto", syscall.EINVAL), true, false},
		{"EPERM com o IPv4 ainda la", sockErr("write", "sendto", syscall.EPERM), false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.net.set("wlp2s0", wifi2)
			h.ticks = 1
			h.onOpen = func(n int, conn *fakeConn) {
				switch n {
				case 1: // depois do primeiro par, o envio passa a falhar
					conn.onSend = func(k int) {
						if k == 2 {
							conn.setSendErr(c.err)
							if c.netGone {
								h.net.set("")
							}
						}
					}
				case 2:
					conn.onSend = func(k int) {
						if k == 2 {
							h.sigterm()
						}
					}
				}
			}
			h.onWait = func(n int) {
				if n == 2 || (!c.netGone && n == 1) {
					h.net.set("wlp2s0", wifi0) // a rede nova
				}
			}
			code := h.run()
			cs := h.net.conns()
			if c.fatal {
				if code != 1 || h.count(falha+" envio do NOTIFY") != 1 || h.count(semRede) != 0 || len(cs) != 1 || !cs[0].isClosed() {
					t.Fatalf("esperado exit 1 sem espera: saida %d, %d conexoes:\n%s", code, len(cs), h.log())
				}
				return
			}
			if code != 0 || len(cs) != 2 {
				t.Fatalf("saida %d, %d conexoes:\n%s", code, len(cs), h.log())
			}
			if !cs[0].isClosed() || cs[1].ch.Addr != "192.168.0.23" || cs[1].ch.Name != "wlp2s0" {
				t.Fatalf("1a fechada=%v; 2a %+v", cs[0].isClosed(), cs[1].ch)
			}
			h.wantLog(semRede+" (envio do NOTIFY: ", 1)
			h.wantLog(deVolta, 1)
			h.wantLog("LOCATION http://192.168.2.7:44642/manifest", 1)
			h.wantLog("LOCATION http://192.168.0.23:44642/manifest (host via local-ip)", 1)
			h.wantLog(falha, 0)
			if c.netGone {
				h.wantWaits(1*time.Second, 2*time.Second)
			} else {
				h.wantWaits(1 * time.Second)
			}
		})
	}
}

// Erro de leitura do grupo: mesma classificacao do envio.
func TestErroDeLeitura(t *testing.T) {
	h := newHarness(t)
	h.net.set("wlp2s0", wifi2)
	h.onOpen = func(n int, conn *fakeConn) {
		switch n {
		case 1:
			conn.in <- readMsg{err: sockErr("read", "recvfrom", syscall.ENETDOWN)}
		case 2:
			conn.onSend = func(k int) {
				if k == 2 {
					h.sigterm()
				}
			}
		}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog(semRede+" (leitura do grupo SSDP: ", 1)
	h.wantLog(deVolta, 1)
	h.wantWaits(1 * time.Second)
}

// Erros de configuracao: exit 1 (a borda cai), sem esperar a rede.
func TestErrosDeConfiguracaoEncerram(t *testing.T) {
	inUse := sockErr("listen", "bind", syscall.EADDRINUSE)
	for _, c := range []struct {
		name    string
		env     map[string]string
		openErr []error
		noNet   bool // sem rede na 1a tentativa
		want    string
		opens   int
	}{
		{"porta invalida", map[string]string{"EDGE_HTTP_PORT": "abc"}, nil, false,
			falha + " configuracao do anuncio (EDGE_HTTP_PORT/EDGE_HTTPS_PORT): EDGE_HTTP_PORT='abc'", 0},
		{"SSDP_INTERFACE inexistente", map[string]string{"SSDP_INTERFACE": "eth9"}, nil, false,
			falha + " configuracao da interface do anuncio (SSDP_INTERFACE): SSDP_INTERFACE='eth9' nao existe", 0},
		{"SSDP_INTERFACE inexistente e sem rede", map[string]string{"SSDP_INTERFACE": "eth9"}, nil, true,
			"SSDP_INTERFACE='eth9' nao existe", 0},
		{"SSDP_INTERFACE de loopback", map[string]string{"SSDP_INTERFACE": "lo"}, nil, false, "loopback", 0},
		{"UDP 1900 presa sem SO_REUSEADDR", nil, []error{step("socket UDP 1900 (escuta do grupo)", inUse)}, false,
			falha + " socket UDP 1900 (escuta do grupo): listen udp4: bind: address already in use", 0},
		{"UDP 1900 presa, depois de uma espera sem rede", nil, []error{step("socket UDP 1900 (escuta do grupo)", inUse)}, true,
			"bind: address already in use", 0},
		{"outro erro de socket", nil, []error{step("socket de envio em 192.168.2.7", sockErr("listen", "bind", syscall.EACCES))}, false,
			falha + " socket de envio em 192.168.2.7", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			for k, v := range c.env {
				h.env[k] = v
			}
			h.net.openErr = c.openErr
			if !c.noNet {
				h.net.set("wlp2s0", wifi2)
			}
			h.onWait = func(n int) { h.net.set("wlp2s0", wifi2) }
			if code := h.run(); code != 1 {
				t.Fatalf("saida %d, esperado 1:\n%s", code, h.log())
			}
			h.wantLog(c.want, 1)
			h.wantLog(falha, 1)
			h.wantLog(anunciando, 0)
			if n := len(h.net.conns()); n != c.opens {
				t.Fatalf("%d conexoes abertas", n)
			}
		})
	}
}

// SSDP_INTERFACE existe mas sem IPv4: espera (nao e erro de configuracao) e
// anuncia por ela quando o IPv4 chega.
func TestInterfaceForcadaSemIPv4Espera(t *testing.T) {
	h := newHarness(t)
	h.env["SSDP_INTERFACE"] = "wlp2s0"
	h.env["SSDP_ADVERTISE_HOST"] = "192.168.2.7"
	h.net.names["wlp2s0"] = true
	h.net.set("docker0", dock)
	h.onWait = func(n int) { h.net.set("wlp2s0", dock, wifi2) }
	h.stopAfterFirstNotify(1)
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog(semRede+" (escolha da interface do anuncio: a interface SSDP_INTERFACE='wlp2s0' esta sem IPv4", 1)
	h.wantLog("pela interface wlp2s0 (192.168.2.7, via SSDP_INTERFACE)", 1)
	h.wantWaits(1 * time.Second)
}

// Erro de rede ao abrir os sockets (o IPv4 sumiu entre a escolha e o bind,
// ou o grupo nao entra na interface): espera e tenta de novo.
func TestErroDeRedeNaAbertura(t *testing.T) {
	h := newHarness(t)
	h.net.set("wlp2s0", wifi2)
	h.net.openErr = []error{
		step("socket de envio em 192.168.2.7", sockErr("listen", "bind", syscall.EADDRNOTAVAIL)),
		step("socket UDP 1900 (escuta do grupo)", sockErr("listen", "setsockopt", syscall.ENODEV)),
	}
	h.stopAfterFirstNotify(1)
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog(semRede+" (socket de envio em 192.168.2.7", 1)
	h.wantLog(deVolta+": anunciando de novo depois de 3s sem anunciar (2 tentativas sem rede)", 1)
	h.wantWaits(1*time.Second, 2*time.Second)
}

// Os avisos do anuncio (D9) saem uma vez; numa sessao nova so os que mudaram.
func TestAvisosNaoSeRepetem(t *testing.T) {
	h := newHarness(t)
	h.env["SSDP_ADVERTISE_HOST"] = "tv.local"
	h.net.set("wlp2s0", wifi2)
	h.ticks = 1
	h.onOpen = func(n int, conn *fakeConn) {
		conn.onSend = func(k int) {
			if k != 2 {
				return
			}
			switch n {
			case 1: // cai e volta na mesma rede: nenhum aviso novo
				conn.setSendErr(sockErr("write", "sendto", syscall.ENETUNREACH))
			case 2: // cai e volta numa rede nova: o aviso da rota padrao muda
				conn.setSendErr(sockErr("write", "sendto", syscall.ENETUNREACH))
				h.net.set("wlp2s0", wifi0)
			case 3:
				h.sigterm()
			}
		}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog(anunciando, 3)
	h.wantLog("SEM TLS", 1)
	h.wantLog("AVISO host anunciado: 'tv.local' nao e um IPv4 (nome ou IPv6); o anuncio sai pela interface da rota padrao wlp2s0 (192.168.2.7)", 1)
	h.wantLog("AVISO host anunciado: 'tv.local' nao e um IPv4 (nome ou IPv6); o anuncio sai pela interface da rota padrao wlp2s0 (192.168.0.23)", 1)
	h.wantLog(semRede, 2) // uma por queda
	h.wantLog(deVolta, 2)
}

// Busca respondida pela interface da sessao; SIGTERM manda o byebye.
func TestRespondeBuscaEByebye(t *testing.T) {
	h := newHarness(t)
	h.net.set("wlp2s0", wifi2)
	from := &net.UDPAddr{IP: net.ParseIP("192.168.2.5"), Port: 50000}
	h.onOpen = func(n int, conn *fakeConn) {
		conn.onReply = h.sigterm
		conn.in <- readMsg{msg: "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + ssdpST + "\r\n\r\n", from: from}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	c := h.net.conns()[0]
	c.mu.Lock()
	replies := append([]string(nil), c.replies...)
	c.mu.Unlock()
	if len(replies) != 1 || !strings.Contains(replies[0], "LOCATION: http://192.168.2.7:44642/manifest\r\n") ||
		!strings.Contains(replies[0], "DATE: Sat, 10 Oct 2026 12:00:00 UTC\r\n") {
		t.Fatalf("respostas: %q", replies)
	}
	h.wantLog("terminated: "+enviaByebye, 1)
	if c.count(ssdpByebye) != 2 || !c.isClosed() {
		t.Fatalf("byebye %d, fechada %v", c.count(ssdpByebye), c.isClosed())
	}
}

func TestIsNetDown(t *testing.T) {
	for _, e := range netErrnos {
		if !isNetDown(step("x", sockErr("write", "sendto", e))) {
			t.Errorf("%v deveria ser falta de rede", e)
		}
	}
	for _, e := range []syscall.Errno{syscall.EADDRINUSE, syscall.EACCES, syscall.EPERM, syscall.EINVAL} {
		if isNetDown(step("x", sockErr("listen", "bind", e))) {
			t.Errorf("%v nao deveria ser falta de rede", e)
		}
	}
	if !isNetDown(step("x", netDown{fmt.Errorf("y")})) {
		t.Error("netDown embrulhado")
	}
}
