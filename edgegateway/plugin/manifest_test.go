package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func manifestHandler(t *testing.T, env map[string]string) *authHandler {
	t.Helper()
	cfg := testConfig(t, modeEnforce)
	manifest, err := loadManifestConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Manifest = manifest
	for _, rt := range cfg.Routes.routes {
		if rt.Path == "/manifest" {
			rt.Edge = edgeManifest
		}
	}
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("/manifest reached the backend")
	})
	return newHandler(cfg, baseStore(), backend)
}

func getManifest(h *authHandler, host string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	request.Host = host
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

// exact is the value of a header under the name as written, without the
// canonical form of http.Header.Get.
func exact(header http.Header, name string) string {
	values := header[name]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func TestManifestAnnouncesTheHostOfTheRequest(t *testing.T) {
	h := manifestHandler(t, map[string]string{"BRAND_NAME": "Marca", "MODEL": "M1", "FRIENDLY_NAME": "Sala"})
	recorder := getManifest(h, "192.168.0.23:44642")
	header := recorder.Header()
	want := map[string]string{
		"Server-BaseURL":        "192.168.0.23:44642",
		"Server-SecureBaseURL":  "192.168.0.23:44643",
		"Server-PairingMethods": "qrcode,kex",
		"Device-BrandName":      "Marca",
		"Device-Model":          "M1",
		"Device-FriendlyName":   "Sala",
		"Content-Length":        "0",
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("status %d, body %q", recorder.Code, recorder.Body.String())
	}
	for name, value := range want {
		if got := header[name]; len(got) != 1 || got[0] != value {
			t.Errorf("%s=%q, expected %q", name, got, value)
		}
	}
	exposed := strings.Join(header.Values("Access-Control-Expose-Headers"), ", ")
	if !strings.Contains(exposed, "Server-BaseURL") || !strings.Contains(exposed, "Device-FriendlyName") ||
		header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("CORS: expose %q, allow origin %q", exposed, header.Get("Access-Control-Allow-Origin"))
	}
}

func TestManifestWithOverrideHostPortsAndIPv6(t *testing.T) {
	h := manifestHandler(t, map[string]string{"SSDP_ADVERTISE_HOST": "http://tv.local:9/x", "EDGE_HTTPS_PORT": "44743"})
	header := getManifest(h, "192.168.0.23:44642").Header()
	if exact(header, "Server-BaseURL") != "tv.local:44642" || exact(header, "Server-SecureBaseURL") != "tv.local:44743" {
		t.Errorf("override: %q %q", exact(header, "Server-BaseURL"), exact(header, "Server-SecureBaseURL"))
	}
	if exact(header, "Device-BrandName") != defaultBrandName || exact(header, "Device-FriendlyName") != defaultFriendlyName {
		t.Errorf("defaults: %q %q", exact(header, "Device-BrandName"), exact(header, "Device-FriendlyName"))
	}
	h6 := manifestHandler(t, map[string]string{})
	if got := exact(getManifest(h6, "[fe80::5]:44642").Header(), "Server-BaseURL"); got != "[fe80::5]:44642" {
		t.Errorf("IPv6: %q", got)
	}
}

func TestManifestConfigurationErrorsAndWarnings(t *testing.T) {
	for _, env := range []map[string]string{
		{"EDGE_HTTP_PORT": "0"},
		{"EDGE_HTTPS_PORT": "x"},
		{"SSDP_ADVERTISE_HOST": "http:///"},
	} {
		if _, err := loadManifestConfig(func(k string) string { return env[k] }); err == nil {
			t.Errorf("%v accepted", env)
		}
	}
	manifest, err := loadManifestConfig(func(k string) string {
		return map[string]string{"SSDP_ADVERTISE_HOST": "127.0.0.1", "EDGE_HTTP_PORT": "8080"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if warnings := manifestWarnings(manifest); len(warnings) != 2 {
		t.Errorf("warnings: %q", warnings)
	}
	standard, _ := loadManifestConfig(func(string) string { return "" })
	if warnings := manifestWarnings(standard); len(warnings) != 0 {
		t.Errorf("warnings with the defaults: %q", warnings)
	}
}
