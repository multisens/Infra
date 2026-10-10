package main

import (
	"fmt"
	"net"
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
	Secret  []byte
	Issuer  string
	// valores de {serviceContextId} no caminho tratados como o servico
	// corrente (alem do literal current-service). PENDENTE (Joel): lacuna
	// L2 — o tv3ws usa um serviceContextId CONSTANTE para todo servico
	// (tv3ws/src/core.ts), entao nao ha como ligar outro valor a um servico;
	// qualquer outro valor da 108 nas rotas token+bind (provisorio).
	CurrentSCIDs []string
	// serviceContextId que as respostas da C.6.8 devolvem (o mesmo valor de
	// current_service_context_id; L2 acima). Obrigatorio se alguma rota
	// bind-context-* eh respondida pela borda.
	ServiceContextID string
	Routes           *routeTable
	// APIs implementadas (id da Tabela C.2 + versao), na ordem da tabela,
	// para as APIs C.6.7.8/C.6.7.9 (apiinfo.go). Vem de routes.json.
	APIs         *apiCatalog
	RedisAddr    string
	RedisTimeout time.Duration
	// Access-Control-Allow-Headers do OPTIONS que nao eh preflight
	// (C.4.1.9.3); vem de cors.allow_headers do routes.json.
	CORSAllowHeaders string
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

	if v, present := m["current_service_context_id"]; present {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("current_service_context_id invalido: %v", v)
		}
		cfg.CurrentSCIDs = append(cfg.CurrentSCIDs, s)
		cfg.ServiceContextID = s
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
		table.add(rt)
	}
	cfg.Routes = table

	var needAPIs, needSCID bool
	for _, rt := range table.routes {
		switch {
		case rt.Edge == edgeAPIInfo || rt.Edge == edgeAPIList:
			needAPIs = true
		case strings.HasPrefix(rt.Edge, "bind-context-"):
			needSCID = true
		}
	}
	if needSCID && cfg.ServiceContextID == "" {
		return nil, fmt.Errorf("current_service_context_id ausente: as respostas da C.6.8 respondidas pela borda o devolvem")
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

	switch mode := strings.TrimSpace(getenv("AUTH_ENFORCE")); mode {
	case "", modeWarn:
		cfg.Mode = modeWarn
	case modeEnforce:
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
