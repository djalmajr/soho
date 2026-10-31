package archtest

// The peer sessions default path: oneSnapshot and listMachines take an
// injectable runner (SnapshotOptions.Run). When it is nil — the default
// path — they must reach Herdr through the backend's raw methods
// (Legacy.SnapshotRaw, Legacy.MachineListRaw) and make no direct herdr run.
// The rule-1 allowlist entries for both functions exempt the whole
// function, so the plain boundary test would still pass a default path that
// runs herdr directly; this pin reads the source of
// internal/peer/sessions.go and checks the default branch — the else of the
// opts.Run != nil test, or the whole body when the seam is gone — for every
// direct-call form the boundary rules forbid, and requires the backend raw
// call.
//
// It fails when the default path runs herdr directly (the rule 1 form with
// any helper, platform.RunCli, platform.RunExecutable, or exec.Command) or
// when it stops calling the backend raw method.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// runSeamCondition reports whether cond is the opts.Run != nil test that
// selects the injected runner.
func runSeamCondition(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	var other ast.Expr
	switch {
	case isRunSelector(bin.X):
		other = bin.Y
	case isRunSelector(bin.Y):
		other = bin.X
	default:
		return false
	}
	id, ok := other.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isRunSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Run"
}

// directHerdrCall names the direct-call form of call that the default path
// must not contain: the rule 1 form (first string argument "herdr", any
// helper), a platform.RunCli/RunExecutable call, or an exec.Command call.
func directHerdrCall(call *ast.CallExpr) string {
	if value, ok := firstStringLit(call); ok && value == "herdr" {
		return `the rule 1 form (first argument "herdr")`
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch {
	case pkg.Name == "platform" && (sel.Sel.Name == "RunCli" || sel.Sel.Name == "RunExecutable"):
		return "platform." + sel.Sel.Name
	case pkg.Name == "exec" && sel.Sel.Name == "Command":
		return "exec.Command"
	}
	return ""
}

func TestPeerSessionsDefaultPathUsesTheBackend(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "internal", "peer", "sessions.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	cases := []struct {
		function string
		raw      string // the backend raw method the default path must call
	}{
		{"oneSnapshot", "SnapshotRaw"},
		{"listMachines", "MachineListRaw"},
	}
	for _, c := range cases {
		t.Run(c.function, func(t *testing.T) {
			var body *ast.BlockStmt
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == c.function {
					body = fd.Body
				}
			}
			if body == nil {
				t.Fatalf("%s not found in %s", c.function, path)
				return
			}
			// The default path is the whole body except the then block of
			// the runner seam (`if opts.Run != nil { … }`): a direct run
			// before, after or around the seam is on the default path too.
			seamThen := map[*ast.BlockStmt]bool{}
			ast.Inspect(body, func(n ast.Node) bool {
				if stmt, ok := n.(*ast.IfStmt); ok && runSeamCondition(stmt.Cond) {
					seamThen[stmt.Body] = true
				}
				return true
			})
			found := false
			ast.Inspect(body, func(n ast.Node) bool {
				if blk, ok := n.(*ast.BlockStmt); ok && seamThen[blk] {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if kind := directHerdrCall(call); kind != "" {
					t.Errorf("%s default path at %s makes a direct herdr run (%s); the default path must go through the backend",
						c.function, fset.Position(call.Pos()), kind)
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == c.raw {
					found = true
				}
				return true
			})
			if !found {
				t.Errorf("%s default path never calls the backend %s", c.function, c.raw)
			}
		})
	}
}
