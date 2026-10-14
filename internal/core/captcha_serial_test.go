package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Ответ на капчу у ядра — один глобальный CaptchaResultChan с буфером 1, без
// привязки к просьбе, и каждая просьба перед ожиданием его чистит. С двумя
// просьбами разом это сломалось бы: токен одной забрала бы другая, отказ
// второй вышиб бы из канала токен первой. Не ломается потому, что просьб разом
// не бывает: капчу просят только из fetchVkCreds, а его зовёт только
// fetchVkCredsSerialized под vkRequestMu — на всё время, вместе с ожиданием
// ответа. На этом стоит и диспетчер решателя (internal/captcha, ErrBusy).
//
// Тест — сторож этой сериализации на случай обновления ядра: он падает, если
// апстрим позовёт requestWebViewCaptcha мимо fetchVkCreds, позовёт fetchVkCreds
// не из fetchVkCredsSerialized или снимет с него замок. Тогда диспетчеру нужен
// канал на просьбу (architecture.md, задача «Один канал ответа капчи»).
//
// Граф строится по именам: ребро — любое упоминание функции, не только вызов,
// поэтому функция, переданная значением, тоже в нём есть. Одноимённые методы
// разных типов сливаются — это делает проверку только строже.
func TestCaptchaRequestsSerialized(t *testing.T) {
	const (
		ask    = "requestWebViewCaptcha"
		guard  = "fetchVkCreds"
		locker = "fetchVkCredsSerialized"
	)

	funcs, refs := packageGraph(t)
	for _, name := range []string{ask, guard, locker} {
		if funcs[name] == nil {
			t.Fatalf("в ядре нет функции %s — апстрим переписал получение кредов, проверьте сериализацию капчи заново", name)
		}
	}

	// Вверх от просьбы капчи до fetchVkCreds: любая цепочка, которая
	// кончается раньше, — путь к капче без замка.
	seen := map[string]bool{ask: true}
	queue := [][]string{{ask}}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		callers := refs[path[0]]
		if len(callers) == 0 {
			t.Errorf("путь к капче мимо %s: %s", guard, strings.Join(path, " ← "))
			continue
		}
		for _, caller := range callers {
			if caller == guard || seen[caller] {
				continue
			}
			seen[caller] = true
			queue = append(queue, append([]string{caller}, path...))
		}
	}

	if got := refs[guard]; !slices.Equal(got, []string{locker}) {
		t.Errorf("%s упоминают %v, ждали только %s", guard, got, locker)
	}

	// Замок берётся первым делом и отпускается при выходе.
	body := funcs[locker].Body.List
	if len(body) < 2 || !isCall(body[0], "vkRequestMu.Lock") || !isDeferCall(body[1], "vkRequestMu.Unlock") {
		t.Errorf("%s больше не начинается с vkRequestMu.Lock(); defer vkRequestMu.Unlock()", locker)
	}
}

// packageGraph разбирает файлы ядра (без тестов) и возвращает функции верхнего
// уровня по имени и для каждой — кто её упоминает, по порядку и без повторов.
func packageGraph(t *testing.T) (map[string]*ast.FuncDecl, map[string][]string) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}

	refs := map[string][]string{}
	add := func(callee, caller string) {
		if callee != caller && !slices.Contains(refs[callee], caller) {
			refs[callee] = append(refs[callee], caller)
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			// Упоминание вне функций — в инициализаторе переменной: такая
			// функция зовётся откуда угодно, у неё «вызывающий» без имени.
			caller := "<пакет>"
			var root ast.Node = decl
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Body == nil {
					continue
				}
				caller, root = fn.Name.Name, fn.Body
			}
			ast.Inspect(root, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && funcs[id.Name] != nil {
					add(id.Name, caller)
				}
				return true
			})
		}
	}
	// «<пакет>» сам никем не упоминается — цепочка через него оборвётся
	// и попадёт в отчёт как путь мимо замка.
	return funcs, refs
}

func isCall(stmt ast.Stmt, name string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	return ok && exprName(call.Fun) == name
}

func isDeferCall(stmt ast.Stmt, name string) bool {
	d, ok := stmt.(*ast.DeferStmt)
	return ok && exprName(d.Call.Fun) == name
}

// exprName — «a.b» для селектора и «a» для имени.
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	}
	return ""
}
