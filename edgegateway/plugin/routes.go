package main

import (
	"fmt"
	"strings"
)

// Credential required by a route, the "auth" field of routes.json.
const (
	// No credential.
	authNone = "none"
	// Access token.
	authToken = "token"
	// Access token and bind-token.
	authTokenBind = "token+bind"
)

// Client classes, the values of the access token claim `class`.
const (
	classAssociated = "associated"
	classStandAlone = "standAlone"
	classNonLocal   = "nonLocal"
)

func validClass(c string) bool {
	return c == classAssociated || c == classStandAlone || c == classNonLocal
}

type segment struct {
	lit string
	// Parameter name of a {param} segment; empty for a literal segment.
	param string
}

type route struct {
	Method string
	Path   string
	Auth   string
	// Classes allowed to use the route; nil allows all classes.
	Classes []string
	// Name of the edgeHandlers entry that answers the route at the edge;
	// empty when the route is forwarded.
	Edge string
	// Error code the route answers on this surface to any request, in both
	// modes (106 on the external surface, C.4.1.8); 0 for none.
	SurfaceError int
	// The route is one a non-local client may use over HTTP (C.6.1.2 and
	// C.6.1.3, C.4.1.6); only read when NonLocalHTTPSOnly is set.
	NonLocalHTTP bool
	// Error code when no DTV service is selected, checked before the
	// bind-token (300); 0 leaves the case to the bind-token check, which
	// gives 108.
	NoService int
	// Error code when {serviceContextId} is not in the Application Catalog,
	// checked after 300 and before the bind-token (304, Tables C.71 and
	// C.74); 0 for no check.
	NotInCatalog int
	// Served versions, in ascending order; 2.0 when absent from routes.json.
	Versions []string
	segs     []segment
}

func newRoute(method, path, auth string, classes []string) (*route, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return nil, fmt.Errorf("method ausente")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q nao comeca com /", path)
	}
	if internalPrefix.MatchString(path) {
		return nil, fmt.Errorf("path %q com o prefixo /v<X.Y>, reservado ao caminho interno", path)
	}
	switch auth {
	case authNone, authToken, authTokenBind:
	default:
		return nil, fmt.Errorf("auth %q invalido (none, token ou token+bind)", auth)
	}
	for _, c := range classes {
		if !validClass(c) {
			return nil, fmt.Errorf("classe %q invalida (%s, %s ou %s)", c, classAssociated, classStandAlone, classNonLocal)
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

// allows reports whether the class may use the route. An empty class, an
// unidentified client, is allowed if the route admits any class other than
// associated.
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

func (rt *route) hasParam(name string) bool {
	for _, s := range rt.segs {
		if s.param == name {
			return true
		}
	}
	return false
}

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

// match finds the route for method and path, and returns the path
// parameters. A literal segment takes precedence over {param} at the same
// position: GET /tv3/current-service/users/files matches the literal route,
// not /tv3/current-service/users/{userid}. A trailing slash is ignored.
// Returns nil if no route matches.
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

// pathDeclared reports whether the path matches a route of the table with
// any method.
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

// moreSpecific reports whether a is more specific than b: at the first
// position where one segment is literal and the other is not, the literal
// wins.
func moreSpecific(a, b *route) bool {
	for i := range a.segs {
		al, bl := a.segs[i].param == "", b.segs[i].param == ""
		if al != bl {
			return al
		}
	}
	return false
}
