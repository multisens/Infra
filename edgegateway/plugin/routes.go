package main

import (
	"fmt"
	"strings"
)

// Politica por rota (routes.json, campos "auth" e "classes").
const (
	authNone      = "none"       // sem credencial (health, manifest, authorize, token, POST/DELETE bind-context)
	authToken     = "token"      // access token
	authTokenBind = "token+bind" // access token + bind-token ("Security requirements" = shall)
)

// Classes de cliente: EXATAMENTE os valores que o tv3ws grava no claim
// `class` (tv3ws/src/modules/auth-manager/client.ts, ClientClass).
const (
	classAssociated = "local-associated"
	classAutonomous = "local-autonomous"
	classNonLocal   = "non-local"
)

func validClass(c string) bool {
	return c == classAssociated || c == classAutonomous || c == classNonLocal
}

type segment struct {
	lit   string
	param string // != "" => segmento {param}
}

type route struct {
	Method  string
	Path    string
	Auth    string
	Classes []string // nil = todas as classes
	segs    []segment
}

func newRoute(method, path, auth string, classes []string) (*route, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return nil, fmt.Errorf("method ausente")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q nao comeca com /", path)
	}
	switch auth {
	case authNone, authToken, authTokenBind:
	default:
		return nil, fmt.Errorf("auth %q invalido (none, token ou token+bind)", auth)
	}
	for _, c := range classes {
		if !validClass(c) {
			return nil, fmt.Errorf("classe %q invalida (%s, %s ou %s)", c, classAssociated, classAutonomous, classNonLocal)
		}
	}
	rt := &route{Method: method, Path: path, Auth: auth, Classes: classes}
	for _, s := range splitPath(path) {
		if strings.HasPrefix(s, "{") || strings.HasSuffix(s, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
			if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") || name == "" || strings.ContainsAny(name, "{}") {
				return nil, fmt.Errorf("segmento %q malformado em %s", s, path)
			}
			rt.segs = append(rt.segs, segment{param: name})
			continue
		}
		if s == "" {
			return nil, fmt.Errorf("segmento vazio em %s", path)
		}
		rt.segs = append(rt.segs, segment{lit: s})
	}
	return rt, nil
}

// allows diz se a classe pode usar a rota. Classe vazia = nao identificada
// (sem token valido e sem Origin associado): nao eh o local associado, mas
// pode ser autonomo ou nao local — passa se a rota admite alguma dessas
// (a exigencia de token, se houver, vem depois e da 107).
func (rt *route) allows(class string) bool {
	if rt.Classes == nil {
		return true
	}
	for _, c := range rt.Classes {
		if class != "" && c == class {
			return true
		}
		if class == "" && c != classAssociated {
			return true
		}
	}
	return false
}

func (rt *route) String() string { return rt.Method + " " + rt.Path }

type routeTable struct {
	routes []*route
}

func (t *routeTable) add(rt *route) { t.routes = append(t.routes, rt) }

func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// match casa metodo+caminho com a tabela. Um segmento literal tem
// precedencia sobre {param} na mesma posicao (como no roteador do KrakenD):
// GET /tv3/current-service/users/files casa a rota literal, nao
// /tv3/current-service/users/{userid}. Uma barra final eh tolerada (o
// roteador redireciona; a requisicao redirecionada volta a passar aqui).
func (t *routeTable) match(method, path string) (*route, map[string]string) {
	if !strings.HasPrefix(path, "/") {
		return nil, nil
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	segs := splitPath(path)
	var best *route
	for _, rt := range t.routes {
		if rt.Method != method || len(rt.segs) != len(segs) || !rt.matches(segs) {
			continue
		}
		if best == nil || moreSpecific(rt, best) {
			best = rt
		}
	}
	if best == nil {
		return nil, nil
	}
	params := map[string]string{}
	for i, s := range best.segs {
		if s.param != "" {
			params[s.param] = segs[i]
		}
	}
	return best, params
}

// pathDeclared diz se o caminho casa alguma rota da tabela, com qualquer
// metodo (o OPTIONS da C.4.1.9.3 vale para toda API declarada).
func (t *routeTable) pathDeclared(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	segs := splitPath(path)
	for _, rt := range t.routes {
		if len(rt.segs) == len(segs) && rt.matches(segs) {
			return true
		}
	}
	return false
}

func (rt *route) matches(segs []string) bool {
	for i, s := range rt.segs {
		if s.param != "" {
			if segs[i] == "" {
				return false
			}
			continue
		}
		if s.lit != segs[i] {
			return false
		}
	}
	return true
}

// moreSpecific: na primeira posicao em que os dois diferem, literal vence.
func moreSpecific(a, b *route) bool {
	for i := range a.segs {
		al, bl := a.segs[i].param == "", b.segs[i].param == ""
		if al != bl {
			return al
		}
	}
	return false
}
