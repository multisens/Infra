// Anunciante SSDP da borda (C.3.4).
//
// L6 decidida = opcao A (Luis, 09/10): quem anuncia e a borda. Roda como
// processo do container edgegateway, ao lado dos dois KrakenD, quando a borda
// esta em rede do host (docker-compose.ssdp.yml, so Linux nativo — decisao
// do Joel, 04/10) e SSDP_ENABLED=true. Na bridge o multicast nao sai para a
// LAN (docs/ssdp-verificacao.md), entao ali o anuncio fica desligado.
//
// Morre-inteiro (decisao do Luis, 09/10): qualquer falha do anuncio encerra
// este processo com exit 1, e o entrypoint derruba a borda inteira (o
// restart do compose a traz de volta). O LOCATION aponta para o /manifest,
// que continua no tv3ws, atras da propria borda (44642).
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	notifyInterval = 10 * time.Second
	multicastTTL   = 4
)

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[ssdp] "+format+"\n", a...)
}

func fatal(what string, err error) {
	logf("FALHA em %s: %v — encerrando (morre-inteiro: a borda cai junto)", what, err)
	os.Exit(1)
}

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

func main() {
	ifaces := systemInterfaces()
	localIP := func() string {
		if r := readDefaultRoute(); r != "" {
			if ifi := find(ifaces, r); ifi != nil {
				return ifi.IPv4s[0]
			}
		}
		if len(ifaces) > 0 {
			return ifaces[0].IPv4s[0]
		}
		return "127.0.0.1"
	}

	ep, err := resolveEndpoint(os.Getenv, localIP)
	if err != nil {
		fatal("configuracao do anuncio (EDGE_HTTP_PORT/EDGE_HTTPS_PORT)", err)
	}
	for _, w := range warnings(ep) {
		logf("AVISO %s", w)
	}
	ch, err := chooseInterface(ep.Host, os.Getenv("SSDP_INTERFACE"), ifaces, readDefaultRoute)
	if err != nil {
		fatal("escolha da interface do anuncio (SSDP_INTERFACE)", err)
	}
	for _, w := range ch.Warnings {
		logf("AVISO %s", w)
	}
	ifi, err := net.InterfaceByName(ch.Name)
	if err != nil {
		fatal("interface "+ch.Name, err)
	}

	udn := os.Getenv("UDN")
	if udn == "" {
		udn = defaultUDN
	}
	loc := ep.location()
	group := &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}

	// Escuta: UDP 1900 (SO_REUSEADDR), entrando no grupo so pela interface
	// escolhida.
	lc, err := net.ListenMulticastUDP("udp4", ifi, group)
	if err != nil {
		fatal("socket UDP 1900 (escuta do grupo)", err)
	}
	// Envio: socket proprio no IPv4 da interface (origem fixa), multicast
	// pela mesma interface.
	snd, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ch.Addr)})
	if err != nil {
		fatal("socket de envio em "+ch.Addr, err)
	}
	if err := setMulticastOut(snd, ch.Addr, multicastTTL); err != nil {
		fatal("interface de saida do multicast ("+ch.Addr+")", err)
	}

	sendAll := func(alive bool) error {
		for _, u := range usns(udn) {
			if _, err := snd.WriteToUDP([]byte(notify(u, loc, alive)), group); err != nil {
				return err
			}
		}
		return nil
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, src, err := lc.ReadFromUDP(buf)
			if err != nil {
				fatal("leitura do grupo SSDP", err)
			}
			st := parseMSearch(string(buf[:n]))
			if st == "" {
				continue
			}
			for _, r := range answers(st, udn, loc, time.Now()) {
				// Endereco ruim de um cliente nao derruba a borda: so avisa.
				if _, err := snd.WriteToUDP([]byte(r), src); err != nil {
					logf("AVISO resposta a %s nao enviada: %v", src, err)
				}
			}
		}
	}()

	if err := sendAll(true); err != nil {
		fatal("envio do NOTIFY", err)
	}
	logf("anunciando %s em UDP 1900 pela interface %s (%s, via %s); LOCATION %s (host via %s)",
		ssdpST, ch.Name, ch.Addr, ch.Source, loc, ep.Source)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(notifyInterval)
	for {
		select {
		case <-tick.C:
			if err := sendAll(true); err != nil {
				fatal("envio do NOTIFY", err)
			}
		case s := <-sig:
			logf("%s: enviando ssdp:byebye", s)
			if err := sendAll(false); err != nil {
				logf("byebye nao enviado: %v", err)
			}
			os.Exit(0)
		}
	}
}
