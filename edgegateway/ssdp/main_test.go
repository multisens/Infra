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

// Failure policy of the announcer, with a fake network: a configuration
// error exits (exit 1); a lack of network waits and tries again, with one
// warning on entering and another on leaving.

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
	// After the n-th successful send.
	onSend  func(n int)
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
	h      *harness
	mu     sync.Mutex
	ifaces []ifaceInfo
	route  string
	// Exist on the machine (Lookup), with or without IPv4.
	names map[string]bool
	// Errors of the next Open calls, one per call.
	openErr []error
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
	t     *testing.T
	net   *fakeNet
	env   map[string]string
	stop  chan os.Signal
	mu    sync.Mutex
	logs  []string
	waits []time.Duration
	clock time.Time
	// Ticks ready in each session.
	ticks int
	// After the n-th wait.
	onWait func(n int)
	// The n-th connection opened.
	onOpen func(n int, c *fakeConn)
}

// The harness sets SSDP_ADVERTISE_HOST to the IPv4 of the test Wi-Fi
// (wifi2); a test that wants another one replaces or removes it.
func newHarness(t *testing.T) *harness {
	h := &harness{t: t, env: map[string]string{"SSDP_ADVERTISE_HOST": "192.168.2.7"}, stop: make(chan os.Signal, 4),
		clock: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	h.net = &fakeNet{h: h, names: map[string]bool{}}
	return h
}

func (h *harness) sigterm() { h.stop <- syscall.SIGTERM }

// stopAfterFirstNotify: SIGTERM right after the first pair of NOTIFY of the
// n-th connection.
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

// Wi-Fi still without IPv4 at startup: the announcer waits (1, 2, 4, 8 s),
// warns once and, with the network back, announces on the interface that has
// the IPv4 of SSDP_ADVERTISE_HOST.
func TestSemRedeNaPartidaEsperaEVolta(t *testing.T) {
	h := newHarness(t)
	// Exists, still without IPv4.
	h.net.names["wlp2s0"] = true
	// A pending SIGTERM wins over the tick: no alive after the byebye.
	h.ticks = 3
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
		"LOCATION http://192.168.2.7:44642/manifest (host de SSDP_ADVERTISE_HOST)", 1)
	h.wantLog(falha, 0)
	cs := h.net.conns()
	if len(cs) != 1 || cs[0].count(ssdpAlive) != 2 || cs[0].count(ssdpByebye) != 2 || !cs[0].isClosed() {
		t.Fatalf("conexoes: %d", len(cs))
	}
}

// Increasing wait up to 30 s, without repeating the warning; SIGTERM without
// network exits with 0 and without byebye (there is no socket).
func TestEsperaCrescenteAte30s(t *testing.T) {
	h := newHarness(t)
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

// Network loss while announcing (Wi-Fi change, docker network disconnect):
// closes the sockets, waits and comes back on the new network, with the
// interface and source IPv4 chosen again. LOCATION does not change: the host
// is SSDP_ADVERTISE_HOST, and the warning says the configured IPv4 is on no
// interface. A send error is a lack of network by the errno or because the
// IPv4 of the session is gone; with the IPv4 still there and an errno
// outside the list, it is a configuration error.
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
				// After the first pair, sending fails.
				case 1:
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
					// The new network.
					h.net.set("wlp2s0", wifi0)
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
			h.wantLog("pela interface wlp2s0 (192.168.2.7, via host-ip); LOCATION http://192.168.2.7:44642/manifest", 1)
			h.wantLog("pela interface wlp2s0 (192.168.0.23, via default-route); LOCATION http://192.168.2.7:44642/manifest", 1)
			h.wantLog("AVISO host anunciado: o IPv4 192.168.2.7 nao esta em nenhuma interface externa desta maquina; "+
				"o anuncio sai pela interface da rota padrao wlp2s0 (192.168.0.23)", 1)
			h.wantLog("192.168.0.23:44642/manifest", 0)
			h.wantLog(falha, 0)
			if c.netGone {
				h.wantWaits(1*time.Second, 2*time.Second)
			} else {
				h.wantWaits(1 * time.Second)
			}
		})
	}
}

// A read error on the group: the same classification as sending.
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

// Configuration errors: exit 1 (the edge goes down), without waiting for the
// network.
func TestErrosDeConfiguracaoEncerram(t *testing.T) {
	inUse := sockErr("listen", "bind", syscall.EADDRINUSE)
	const cfg = falha + " configuracao do anuncio (SSDP_ADVERTISE_HOST, EDGE_HTTP_PORT, EDGE_HTTPS_PORT): "
	for _, c := range []struct {
		name string
		// "-" removes the variable.
		env     map[string]string
		openErr []error
		// No network on the first attempt.
		noNet bool
		want  string
		opens int
	}{
		// Absent or empty means a detected host (TestHostDetectado).
		{"SSDP_ADVERTISE_HOST sem host", map[string]string{"SSDP_ADVERTISE_HOST": "http://:44642"}, nil, false,
			cfg + "SSDP_ADVERTISE_HOST=\"http://:44642\" sem host", 0},
		{"porta invalida", map[string]string{"EDGE_HTTP_PORT": "abc"}, nil, false,
			cfg + "EDGE_HTTP_PORT='abc'", 0},
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
				if v == "-" {
					delete(h.env, k)
				} else {
					h.env[k] = v
				}
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

// SSDP_INTERFACE exists but without IPv4: waits (not a configuration error)
// and announces on it when the IPv4 arrives.
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

// A network error opening the sockets (the IPv4 gone between the choice and
// the bind, or the group does not join the interface): waits and tries
// again.
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

// The warnings of the announcement are logged once; in a new session, only
// those that changed.
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
			// Down and back on the same network: no new warning.
			case 1:
				conn.setSendErr(sockErr("write", "sendto", syscall.ENETUNREACH))
			// Down and back on a new network: the default route warning
			// changes.
			case 2:
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
	// 44643 has TLS: no warning of a port without TLS.
	h.wantLog("SEM TLS", 0)
	h.wantLog("AVISO host anunciado: 'tv.local' nao e um IPv4 (nome ou IPv6); o anuncio sai pela interface da rota padrao wlp2s0 (192.168.2.7)", 1)
	h.wantLog("AVISO host anunciado: 'tv.local' nao e um IPv4 (nome ou IPv6); o anuncio sai pela interface da rota padrao wlp2s0 (192.168.0.23)", 1)
	// One per loss.
	h.wantLog(semRede, 2)
	h.wantLog(deVolta, 2)
}

// A search answered on the interface of the session; SIGTERM sends the
// byebye.
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

// Without SSDP_ADVERTISE_HOST, the host of LOCATION is the IPv4 of the
// interface of the default route, without a warning.
func TestHostDetectado(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "SSDP_ADVERTISE_HOST")
	h.net.set("wlp2s0", wifi2, dock)
	h.stopAfterFirstNotify(1)
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	c := h.net.conns()[0]
	c.mu.Lock()
	sent := append([]string(nil), c.sent...)
	c.mu.Unlock()
	if len(sent) == 0 || !strings.Contains(sent[0], "LOCATION: http://192.168.2.7:44642/manifest\r\n") {
		t.Fatalf("NOTIFY: %q", sent)
	}
	h.wantLog("LOCATION http://192.168.2.7:44642/manifest (host detectado: IPv4 da interface)", 1)
	h.wantLog("AVISO host anunciado", 0)
	h.wantLog(falha, 0)
}

// With the detected host, a network change while announcing is noticed on
// the next NOTIFY: byebye of the old address, a warning in the log, without
// going through the "no network" state, and a new announcement with the new
// IP.
func TestTrocaDeRedeComHostDetectado(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "SSDP_ADVERTISE_HOST")
	h.ticks = 1
	h.net.set("wlp2s0", wifi2)
	h.onOpen = func(n int, c *fakeConn) {
		switch n {
		case 1:
			c.onSend = func(k int) {
				if k == 2 {
					// Another Wi-Fi: the same adapter, another IPv4.
					h.net.set("wlp2s0", wifi0)
				}
			}
		case 2:
			c.onSend = func(k int) {
				if k == 2 {
					h.sigterm()
				}
			}
		}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog("AVISO a rede mudou (wlp2s0 192.168.2.7 -> wlp2s0 192.168.0.23): anunciando de novo com o endereco novo", 1)
	h.wantLog(semRede, 0)
	h.wantLog(anunciando, 2)
	h.wantLog("LOCATION http://192.168.2.7:44642/manifest (host detectado", 1)
	h.wantLog("LOCATION http://192.168.0.23:44642/manifest (host detectado", 1)
	cs := h.net.conns()
	if len(cs) != 2 || cs[0].count(ssdpByebye) != 2 || !cs[0].isClosed() || cs[1].ch.Addr != "192.168.0.23" {
		t.Fatalf("conexoes %d; byebye da antiga %d", len(cs), cs[0].count(ssdpByebye))
	}
	// With SSDP_ADVERTISE_HOST set, an IPv4 change does not end the session
	// (the host is fixed): TestQuedaDeRedeDuranteOAnuncio covers the failure
	// path.
}

// Stop file (SSDP_STOP_FILE): when it is created, the announcer sends the
// byebye as on SIGTERM.
func TestArquivoDeParada(t *testing.T) {
	sig := make(chan os.Signal, 1)
	n := 0
	go watchStopFile("/tmp/tv30-parar", time.Millisecond, sig, func(string) bool { n++; return n >= 3 })
	select {
	case s := <-sig:
		if s.String() != "arquivo de parada /tmp/tv30-parar" {
			t.Fatalf("sinal %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o arquivo de parada nao foi percebido")
	}

	h := newHarness(t)
	h.net.set("wlp2s0", wifi2)
	h.onOpen = func(n int, c *fakeConn) {
		c.onSend = func(k int) {
			if k == 2 {
				h.stop <- stopFile("/tmp/tv30-parar")
			}
		}
	}
	if code := h.run(); code != 0 {
		t.Fatalf("saida %d:\n%s", code, h.log())
	}
	h.wantLog("arquivo de parada /tmp/tv30-parar: "+enviaByebye, 1)
	if c := h.net.conns()[0]; c.count(ssdpByebye) != 2 {
		t.Fatalf("byebye %d", c.count(ssdpByebye))
	}
}
