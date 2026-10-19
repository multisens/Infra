package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Endereco que a descoberta SSDP divulga (C.3.4). LOCATION e Server-BaseURL
// (o /manifest, montado pelo tv3ws/src/ssdp-config.ts) tem de sair do mesmo
// host. Desde o complemento do Joel de 15/10, quem anuncia e este programa,
// nativo no host, fora do Docker, nos tres sistemas (./tv30 up; ate ali, a
// borda em rede do host, so Linux nativo, opcao A do Luis de 09/10).
//
// B2 revista pelo Joel em 15/10: o host e DETECTADO — o IPv4 da interface por
// onde o anuncio sai (iface.go), e acompanha a troca de rede (main.go). O
// SSDP_ADVERTISE_HOST deixou de ser obrigatorio e fica como sobreposicao:
// definido, vale ele, fixo. Definido sem host ("http://:44642") continua erro
// de configuracao. De 11/10 a 15/10 (B2 do Luis) ele era obrigatorio. O
// /manifest do tv3ws tem de sair com o mesmo host (complemento do Joel, 3.4).

const (
	ssdpST              = "urn:schemas-sbtvd-org:service:TV3.0WebServices:1"
	defaultUDN          = "uuid:TV30-1234-5678-9012-345678901234"
	defaultEdgeHTTPPort = 44642
	defaultEdgeHTTPS    = 44643
)

type endpoint struct {
	Host      string // de SSDP_ADVERTISE_HOST; "" = detectado (B2 revista)
	HTTPPort  int
	HTTPSPort int
}

var schemeRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// normalizeHost tira esquema, caminho e porta ("http://192.168.1.150:44642/x"
// -> "192.168.1.150"); IPv6 sai entre colchetes.
func normalizeHost(raw string) string {
	h := schemeRE.ReplaceAllString(strings.TrimSpace(raw), "")
	h = strings.SplitN(h, "/", 2)[0]
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end > 0 {
			return h[:end+1]
		}
		return ""
	}
	switch strings.Count(h, ":") {
	case 0:
		return h
	case 1:
		return h[:strings.Index(h, ":")]
	default:
		return "[" + h + "]"
	}
}

func parsePort(name, raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s='%s' is not a valid TCP port", name, raw)
	}
	return n, nil
}

// resolveEndpoint: <host> = SSDP_ADVERTISE_HOST, se definido; vazio ou ausente
// = detectado a cada sessao (B2 revista). Conferido uma vez, na partida: erro
// aqui e de configuracao.
func resolveEndpoint(getenv func(string) string) (endpoint, error) {
	httpPort, err := parsePort("EDGE_HTTP_PORT", getenv("EDGE_HTTP_PORT"), defaultEdgeHTTPPort)
	if err != nil {
		return endpoint{}, err
	}
	httpsPort, err := parsePort("EDGE_HTTPS_PORT", getenv("EDGE_HTTPS_PORT"), defaultEdgeHTTPS)
	if err != nil {
		return endpoint{}, err
	}
	raw := getenv("SSDP_ADVERTISE_HOST")
	if strings.TrimSpace(raw) == "" {
		return endpoint{"", httpPort, httpsPort}, nil
	}
	h := normalizeHost(raw)
	if h == "" {
		return endpoint{}, fmt.Errorf("SSDP_ADVERTISE_HOST=%q sem host (esperado IP ou nome, com ou sem "+
			"esquema e porta)", raw)
	}
	return endpoint{h, httpPort, httpsPort}, nil
}

func (e endpoint) location() string   { return fmt.Sprintf("http://%s:%d/manifest", e.Host, e.HTTPPort) }
func (e endpoint) secureBase() string { return fmt.Sprintf("%s:%d", e.Host, e.HTTPSPort) }

var loopbackRE = regexp.MustCompile(`^127\.\d+\.\d+\.\d+$`)

func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.ToLower(host), "[]")
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || loopbackRE.MatchString(h) ||
		h == "::1" || h == "0:0:0:0:0:0:0:1"
}

// warnings: o que sera anunciado e merece aviso no boot (D9: nada em
// silencio). Nao mudam o anuncio.
func warnings(e endpoint) []string {
	var out []string
	if e.Host != "" && isLoopbackHost(e.Host) {
		// um valor de loopback configurado de proposito segue so com aviso
		out = append(out, fmt.Sprintf("SSDP_ADVERTISE_HOST='%s' e de loopback: um cliente em outro "+
			"equipamento recebe o anuncio mas nao alcanca o LOCATION nem o Server-BaseURL (C.3.4). "+
			"Defina no .env da raiz o IP do equipamento na rede.", e.Host))
	}
	if e.HTTPPort != defaultEdgeHTTPPort {
		out = append(out, fmt.Sprintf("EDGE_HTTP_PORT=%d: a C.3.4 fixa %d no Server-BaseURL; "+
			"o anuncio fica fora da norma (use so para teste).", e.HTTPPort, defaultEdgeHTTPPort))
	}
	// a 44643 tem TLS desde a A7 (Joel, 15/10): o aviso de porta sem TLS saiu
	return out
}
