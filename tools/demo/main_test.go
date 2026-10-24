package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The demo opens its store with the blob store of its own data directory,
// as openApp does: every writer of a hash then checks that its file is
// there. cmd/paperless-go's TestOnlyOpenAppOpensTheStore allows the call
// in run; this checks its argument, which that guard does not.
func TestStoreOpensWithTheDataDirectory(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// call reports whether e is pkg.name(arg) with arg the identifier want.
	call := func(e ast.Expr, pkg, name, want string) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) == 0 {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != pkg {
			return false
		}
		id, ok := c.Args[0].(*ast.Ident)
		return ok && id.Name == want
	}
	var opens, blobs int
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		switch {
		case ok && id.Name == "store" && sel.Sel.Name == "Open":
			opens++
			if len(c.Args) != 3 || !call(c.Args[2], "blobstore", "OpenFiles", "dataDir") {
				t.Errorf("store.Open is not given blobstore.OpenFiles(dataDir)")
			}
		case ok && id.Name == "blobstore" && sel.Sel.Name == "New":
			blobs++
			if !call(c, "blobstore", "New", "dataDir") {
				t.Errorf("blobstore.New is not given dataDir")
			}
		}
		return true
	})
	if opens != 1 || blobs != 1 {
		t.Errorf("store.Open called %d times and blobstore.New %d times in main.go, want once each", opens, blobs)
	}
}
