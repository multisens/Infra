package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// P4 (decisao do Luis em 10/10, "Funcoes + auditoria"): teste ESTATICO — a
// borda so grava no Redis por FCALL das funcoes da biblioteca tv30
// (tv30_bind_registra, tv30_bind_remove; redis.go), que conferem o esquema
// (infra/redis/esquema.json). Falha se o codigo (sem os _test.go) tiver
// algum texto que seja nome de comando de escrita do Redis — RPUSH, LREM,
// SET, HSET, DEL, EVAL, MULTI... Roda no build da imagem (Dockerfile), sem o
// resto do repositorio; a mesma regra roda em infra/redis/test/escritas.js,
// que tambem confere as funcoes chamadas contra o esquema.json.
// Mesma lista de infra/redis/test/escritas.js (COMANDOS_ESCRITA).
var comandosEscrita = strings.Fields(`APPEND BITFIELD BITOP BLMOVE BLMPOP BLPOP BRPOP BRPOPLPUSH BZMPOP BZPOPMAX BZPOPMIN
 COPY DECR DECRBY DEL EVAL EVALSHA EVAL_RO EVALSHA_RO EXEC EXPIRE EXPIREAT FLUSHALL FLUSHDB FUNCTION GEOADD
 GETDEL GETEX GETSET HDEL HEXPIRE HINCRBY HINCRBYFLOAT HMSET HPERSIST HPEXPIRE HSET HSETNX INCR INCRBY
 INCRBYFLOAT LINSERT LMOVE LMPOP LPOP LPUSH LPUSHX LREM LSET LTRIM MIGRATE MOVE MSET MSETNX MULTI PERSIST
 PEXPIRE PEXPIREAT PFADD PFMERGE PSETEX RENAME RENAMENX RESTORE RPOP RPOPLPUSH RPUSH RPUSHX SADD SCRIPT
 SDIFFSTORE SET SETBIT SETEX SETNX SETRANGE SINTERSTORE SMOVE SORT SPOP SREM SUNIONSTORE SWAPDB UNLINK XADD
 XAUTOCLAIM XCLAIM XDEL XGROUP XTRIM ZADD ZDIFFSTORE ZINCRBY ZINTERSTORE ZMPOP ZPOPMAX ZPOPMIN ZRANGESTORE
 ZREM ZREMRANGEBYLEX ZREMRANGEBYRANK ZREMRANGEBYSCORE ZUNIONSTORE`)

// funcoes que a borda pode chamar (familia bind-context:{serviceId})
var funcoesDaBorda = map[string]bool{"tv30_bind_registra": true, "tv30_bind_remove": true}

var nomeDeFuncao = regexp.MustCompile(`^tv30_[a-z_]+$`)

func TestEscritasSoPorFuncoes(t *testing.T) {
	proibidos := map[string]bool{}
	for _, c := range comandosEscrita {
		proibidos[c] = true
	}
	arquivos, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	lidos := 0
	usadas := map[string]bool{}
	for _, f := range arquivos {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		arq, err := parser.ParseFile(fset, f, nil, 0) // sem comentarios
		if err != nil {
			t.Fatal(err)
		}
		lidos++
		ast.Inspect(arq, func(n ast.Node) bool {
			if _, ok := n.(*ast.ImportSpec); ok {
				return false // caminho de import ("sort" nao e o comando SORT)
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if proibidos[strings.ToUpper(v)] {
				t.Errorf("%s: comando de escrita %q no codigo da borda; a escrita sai so por FCALL (infra/redis/esquema.json)", fset.Position(lit.Pos()), v)
			}
			if nomeDeFuncao.MatchString(v) {
				usadas[v] = true
				if !funcoesDaBorda[v] {
					t.Errorf("%s: funcao %q nao e da borda (so %v)", fset.Position(lit.Pos()), v, funcoesDaBorda)
				}
			}
			return true
		})
	}
	if lidos < 5 {
		t.Fatalf("so %d arquivo(s) .go lidos", lidos)
	}
	for f := range funcoesDaBorda {
		if !usadas[f] {
			t.Errorf("funcao %s sem chamada no codigo", f)
		}
	}
}
