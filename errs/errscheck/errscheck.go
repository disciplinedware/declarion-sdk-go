// Package errscheck is the call-site gate for errs.New: a code is a plain
// string, so the compiler cannot catch a typo and this reads the AST instead.
// It reads LITERALS - a code held in a variable is invisible to it, and such a
// type arrives undeclared and takes the fallback rendering.
//
// An application calls Check once from a test, against the catalogue its own
// loader produces in the same process, so nothing can be stale. It lives
// apart from errs so a sidecar does not link the Go parser.
package errscheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

const ImportPath = "github.com/disciplinedware/declarion-sdk-go/errs"

// Finding is one call site the gate refuses.
type Finding struct {
	Pos     string
	Code    string // empty when the call site's code is not a literal
	Message string
}

func (f Finding) String() string {
	if f.Code == "" {
		return f.Pos + ": " + f.Message
	}
	return f.Pos + ": " + f.Code + ": " + f.Message
}

type Options struct {
	// Directory NAMES pruned anywhere below the root.
	SkipDirs []string
	// Member names permitted on any type - the platform's own container
	// members, which no error type declares.
	ExemptFields []string
}

// Check walks every Go file under root and returns one Finding per refused
// call site, sorted by position. An empty catalogue is itself a failure: a
// gate reporting ok having read nothing is the cheapest possible lie.
func Check(root string, cat errs.Catalogue, opts Options) ([]Finding, error) {
	if len(cat) == 0 {
		return nil, fmt.Errorf("errscheck: empty catalogue - the gate read nothing and must not report ok")
	}

	skip := map[string]bool{".git": true, "node_modules": true, "testdata": true, "vendor": true}
	for _, d := range opts.SkipDirs {
		skip[d] = true
	}
	exempt := map[string]bool{}
	for _, f := range opts.ExemptFields {
		exempt[f] = true
	}

	fset := token.NewFileSet()
	var findings []Finding

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Never prune the root: a caller may point the gate at a fixture
			// directory whose own name is on the skip list.
			if path != root && skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// A test proving the undeclared-type fallback has to raise one, so
		// test files are out of scope.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("errscheck: parse %s: %w", path, perr)
		}
		pkgName, ok := importedAs(file, ImportPath)
		if !ok {
			return nil
		}
		findings = append(findings, checkFile(fset, file, pkgName, cat, exempt)...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].Pos < findings[j].Pos })
	return findings, nil
}

func importedAs(file *ast.File, path string) (string, bool) {
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || p != path {
			continue
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				return "", false
			}
			return spec.Name.Name, true
		}
		return "errs", true
	}
	return "", false
}

func checkFile(fset *token.FileSet, file *ast.File, pkgName string, cat errs.Catalogue, exempt map[string]bool) []Finding {
	var out []Finding

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSelector(call.Fun, pkgName, "New") {
			return true
		}
		pos := fset.Position(call.Pos()).String()

		if len(call.Args) == 0 {
			out = append(out, Finding{Pos: pos, Message: "errs.New called with no code"})
			return true
		}
		code, ok := stringLiteral(call.Args[0])
		if !ok {
			// A code the gate cannot read - a parameter of a generic writer,
			// or one assembled at runtime. Such a type simply arrives
			// undeclared and takes the fallback rendering; nothing catches it
			// here, by design.
			return true
		}
		if !errs.ValidCode(code) {
			out = append(out, Finding{Pos: pos, Code: code, Message: "not spelled <owner>.<name> in lower_snake"})
			return true
		}
		def, declared := cat.Lookup(code)
		if !declared {
			out = append(out, Finding{Pos: pos, Code: code, Message: "no module declares this type"})
			return true
		}
		if len(call.Args) > 2 {
			out = append(out, Finding{Pos: pos, Code: code, Message: "at most one Args is legal"})
			return true
		}
		if len(call.Args) == 2 {
			out = append(out, checkArgs(fset, call.Args[1], pos, code, def, exempt)...)
		}
		return true
	})

	return out
}

func checkArgs(fset *token.FileSet, arg ast.Expr, pos, code string, def *errs.TypeDef, exempt map[string]bool) []Finding {
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		// A variable or call result: unreadable keys, and refusing it would
		// ban a legitimate shape.
		return nil
	}
	var out []Finding
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		member, ok := stringLiteral(kv.Key)
		if !ok {
			continue
		}
		memberPos := fset.Position(kv.Pos()).String()
		if exempt[member] {
			continue
		}
		if !errs.ValidMemberName(member) {
			out = append(out, Finding{Pos: memberPos, Code: code, Message: fmt.Sprintf("member %q is not a legal name (letter first, ALPHA/DIGIT/_, three characters or longer)", member)})
			continue
		}
		if _, declared := def.Fields[member]; !declared {
			out = append(out, Finding{Pos: memberPos, Code: code, Message: fmt.Sprintf("member %q is not declared by this type", member)})
		}
	}
	return out
}

func isSelector(fun ast.Expr, pkgName, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkgName
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
