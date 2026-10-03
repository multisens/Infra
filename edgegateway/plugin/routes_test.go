package main

import "testing"

func mustRoute(t *testing.T, method, path, auth string, classes ...string) *route {
	t.Helper()
	var cl []string
	if classes != nil {
		cl = classes
	}
	rt, err := newRoute(method, path, auth, cl)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func testTable(t *testing.T) *routeTable {
	tb := &routeTable{}
	// ordem proposital: as rotas com {param} antes das literais
	tb.add(mustRoute(t, "GET", "/tv3/{serviceContextId}/users/{userid}", authTokenBind))
	tb.add(mustRoute(t, "GET", "/tv3/current-service/users/{userid}", authTokenBind))
	tb.add(mustRoute(t, "GET", "/tv3/current-service/users/files", authTokenBind))
	tb.add(mustRoute(t, "GET", "/tv3/current-service/users/current-user", authTokenBind))
	tb.add(mustRoute(t, "POST", "/tv3/{serviceContextId}/users", authToken))
	tb.add(mustRoute(t, "POST", "/tv3/current-service/users", authTokenBind))
	tb.add(mustRoute(t, "GET", "/tv3/current-service", authToken))
	tb.add(mustRoute(t, "GET", "/health", authNone))
	tb.add(mustRoute(t, "DELETE", "/tv3/remote-device/{handle}", authToken))
	return tb
}

func TestMatchPrecedenciaLiteral(t *testing.T) {
	tb := testTable(t)
	cases := []struct {
		method, path, want string
		params             map[string]string
	}{
		{"GET", "/tv3/current-service/users/files", "GET /tv3/current-service/users/files", nil},
		{"GET", "/tv3/current-service/users/current-user", "GET /tv3/current-service/users/current-user", nil},
		{"GET", "/tv3/current-service/users/u-42", "GET /tv3/current-service/users/{userid}", map[string]string{"userid": "u-42"}},
		{"GET", "/tv3/urn:svc/users/u-42", "GET /tv3/{serviceContextId}/users/{userid}", map[string]string{"serviceContextId": "urn:svc", "userid": "u-42"}},
		{"POST", "/tv3/current-service/users", "POST /tv3/current-service/users", nil},
		{"POST", "/tv3/c08b2c72/users", "POST /tv3/{serviceContextId}/users", map[string]string{"serviceContextId": "c08b2c72"}},
		{"GET", "/health", "GET /health", nil},
		{"GET", "/tv3/current-service/", "GET /tv3/current-service", nil}, // barra final tolerada
		{"DELETE", "/tv3/remote-device/7", "DELETE /tv3/remote-device/{handle}", map[string]string{"handle": "7"}},
	}
	for _, c := range cases {
		rt, params := tb.match(c.method, c.path)
		if rt == nil {
			t.Errorf("%s %s: nao casou (esperado %s)", c.method, c.path, c.want)
			continue
		}
		if rt.String() != c.want {
			t.Errorf("%s %s: casou %s, esperado %s", c.method, c.path, rt, c.want)
		}
		for k, v := range c.params {
			if params[k] != v {
				t.Errorf("%s %s: param %s=%q, esperado %q", c.method, c.path, k, params[k], v)
			}
		}
	}
}

func TestMatchNaoDeclarado(t *testing.T) {
	tb := testTable(t)
	cases := [][2]string{
		{"GET", "/tv3/abc"},                    // panic do Gin em KNOWN-ISSUES
		{"POST", "/tv3/users"},                 // rota que saiu (item 24)
		{"DELETE", "/health"},                  // metodo nao declarado
		{"GET", "/"},                           // raiz
		{"GET", "/tv3/current-service/users/"}, // so a barra: nao eh {userid} vazio
		{"GET", "/tv3//users/x"},               // segmento vazio nao casa {param}
		{"GET", "/tv3/current-service/users/a/b"},
		{"GET", "relativo"},
	}
	for _, c := range cases {
		if rt, _ := tb.match(c[0], c[1]); rt != nil {
			t.Errorf("%s %s: deveria ser nao declarado, casou %s", c[0], c[1], rt)
		}
	}
}

func TestNewRouteValidacao(t *testing.T) {
	bad := []struct {
		method, path, auth string
		classes            []string
	}{
		{"", "/x", authNone, nil},
		{"GET", "x", authNone, nil},
		{"GET", "/x", "bind", nil},
		{"GET", "/x", authNone, []string{"local-standalone"}}, // valor que o tv3ws NAO usa
		{"GET", "/x/{", authNone, nil},
		{"GET", "/x/{}", authNone, nil},
		{"GET", "/x//y", authNone, nil},
	}
	for _, b := range bad {
		if _, err := newRoute(b.method, b.path, b.auth, b.classes); err == nil {
			t.Errorf("%+v deveria ser rejeitada", b)
		}
	}
}

func TestAllows(t *testing.T) {
	all := mustRoute(t, "GET", "/a", authToken)
	onlyAssoc := mustRoute(t, "POST", "/b", authNone, classAssociated)
	notAssoc := mustRoute(t, "GET", "/c", authNone, classAutonomous, classNonLocal)
	none := &route{Classes: []string{}}
	type c struct {
		rt    *route
		class string
		want  bool
	}
	for i, x := range []c{
		{all, "", true}, {all, classAssociated, true},
		{onlyAssoc, classAssociated, true}, {onlyAssoc, classAutonomous, false}, {onlyAssoc, "", false},
		{notAssoc, classAssociated, false}, {notAssoc, classNonLocal, true}, {notAssoc, "", true},
		{none, "", false}, {none, classNonLocal, false},
	} {
		if got := x.rt.allows(x.class); got != x.want {
			t.Errorf("caso %d: allows(%q)=%v, esperado %v", i, x.class, got, x.want)
		}
	}
}
