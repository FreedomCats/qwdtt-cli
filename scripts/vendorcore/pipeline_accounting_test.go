package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func nodeText(n ast.Node) string {
	var b bytes.Buffer
	if n != nil {
		if err := format.Node(&b, token.NewFileSet(), n); err != nil {
			panic(err)
		}
	}
	return b.String()
}

func exprIs(n ast.Node, shape string) bool {
	expr, err := parser.ParseExpr(shape)
	if err != nil {
		panic(err)
	}
	return nodeText(n) == nodeText(expr)
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func textMapSelector(expr ast.Expr, receiver string) bool {
	field, ok := unparen(expr).(*ast.SelectorExpr)
	if !ok || field.Sel.Name != "text" {
		return false
	}
	base, ok := unparen(field.X).(*ast.Ident)
	return ok && base.Name == receiver
}

func textMapIndex(expr ast.Expr, receiver string) (ast.Expr, bool) {
	index, ok := unparen(expr).(*ast.IndexExpr)
	if !ok || !textMapSelector(index.X, receiver) {
		return nil, false
	}
	return unparen(index.Index), true
}

func isTextMapIndex(expr ast.Expr, receiver, index string) bool {
	key, ok := textMapIndex(expr, receiver)
	ident, isIdent := key.(*ast.Ident)
	return ok && isIdent && ident.Name == index
}

func isTextMapAccess(expr ast.Expr) bool {
	index, ok := unparen(expr).(*ast.IndexExpr)
	if !ok {
		return false
	}
	field, ok := unparen(index.X).(*ast.SelectorExpr)
	return ok && field.Sel.Name == "text"
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := unparen(expr).(*ast.Ident)
	return ok && ident.Name == name
}

func isMapParameterIndex(expr ast.Expr, parameters map[string]bool) bool {
	index, ok := unparen(expr).(*ast.IndexExpr)
	if !ok {
		return false
	}
	ident, ok := unparen(index.X).(*ast.Ident)
	return ok && parameters[ident.Name]
}

func mapParameters(fn *ast.FuncDecl) map[string]bool {
	parameters := map[string]bool{}
	if fn.Type.Params == nil {
		return parameters
	}
	for _, field := range fn.Type.Params.List {
		if _, ok := unparen(field.Type).(*ast.MapType); !ok {
			continue
		}
		for _, name := range field.Names {
			parameters[name.Name] = true
		}
	}
	return parameters
}

func hasDirectTextMapArgument(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		if textMapSelector(argument, "plan") || textMapSelector(argument, "p") {
			return true
		}
	}
	return false
}

func hasPlanTextAlias(lhs, rhs []ast.Expr) bool {
	for i, value := range rhs {
		if !textMapSelector(value, "plan") {
			continue
		}
		if i >= len(lhs) || !isIdent(lhs[i], "_") {
			return true
		}
	}
	return false
}

func payloadSinkInFunction(fn *ast.FuncDecl) string {
	if fn.Body == nil {
		return ""
	}
	sink := ""
	mapParameters := mapParameters(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil || sink != "" {
			return sink == ""
		}
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if isTextMapAccess(lhs) || isMapParameterIndex(lhs, mapParameters) {
					sink = "planned output map write"
					break
				}
				if n.Tok != token.DEFINE && (exprIs(lhs, "text") || exprIs(lhs, "converted")) {
					sink = "text payload mutation"
					break
				}
			}
		case *ast.RangeStmt:
			if isTextMapAccess(n.Key) || isTextMapAccess(n.Value) || isMapParameterIndex(n.Key, mapParameters) || isMapParameterIndex(n.Value, mapParameters) || textMapSelector(n.Key, "plan") || textMapSelector(n.Key, "p") || textMapSelector(n.Value, "plan") || textMapSelector(n.Value, "p") {
				sink = "planned output range assignment"
			} else if exprIs(n.Key, "text") || exprIs(n.Key, "converted") || exprIs(n.Value, "text") || exprIs(n.Value, "converted") {
				sink = "text payload range assignment"
			}
		case *ast.IncDecStmt:
			if isTextMapAccess(n.X) || isMapParameterIndex(n.X, mapParameters) || textMapSelector(n.X, "plan") || textMapSelector(n.X, "p") || exprIs(n.X, "text") || exprIs(n.X, "converted") {
				sink = "text payload mutation"
			}
		case *ast.CallExpr:
			if hasDirectTextMapArgument(n) {
				sink = "planned output map argument"
			} else if (exprIs(n.Fun, "delete") || exprIs(n.Fun, "clear")) && len(n.Args) > 0 &&
				(textMapSelector(n.Args[0], "plan") || textMapSelector(n.Args[0], "p")) {
				sink = "planned output map mutation"
			}
		}
		return sink == ""
	})
	return sink
}

func inRange(stack []ast.Node, table, variable string) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		if loop, ok := stack[i].(*ast.RangeStmt); ok && exprIs(loop.X, table) && exprIs(loop.Value, variable) {
			return true
		}
	}
	return false
}

func inIf(stack []ast.Node, condition string) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		if branch, ok := stack[i].(*ast.IfStmt); ok && exprIs(branch.Cond, condition) {
			// The current statement must be in the then branch, not its else.
			return i+1 < len(stack) && stack[i+1] == branch.Body
		}
	}
	return false
}

func directRangeBody(stack []ast.Node, table string) bool {
	if len(stack) < 3 {
		return false
	}
	loop, ok := stack[len(stack)-3].(*ast.RangeStmt)
	return ok && exprIs(loop.X, table) && stack[len(stack)-2] == loop.Body
}

func topLevelRange(stack []ast.Node, table string, body *ast.BlockStmt) bool {
	for i, n := range stack {
		if loop, ok := n.(*ast.RangeStmt); ok && exprIs(loop.X, table) && i > 0 && stack[i-1] == body {
			return true
		}
	}
	return false
}

// This is a bounded source contract for the converter's payload variables and
// sinks, not a Go evaluator. New shapes fail visibly and need an explicit review.
func validatePipeline(source fs.FS, metadata []accounting) error {
	fset := token.NewFileSet()
	files, err := fs.ReadDir(source, ".")
	if err != nil {
		return err
	}
	var parsed []*ast.File
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		data, err := fs.ReadFile(source, file.Name())
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, "scripts/vendorcore/"+file.Name(), data, 0)
		if err != nil {
			return err
		}
		parsed = append(parsed, f)
	}
	descriptors := map[string]accounting{}
	for _, a := range metadata {
		if _, exists := descriptors[a.id]; exists {
			return fmt.Errorf("%s at scripts/vendorcore/accounting.go: duplicate pipeline metadata", a.id)
		}
		if a.class != "local" {
			return fmt.Errorf("%s at scripts/vendorcore/accounting.go: pipeline must be local", a.id)
		}
		descriptors[a.id] = a
	}
	fail := func(n ast.Node, message string) error {
		return fmt.Errorf("%s: %s", fset.Position(n.Pos()), message)
	}
	markers := map[*ast.CallExpr]string{}
	counts := map[string]int{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !exprIs(call.Fun, "accountedText") || err != nil {
				return err == nil
			}
			if len(call.Args) != 2 {
				err = fail(call, "unsupported accountedText shape")
				return false
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				err = fail(call, "pipeline ID must be a string literal")
				return false
			}
			id, _ := strconv.Unquote(lit.Value)
			if _, exists := descriptors[id]; !exists {
				err = fail(call, id+": missing metadata or unknown pipeline ID")
				return false
			}
			counts[id]++
			if counts[id] > 1 {
				err = fail(call, id+": duplicate pipeline use site")
			}
			markers[call] = id
			return err == nil
		})
	}
	if err != nil {
		return err
	}
	bound := map[*ast.CallExpr]bool{}
	bind := func(expr ast.Expr, id, payload string) error {
		call, ok := expr.(*ast.CallExpr)
		if !ok || markers[call] != id || !exprIs(call.Args[1], payload) {
			return fail(expr, id+": missing or bypassed marker on actual payload; unsupported shape "+nodeText(expr))
		}
		bound[call] = true
		return nil
	}
	functions := map[string]int{}
	roles := map[string]int{}
	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			if name != "readText" && name != "convertFile" && name != "run" && name != "lookup" && name != "set" && name != "accountedText" {
				if sink := payloadSinkInFunction(fn); sink != "" {
					return fail(fn, fmt.Sprintf("unexpected function %s contains %s", name, sink))
				}
				continue
			}
			functions[name]++
			if fn.Body == nil {
				return fail(fn, name+": unsupported missing function body")
			}
			if name == "accountedText" {
				if !exprIs(fn.Type, "func(id, value string) string") || len(fn.Body.List) != 1 {
					return fail(fn, "accountedText must return its value unchanged")
				}
				if _, ok := fn.Body.List[0].(*ast.ReturnStmt); !ok {
					return fail(fn, "accountedText must return its value unchanged")
				}
			}
			var stack []ast.Node
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return false
				}
				stack = append(stack, n)
				if err != nil {
					return false
				}
				switch n := n.(type) {
				case *ast.AssignStmt:
					if name == "run" && len(n.Lhs) == 1 && len(n.Rhs) == 1 && isIdent(n.Rhs[0], "plan") && !isIdent(n.Lhs[0], "plan") {
						err = fail(n, "run: unsupported direct planned output alias")
					}
					if name == "run" && hasPlanTextAlias(n.Lhs, n.Rhs) {
						err = fail(n, "run: unsupported direct planned output alias")
					}
					for _, lhs := range n.Lhs {
						text := exprIs(lhs, "text") || exprIs(lhs, "converted")
						planWrite := textMapSelector(lhs, "plan") || isTextMapIndex(lhs, "plan", "name")
						planWrite = planWrite || name == "set" && (textMapSelector(lhs, "p") || isTextMapIndex(lhs, "p", "n") || isTextMapIndex(lhs, "p", "name"))
						if !planWrite {
							_, planWrite = textMapIndex(lhs, "plan")
						}
						if name == "set" && !planWrite {
							_, planWrite = textMapIndex(lhs, "p")
						}
						if !text && !planWrite && !(name == "readText" && exprIs(lhs, "raw")) &&
							!(name == "convertFile" && exprIs(lhs, "replacement")) && !(name == "run" && exprIs(lhs, "plan")) {
							continue
						}
						if len(n.Rhs) != 1 {
							err = fail(n, name+": unsupported payload assignment")
							break
						}
						rhs := n.Rhs[0]
						supported := false
						switch name {
						case "readText":
							supported = n.Tok == token.DEFINE && exprIs(rhs, "os.ReadFile(path)")
						case "convertFile":
							if exprIs(lhs, "replacement") {
								supported = inRange(stack, "replacements", "r") && n.Tok == token.ASSIGN &&
									(exprIs(rhs, "*r.Replace") || exprIs(rhs, `"${0}\n${1}" + r.InsertAfter`) || exprIs(rhs, `"${1}" + r.InsertBefore + "\n${0}"`))
							} else if n.Tok == token.ASSIGN && exprIs(lhs, "text") {
								switch {
								case inRange(stack, "replacements", "r"):
									supported = directRangeBody(stack, "replacements") && topLevelRange(stack, "replacements", fn.Body) && exprIs(rhs, "re.ReplaceAllString(text, replacement)")
								case inRange(stack, "logReplacements", "pair"):
									supported = directRangeBody(stack, "logReplacements") && topLevelRange(stack, "logReplacements", fn.Body) && exprIs(rhs, "replaceWord(text, pair.from, pair.to)")
								case inRange(stack, "prunableImports", "imp"):
									supported = directRangeBody(stack, "prunableImports") && topLevelRange(stack, "prunableImports", fn.Body) && exprIs(rhs, `line.ReplaceAllString(text, "")`)
								default:
									id, payload := "normalize-input-crlf", `strings.ReplaceAll(text, "\r\n", "\n")`
									call, _ := rhs.(*ast.CallExpr)
									if markers[call] == "rewrite-package-main" || exprIs(rhs, `packageMain.ReplaceAllString(text, "package core")`) {
										id, payload = "rewrite-package-main", `packageMain.ReplaceAllString(text, "package core")`
									}
									err = bind(rhs, id, payload)
									supported = err == nil && stack[len(stack)-2] == fn.Body
								}
							}
						case "run":
							switch {
							case exprIs(lhs, "plan"):
								supported = n.Tok == token.DEFINE && exprIs(rhs, "&planned{text: map[string]string{}}")
							case planWrite:
								supported = n.Tok == token.ASSIGN && isTextMapIndex(lhs, "plan", "name") && exprIs(rhs, "text") && directRangeBody(stack, "plan.names") && topLevelRange(stack, "plan.names", fn.Body)
							case exprIs(lhs, "converted"):
								supported = n.Tok == token.DEFINE && exprIs(rhs, "convertFile(name, text)") && inRange(stack, "sourceNames", "name")
							case exprIs(rhs, `readText(filepath.Join(target, "CORE_VERSION"))`):
								supported = n.Tok == token.DEFINE && inIf(stack, "check")
							case exprIs(rhs, "readText(filepath.Join(source, name))"):
								supported = n.Tok == token.DEFINE && inRange(stack, "sourceNames", "name")
							case inRange(stack, "plan.names", "name"):
								if n.Tok == token.DEFINE {
									err = bind(rhs, "normalize-output-crlf", `strings.ReplaceAll(plan.text[name], "\r\n", "\n")`)
									if err == nil && (!directRangeBody(stack, "plan.names") || !topLevelRange(stack, "plan.names", fn.Body)) {
										err = fail(n, "normalize-output-crlf: marker must assign the actual planned output")
									}
								} else if n.Tok == token.ASSIGN && inIf(stack, `!strings.HasSuffix(text, "\n")`) {
									err = bind(rhs, "ensure-output-newline", `text + "\n"`)
									if err == nil && (!directRangeBody(stack[:len(stack)-2], "plan.names") || !topLevelRange(stack, "plan.names", fn.Body)) {
										err = fail(n, "ensure-output-newline: marker must append to the actual planned output")
									}
								} else {
									err = fail(n, "ensure-output-newline: unsupported payload assignment or missing conditional append")
								}
								supported = err == nil
							}
						case "set":
							supported = n.Tok == token.ASSIGN && (isTextMapIndex(lhs, "p", "n") || isTextMapIndex(lhs, "p", "name")) && exprIs(rhs, "text")
						}
						if !supported && err == nil {
							err = fail(n, name+": unsupported payload assignment "+nodeText(n))
						}
						if supported && name == "convertFile" && exprIs(lhs, "text") {
							for _, table := range []string{"replacements", "logReplacements", "prunableImports"} {
								if directRangeBody(stack, table) {
									roles[table]++
								}
							}
						}
						if supported && name == "run" && planWrite {
							roles["planned-output-copy"]++
						}
					}
					// The two regex producers must still select annotated rule/import payloads.
					if name == "convertFile" && len(n.Rhs) == 1 {
						for _, lhs := range n.Lhs {
							if exprIs(lhs, "re") && (!exprIs(n.Rhs[0], "regexp.Compile(r.Pattern)") || !inRange(stack, "replacements", "r")) ||
								exprIs(lhs, "line") && (!exprIs(n.Rhs[0], "regexp.MustCompile(`(?m)^\\t\"` + regexp.QuoteMeta(imp.path) + `\"\\n`)") || !inRange(stack, "prunableImports", "imp")) {
								err = fail(n, "convertFile: unsupported regex producer bypasses annotated table payload")
							}
						}
					}
				case *ast.ValueSpec:
					if name == "run" {
						lhs := make([]ast.Expr, len(n.Names))
						for i, name := range n.Names {
							lhs[i] = name
						}
						if hasPlanTextAlias(lhs, n.Values) {
							err = fail(n, "run: unsupported direct planned output alias")
						}
						for i, value := range n.Values {
							if i < len(n.Names) && isIdent(value, "plan") && n.Names[i].Name != "plan" {
								err = fail(n, "run: unsupported direct planned output alias")
							}
						}
					}
					if name == "convertFile" {
						for _, declared := range n.Names {
							switch declared.Name {
							case "re":
								if len(n.Names) != 2 || n.Names[1].Name != "err" || len(n.Values) != 1 ||
									!exprIs(n.Values[0], "regexp.Compile(r.Pattern)") || !inRange(stack, "replacements", "r") || !topLevelRange(stack, "replacements", fn.Body) {
									err = fail(n, "convertFile: unsupported regexp producer declaration for annotated replacement pattern")
								}
							case "line":
								if len(n.Names) != 1 || len(n.Values) != 1 ||
									!exprIs(n.Values[0], "regexp.MustCompile(`(?m)^\\t\"` + regexp.QuoteMeta(imp.path) + `\"\\n`)") ||
									!directRangeBody(stack, "prunableImports") || !topLevelRange(stack, "prunableImports", fn.Body) {
									err = fail(n, "convertFile: unsupported regexp producer declaration for annotated import path")
								}
							}
						}
					}
					for _, id := range n.Names {
						if id.Name == "text" || id.Name == "converted" || id.Name == "raw" || id.Name == "plan" ||
							id.Name == "replacement" && (len(n.Values) != 0 || name != "convertFile" || !inRange(stack, "replacements", "r")) {
							err = fail(n, name+": unsupported payload declaration")
						}
					}
				case *ast.RangeStmt:
					if isTextMapAccess(n.Key) || isTextMapAccess(n.Value) || textMapSelector(n.Key, "plan") || textMapSelector(n.Key, "p") || textMapSelector(n.Value, "plan") || textMapSelector(n.Value, "p") ||
						exprIs(n.Key, "text") || exprIs(n.Key, "converted") || exprIs(n.Value, "text") || exprIs(n.Value, "converted") {
						err = fail(n, name+": unsupported payload range assignment")
					}
				case *ast.IncDecStmt:
					if isTextMapAccess(n.X) || textMapSelector(n.X, "plan") || textMapSelector(n.X, "p") || exprIs(n.X, "text") || exprIs(n.X, "converted") {
						err = fail(n, name+": unsupported payload mutation")
					}
				case *ast.ReturnStmt:
					if name == "run" || name == "set" {
						break
					}
					if name == "accountedText" {
						if len(fn.Body.List) != 1 || len(n.Results) != 1 || !exprIs(n.Results[0], "value") {
							err = fail(n, "accountedText must return its value unchanged")
						}
						break
					}
					if len(n.Results) != 2 {
						err = fail(n, name+": unsupported payload return")
						break
					}
					value, result := n.Results[0], n.Results[1]
					switch name {
					case "readText":
						if exprIs(value, `""`) && exprIs(result, "err") && inIf(stack, "err != nil") {
							break
						}
						err = bind(value, "strip-utf8-bom", `string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))`)
						if err == nil && (fn.Body.List[len(fn.Body.List)-1] != n || !exprIs(result, "nil")) {
							err = fail(n, "strip-utf8-bom: marker must return the actual file payload")
						}
					case "convertFile":
						if exprIs(value, `""`) && strings.HasPrefix(nodeText(result), "fmt.Errorf(") {
							break
						}
						if exprIs(value, "text") && exprIs(result, "nil") && fn.Body.List[len(fn.Body.List)-1] == n {
							roles["converted-return"]++
							break
						}
						if exprIs(value, "override") && exprIs(result, "nil") {
							for _, parent := range stack {
								if branch, ok := parent.(*ast.IfStmt); ok && branch.Init != nil && exprIs(branch.Cond, "ok") &&
									fn.Body.List[0] == branch && len(branch.Body.List) == 1 && branch.Body.List[0] == n {
									if init, ok := branch.Init.(*ast.AssignStmt); ok && len(init.Lhs) == 2 && exprIs(init.Lhs[0], "override") && len(init.Rhs) == 1 && exprIs(init.Rhs[0], "lookup(fileOverrides, name)") {
										roles["override-return"]++
										return true
									}
								}
							}
						}
						err = fail(n, "convertFile: unsupported payload return")
					case "lookup":
						if !(exprIs(value, "v.text") && exprIs(result, "true") && inRange(stack, "m", "v") && inIf(stack, "strings.EqualFold(k, name)")) &&
							!(exprIs(value, `""`) && exprIs(result, "false")) {
							err = fail(n, "lookup: unsupported annotated payload return")
						}
					}
				case *ast.CallExpr:
					if name == "run" && hasDirectTextMapArgument(n) && !exprIs(n.Fun, "plan.set") {
						err = fail(n, "run: unsupported direct planned output map argument")
					}
					if name == "run" && (exprIs(n.Fun, "delete") || exprIs(n.Fun, "clear")) && len(n.Args) > 0 && textMapSelector(n.Args[0], "plan") {
						err = fail(n, "run: unsupported planned output map mutation")
					}
					if name == "run" && exprIs(n.Fun, "plan.set") {
						switch {
						case len(n.Args) != 2:
							err = fail(n, "run: unsupported plan.set shape")
						case exprIs(n.Args[0], `"CORE_VERSION"`) && len(stack) >= 3 && stack[len(stack)-3] == fn.Body:
							err = bind(n.Args[1], "generate-core-version", `strings.Join(header, "\n") + "\n"`)
						case exprIs(n.Args[0], "name") && exprIs(n.Args[1], "converted") && inRange(stack, "sourceNames", "name"):
						case exprIs(n.Args[0], "name") && exprIs(n.Args[1], "generatedFiles[name].text") && inRange(stack, "generated", "name"):
						default:
							err = fail(n, "run: unsupported plan.set payload")
						}
					}
					if name == "run" && exprIs(n.Fun, "os.WriteFile") && (len(n.Args) != 3 || !exprIs(n.Args[1], "[]byte(plan.text[name])") || !inRange(stack, "plan.names", "name")) {
						err = fail(n, "run: unsupported os.WriteFile payload")
					}
				}
				return err == nil
			})
			if err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"readText", "convertFile", "run", "lookup", "set", "accountedText"} {
		if functions[name] != 1 {
			return fmt.Errorf("scripts/vendorcore: missing or duplicate source function %s", name)
		}
	}
	for _, role := range []string{"replacements", "logReplacements", "prunableImports", "planned-output-copy", "override-return", "converted-return"} {
		if roles[role] != 1 {
			return fmt.Errorf("scripts/vendorcore: missing or duplicate annotated payload site %s", role)
		}
	}
	for call, id := range markers {
		if !bound[call] {
			return fail(call, id+": marker is not bound to an actual payload site")
		}
	}
	for id := range descriptors {
		if counts[id] == 0 {
			return fmt.Errorf("%s at scripts/vendorcore/accounting.go: orphan pipeline metadata; missing actual use site", id)
		}
	}
	return nil
}

func pipelineFixture(t *testing.T) fstest.MapFS {
	t.Helper()
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fixture := fstest.MapFS{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".go") && !strings.HasSuffix(f.Name(), "_test.go") {
			data, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			fixture[f.Name()] = &fstest.MapFile{Data: data}
		}
	}
	return fixture
}

func replaceFixture(t *testing.T, fixture fstest.MapFS, file, from, to string) {
	t.Helper()
	data := string(fixture[file].Data)
	if strings.Count(data, from) != 1 {
		t.Fatalf("fixture mutation in %s needs exactly one %q", file, from)
	}
	fixture[file] = &fstest.MapFile{Data: []byte(strings.Replace(data, from, to, 1))}
}

func TestPipelineSiteMutations(t *testing.T) {
	if err := validatePipeline(pipelineFixture(t), pipelineAccounting); err != nil {
		t.Fatalf("valid source fixture: %v", err)
	}
	cases := []struct{ id, file, marker, payload string }{
		{"strip-utf8-bom", "main.go", `accountedText("strip-utf8-bom", string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))`, `string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))`},
		{"normalize-input-crlf", "convert.go", `accountedText("normalize-input-crlf", strings.ReplaceAll(text, "\r\n", "\n"))`, `strings.ReplaceAll(text, "\r\n", "\n")`},
		{"rewrite-package-main", "convert.go", `accountedText("rewrite-package-main", packageMain.ReplaceAllString(text, "package core"))`, `packageMain.ReplaceAllString(text, "package core")`},
		{"generate-core-version", "main.go", `accountedText("generate-core-version", strings.Join(header, "\n")+"\n")`, `strings.Join(header, "\n")+"\n"`},
		{"normalize-output-crlf", "main.go", `accountedText("normalize-output-crlf", strings.ReplaceAll(plan.text[name], "\r\n", "\n"))`, `strings.ReplaceAll(plan.text[name], "\r\n", "\n")`},
		{"ensure-output-newline", "main.go", `accountedText("ensure-output-newline", text+"\n")`, `text+"\n"`},
	}
	for _, tc := range cases {
		for _, mutation := range []string{"missing metadata", "missing marker", "bypassed marker"} {
			t.Run(tc.id+"/"+mutation, func(t *testing.T) {
				fixture := pipelineFixture(t)
				metadata := currentTables().clone().pipeline
				diagnostic := "missing or bypassed marker"
				switch mutation {
				case "missing metadata":
					diagnostic = "missing metadata or unknown pipeline ID"
					for i := range metadata {
						if metadata[i].id == tc.id {
							metadata = append(metadata[:i], metadata[i+1:]...)
							break
						}
					}
				case "missing marker":
					replaceFixture(t, fixture, tc.file, tc.marker, tc.payload)
				case "bypassed marker":
					replaceFixture(t, fixture, tc.file, tc.marker, tc.payload)
					fixture["detached.go"] = &fstest.MapFile{Data: []byte("package main\nvar _ = accountedText(" + strconv.Quote(tc.id) + ", \"detached\")\n")}
				}
				err := validatePipeline(fixture, metadata)
				requireDiagnostic(t, err, tc.id, diagnostic, "scripts/vendorcore/")
			})
		}
	}
}

func TestPipelineUnsupportedMutations(t *testing.T) {
	cases := []struct{ name, file, from, to, diagnostic string }{
		{"unknown ID", "main.go", `"strip-utf8-bom"`, `"unregistered-bom"`, "unregistered-bom: missing metadata or unknown"},
		{"extra rewrite", "convert.go", "return text, nil", "text = strings.TrimSpace(text)\nreturn text, nil", "unsupported"},
		{"extra converted rewrite", "main.go", "plan.set(name, converted)", "converted = strings.TrimSpace(converted)\nplan.set(name, converted)", "unsupported payload assignment"},
		{"shadowed payload", "convert.go", "return text, nil", "var text = strings.TrimSpace(text)\nreturn text, nil", "unsupported payload declaration"},
		{"zeroed payload", "convert.go", "return text, nil", "{ var text string; return text, nil }", "unsupported payload declaration"},
		{"range payload", "convert.go", "return text, nil", "for _, text = range []string{\"extra\"} {}\nreturn text, nil", "unsupported payload range assignment"},
		{"duplicate site", "convert.go", "return text, nil", "text = accountedText(\"normalize-input-crlf\", text)\nreturn text, nil", "duplicate pipeline use site"},
		{"wrong package payload", "convert.go", `packageMain.ReplaceAllString(text, "package core")`, `packageMain.ReplaceAllString(text, "package other")`, "rewrite-package-main: missing or bypassed"},
		{"wrong BOM return", "main.go", `string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))`, `string(raw)`, "strip-utf8-bom: missing or bypassed"},
		{"unconditional LF", "main.go", `!strings.HasSuffix(text, "\n")`, "true", "ensure-output-newline"},
		{"wrong emission", "main.go", "plan.set(name, converted)", `plan.set(name, strings.TrimSpace(converted))`, "unsupported plan.set payload"},
		{"plan rewrite", "main.go", "plan.text[name] = text", "plan.text[name] = strings.TrimSpace(text)", "unsupported payload assignment"},
		{"parenthesized planned output rewrite", "main.go", "plan.text[name] = text", "plan.text[name] = text\n(plan.text)[name] = strings.TrimSpace(plan.text[name])", "run: unsupported payload assignment"},
		{"planned output map replacement", "main.go", "plan.text[name] = text", "plan.text = map[string]string{}\n\t\t\tplan.text[name] = text", "run: unsupported payload assignment"},
		{"write rewrite", "main.go", "[]byte(plan.text[name])", "[]byte(strings.TrimSpace(plan.text[name]))", "unsupported os.WriteFile payload"},
		{"regexp ValueSpec rewrite", "convert.go", "re, err := regexp.Compile(r.Pattern)", `var re, err = regexp.Compile(r.Pattern + "|extra")`, "unsupported regexp producer declaration for annotated replacement pattern"},
		{"import regexp ValueSpec rewrite", "convert.go", "line := regexp.MustCompile(`(?m)^\\t\"` + regexp.QuoteMeta(imp.path) + `\"\\n`)", "var line = regexp.MustCompile(" + "`(?m)^\\t\"` + regexp.QuoteMeta(imp.path) + `\"\\n`" + ")", "unsupported regexp producer declaration for annotated import path"},
		{"unannotated rule loop", "convert.go", "range replacements", "range otherRules", "unsupported"},
		{"unannotated generated", "main.go", "generatedFiles[name].text", `"unclassified output"`, "unsupported plan.set payload"},
		{"override bypass", "convert.go", "return override, nil", `return strings.TrimSpace(override), nil`, "unsupported payload return"},
		{"marker changes value", "accounting.go", "return value", `return value + "\n"`, "must return its value unchanged"},
		{"discarded marker result", "convert.go", `text = accountedText("normalize-input-crlf"`, `_ = accountedText("normalize-input-crlf"`, "marker is not bound"},
		{"marked temporary", "main.go", `text := accountedText("normalize-output-crlf"`, `temporary := accountedText("normalize-output-crlf"`, "marker is not bound"},
		{"inactive package marker", "convert.go", `text = accountedText("rewrite-package-main", packageMain.ReplaceAllString(text, "package core"))`, `if false { text = accountedText("rewrite-package-main", packageMain.ReplaceAllString(text, "package core")) }`, "unsupported payload assignment"},
		{"early bypass return", "convert.go", `text = accountedText("normalize-input-crlf"`, "return text, nil\ntext = accountedText(\"normalize-input-crlf\"", "unsupported payload return"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := pipelineFixture(t)
			replaceFixture(t, fixture, tc.file, tc.from, tc.to)
			requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), tc.diagnostic, "scripts/vendorcore/")
		})
	}
	t.Run("orphan metadata", func(t *testing.T) {
		metadata := append(currentTables().clone().pipeline, localChange("orphan-operation", "Describe a nonexistent output transformation."))
		requireDiagnostic(t, validatePipeline(pipelineFixture(t), metadata), "orphan-operation", "orphan pipeline metadata")
	})
	t.Run("duplicate metadata", func(t *testing.T) {
		metadata := append(currentTables().clone().pipeline, pipelineAccounting[0])
		requireDiagnostic(t, validatePipeline(pipelineFixture(t), metadata), pipelineAccounting[0].id, "duplicate pipeline metadata")
	})
	t.Run("added unknown ID", func(t *testing.T) {
		fixture := pipelineFixture(t)
		fixture["extra.go"] = &fstest.MapFile{Data: []byte("package main\nfunc extra() { _ = accountedText(\"unknown-operation\", \"extra\") }\n")}
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "unknown-operation", "missing metadata or unknown pipeline ID", "scripts/vendorcore/extra.go")
	})
	t.Run("planned output range mutation", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", `text := accountedText("normalize-output-crlf"`, `for _, (plan.text)[name] = range []string{"extra"} {}
		text := accountedText("normalize-output-crlf"`)
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "run: unsupported payload range assignment")
	})
	t.Run("helper payload rewrite", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", "// UTF-8 без BOM и с LF:", "rewritePlanned(plan, name)\n\t\t// UTF-8 без BOM и с LF:")
		fixture["main.go"] = &fstest.MapFile{Data: append(fixture["main.go"].Data, []byte("\nfunc rewritePlanned(p *planned, name string) { p.text[name] = strings.TrimSpace(p.text[name]) }\n")...)}
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "unexpected function rewritePlanned contains planned output map write")
	})
	t.Run("direct plan alias", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", `text := accountedText("normalize-output-crlf"`, `alias := plan
		alias.text[name] = strings.TrimSpace(alias.text[name])
		text := accountedText("normalize-output-crlf"`)
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "run: unsupported direct planned output alias")
	})
	t.Run("map argument helper", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", "// UTF-8 без BOM и с LF:", "rewritePayload(plan.text, name)\n\t\t// UTF-8 без BOM и с LF:")
		fixture["main.go"] = &fstest.MapFile{Data: append(fixture["main.go"].Data, []byte("\nfunc rewritePayload(payload map[string]string, key string) { payload[key] = strings.TrimSpace(payload[key]) }\n")...)}
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "run: unsupported direct planned output map argument")
	})
	t.Run("split plan.text alias", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", `text := accountedText("normalize-output-crlf"`, `var output map[string]string
		output = plan.text
		output[name] = strings.TrimSpace(output[name])
		text := accountedText("normalize-output-crlf"`)
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "run: unsupported direct planned output alias")
	})
	t.Run("tuple plan.text alias", func(t *testing.T) {
		fixture := pipelineFixture(t)
		replaceFixture(t, fixture, "main.go", `text := accountedText("normalize-output-crlf"`, `var output map[string]string
		output, _ = plan.text, true
		output[name] = strings.TrimSpace(output[name])
		text := accountedText("normalize-output-crlf"`)
		requireDiagnostic(t, validatePipeline(fixture, pipelineAccounting), "scripts/vendorcore/main.go", "run: unsupported direct planned output alias")
	})
}
