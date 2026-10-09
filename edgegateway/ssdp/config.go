package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Endereco que a descoberta SSDP divulga (C.3.4). Mesma regra do
// tv3ws/src/ssdp-config.ts, que monta o /manifest: LOCATION e Server-BaseURL
// tem de sair do mesmo host. L6 decidida = opcao A (Luis, 09/10): quem
// anuncia e a borda, em rede do host (so Linux nativo).

const (
	ssdpST              = "urn:schemas-sbtvd-org:service:TV3.0WebServices:1"
	defaultUDN          = "uuid:TV30-1234-5678-9012-345678901234"
	defaultEdgeHTTPPort = 44642
	defaultEdgeHTTPS    = 44643
)

type endpoint struct {
	Host      string
	Source    string // SSDP_ADVERTISE_HOST | SERVER_URL | local-ip
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

// resolveEndpoint: <host> = SSDP_ADVERTISE_HOST, senao SERVER_URL, senao o IP
// da interface que anuncia (B2 segue aberta: mesma cadeia do tv3ws).
func resolveEndpoint(getenv func(string) string, localIP func() string) (endpoint, error) {
	httpPort, err := parsePort("EDGE_HTTP_PORT", getenv("EDGE_HTTP_PORT"), defaultEdgeHTTPPort)
	if err != nil {
		return endpoint{}, err
	}
	httpsPort, err := parsePort("EDGE_HTTPS_PORT", getenv("EDGE_HTTPS_PORT"), defaultEdgeHTTPS)
	if err != nil {
		return endpoint{}, err
	}
	if h := normalizeHost(getenv("SSDP_ADVERTISE_HOST")); h != "" {
		return endpoint{h, "SSDP_ADVERTISE_HOST", httpPort, httpsPort}, nil
	}
	if h := normalizeHost(getenv("SERVER_URL")); h != "" {
		return endpoint{h, "SERVER_URL", httpPort, httpsPort}, nil
	}
	return endpoint{localIP(), "local-ip", httpPort, httpsPort}, nil
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
	if isLoopbackHost(e.Host) {
		// PENDENTE (Joel): B2 — padrao do host anunciado quando nada e configurado.
		out = append(out, fmt.Sprintf("host anunciado '%s' (via %s) e de loopback: um cliente em outro "+
			"equipamento recebe o anuncio mas nao alcanca o LOCATION nem o Server-BaseURL (C.3.4). "+
			"Defina SSDP_ADVERTISE_HOST no .env da raiz com o IP do equipamento na rede.", e.Host, e.Source))
	}
	if e.HTTPPort != defaultEdgeHTTPPort {
		out = append(out, fmt.Sprintf("EDGE_HTTP_PORT=%d: a C.3.4 fixa %d no Server-BaseURL; "+
			"o anuncio fica fora da norma (use so para teste).", e.HTTPPort, defaultEdgeHTTPPort))
	}
	out = append(out, fmt.Sprintf("Server-SecureBaseURL %s aponta para porta SEM TLS (a borda ainda nao "+
		"tem HTTPS, lacuna L3): cliente que use https:// nesse endereco falha.", e.secureBase()))
	return out
}
