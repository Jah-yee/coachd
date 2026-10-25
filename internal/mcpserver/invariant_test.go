package mcpserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestNoRunnerOnlyPaths scans the agent-facing packages' and the device
// API's sources and fails if they import runner-only packages or call
// runner-only store methods.
func TestNoRunnerOnlyPaths(t *testing.T) {
	agentImports := []string{
		"github.com/stefiix92/coachd/internal/web",
		"github.com/stefiix92/coachd/internal/passkey",
		"github.com/stefiix92/coachd/internal/fitworkout",
		"github.com/stefiix92/coachd/internal/deviceapi",
	}
	agentCalls := []string{"ApproveProposal", "UpdateRunnerNote", "DeleteNote", "EnrollPasskey", "RevokePasskey", "CreateSession", "UsePasskey",
		"ConfirmPairing", "ClaimPairing", "RecordFetch", "AckDelivery", "RevokeDevice"}
	// The watch may claim its pairing, fetch and acknowledge; it never
	// approves, transitions, writes notes, messages or passkeys, confirms a
	// pairing or revokes a device.
	deviceCalls := []string{"ApproveProposal", "TransitionProposal", "UpdateRunnerNote", "DeleteNote", "InsertNote", "InsertMessage",
		"EnrollPasskey", "RevokePasskey", "CreateSession", "UsePasskey", "ConfirmPairing", "RevokeDevice"}
	pkgs := []struct {
		dir     string
		imports []string
		calls   []string
	}{
		{".", agentImports, append(slices.Clone(agentCalls), "TransitionProposal")},
		{"../proposals", agentImports, agentCalls},
		{"../deviceapi", nil, deviceCalls},
	}
	for _, pkg := range pkgs {
		files, err := filepath.Glob(filepath.Join(pkg.dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources in %s: %v", pkg.dir, err)
		}
		fset := token.NewFileSet()
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range pkg.imports {
					if path == bad {
						t.Errorf("%s: imports %s", fset.Position(imp.Pos()), path)
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				if slices.Contains(pkg.calls, name) {
					t.Errorf("%s: calls %s", fset.Position(sel.Sel.Pos()), name)
				}
				return true
			})
		}
	}
}
