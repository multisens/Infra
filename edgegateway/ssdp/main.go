// SSDP announcer of the testbed (C.3.4). Runs natively on the host, outside
// Docker. It only announces where the /manifest is and answers searches.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	notifyInterval = 10 * time.Second
	multicastTTL   = 4
	retryMin       = 1 * time.Second
	retryMax       = 30 * time.Second
)

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[ssdp] "+format+"\n", a...)
}

// netDown marks a lack of network: wait and try again, without exiting.
type netDown struct{ err error }

func (e netDown) Error() string { return e.err.Error() }
func (e netDown) Unwrap() error { return e.err }

// netErrnos and errAddrInUse depend on the system: sockopt_other.go and
// sockopt_windows.go.

func isNetDown(err error) bool {
	var nd netDown
	if errors.As(err, &nd) {
		return true
	}
	for _, e := range netErrnos {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// stepError: the step that failed, for the log ("FALHA em <etapa>: ...").
type stepError struct {
	what string
	err  error
}

func (e *stepError) Error() string { return e.what + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func step(what string, err error) error { return &stepError{what, err} }

// network is injectable, so the tests can use a fake network.
type network interface {
	// Up, not loopback, with IPv4.
	Interfaces() []ifaceInfo
	// Whether the interface exists on the machine.
	Lookup(name string) (exists, loopback bool)
	DefaultRoute() string
	Open(ch choice) (ssdpConn, error)
}

// ssdpConn listens to the group on the chosen interface and sends with its
// IPv4 as the source.
type ssdpConn interface {
	// Multicast to the group.
	Send(msg []byte) error
	Reply(msg []byte, to *net.UDPAddr) error
	Read(buf []byte) (int, *net.UDPAddr, error)
	Close()
}

type realNet struct{}

func (realNet) Interfaces() []ifaceInfo { return systemInterfaces() }
func (realNet) DefaultRoute() string    { return readDefaultRoute() }

func (realNet) Lookup(name string) (bool, bool) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return false, false
	}
	return true, ifi.Flags&net.FlagLoopback != 0
}

func (realNet) Open(ch choice) (ssdpConn, error) {
	// The interface existed when chosen; its absence now is a lack of network.
	ifi, err := net.InterfaceByName(ch.Name)
	if err != nil {
		return nil, step("interface "+ch.Name, netDown{err})
	}
	group := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}
	// Listening: UDP 1900 (SO_REUSEADDR), joining the group only on the chosen
	// interface. EADDRINUSE here means 1900 held without SO_REUSEADDR.
	lc, err := net.ListenMulticastUDP("udp4", ifi, group)
	if err != nil {
		return nil, step("socket UDP 1900 (escuta do grupo)", err)
	}
	// Sending: a socket of its own on the IPv4 of the interface (fixed
	// source), multicast on the same interface.
	snd, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ch.Addr)})
	if err != nil {
		lc.Close()
		return nil, step("socket de envio em "+ch.Addr, err)
	}
	if err := setMulticastOut(snd, ch.Addr, multicastTTL); err != nil {
		lc.Close()
		snd.Close()
		return nil, step("interface de saida do multicast ("+ch.Addr+")", err)
	}
	return &udpConn{lc, snd, group}, nil
}

type udpConn struct {
	lc, snd *net.UDPConn
	group   *net.UDPAddr
}

func (c *udpConn) Send(msg []byte) error { _, err := c.snd.WriteToUDP(msg, c.group); return err }
func (c *udpConn) Reply(msg []byte, to *net.UDPAddr) error {
	_, err := c.snd.WriteToUDP(msg, to)
	return err
}
func (c *udpConn) Read(buf []byte) (int, *net.UDPAddr, error) { return c.lc.ReadFromUDP(buf) }
func (c *udpConn) Close()                                     { c.lc.Close(); c.snd.Close() }

// setMulticastOut sends the NOTIFY through the chosen interface, not the
// default route, with a fixed TTL.
func setMulticastOut(conn *net.UDPConn, addr string, ttl int) error {
	ip := net.ParseIP(addr).To4()
	if ip == nil {
		return fmt.Errorf("endereco de origem '%s' nao e IPv4", addr)
	}
	var a [4]byte
	copy(a[:], ip)
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		// Per system: sockopt_*.go.
		serr = setMulticastOpts(fd, a, ttl)
	}); err != nil {
		return err
	}
	return serr
}

type announcer struct {
	net    network
	getenv func(string) string
	logf   func(format string, a ...any)
	now    func() time.Time
	// Wait between attempts.
	after func(time.Duration) <-chan time.Time
	// Periodic NOTIFY.
	ticker   func(time.Duration) (<-chan time.Time, func())
	stop     <-chan os.Signal
	interval time.Duration
}

// session is a pair of sockets open on an interface, until the network goes
// down.
type session struct {
	ch choice
	// With the host of this session (the detected one, without
	// SSDP_ADVERTISE_HOST).
	ep endpoint
	// Detected host: a network change ends the session.
	auto    bool
	forced  string
	udn     string
	conn    ssdpConn
	readErr chan error
}

// netChanged: the interface or the IPv4 of the default route changed while
// announcing with the detected host. Not a lack of network: the loop
// announces again at once.
type netChanged struct{ from, to choice }

func (e netChanged) Error() string {
	return fmt.Sprintf("a rede mudou (%s %s -> %s %s)", e.from.Name, e.from.Addr, e.to.Name, e.to.Addr)
}

func (s *session) sendAll(alive bool) error {
	for _, u := range usns(s.udn) {
		if err := s.conn.Send([]byte(notify(u, s.ep.location(), alive))); err != nil {
			return err
		}
	}
	return nil
}

// read answers searches until the socket closes (end of session) or fails.
func (a *announcer) read(s *session) {
	buf := make([]byte, 4096)
	for {
		n, src, err := s.conn.Read(buf)
		if err != nil {
			// A socket closed by the session is a normal end.
			if !errors.Is(err, net.ErrClosed) {
				s.readErr <- err
			}
			return
		}
		st := parseMSearch(string(buf[:n]))
		if st == "" {
			continue
		}
		for _, r := range answers(st, s.udn, s.ep.location(), a.now()) {
			// A bad address of a client only gives a warning.
			if err := s.conn.Reply([]byte(r), src); err != nil && !errors.Is(err, net.ErrClosed) {
				a.logf("AVISO resposta a %s nao enviada: %v", src, err)
			}
		}
	}
}

// start makes one attempt: chooses the interface and source IPv4 with the
// current network, opens the sockets and sends the first NOTIFY.
func (a *announcer) start(ep endpoint, forced, udn string) (*session, []string, error) {
	ch, err := chooseInterface(ep.Host, forced, a.net.Interfaces(), a.net.DefaultRoute)
	if err != nil {
		return nil, nil, step("escolha da interface do anuncio", err)
	}
	conn, err := a.net.Open(ch)
	if err != nil {
		return nil, nil, a.classify(ch, err)
	}
	auto := ep.Host == ""
	if auto {
		// Detected host: the IPv4 of the interface.
		ep.Host = ch.Addr
	}
	s := &session{ch: ch, ep: ep, auto: auto, forced: forced, udn: udn, conn: conn, readErr: make(chan error, 1)}
	go a.read(s)
	if err := s.sendAll(true); err != nil {
		err = a.classify(ch, step("envio do NOTIFY", err))
		conn.Close()
		return nil, nil, err
	}
	return s, append(warnings(ep), ch.Warnings...), nil
}

// classify: a socket error is a lack of network when the errno says so or
// when the chosen interface or IPv4 no longer exists, whatever the errno.
// EADDRINUSE is always a configuration error (1900 held without
// SO_REUSEADDR).
func (a *announcer) classify(ch choice, err error) error {
	if isNetDown(err) || errors.Is(err, errAddrInUse) {
		return err
	}
	if ifi := owner(a.net.Interfaces(), ch.Addr); ifi == nil || ifi.Name != ch.Name {
		return netDown{fmt.Errorf("%w (o IPv4 %s saiu da interface %s)", err, ch.Addr, ch.Name)}
	}
	return err
}

func (a *announcer) stopped() (os.Signal, bool) {
	select {
	case sig := <-a.stop:
		return sig, true
	default:
		return nil, false
	}
}

// serve announces until a signal (stop=true) or an error.
func (a *announcer) serve(s *session) (stop bool, err error) {
	tick, stopTick := a.ticker(a.interval)
	defer stopTick()
	for {
		// A pending signal has priority over the tick, so that no ssdp:alive
		// follows the byebye.
		sig, ok := a.stopped()
		if !ok {
			select {
			case <-tick:
				if err := a.checkNetwork(s); err != nil {
					return false, err
				}
				if err := s.sendAll(true); err != nil {
					return false, a.classify(s.ch, step("envio do NOTIFY", err))
				}
				continue
			case err := <-s.readErr:
				return false, a.classify(s.ch, step("leitura do grupo SSDP", err))
			case sig = <-a.stop:
			}
		}
		a.logf("%s: enviando ssdp:byebye", sig)
		if err := s.sendAll(false); err != nil {
			a.logf("byebye nao enviado: %v", err)
		}
		s.conn.Close()
		return true, nil
	}
}

// checkNetwork: with the detected host, the current default route must be
// the one of the session. Changed: byebye (best effort, the old IPv4 may be
// gone) and netChanged; gone: lack of network.
func (a *announcer) checkNetwork(s *session) error {
	if !s.auto {
		return nil
	}
	ch, err := chooseInterface("", s.forced, a.net.Interfaces(), a.net.DefaultRoute)
	if err != nil {
		return step("escolha da interface do anuncio", err)
	}
	if ch.Name == s.ch.Name && ch.Addr == s.ch.Addr {
		return nil
	}
	if err := s.sendAll(false); err != nil {
		a.logf("byebye do endereco antigo nao enviado: %v", err)
	}
	return netChanged{s.ch, ch}
}

func (a *announcer) fatal(err error) int {
	a.logf("FALHA em %v — encerrando (erro de configuracao, nao de rede)", err)
	return 1
}

// run returns the exit code: 0 on a signal, 1 on a configuration error. A
// lack of network does not exit: it closes the sockets, warns on entering
// and leaving that state and tries again with an increasing wait, choosing
// the interface again.
func (a *announcer) run() int {
	// Configuration, checked once before anything else.
	ep, err := resolveEndpoint(a.getenv)
	if err != nil {
		return a.fatal(step("configuracao do anuncio (SSDP_ADVERTISE_HOST, EDGE_HTTP_PORT, EDGE_HTTPS_PORT)", err))
	}
	forced := strings.TrimSpace(a.getenv("SSDP_INTERFACE"))
	if err := checkForced(forced, a.net.Lookup); err != nil {
		return a.fatal(step("configuracao da interface do anuncio (SSDP_INTERFACE)", err))
	}
	udn := a.getenv("UDN")
	if udn == "" {
		udn = defaultUDN
	}

	wait := retryMin
	// Zero while announcing, or before the first attempt.
	var downSince time.Time
	tries := 0
	// Warnings of the previous session, not repeated.
	shown := map[string]bool{}
	for {
		s, warns, err := a.start(ep, forced, udn)
		if err == nil {
			if !downSince.IsZero() {
				a.logf("rede de volta: anunciando de novo depois de %s sem anunciar (%d tentativas sem rede)",
					a.now().Sub(downSince).Round(time.Second), tries)
			}
			downSince, tries, wait = time.Time{}, 0, retryMin
			now := map[string]bool{}
			for _, w := range warns {
				if !shown[w] {
					a.logf("AVISO %s", w)
				}
				now[w] = true
			}
			shown = now
			origem := "host de SSDP_ADVERTISE_HOST"
			if s.auto {
				origem = "host detectado: IPv4 da interface"
			}
			a.logf("anunciando %s em UDP 1900 pela interface %s (%s, via %s); LOCATION %s (%s)",
				ssdpST, s.ch.Name, s.ch.Addr, s.ch.Source, s.ep.location(), origem)
			stop, serr := a.serve(s)
			if stop {
				return 0
			}
			s.conn.Close()
			err = serr
			var nc netChanged
			if errors.As(err, &nc) {
				a.logf("AVISO %v: anunciando de novo com o endereco novo", nc)
				continue
			}
		}
		if !isNetDown(err) {
			return a.fatal(err)
		}
		if downSince.IsZero() {
			downSince = a.now()
			a.logf("AVISO sem rede para o anuncio (%v): sockets fechados, nada e anunciado nem respondido; "+
				"as APIs seguem de pe. Nova tentativa em %s, com espera crescente ate %s, escolhendo "+
				"de novo a interface e o IP; o proximo aviso sai quando a rede voltar.", err, wait, retryMax)
		}
		tries++
		// The wait is created before looking at the signal, so that a signal
		// that arrives with it is not lost in the select.
		c := a.after(wait)
		sig, ok := a.stopped()
		if !ok {
			select {
			case <-c:
			case sig = <-a.stop:
				ok = true
			}
		}
		if ok {
			a.logf("%s: sem rede, nada anunciado — saindo sem ssdp:byebye", sig)
			return 0
		}
		if wait *= 2; wait > retryMax {
			wait = retryMax
		}
	}
}

// stopFile: the "signal" of the stop file (SSDP_STOP_FILE).
type stopFile string

func (f stopFile) String() string { return "arquivo de parada " + string(f) }
func (stopFile) Signal()          {}

func watchStopFile(path string, every time.Duration, sig chan<- os.Signal, exists func(string) bool) {
	for {
		if exists(path) {
			sig <- stopFile(path)
			return
		}
		time.Sleep(every)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func main() {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	if f := os.Getenv("SSDP_STOP_FILE"); f != "" {
		go watchStopFile(f, 500*time.Millisecond, sig, fileExists)
	}
	a := &announcer{
		net:    realNet{},
		getenv: os.Getenv,
		logf:   logf,
		now:    time.Now,
		after:  time.After,
		ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		stop:     sig,
		interval: notifyInterval,
	}
	os.Exit(a.run())
}
