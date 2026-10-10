// Anunciante SSDP da borda (C.3.4).
//
// L6 decidida = opcao A (Luis, 09/10): quem anuncia e a borda. Roda como
// processo do container edgegateway, ao lado dos dois KrakenD, quando a borda
// esta em rede do host (docker-compose.ssdp.yml, so Linux nativo — decisao
// do Joel, 04/10) e SSDP_ENABLED=true. Na bridge o multicast nao sai para a
// LAN (docs/ssdp-verificacao.md), entao ali o anuncio fica desligado.
//
// Politica de falha (decisao do Luis, 10/10, revendo o morre-inteiro de
// 09/10): "derrubar a borda so em erro de configuracao e tolerar a falta de
// rede com novas tentativas". Motivo: numa troca de rede, a borda reiniciou
// 12 vezes, com todas as APIs fora, so porque o Wi-Fi ainda nao tinha IPv4
// (testes 44-45 de docs/ssdp-verificacao.md).
//   - Erro de configuracao: encerra com exit 1, e o entrypoint derruba a
//     borda inteira (morre-inteiro; o restart do compose a traz de volta).
//     Porta invalida em EDGE_HTTP_PORT/EDGE_HTTPS_PORT, SSDP_INTERFACE com
//     nome que nao existe na maquina (ou de loopback), UDP 1900 presa por
//     socket sem SO_REUSEADDR (EADDRINUSE no bind) e todo erro que nao seja
//     de rede.
//   - Falta de rede: o processo fica de pe. Nenhuma interface com IPv4, sem
//     rota padrao, a interface de SSDP_INTERFACE sem IPv4, erro de rede ao
//     abrir os sockets ou ao enviar (netErrnos, ou a interface/IPv4 escolhidos
//     sumiram). Fecha os sockets (nao anuncia nem responde a buscas), avisa
//     UMA vez ao entrar no estado e outra ao sair, e tenta de novo com espera
//     crescente (retryMin ate retryMax), escolhendo de novo a interface e o
//     IP a cada tentativa.
//
// O LOCATION aponta para o /manifest, que continua no tv3ws, atras da propria
// borda (44642), com o host da cadeia de config.go (B2 segue com o Joel). O
// anunciante Node do tv3ws (dev-host, tv3ws/src/ssdp-server.ts) nao mudou: la,
// qualquer falha do anuncio ainda encerra o processo.
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

// --- classificacao das falhas ---

// netDown marca a falta de rede: espera e tenta de novo, sem derrubar a borda.
type netDown struct{ err error }

func (e netDown) Error() string { return e.err.Error() }
func (e netDown) Unwrap() error { return e.err }

// netErrnos: erros do sistema que sao falta de rede (endereco ou interface
// que sumiu, sem rota). Lista fechada: erro fora dela e de configuracao.
var netErrnos = []syscall.Errno{
	syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN,
	syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL, syscall.ENODEV,
}

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

// stepError: a etapa que falhou, para o log ("FALHA em <etapa>: ...").
type stepError struct {
	what string
	err  error
}

func (e *stepError) Error() string { return e.what + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func step(what string, err error) error { return &stepError{what, err} }

// --- rede (injetavel: os testes usam uma rede falsa, main_test.go) ---

type network interface {
	Interfaces() []ifaceInfo                    // de pe, fora loopback, com IPv4
	Lookup(name string) (exists, loopback bool) // a interface existe na maquina?
	DefaultRoute() string
	Open(ch choice) (ssdpConn, error)
}

// ssdpConn: escuta do grupo pela interface escolhida + envio com origem no
// IPv4 dela.
type ssdpConn interface {
	Send(msg []byte) error // multicast para o grupo
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
	// a interface existia na escolha; sumir agora e falta de rede
	ifi, err := net.InterfaceByName(ch.Name)
	if err != nil {
		return nil, step("interface "+ch.Name, netDown{err})
	}
	group := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}
	// Escuta: UDP 1900 (SO_REUSEADDR), entrando no grupo so pela interface
	// escolhida. EADDRINUSE aqui = 1900 presa sem SO_REUSEADDR.
	lc, err := net.ListenMulticastUDP("udp4", ifi, group)
	if err != nil {
		return nil, step("socket UDP 1900 (escuta do grupo)", err)
	}
	// Envio: socket proprio no IPv4 da interface (origem fixa), multicast
	// pela mesma interface.
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

// setMulticastOut: o NOTIFY sai pela interface escolhida (e nao pela rota
// padrao) e com TTL fixo.
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
		if serr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, a); serr != nil {
			return
		}
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl)
	}); err != nil {
		return err
	}
	return serr
}

// --- laco do anuncio ---

type announcer struct {
	net      network
	getenv   func(string) string
	logf     func(format string, a ...any)
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time           // espera entre tentativas
	ticker   func(time.Duration) (<-chan time.Time, func()) // NOTIFY periodico
	stop     <-chan os.Signal
	interval time.Duration
}

// session: um par de sockets aberto numa interface, ate a rede cair.
type session struct {
	ch      choice
	ep      endpoint
	udn     string
	conn    ssdpConn
	readErr chan error
}

func (s *session) sendAll(alive bool) error {
	for _, u := range usns(s.udn) {
		if err := s.conn.Send([]byte(notify(u, s.ep.location(), alive))); err != nil {
			return err
		}
	}
	return nil
}

// read responde as buscas ate o socket fechar (fim da sessao) ou falhar.
func (a *announcer) read(s *session) {
	buf := make([]byte, 4096)
	for {
		n, src, err := s.conn.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) { // fechado por nos: fim normal
				s.readErr <- err
			}
			return
		}
		st := parseMSearch(string(buf[:n]))
		if st == "" {
			continue
		}
		for _, r := range answers(st, s.udn, s.ep.location(), a.now()) {
			// Endereco ruim de um cliente nao derruba a borda: so avisa.
			if err := s.conn.Reply([]byte(r), src); err != nil && !errors.Is(err, net.ErrClosed) {
				a.logf("AVISO resposta a %s nao enviada: %v", src, err)
			}
		}
	}
}

// start: uma tentativa — escolhe interface e IP com a rede de agora, abre os
// sockets e manda o primeiro NOTIFY.
func (a *announcer) start(forced, udn string) (*session, []string, error) {
	ifaces := a.net.Interfaces()
	ep, err := resolveEndpoint(a.getenv, func() string { return localIP(ifaces, a.net.DefaultRoute) })
	if err != nil {
		return nil, nil, step("configuracao do anuncio (EDGE_HTTP_PORT/EDGE_HTTPS_PORT)", err)
	}
	ch, err := chooseInterface(ep.Host, forced, ifaces, a.net.DefaultRoute)
	if err != nil {
		return nil, nil, step("escolha da interface do anuncio", err)
	}
	conn, err := a.net.Open(ch)
	if err != nil {
		return nil, nil, a.classify(ch, err)
	}
	s := &session{ch: ch, ep: ep, udn: udn, conn: conn, readErr: make(chan error, 1)}
	go a.read(s)
	if err := s.sendAll(true); err != nil {
		err = a.classify(ch, step("envio do NOTIFY", err))
		conn.Close()
		return nil, nil, err
	}
	return s, append(warnings(ep), ch.Warnings...), nil
}

// classify: erro de socket e falta de rede quando o errno diz isso ou quando
// a interface/IPv4 escolhidos ja nao existem (o errno varia com o kernel: o
// que importa e que a origem do anuncio sumiu). EADDRINUSE e sempre
// configuracao (a 1900 presa sem SO_REUSEADDR).
func (a *announcer) classify(ch choice, err error) error {
	if isNetDown(err) || errors.Is(err, syscall.EADDRINUSE) {
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

// serve anuncia ate um sinal (stop=true) ou ate um erro.
func (a *announcer) serve(s *session) (stop bool, err error) {
	tick, stopTick := a.ticker(a.interval)
	defer stopTick()
	for {
		// sinal pendente tem prioridade sobre o tick (sem ssdp:alive depois
		// do byebye)
		sig, ok := a.stopped()
		if !ok {
			select {
			case <-tick:
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

func (a *announcer) fatal(err error) int {
	a.logf("FALHA em %v — encerrando (erro de configuracao, nao de rede: a borda cai junto)", err)
	return 1
}

// run devolve o codigo de saida: 0 num sinal, 1 em erro de configuracao. Na
// falta de rede nao volta.
func (a *announcer) run() int {
	// Configuracao: conferida uma vez, antes de tudo.
	if _, err := resolveEndpoint(a.getenv, func() string { return "" }); err != nil {
		return a.fatal(step("configuracao do anuncio (EDGE_HTTP_PORT/EDGE_HTTPS_PORT)", err))
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
	var downSince time.Time // zero = anunciando (ou ainda sem a 1a tentativa)
	tries := 0
	shown := map[string]bool{} // avisos da sessao anterior: nao se repetem
	for {
		s, warns, err := a.start(forced, udn)
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
			a.logf("anunciando %s em UDP 1900 pela interface %s (%s, via %s); LOCATION %s (host via %s)",
				ssdpST, s.ch.Name, s.ch.Addr, s.ch.Source, s.ep.location(), s.ep.Source)
			stop, serr := a.serve(s)
			if stop {
				return 0
			}
			s.conn.Close()
			err = serr
		}
		if !isNetDown(err) {
			return a.fatal(err)
		}
		if downSince.IsZero() {
			downSince = a.now()
			a.logf("AVISO sem rede para o anuncio (%v): sockets fechados, nada e anunciado nem respondido; "+
				"a borda e as APIs seguem de pe. Nova tentativa em %s, com espera crescente ate %s, escolhendo "+
				"de novo a interface e o IP; o proximo aviso sai quando a rede voltar.", err, wait, retryMax)
		}
		tries++
		// a espera e criada antes de olhar o sinal: um sinal que chegue
		// junto com ela nao se perde na escolha do select
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

func main() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
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
