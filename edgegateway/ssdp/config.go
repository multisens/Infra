package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Endereco que a descoberta SSDP divulga (C.3.4). LOCATION e Server-BaseURL
// (o /manifest, montado pelo tv3ws/src/ssdp-config.ts) tem de sair do mesmo
// host. L6 decidida = opcao A (Luis, 09/10): quem anuncia e a borda, em rede
// do host (so Linux nativo).
//
// B2 (decisao do Luis em 11/10): com o anuncio ligado (SSDP_ENABLED=true), o
// host e o SSDP_ADVERTISE_HOST, OBRIGATORIO — ausente, vazio ou sem host e
// erro de configuracao (o anunciante sai com 1 e a borda cai inteira, como os
// outros erros de configuracao da C8). O SERVER_URL e o IP local deixaram de
// ser fonte do host do anuncio da borda. O tv3ws manteve a cadeia dele
// (SSDP_ADVERTISE_HOST > SERVER_URL > IP local; o anunciante Node do dev-host
// nao mudou) e le os mesmos arquivos de ambiente que a borda: com o
// SSDP_ADVERTISE_HOST definido, os dois saem do mesmo valor.

const (
	ssdpST              = "urn:schemas-sbtvd-org:service:TV3.0WebServices:1"
	defaultUDN          = "uuid:TV30-1234-5678-9012-345678901234"
	defaultEdgeHTTPPort = 44642
	defaultEdgeHTTPS    = 44643
)

type endpoint struct {
	Host      string // de SSDP_ADVERTISE_HOST (B2)
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

// resolveEndpoint: <host> = SSDP_ADVERTISE_HOST, obrigatorio (B2, decisao do
// Luis em 11/10). Conferido uma vez, na partida: erro aqui e de configuracao.
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
		return endpoint{}, errors.New("SSDP_ADVERTISE_HOST ausente ou vazio: com SSDP_ENABLED=true ele e " +
			"obrigatorio (B2). Defina no .env da raiz o IP (ou nome) desta maquina na rede, por exemplo " +
			"SSDP_ADVERTISE_HOST=192.168.0.23")
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
	if isLoopbackHost(e.Host) {
		// A B2 (Luis, 11/10) fez do SSDP_ADVERTISE_HOST ausente ou vazio um erro;
		// um valor de loopback configurado de proposito segue so com aviso.
		out = append(out, fmt.Sprintf("SSDP_ADVERTISE_HOST='%s' e de loopback: um cliente em outro "+
			"equipamento recebe o anuncio mas nao alcanca o LOCATION nem o Server-BaseURL (C.3.4). "+
			"Defina no .env da raiz o IP do equipamento na rede.", e.Host))
	}
	if e.HTTPPort != defaultEdgeHTTPPort {
		out = append(out, fmt.Sprintf("EDGE_HTTP_PORT=%d: a C.3.4 fixa %d no Server-BaseURL; "+
			"o anuncio fica fora da norma (use so para teste).", e.HTTPPort, defaultEdgeHTTPPort))
	}
	out = append(out, fmt.Sprintf("Server-SecureBaseURL %s aponta para porta SEM TLS (a borda ainda nao "+
		"tem HTTPS, lacuna L3): cliente que use https:// nesse endereco falha.", e.secureBase()))
	return out
}
