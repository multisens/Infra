package main

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Surface string
	Mode    string
	// The HTTP surface (Server-BaseURL): a non-local client may only use the
	// NonLocalHTTP routes and gets 106 on the others (C.4.1.6).
	NonLocalHTTPSOnly bool
	// Versions served by any route, in ascending order; LatestVersion goes in
	// the API-Version header of error 100 (C.3.6.6).
	AllVersions   []string
	LatestVersion string
	Secret        []byte
	Issuer        string
	Routes        *routeTable
	// Implemented APIs, in the order of Table C.2 (C.6.7.8, C.6.7.9).
	APIs         *apiCatalog
	RedisAddr    string
	RedisTimeout time.Duration
	// CORS headers (C.4.1.9); an empty value omits the header.
	// Access-Control-Allow-Headers of every OPTIONS response.
	CORSAllowHeaders string
	// Access-Control-Expose-Headers of every response.
	CORSExposeHeaders string
	// Access-Control-Max-Age of the preflight response, in seconds.
	CORSMaxAge string
	// What the /manifest announces (C.3.4).
	Manifest manifestConfig
}

const (
	modeWarn    = "warn"
	modeEnforce = "enforce"

	// Development JWT_SECRET; when in use, a warning is logged.
	devSecret = "tv30-dev-secret-nao-usar-em-producao"

	defaultIssuer       = "GenericIssuer"
	defaultRedisHost    = "redis"
	defaultRedisPort    = "6379"
	defaultRedisTimeout = 500 * time.Millisecond

	// Default of C.4.1.9.3 plus the key header of DELETE
	// /tv3/bind-context (C.6.8.4).
	defaultCORSAllowHeaders = "Content-Type, Authorization, bind-token, Accept, Accept-Version, key"
)

// loadConfig reads the "tv30-auth" block of
// extra_config["plugin/http-server"] and the environment. Returns an error
// for any invalid value.
func loadConfig(raw interface{}, getenv func(string) string) (*config, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("bloco %q ausente em extra_config[\"plugin/http-server\"]", pluginName)
	}
	cfg := &config{}

	cfg.Surface, _ = m["surface"].(string)
	if cfg.Surface == "" {
		return nil, fmt.Errorf("campo surface ausente")
	}

	if _, present := m["current_service_context_id"]; present {
		return nil, fmt.Errorf("current_service_context_id nao eh aceito: o serviceContextId e o globalServiceId do servico")
	}

	table, err := loadRoutes(m)
	if err != nil {
		return nil, err
	}
	cfg.Routes = table
	cfg.AllVersions = allVersions(table)
	cfg.LatestVersion = cfg.AllVersions[len(cfg.AllVersions)-1]

	if v, present := m["nonlocal_https_only"]; present {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("nonlocal_https_only nao eh booleano: %v", v)
		}
		cfg.NonLocalHTTPSOnly = b
	}

	apis, err := loadAPIs(m, table)
	if err != nil {
		return nil, err
	}
	cfg.APIs = apis

	if err := loadCORS(m, cfg); err != nil {
		return nil, err
	}

	mode, err := loadMode(getenv)
	if err != nil {
		return nil, err
	}
	cfg.Mode = mode

	secret := getenv("JWT_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("JWT_SECRET ausente (tem que ser o MESMO do tv3ws, que emite o access token)")
	}
	cfg.Secret = []byte(secret)

	cfg.Issuer = valueOr(getenv("JWT_ISSUER"), defaultIssuer)

	addr, timeout, err := loadRedis(getenv)
	if err != nil {
		return nil, err
	}
	cfg.RedisAddr = addr
	cfg.RedisTimeout = timeout

	manifest, err := loadManifestConfig(getenv)
	if err != nil {
		return nil, err
	}
	cfg.Manifest = manifest
	return cfg, nil
}

// loadRoutes reads the "routes" list, which must not be empty.
func loadRoutes(m map[string]interface{}) (*routeTable, error) {
	rawRoutes, ok := m["routes"].([]interface{})
	if !ok || len(rawRoutes) == 0 {
		return nil, fmt.Errorf("lista routes ausente ou vazia")
	}
	table := &routeTable{}
	for i, rr := range rawRoutes {
		rm, ok := rr.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("routes[%d] nao eh objeto", i)
		}
		rt, err := parseRoute(rm)
		if err != nil {
			return nil, fmt.Errorf("routes[%d]%v", i, err)
		}
		table.add(rt)
	}
	return table, nil
}

// parseRoute reads one entry of "routes". The error starts with the field,
// as in ".auth invalido", or with ": " for an error of newRoute.
func parseRoute(rm map[string]interface{}) (*route, error) {
	method, _ := rm["method"].(string)
	path, _ := rm["path"].(string)
	auth := authToken
	if v, present := rm["auth"]; present {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf(".auth invalido: %v", v)
		}
		auth = s
	}
	classes, err := parseClasses(rm)
	if err != nil {
		return nil, err
	}
	rt, err := newRoute(method, path, auth, classes)
	if err != nil {
		return nil, fmt.Errorf(": %v", err)
	}
	if v, present := rm["edge"]; present {
		name, ok := v.(string)
		if !ok || edgeHandlers[name] == nil {
			return nil, fmt.Errorf(".edge invalido: %v (%s)", v, edgeHandlerNames())
		}
		rt.Edge = name
	}
	if v, present := rm["surface_error"]; present {
		n, ok := v.(float64)
		if !ok || n != 106 {
			return nil, fmt.Errorf(".surface_error invalido: %v (so 106, C.4.1.8)", v)
		}
		rt.SurfaceError = 106
	}
	if v, present := rm["nonlocal_http"]; present {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf(".nonlocal_http nao eh booleano: %v", v)
		}
		rt.NonLocalHTTP = b
	}
	rt.Versions = []string{defaultVersion}
	if v, present := rm["versions"]; present {
		versions, err := parseVersions(v)
		if err != nil {
			return nil, err
		}
		rt.Versions = versions
	}
	if v, present := rm["no_service"]; present {
		n, ok := v.(float64)
		if !ok || n != 300 {
			return nil, fmt.Errorf(".no_service invalido: %v (so 300)", v)
		}
		if rt.Auth != authTokenBind {
			return nil, fmt.Errorf(".no_service so vale em rota %s", authTokenBind)
		}
		rt.NoService = 300
	}
	if v, present := rm["not_in_catalog"]; present {
		n, ok := v.(float64)
		if !ok || n != 304 {
			return nil, fmt.Errorf(".not_in_catalog invalido: %v (so 304)", v)
		}
		if rt.Auth != authTokenBind || !rt.hasParam("serviceContextId") {
			return nil, fmt.Errorf(".not_in_catalog so vale em rota %s com {serviceContextId}", authTokenBind)
		}
		rt.NotInCatalog = 304
	}
	return rt, nil
}

// parseClasses reads the optional "classes" list of a route; nil when
// absent.
func parseClasses(rm map[string]interface{}) ([]string, error) {
	v, present := rm["classes"]
	if !present {
		return nil, nil
	}
	list, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf(".classes nao eh lista")
	}
	classes := []string{}
	for _, c := range list {
		s, ok := c.(string)
		if !ok {
			return nil, fmt.Errorf(".classes contem valor nao-texto: %v", c)
		}
		classes = append(classes, s)
	}
	return classes, nil
}

// parseVersions reads the "versions" list of a route: X.Y values without
// repetition, returned in ascending order.
func parseVersions(v interface{}) ([]string, error) {
	list, ok := v.([]interface{})
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf(".versions nao eh lista nao vazia: %v", v)
	}
	versions := []string{}
	for _, x := range list {
		s, ok := x.(string)
		if !ok || !versionPattern.MatchString(s) {
			return nil, fmt.Errorf(".versions com valor fora do formato X.Y: %v", x)
		}
		for _, y := range versions {
			if y == s {
				return nil, fmt.Errorf(".versions com %s repetida", s)
			}
		}
		versions = append(versions, s)
	}
	sortVersions(versions)
	return versions, nil
}

// allVersions returns the versions served by any route, in ascending order.
func allVersions(table *routeTable) []string {
	seen := map[string]bool{}
	versions := []string{}
	for _, rt := range table.routes {
		for _, v := range rt.Versions {
			if !seen[v] {
				seen[v] = true
				versions = append(versions, v)
			}
		}
	}
	sortVersions(versions)
	return versions
}

// loadAPIs reads the "apis" list, required when a route is answered by
// C.6.7.8 or C.6.7.9 at the edge.
func loadAPIs(m map[string]interface{}, table *routeTable) (*apiCatalog, error) {
	if v, present := m["apis"]; present {
		return loadAPICatalog(v)
	}
	for _, rt := range table.routes {
		if rt.Edge == edgeAPIInfo || rt.Edge == edgeAPIList {
			return nil, fmt.Errorf("lista apis ausente: as rotas da C.6.7.8/C.6.7.9 respondidas pela borda a usam")
		}
	}
	return &apiCatalog{}, nil
}

// loadCORS reads the CORS headers (C.4.1.9) into cfg.
func loadCORS(m map[string]interface{}, cfg *config) error {
	cfg.CORSAllowHeaders = defaultCORSAllowHeaders
	if v, present := m["cors_allow_headers"]; present {
		names, err := headerList("cors_allow_headers", v, false)
		if err != nil {
			return err
		}
		cfg.CORSAllowHeaders = names
	}
	if v, present := m["cors_expose_headers"]; present {
		names, err := headerList("cors_expose_headers", v, true)
		if err != nil {
			return err
		}
		cfg.CORSExposeHeaders = names
	}
	if v, present := m["cors_max_age"]; present {
		s, ok := v.(string)
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if !ok || err != nil || d <= 0 || d%time.Second != 0 {
			return fmt.Errorf("cors_max_age nao eh duracao positiva em segundos inteiros (ex.: 12h): %v", v)
		}
		cfg.CORSMaxAge = strconv.FormatInt(int64(d/time.Second), 10)
	}
	return nil
}

// loadMode reads AUTH_ENFORCE; empty means enforce.
func loadMode(getenv func(string) string) (string, error) {
	switch mode := strings.TrimSpace(getenv("AUTH_ENFORCE")); mode {
	case modeWarn:
		return modeWarn, nil
	case "", modeEnforce:
		return modeEnforce, nil
	default:
		return "", fmt.Errorf("AUTH_ENFORCE=%q invalido (use warn ou enforce)", mode)
	}
}

// loadRedis reads the Redis address and timeout from the environment.
func loadRedis(getenv func(string) string) (string, time.Duration, error) {
	host := valueOr(getenv("REDIS_HOST"), defaultRedisHost)
	port := valueOr(getenv("REDIS_PORT"), defaultRedisPort)
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > maxTCPPort {
		return "", 0, fmt.Errorf("REDIS_PORT=%q invalido", port)
	}
	timeout := defaultRedisTimeout
	if v := getenv("REDIS_TIMEOUT_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms <= 0 {
			return "", 0, fmt.Errorf("REDIS_TIMEOUT_MS=%q invalido", v)
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	return net.JoinHostPort(host, port), timeout, nil
}

// headerList joins a list of header names with ", "; an empty list is
// accepted only when allowEmpty is set.
func headerList(field string, v interface{}, allowEmpty bool) (string, error) {
	list, ok := v.([]interface{})
	if !ok || (len(list) == 0 && !allowEmpty) {
		if allowEmpty {
			return "", fmt.Errorf("%s nao eh lista: %v", field, v)
		}
		return "", fmt.Errorf("%s nao eh lista nao vazia: %v", field, v)
	}
	names := make([]string, 0, len(list))
	for _, h := range list {
		s, ok := h.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("%s contem valor invalido: %v", field, h)
		}
		names = append(names, strings.TrimSpace(s))
	}
	return strings.Join(names, ", "), nil
}

func sortVersions(versions []string) {
	sort.Slice(versions, func(a, b int) bool {
		return compareVersions(versions[a], versions[b]) < 0
	})
}
