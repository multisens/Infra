package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// The /manifest of the discovery for non-local clients (ABNT NBR 25608,
// C.3.4), the target of the LOCATION of the SSDP announcement: an empty 200
// with the headers of the TV 3.0 WebServices.

// manifestConfig is what the /manifest announces besides the host of the
// request.
type manifestConfig struct {
	// From SSDP_ADVERTISE_HOST; empty for the host of the request.
	Host string
	// Ports of the two surfaces as the host publishes them (EDGE_HTTP_PORT,
	// EDGE_HTTPS_PORT).
	HTTPPort  int
	HTTPSPort int
	// Platform attributes "manufacturer brand name", "model" and "friendly
	// name" (7.7).
	BrandName    string
	Model        string
	FriendlyName string
}

const (
	// C.3.4: the port of Server-BaseURL is fixed.
	standardHTTPPort     = 44642
	defaultHTTPSPort     = 44643
	defaultBrandName     = "GenericBrand"
	defaultModel         = "GenericModel"
	defaultFriendlyName  = "TV 3.0 Receiver"
	manifestPairingNames = "qrcode,kex"
	maxTCPPort           = 65535
)

// Headers of the response (C.3.4), exposed to scripts of another origin.
var manifestHeaders = []string{
	"Server-BaseURL",
	"Server-SecureBaseURL",
	"Server-PairingMethods",
	"Device-BrandName",
	"Device-Model",
	"Device-FriendlyName",
}

var hostSchemePattern = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

var loopbackIPv4Pattern = regexp.MustCompile(`^127\.\d+\.\d+\.\d+$`)

// loadManifestConfig reads the environment; an invalid port is an error.
func loadManifestConfig(getenv func(string) string) (manifestConfig, error) {
	httpPort, err := parsePort("EDGE_HTTP_PORT", getenv("EDGE_HTTP_PORT"), standardHTTPPort)
	if err != nil {
		return manifestConfig{}, err
	}
	httpsPort, err := parsePort("EDGE_HTTPS_PORT", getenv("EDGE_HTTPS_PORT"), defaultHTTPSPort)
	if err != nil {
		return manifestConfig{}, err
	}
	rawHost := getenv("SSDP_ADVERTISE_HOST")
	host := normalizeHost(rawHost)
	if strings.TrimSpace(rawHost) != "" && host == "" {
		return manifestConfig{}, fmt.Errorf("SSDP_ADVERTISE_HOST=%q sem host (esperado IP ou nome, com ou sem "+
			"esquema e porta)", rawHost)
	}
	return manifestConfig{
		Host:         host,
		HTTPPort:     httpPort,
		HTTPSPort:    httpsPort,
		BrandName:    valueOr(getenv("BRAND_NAME"), defaultBrandName),
		Model:        valueOr(getenv("MODEL"), defaultModel),
		FriendlyName: valueOr(getenv("FRIENDLY_NAME"), defaultFriendlyName),
	}, nil
}

// manifestWarnings are logged at registration; they do not change what is
// announced.
func manifestWarnings(mc manifestConfig) []string {
	warnings := []string{}
	if mc.Host != "" && isLoopbackHost(mc.Host) {
		warnings = append(warnings, fmt.Sprintf("SSDP_ADVERTISE_HOST='%s' e de loopback: um cliente em outro "+
			"equipamento nao alcanca o Server-BaseURL (C.3.4). Defina no .env da raiz o IP do equipamento na rede.",
			mc.Host))
	}
	if mc.HTTPPort != standardHTTPPort {
		warnings = append(warnings, fmt.Sprintf("EDGE_HTTP_PORT=%d: a C.3.4 fixa %d no Server-BaseURL; o "+
			"/manifest fica fora da norma (use so para teste).", mc.HTTPPort, standardHTTPPort))
	}
	return warnings
}

// manifest answers GET /manifest. The host is SSDP_ADVERTISE_HOST, when set,
// or the host of the request; Server-PairingMethods names the two methods
// required of every receiver (C.4.3.1).
func (h *authHandler) manifest(r *http.Request, _ map[string]string) edgeReply {
	mc := h.cfg.Manifest
	host := mc.Host
	if host == "" {
		host = normalizeHost(r.Host)
	}
	if host == "" {
		return edgeReply{Code: 101, Detail: "pedido sem Host"}
	}
	// The names as C.3.4 writes them, not in the canonical form of
	// http.Header.Set.
	header := http.Header{
		"Server-BaseURL":                {host + ":" + strconv.Itoa(mc.HTTPPort)},
		"Server-SecureBaseURL":          {host + ":" + strconv.Itoa(mc.HTTPSPort)},
		"Server-PairingMethods":         {manifestPairingNames},
		"Device-BrandName":              {mc.BrandName},
		"Device-Model":                  {mc.Model},
		"Device-FriendlyName":           {mc.FriendlyName},
		"Access-Control-Expose-Headers": {strings.Join(manifestHeaders, ", ")},
	}
	return edgeReply{Header: header}
}

// normalizeHost removes scheme, path and port ("http://192.168.1.150:44642/x"
// becomes "192.168.1.150"); an IPv6 address comes out in brackets, as the
// <ip_or_hostname>:<port> format of C.3.4 requires.
func normalizeHost(raw string) string {
	host := hostSchemePattern.ReplaceAllString(strings.TrimSpace(raw), "")
	host = strings.SplitN(host, "/", 2)[0]
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end > 0 {
			return host[:end+1]
		}
		return ""
	}
	switch strings.Count(host, ":") {
	case 0:
		return host
	case 1:
		return host[:strings.Index(host, ":")]
	default:
		return "[" + host + "]"
	}
}

func parsePort(name string, raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > maxTCPPort {
		return 0, fmt.Errorf("%s=%q nao e porta TCP valida", name, raw)
	}
	return port, nil
}

func isLoopbackHost(host string) bool {
	bare := strings.Trim(strings.ToLower(host), "[]")
	switch {
	case bare == "localhost", strings.HasSuffix(bare, ".localhost"):
		return true
	case bare == "::1", bare == "0:0:0:0:0:0:0:1":
		return true
	default:
		return loopbackIPv4Pattern.MatchString(bare)
	}
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
