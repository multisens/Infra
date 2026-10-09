package main

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"192.168.2.7":                     "192.168.2.7",
		" http://192.168.1.150:44642/x  ": "192.168.1.150",
		"tv.local:80":                     "tv.local",
		"fe80::1":                         "[fe80::1]",
		"[fe80::1]:44642":                 "[fe80::1]",
		"":                                "",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveEndpoint(t *testing.T) {
	local := func() string { return "10.0.0.9" }
	e, err := resolveEndpoint(env(map[string]string{"SSDP_ADVERTISE_HOST": "192.168.2.7", "SERVER_URL": "x"}), local)
	if err != nil || e.Host != "192.168.2.7" || e.Source != "SSDP_ADVERTISE_HOST" || e.HTTPPort != 44642 || e.HTTPSPort != 44643 {
		t.Fatalf("advertise: %+v %v", e, err)
	}
	if e.location() != "http://192.168.2.7:44642/manifest" {
		t.Fatalf("location = %s", e.location())
	}
	e, _ = resolveEndpoint(env(map[string]string{"SERVER_URL": "localhost"}), local)
	if e.Host != "localhost" || e.Source != "SERVER_URL" {
		t.Fatalf("server_url: %+v", e)
	}
	e, _ = resolveEndpoint(env(nil), local)
	if e.Host != "10.0.0.9" || e.Source != "local-ip" {
		t.Fatalf("local-ip: %+v", e)
	}
	if _, err := resolveEndpoint(env(map[string]string{"EDGE_HTTP_PORT": "abc"}), local); err == nil {
		t.Fatal("porta invalida deveria falhar")
	}
}

func TestWarnings(t *testing.T) {
	w := warnings(endpoint{"localhost", "SERVER_URL", 44642, 44643})
	if len(w) != 2 || !strings.Contains(w[0], "loopback") || !strings.Contains(w[1], "SEM TLS") {
		t.Fatalf("loopback: %v", w)
	}
	w = warnings(endpoint{"192.168.2.7", "SSDP_ADVERTISE_HOST", 8080, 44643})
	if len(w) != 2 || !strings.Contains(w[0], "EDGE_HTTP_PORT=8080") {
		t.Fatalf("porta: %v", w)
	}
}

func TestParseDefaultRoute(t *testing.T) {
	text := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"wlp2s0\t00000000\t0102A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t0102A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	if got := parseDefaultRoute(text); got != "eth0" {
		t.Fatalf("rota padrao = %q, want eth0 (menor metrica)", got)
	}
	if got := parseDefaultRoute("Iface\tDestination\n"); got != "" {
		t.Fatalf("sem rota = %q", got)
	}
}

var ifs = []ifaceInfo{{"docker0", []string{"172.17.0.1"}}, {"wlp2s0", []string{"192.168.2.7"}}, {"br-1", []string{"172.20.0.1"}}}

func TestChooseInterface(t *testing.T) {
	route := func() string { return "wlp2s0" }
	c, err := chooseInterface("192.168.2.7", "", ifs, route)
	if err != nil || c.Name != "wlp2s0" || c.Addr != "192.168.2.7" || c.Source != "host-ip" || len(c.Warnings) != 0 {
		t.Fatalf("host-ip: %+v %v", c, err)
	}
	c, err = chooseInterface("tv.local", "", ifs, route)
	if err != nil || c.Name != "wlp2s0" || c.Source != "default-route" || len(c.Warnings) != 1 {
		t.Fatalf("default-route: %+v %v", c, err)
	}
	c, err = chooseInterface("192.168.2.7", "docker0", ifs, route)
	if err != nil || c.Name != "docker0" || c.Addr != "172.17.0.1" || len(c.Warnings) != 1 {
		t.Fatalf("forcada divergente: %+v %v", c, err)
	}
	if _, err = chooseInterface("192.168.2.7", "eth9", ifs, route); err == nil || !strings.Contains(err.Error(), "eth9") {
		t.Fatalf("forcada inexistente deveria falhar: %v", err)
	}
	if _, err = chooseInterface("10.9.9.9", "", ifs, func() string { return "" }); err == nil {
		t.Fatal("sem dono e sem rota padrao deveria falhar")
	}
}

func TestNotify(t *testing.T) {
	u := usns(defaultUDN)[0]
	alive := notify(u, "http://192.168.2.7:44642/manifest", true)
	for _, want := range []string{"NOTIFY * HTTP/1.1\r\n", "HOST: 239.255.255.250:1900\r\n", "NT: " + ssdpST + "\r\n",
		"NTS: ssdp:alive\r\n", "USN: " + defaultUDN + "::" + ssdpST + "\r\n",
		"LOCATION: http://192.168.2.7:44642/manifest\r\n", "CACHE-CONTROL: max-age=1800\r\n"} {
		if !strings.Contains(alive, want) {
			t.Errorf("alive sem %q:\n%s", want, alive)
		}
	}
	if !strings.HasSuffix(alive, "\r\n\r\n") {
		t.Error("NOTIFY sem linha em branco final")
	}
	bye := notify(u, "x", false)
	if !strings.Contains(bye, "NTS: ssdp:byebye\r\n") || strings.Contains(bye, "LOCATION") {
		t.Errorf("byebye:\n%s", bye)
	}
}

func TestParseMSearch(t *testing.T) {
	ok := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + ssdpST + "\r\n\r\n"
	if st := parseMSearch(ok); st != ssdpST {
		t.Fatalf("ST = %q", st)
	}
	quoted := strings.Replace(ok, "ST: "+ssdpST, "st: \""+ssdpST+"\"", 1)
	if st := parseMSearch(quoted); st != ssdpST {
		t.Fatalf("ST entre aspas/minusculas = %q", st)
	}
	for name, msg := range map[string]string{
		"sem MAN":  strings.Replace(ok, "MAN: \"ssdp:discover\"\r\n", "", 1),
		"sem MX":   strings.Replace(ok, "MX: 2\r\n", "", 1),
		"NOTIFY":   "NOTIFY * HTTP/1.1\r\nNT: x\r\n\r\n",
		"resposta": "HTTP/1.1 200 OK\r\nST: x\r\n\r\n",
	} {
		if st := parseMSearch(msg); st != "" {
			t.Errorf("%s deveria ser ignorado, ST = %q", name, st)
		}
	}
}

func TestAnswers(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	loc := "http://192.168.2.7:44642/manifest"
	if got := answers(ssdpST, defaultUDN, loc, now); len(got) != 1 || !strings.Contains(got[0], "ST: "+ssdpST+"\r\n") ||
		!strings.Contains(got[0], "LOCATION: "+loc+"\r\n") || !strings.HasPrefix(got[0], "HTTP/1.1 200 OK\r\n") ||
		!strings.Contains(got[0], "DATE: Fri, 09 Oct 2026 12:00:00 UTC\r\n") || !strings.Contains(got[0], "EXT: \r\n") {
		t.Fatalf("URN: %v", got)
	}
	if got := answers(ssdpAll, defaultUDN, loc, now); len(got) != 2 {
		t.Fatalf("ssdp:all deveria responder pelos 2 tipos: %d", len(got))
	}
	if got := answers("urn:outro:servico:1", defaultUDN, loc, now); len(got) != 0 {
		t.Fatalf("ST alheio deveria ficar sem resposta: %d", len(got))
	}
}
