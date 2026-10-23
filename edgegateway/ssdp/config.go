package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Address announced by the SSDP discovery (C.3.4): the host of LOCATION is
// the IPv4 of the announcement interface (iface.go), or SSDP_ADVERTISE_HOST
// when set.

const (
	ssdpST              = "urn:schemas-sbtvd-org:service:TV3.0WebServices:1"
	defaultUDN          = "uuid:TV30-1234-5678-9012-345678901234"
	defaultEdgeHTTPPort = 44642
	defaultEdgeHTTPS    = 44643
)

type endpoint struct {
	// From SSDP_ADVERTISE_HOST; "" when detected.
	Host      string
	HTTPPort  int
	HTTPSPort int
}

var schemeRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// normalizeHost removes scheme, path and port ("http://192.168.1.150:44642/x"
// becomes "192.168.1.150"); an IPv6 address comes out in brackets.
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

// resolveEndpoint: <host> is SSDP_ADVERTISE_HOST, if set; empty or absent,
// detected on each session. Checked once, at startup: an error here is a
// configuration error.
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

// warnings returns the warnings logged at startup about what is announced.
// They do not change the announcement.
func warnings(e endpoint) []string {
	var out []string
	if e.Host != "" && isLoopbackHost(e.Host) {
		// A loopback value is kept, with a warning.
		out = append(out, fmt.Sprintf("SSDP_ADVERTISE_HOST='%s' e de loopback: um cliente em outro "+
			"equipamento recebe o anuncio mas nao alcanca o LOCATION nem o Server-BaseURL (C.3.4). "+
			"Defina no .env da raiz o IP do equipamento na rede.", e.Host))
	}
	if e.HTTPPort != defaultEdgeHTTPPort {
		out = append(out, fmt.Sprintf("EDGE_HTTP_PORT=%d: a C.3.4 fixa %d no Server-BaseURL; "+
			"o anuncio fica fora da norma (use so para teste).", e.HTTPPort, defaultEdgeHTTPPort))
	}
	return out
}
