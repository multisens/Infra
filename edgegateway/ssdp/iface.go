package main

import (
	"fmt"
	"net"
	"strings"
)

// Escolha da interface do anuncio. Uma so, para a busca nao receber respostas
// duplicadas. Ordem: SSDP_INTERFACE, se definida; a interface que tem o IPv4
// do host anunciado; a da rota padrao (com aviso, salvo com o host detectado).
// Quando nada serve, o erro e de falta de rede (netDown).

type ifaceInfo struct {
	Name  string
	IPv4s []string // IPv4 externos (nao loopback)
}

type choice struct {
	Name     string
	Addr     string // IPv4 de origem do anuncio
	Source   string // SSDP_INTERFACE | host-ip | default-route
	Warnings []string
}

func systemInterfaces() []ifaceInfo {
	var out []ifaceInfo
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range list {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		var v4 []string
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip := ipn.IP.To4(); ip != nil && !ip.IsLoopback() {
					v4 = append(v4, ip.String())
				}
			}
		}
		if len(v4) > 0 {
			out = append(out, ifaceInfo{ifi.Name, v4})
		}
	}
	return out
}

// routeProbe: endereco fora de qualquer rede local (TEST-NET-1, RFC 5737),
// para o sistema escolher a rota padrao. O "connect" de UDP nao manda pacote.
const routeProbe = "192.0.2.1:9"

// defaultRouteAddr: o IPv4 local que o sistema usaria para sair pela rota
// padrao ("" sem rota).
func defaultRouteAddr() string {
	c, err := net.Dial("udp4", routeProbe)
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil && !a.IP.IsLoopback() {
		return a.IP.String()
	}
	return ""
}

// readDefaultRoute: a interface que tem o IPv4 da rota padrao.
func readDefaultRoute() string {
	if ifi := owner(systemInterfaces(), defaultRouteAddr()); ifi != nil {
		return ifi.Name
	}
	return ""
}

func describe(ifaces []ifaceInfo) string {
	if len(ifaces) == 0 {
		return "nenhuma"
	}
	var parts []string
	for _, i := range ifaces {
		parts = append(parts, fmt.Sprintf("%s (%s)", i.Name, strings.Join(i.IPv4s, ", ")))
	}
	return strings.Join(parts, ", ")
}

func find(ifaces []ifaceInfo, name string) *ifaceInfo {
	for i := range ifaces {
		if ifaces[i].Name == name {
			return &ifaces[i]
		}
	}
	return nil
}

func owner(ifaces []ifaceInfo, ip string) *ifaceInfo {
	for i := range ifaces {
		for _, a := range ifaces[i].IPv4s {
			if a == ip {
				return &ifaces[i]
			}
		}
	}
	return nil
}

// checkForced: SSDP_INTERFACE com nome que nao existe na maquina, ou de
// loopback (o anuncio nunca sairia para a LAN por ela), e erro de
// configuracao. Conferido so na partida: depois dela, a interface sumir (um
// adaptador USB retirado, a rede do container desconectada) e falta de rede.
func checkForced(forced string, lookup func(string) (exists, loopback bool)) error {
	if forced = strings.TrimSpace(forced); forced == "" {
		return nil
	}
	exists, loopback := lookup(forced)
	if !exists {
		return fmt.Errorf("SSDP_INTERFACE='%s' nao existe nesta maquina", forced)
	}
	if loopback {
		return fmt.Errorf("SSDP_INTERFACE='%s' e a interface de loopback: o anuncio nao sairia para a rede", forced)
	}
	return nil
}

func chooseInterface(host, forced string, ifaces []ifaceInfo, defaultRoute func() string) (choice, error) {
	isV4 := net.ParseIP(host) != nil && net.ParseIP(host).To4() != nil
	var own *ifaceInfo
	if isV4 {
		own = owner(ifaces, host)
	}

	if forced = strings.TrimSpace(forced); forced != "" {
		ifi := find(ifaces, forced)
		if ifi == nil {
			return choice{}, netDown{fmt.Errorf("a interface SSDP_INTERFACE='%s' esta sem IPv4 externo ou fora do ar "+
				"(interfaces com IPv4: %s)", forced, describe(ifaces))}
		}
		c := choice{Name: ifi.Name, Addr: ifi.IPv4s[0], Source: "SSDP_INTERFACE"}
		if own != nil {
			c.Addr = host
			if own.Name != forced {
				c.Addr = ifi.IPv4s[0]
				c.Warnings = append(c.Warnings, fmt.Sprintf("SSDP_INTERFACE=%s, mas o host anunciado %s esta na interface %s: "+
					"o anuncio sai pela %s com LOCATION em outro endereco.", forced, host, own.Name, forced))
			}
		}
		return c, nil
	}

	if own != nil {
		return choice{Name: own.Name, Addr: host, Source: "host-ip"}, nil
	}
	if len(ifaces) == 0 {
		return choice{}, netDown{fmt.Errorf("nenhuma interface de pe com IPv4 externo")}
	}
	// host detectado (sem SSDP_ADVERTISE_HOST): a interface da rota padrao,
	// sem aviso; o IPv4 dela vira o host do LOCATION
	if host == "" {
		if r := defaultRoute(); r != "" {
			if ifi := find(ifaces, r); ifi != nil {
				return choice{Name: ifi.Name, Addr: ifi.IPv4s[0], Source: "default-route"}, nil
			}
		}
		return choice{}, netDown{fmt.Errorf("nenhuma rota padrao com IPv4 para detectar o host do anuncio "+
			"(interfaces com IPv4: %s). Defina SSDP_INTERFACE ou SSDP_ADVERTISE_HOST", describe(ifaces))}
	}

	why := fmt.Sprintf("'%s' nao e um IPv4 (nome ou IPv6)", host)
	if isV4 {
		why = fmt.Sprintf("o IPv4 %s nao esta em nenhuma interface externa desta maquina", host)
	}
	if r := defaultRoute(); r != "" {
		if ifi := find(ifaces, r); ifi != nil {
			return choice{Name: ifi.Name, Addr: ifi.IPv4s[0], Source: "default-route", Warnings: []string{
				fmt.Sprintf("host anunciado: %s; o anuncio sai pela interface da rota padrao %s (%s). "+
					"Defina SSDP_INTERFACE para escolher outra.", why, ifi.Name, strings.Join(ifi.IPv4s, ", ")),
			}}, nil
		}
	}
	return choice{}, netDown{fmt.Errorf("nenhuma interface para anunciar: %s, e nao ha rota padrao com IPv4 "+
		"(interfaces com IPv4: %s). Defina SSDP_INTERFACE", why, describe(ifaces))}
}
