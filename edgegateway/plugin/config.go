package main

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	modeWarn    = "warn"
	modeEnforce = "enforce"

	// mesmo padrao de desenvolvimento do tv3ws no compose raiz — so serve
	// para o aviso no log, nunca como valor assumido.
	devSecret = "tv30-dev-secret-nao-usar-em-producao"

	defaultIssuer       = "GenericIssuer" // igual ao tv3ws (auth-manager/manager.ts)
	defaultRedisHost    = "redis"
	defaultRedisPort    = "6379"
	defaultRedisTimeout = 500 * time.Millisecond
)

type config struct {
	Surface string
	Mode    string
	// NonLocalHTTPSOnly: superficie HTTP (a interna, Server-BaseURL). Nela o
	// nao local identificado pelo access token so usa as rotas NonLocalHTTP
	// (C.6.1.2 e C.6.1.3); nas demais recebe 106 (C.4.1.6; E7, Joel 15/10:
	// vale ja, sem esperar o TLS da 44643, e tambem em warn).
	NonLocalHTTPSOnly bool
	// AllVersions: as versoes servidas por alguma rota, em ordem crescente;
	// LatestVersion, a ultima — o API-Version do erro 100 ("the latest
	// version supported by the server", C.3.6.6). D4 (Joel, 15/10).
	AllVersions   []string
	LatestVersion string
	Secret        []byte
	Issuer        string
	Routes        *routeTable
	// APIs implementadas (id da Tabela C.2 + versao), na ordem da tabela,
	// para as APIs C.6.7.8/C.6.7.9 (apiinfo.go). Vem de routes.json.
	APIs         *apiCatalog
	RedisAddr    string
	RedisTimeout time.Duration
	// Access-Control-Allow-Headers de todo OPTIONS que o plugin responde
	// (C.4.1.9.3), preflight ou nao; vem de cors.allow_headers do routes.json.
	CORSAllowHeaders string
	// Access-Control-Expose-Headers de toda resposta ("" = sem o cabecalho);
	// vem de cors.expose_headers do routes.json. Ate 12/10 era o modulo CORS
	// do KrakenD que o mandava.
	CORSExposeHeaders string
	// Access-Control-Max-Age do preflight, em segundos ("" = sem o
	// cabecalho); vem de cors.max_age do routes.json (ex.: "12h" => "43200"),
	// o mesmo valor que o modulo CORS do KrakenD devolvia ate 12/10.
	CORSMaxAge string
}

// Padrao da C.4.1.9.3 mais o cabecalho key do DELETE /tv3/bind-context
// (C.6.8.4) — o mesmo cors.allow_headers do routes.json.
const defaultCORSAllowHeaders = "Content-Type, Authorization, bind-token, Accept, Accept-Version, key"

// loadConfig le o bloco "tv30-auth" do extra_config["plugin/http-server"]
// (gerado por generate.js a partir de routes.json) e o ambiente do
// container. Qualquer valor invalido eh erro: nada de assumir padrao em
// silencio para algo que mude o que a borda deixa passar.
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

	// A6 (Joel, 15/10): sem constante de serviceContextId — o contexto e o
	// globalServiceId de cada servico (handler.go, pathContext). O campo
	// current_service_context_id, se vier de um routes.json antigo, e recusado.
	if _, present := m["current_service_context_id"]; present {
		return nil, fmt.Errorf("current_service_context_id saiu em 15/10 (A6): o serviceContextId e o globalServiceId do servico")
	}

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
		method, _ := rm["method"].(string)
		path, _ := rm["path"].(string)
		auth := authToken
		if v, present := rm["auth"]; present {
			if auth, ok = v.(string); !ok {
				return nil, fmt.Errorf("routes[%d].auth invalido: %v", i, v)
			}
		}
		var classes []string
		if v, present := rm["classes"]; present {
			list, ok := v.([]interface{})
			if !ok {
				return nil, fmt.Errorf("routes[%d].classes nao eh lista", i)
			}
			classes = []string{}
			for _, c := range list {
				s, ok := c.(string)
				if !ok {
					return nil, fmt.Errorf("routes[%d].classes contem valor nao-texto: %v", i, c)
				}
				classes = append(classes, s)
			}
		}
		rt, err := newRoute(method, path, auth, classes)
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: %v", i, err)
		}
		if v, present := rm["edge"]; present {
			name, ok := v.(string)
			if !ok || edgeHandlers[name] == nil {
				return nil, fmt.Errorf("routes[%d].edge invalido: %v (%s)", i, v, edgeHandlerNames())
			}
			rt.Edge = name
		}
		if v, present := rm["surface_error"]; present {
			// so o 106 da C.4.1.8 existe hoje (F-1, Joel 15/10)
			if n, ok := v.(float64); !ok || n != 106 {
				return nil, fmt.Errorf("routes[%d].surface_error invalido: %v (so 106, C.4.1.8)", i, v)
			}
			rt.SurfaceError = 106
		}
		if v, present := rm["nonlocal_http"]; present {
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("routes[%d].nonlocal_http nao eh booleano: %v", i, v)
			}
			rt.NonLocalHTTP = b
		}
		rt.Versions = []string{defaultVersion}
		if v, present := rm["versions"]; present {
			list, ok := v.([]interface{})
			if !ok || len(list) == 0 {
				return nil, fmt.Errorf("routes[%d].versions nao eh lista nao vazia: %v", i, v)
			}
			rt.Versions = nil
			for _, x := range list {
				s, ok := x.(string)
				if !ok || !versionPattern.MatchString(s) {
					return nil, fmt.Errorf("routes[%d].versions com valor fora do formato X.Y: %v", i, x)
				}
				for _, y := range rt.Versions {
					if y == s {
						return nil, fmt.Errorf("routes[%d].versions com %s repetida", i, s)
					}
				}
				rt.Versions = append(rt.Versions, s)
			}
			sort.Slice(rt.Versions, func(a, b int) bool { return compareVersions(rt.Versions[a], rt.Versions[b]) < 0 })
		}
		if v, present := rm["no_service"]; present {
			// so o 300 (H2, Joel 15/10); e so faz sentido com bind-token
			if n, ok := v.(float64); !ok || n != 300 {
				return nil, fmt.Errorf("routes[%d].no_service invalido: %v (so 300)", i, v)
			}
			if rt.Auth != authTokenBind {
				return nil, fmt.Errorf("routes[%d].no_service so vale em rota %s", i, authTokenBind)
			}
			rt.NoService = 300
		}
		table.add(rt)
	}
	cfg.Routes = table
	all := map[string]bool{}
	for _, rt := range table.routes {
		for _, v := range rt.Versions {
			if !all[v] {
				all[v] = true
				cfg.AllVersions = append(cfg.AllVersions, v)
			}
		}
	}
	sort.Slice(cfg.AllVersions, func(a, b int) bool { return compareVersions(cfg.AllVersions[a], cfg.AllVersions[b]) < 0 })
	cfg.LatestVersion = cfg.AllVersions[len(cfg.AllVersions)-1]
	if v, present := m["nonlocal_https_only"]; present {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("nonlocal_https_only nao eh booleano: %v", v)
		}
		cfg.NonLocalHTTPSOnly = b
	}

	var needAPIs bool
	for _, rt := range table.routes {
		if rt.Edge == edgeAPIInfo || rt.Edge == edgeAPIList {
			needAPIs = true
		}
	}
	cfg.APIs = &apiCatalog{}
	if v, present := m["apis"]; present {
		cat, err := loadAPICatalog(v)
		if err != nil {
			return nil, err
		}
		cfg.APIs = cat
	} else if needAPIs {
		return nil, fmt.Errorf("lista apis ausente: as rotas da C.6.7.8/C.6.7.9 respondidas pela borda a usam")
	}

	cfg.CORSAllowHeaders = defaultCORSAllowHeaders
	if v, present := m["cors_allow_headers"]; present {
		list, ok := v.([]interface{})
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("cors_allow_headers nao eh lista nao vazia: %v", v)
		}
		names := make([]string, 0, len(list))
		for _, h := range list {
			s, ok := h.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("cors_allow_headers contem valor invalido: %v", h)
			}
			names = append(names, strings.TrimSpace(s))
		}
		cfg.CORSAllowHeaders = strings.Join(names, ", ")
	}
	if v, present := m["cors_expose_headers"]; present {
		list, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("cors_expose_headers nao eh lista: %v", v)
		}
		names := make([]string, 0, len(list))
		for _, h := range list {
			s, ok := h.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("cors_expose_headers contem valor invalido: %v", h)
			}
			names = append(names, strings.TrimSpace(s))
		}
		cfg.CORSExposeHeaders = strings.Join(names, ", ")
	}
	if v, present := m["cors_max_age"]; present {
		s, ok := v.(string)
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if !ok || err != nil || d <= 0 || d%time.Second != 0 {
			return nil, fmt.Errorf("cors_max_age nao eh duracao positiva em segundos inteiros (ex.: 12h): %v", v)
		}
		cfg.CORSMaxAge = strconv.FormatInt(int64(d/time.Second), 10)
	}

	// Padrao enforce desde 15/10 (Joel, "Suas decisoes", modo warn): o warn
	// fica como opcao explicita e transitoria (ex.: o Guarana, ate obter o
	// token), ligada por AUTH_ENFORCE=warn. Ate entao o vazio valia warn.
	switch mode := strings.TrimSpace(getenv("AUTH_ENFORCE")); mode {
	case modeWarn:
		cfg.Mode = modeWarn
	case "", modeEnforce:
		cfg.Mode = modeEnforce
	default:
		return nil, fmt.Errorf("AUTH_ENFORCE=%q invalido (use warn ou enforce)", mode)
	}

	secret := getenv("JWT_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("JWT_SECRET ausente (tem que ser o MESMO do tv3ws, que emite o access token)")
	}
	cfg.Secret = []byte(secret)

	cfg.Issuer = getenv("JWT_ISSUER")
	if cfg.Issuer == "" {
		cfg.Issuer = defaultIssuer
	}

	host := getenv("REDIS_HOST")
	if host == "" {
		host = defaultRedisHost
	}
	port := getenv("REDIS_PORT")
	if port == "" {
		port = defaultRedisPort
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return nil, fmt.Errorf("REDIS_PORT=%q invalido", port)
	}
	cfg.RedisAddr = net.JoinHostPort(host, port)

	cfg.RedisTimeout = defaultRedisTimeout
	if v := getenv("REDIS_TIMEOUT_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("REDIS_TIMEOUT_MS=%q invalido", v)
		}
		cfg.RedisTimeout = time.Duration(n) * time.Millisecond
	}
	return cfg, nil
}
