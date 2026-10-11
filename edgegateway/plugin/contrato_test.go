package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// P4 (decisao do Luis em 10/10): CONTRATO entre o redisStore da borda e as
// funcoes do Redis de verdade. So roda com TV30_REDIS_TESTE=host:porta de um
// Redis com a biblioteca tv30 carregada — infra/redis/test/run.sh contrato
// sobe um descartavel e chama este teste; sem a variavel (go test do build da
// imagem), pula. Prova: o formato que a borda grava passa no esquema; entrada
// fora do esquema volta como recusa (schemaRefusal) sem gravar; a API C.6.8
// inteira (POST, GET por bind-token, DELETE) funciona sobre as funcoes.
func TestContratoFuncoes(t *testing.T) {
	addr := os.Getenv("TV30_REDIS_TESTE")
	if addr == "" {
		t.Skip("sem TV30_REDIS_TESTE (rode bash infra/redis/test/run.sh contrato)")
	}
	s := &redisStore{c: newRedisClient(addr, 2*time.Second)}
	const sid = "urn:tv30:test:contrato-go"

	// o que o bindRegister grava (encodeJSON de storedEntry), com PEM de varias linhas
	entry, err := encodeJSON(storedEntry{Alg: "RS256", Key: "-----BEGIN PUBLIC KEY-----\nMFww\n-----END PUBLIC KEY-----", RegisteredAt: 1760000000000})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddBindKey(sid, string(entry)); err != nil {
		t.Fatalf("entrada da borda recusada pelo esquema: %v", err)
	}
	for _, bad := range []string{`{"alg":"none","key":"k","registeredAt":1}`, `nao-json`, `{"alg":"HS256","key":"","registeredAt":1}`, `{"alg":"HS256","key":"k"}`} {
		msg, ok := schemaRefusal(s.AddBindKey(sid, bad))
		if !ok || !strings.HasPrefix(msg, "TV30ESQUEMA tv30_bind_registra: bind-context:"+sid+" entrada: ") {
			t.Errorf("entrada %s: esperada recusa do esquema, veio %q", bad, msg)
		}
	}
	if _, ok := schemaRefusal(s.AddBindKey("", string(entry))); !ok {
		t.Error("serviceId vazio deveria ser recusado")
	}
	if l, err := s.BindKeys(sid); err != nil || len(l) != 1 || l[0] != string(entry) {
		t.Fatalf("depois das recusas: %q %v", l, err)
	}
	if n, err := s.RemoveBindKey(sid, string(entry)); err != nil || n != 1 {
		t.Fatalf("remocao: %d %v", n, err)
	}
	if n, err := s.RemoveBindKey(sid, string(entry)); err != nil || n != 0 {
		t.Fatalf("remocao repetida: %d %v", n, err)
	}

	// API C.6.8 sobre o Redis real: servico corrente e origem associada
	// gravados pelas funcoes (no lugar do tv3ws e do AoP)
	must := func(nome string, args ...string) {
		t.Helper()
		if _, err := s.c.fcall(true, nome, args...); err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
	}
	must("tv30_sessao_servico_id", sid)
	must("tv30_origem_registra", tAssoc, sid)
	defer func() {
		s.c.fcall(true, "tv30_origem_remove", tAssoc)
		s.c.fcall(true, "tv30_sessao_servico_id", "")
	}()
	h := newTestHandler(t, modeEnforce, nil, nil)
	h.store = s
	wantOK(t, "POST", postBind(t, h, regBody("HS512", "segredo-contrato")), okRegister)
	wantOK(t, "POST repetido", postBind(t, h, regBody("HS512", "segredo-contrato")), okRegister)
	if l, _ := s.BindKeys(sid); len(l) != 1 || !strings.HasPrefix(l[0], `{"alg":"HS512","key":"segredo-contrato","registeredAt":`) {
		t.Fatalf("bind-context:%s = %q", sid, l)
	}
	bt := signJWT(t, "HS512", []byte("segredo-contrato"), timeClaims(t0, t0, t0.Add(time.Hour)))
	r := bindGet(t, h, bt)
	if r.status != 200 || !strings.Contains(r.body, `"boundServices":[{"serviceContextId":"`+tSCID+`"`) {
		t.Fatalf("GET bind-context: %d %s", r.status, r.body)
	}
	wantOK(t, "DELETE", bindDelete(t, h, "segredo-contrato"), `{}`)
	if l, _ := s.BindKeys(sid); len(l) != 0 {
		t.Fatalf("lista depois do DELETE: %q", l)
	}
	t.Logf("contrato: entrada da borda aceita, %d entradas invalidas recusadas, API C.6.8 completa sobre as funcoes", 5)
}
