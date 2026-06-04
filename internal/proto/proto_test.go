package proto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestWireTypesMirroredInTS guards the hand-maintained TypeScript mirror
// (docs/protocol.md, CLAUDE.md): every T* discriminator constant declared in
// proto.go must have its string value present in client/src/proto/events.ts.
// A new c2s/s2c frame type therefore can't be added on the Go side without the
// client side at least learning its name — the most common mirror drift.
//
// It parses proto.go for `T<Name> = "<value>"` string constants rather than
// hardcoding the list, so the guard stays correct as the protocol grows.
func TestWireTypesMirroredInTS(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "proto.go", nil, 0)
	if err != nil {
		t.Fatalf("parse proto.go: %v", err)
	}

	values := map[string]string{} // const name -> string value
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "T") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err == nil {
					values[name.Name] = v
				}
			}
		}
	}
	if len(values) == 0 {
		t.Fatal("found no T* string constants in proto.go — parser or naming changed")
	}

	tsPath := filepath.Join("..", "..", "client", "src", "proto", "events.ts")
	b, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("read events.ts: %v", err)
	}
	ts := string(b)

	for name, val := range values {
		// The quotes pin the match to a full string literal, so e.g. "msg"
		// can't spuriously satisfy itself against "msg:send".
		if !strings.Contains(ts, `"`+val+`"`) {
			t.Errorf("proto.%s = %q is not present in events.ts — TS mirror drift", name, val)
		}
	}
}
