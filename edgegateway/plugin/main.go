// tv30-auth: plugin http-server do KrakenD que decide, na borda, credencial,
// tipo de cliente e versao de cada requisicao (handler.go), responde as APIs
// da propria borda (edge.go) e repassa as demais ao roteador. Ver README.md.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
)

const pluginName = "tv30-auth"

// HandlerRegisterer eh o simbolo que o KrakenD procura no .so de um plugin
// do tipo http-server.
var HandlerRegisterer = registerer(pluginName)

type registerer string

func (r registerer) RegisterHandlers(f func(
	name string,
	handler func(context.Context, map[string]interface{}, http.Handler) (http.Handler, error),
)) {
	f(string(r), r.registerHandlers)
}

func (r registerer) registerHandlers(_ context.Context, extra map[string]interface{}, next http.Handler) (http.Handler, error) {
	cfg, err := loadConfig(extra[string(r)], os.Getenv)
	if err != nil {
		// Com erro no registro o KrakenD so avisa e segue sem o plugin, e a
		// borda ficaria sem validacao; encerrar derruba o container inteiro.
		logf("FATAL configuracao invalida: %v", err)
		os.Exit(1)
	}
	if string(cfg.Secret) == devSecret {
		logf("AVISO surface=%s JWT_SECRET eh o padrao de desenvolvimento do compose — nao usar em producao", cfg.Surface)
	}
	edge := 0
	for _, rt := range cfg.Routes.routes {
		if rt.Edge != "" {
			edge++
		}
	}
	logf("registrado surface=%s modo=%s rotas=%d respondidas_pela_borda=%d apis=%d redis=%s issuer=%s",
		cfg.Surface, cfg.Mode, len(cfg.Routes.routes), edge, len(cfg.APIs.list), cfg.RedisAddr, cfg.Issuer)

	store := &redisStore{c: newRedisClient(cfg.RedisAddr, cfg.RedisTimeout)}
	return newHandler(cfg, store, next), nil
}

var logger = log.New(os.Stdout, "", 0)

func logf(format string, args ...interface{}) {
	logger.Print("[tv30-auth] " + fmt.Sprintf(format, args...))
}

func main() {}
