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

// Static test: the edge writes to Redis only by FCALL of the tv30 functions,
// which check the schema. Fails if the code, without the _test.go files, has
// a string that is the name of a write command.
var comandosEscrita = strings.Fields(`APPEND BITFIELD BITOP BLMOVE BLMPOP BLPOP BRPOP BRPOPLPUSH BZMPOP BZPOPMAX BZPOPMIN
 COPY DECR DECRBY DEL EVAL EVALSHA EVAL_RO EVALSHA_RO EXEC EXPIRE EXPIREAT FLUSHALL FLUSHDB FUNCTION GEOADD
 GETDEL GETEX GETSET HDEL HEXPIRE HINCRBY HINCRBYFLOAT HMSET HPERSIST HPEXPIRE HSET HSETNX INCR INCRBY
 INCRBYFLOAT LINSERT LMOVE LMPOP LPOP LPUSH LPUSHX LREM LSET LTRIM MIGRATE MOVE MSET MSETNX MULTI PERSIST
 PEXPIRE PEXPIREAT PFADD PFMERGE PSETEX RENAME RENAMENX RESTORE RPOP RPOPLPUSH RPUSH RPUSHX SADD SCRIPT
 SDIFFSTORE SET SETBIT SETEX SETNX SETRANGE SINTERSTORE SMOVE SORT SPOP SREM SUNIONSTORE SWAPDB UNLINK XADD
 XAUTOCLAIM XCLAIM XDEL XGROUP XTRIM ZADD ZDIFFSTORE ZINCRBY ZINTERSTORE ZMPOP ZPOPMAX ZPOPMIN ZRANGESTORE
 ZREM ZREMRANGEBYLEX ZREMRANGEBYRANK ZREMRANGEBYSCORE ZUNIONSTORE`)

// Functions the edge may call (bind-context:{serviceId} family).
var edgeFunctions = map[string]bool{"tv30_bind_register": true, "tv30_bind_remove": true}

var functionName = regexp.MustCompile(`^tv30_[a-z0-9_]+$`)

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
		// Without comments.
		arq, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		lidos++
		ast.Inspect(arq, func(n ast.Node) bool {
			if _, ok := n.(*ast.ImportSpec); ok {
				// An import path ("sort" is not the SORT command).
				return false
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
				t.Errorf("%s: comando de escrita %q no codigo da borda; a escrita sai so por FCALL (infra/redis/schema.json)", fset.Position(lit.Pos()), v)
			}
			if functionName.MatchString(v) {
				usadas[v] = true
				if !edgeFunctions[v] {
					t.Errorf("%s: funcao %q nao e da borda (so %v)", fset.Position(lit.Pos()), v, edgeFunctions)
				}
			}
			return true
		})
	}
	if lidos < 5 {
		t.Fatalf("so %d arquivo(s) .go lidos", lidos)
	}
	for f := range edgeFunctions {
		if !usadas[f] {
			t.Errorf("funcao %s sem chamada no codigo", f)
		}
	}
}
