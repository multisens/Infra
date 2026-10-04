// tv30-auth — plugin http-server do KrakenD (edgegateway): validacao de
// credenciais na BORDA (item 9; decisao da reuniao de 28/09, D1: o tv3ws so
// implementa as APIs; quem decide se a requisicao chega nele e este plugin).
// Estado corrente, nao a decisao: o tv3ws ainda responde 107 a Authorization
// invalido e 106 ao nao local que chega por HTTP (tv3ws/src/middleware/
// authorization.ts e basic.ts), nos dois modos — a limpeza la esta pendente.
//
// Toda resposta leva Access-Control-Allow-Origin: * (C.4.1.9.2). O preflight
// CORS (OPTIONS com Access-Control-Request-Method) passa sempre ao modulo
// CORS; o OPTIONS sem esse cabecalho numa API declarada eh respondido aqui
// (C.4.1.9.3). O resto passa, nesta ordem, por:
//
//  1. rota: metodo+caminho tem que estar declarado na tabela unica
//     (routes.json) para esta superficie; senao erro 100, sem repassar ao
//     roteador (isso elimina o panic do Gin registrado em KNOWN-ISSUES);
//  2. classe do cliente (106): claim `class` do access token; sem token, o
//     Origin em origins:associated identifica o local associado;
//  3. access token (107): HS256, segredo JWT_SECRET, exp obrigatorio, nbf se
//     houver, iss == JWT_ISSUER; cliente em clients:blocked (C.4.2.2);
//  4. bind-token (104/108): so nas rotas "token+bind" (campo "Security
//     requirements" = shall da norma), contra as chaves registradas pela
//     emissora do servico corrente (C.6.8, bind-context:{serviceId}).
//
// Modo (AUTH_ENFORCE): warn (padrao) nao bloqueia nada de credencial — loga
// "[tv30-auth] WARN ..." e acrescenta o cabecalho X-TV30-Auth-Warn: <codigo>;
// enforce responde 404 + corpo C.3.2. O erro 100 (rota nao declarada) e o
// 200 (panic do roteador, ou resposta 5xx do KrakenD — backend lento ou
// fora do ar, que sairia como 500 sem corpo) valem nos dois modos: nao ha
// cliente que dependa deles (a rota nao declarada nunca chegou ao tv3ws, e
// o tv3ws nunca responde 5xx).
//
// SOMENTE biblioteca padrao (ver go.mod).
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
		// Morre-inteiro (D9): quando o registro devolve erro, o KrakenD so
		// registra um aviso e segue SEM o plugin — a borda ficaria no ar sem
		// validacao nenhuma, em silencio. Encerrar o processo faz o
		// entrypoint derrubar o container inteiro (e o log diz por que).
		logf("FATAL configuracao invalida: %v", err)
		os.Exit(1)
	}
	if string(cfg.Secret) == devSecret {
		logf("AVISO surface=%s JWT_SECRET eh o padrao de desenvolvimento do compose — nao usar em producao", cfg.Surface)
	}
	logf("registrado surface=%s modo=%s rotas=%d redis=%s issuer=%s",
		cfg.Surface, cfg.Mode, len(cfg.Routes.routes), cfg.RedisAddr, cfg.Issuer)

	store := &redisStore{c: newRedisClient(cfg.RedisAddr, cfg.RedisTimeout)}
	return newHandler(cfg, store, next), nil
}

var logger = log.New(os.Stdout, "", 0)

func logf(format string, args ...interface{}) {
	logger.Print("[tv30-auth] " + fmt.Sprintf(format, args...))
}

func main() {}
