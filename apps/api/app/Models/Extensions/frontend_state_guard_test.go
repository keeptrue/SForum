package extensions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// packageLevelStateExemptions 是「确实需要包级可变状态」的豁免清单，每条必须写理由。
//
// 目前为空：本包所有包级 map 都是**静态查找表**（非空复合字面量初始化，运行期只读），
// 由 isStaticLookupTable 放行，不需要登记在这里。
var packageLevelStateExemptions = map[string]string{
	"pluginRuntimeCoordinatorBootOwners": "进程内启动所有权登记（plugin_runtime_coordinator_ownership.go）：它按设计就是每进程一份——记录本进程已占用与已退役的 boot identity，跨进程共享反而会破坏「一次启动只有一个所有者」的语义。与缓存权威无关，不属于 P3-7 的范围。",
}

// TestExtensionsPackageHasNoPackageLevelMutableCacheState 结构性守卫（P3-7 复审）：
// 本包不得出现「包级可变缓存状态」。
//
// 为什么需要它：`TestFrontendServiceHoldsNoInProcessChallengeState` 只查结构体字段，
// 查不到包级变量。reviewer 构造过一个包级 `sync.Map` 门控变异——Challenge 写、
// Grant **未命中即拒**、仍照常调 store.Consume——整包全绿，而它在生产上正是 P3-7
// 要修的缺陷（B 副本拒绝 A 副本创建的合法挑战）。也就是说「库里有 consumed_at 落账」
// 证明不了「权威在库」，必须从结构上禁止这类状态存在。
//
// 判定口径：
//   - 放行：`const`；函数内局部变量；结构体字段（由反射守卫覆盖）；
//     以及**非空复合字面量初始化**的包级 map（静态查找表，运行期只读）。
//   - 拒绝：包级 `sync.Map`（含 `new(sync.Map)` / `&sync.Map{}`）；
//     空 map 字面量（`map[string]struct{}{}`，必然要被运行期填充）；
//     `make(map[...])`；以及把 map / sync.Map 藏进结构体字面量或嵌套类型的包级变量。
//
// 已知盲区（写清楚，别让它变成假声明）：它不做数据流分析——「先写成非空静态表、
// 再在运行期往里塞缓存」这种形态它看不出来。要覆盖那类，需要 SSA 级分析，成本远高于
// 收益；现实里的回归形态是空 map / sync.Map / 结构体字段，这三类都被挡住。
//
// 变异自证：在任意非测试文件里加 `var probeCache = sync.Map{}`（或
// `var probeGate = map[string]struct{}{}`）→ 本用例必须红；删掉恢复绿。
func TestExtensionsPackageHasNoPackageLevelMutableCacheState(t *testing.T) {
	dir := extensionsPackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR {
				continue
			}
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range valueSpec.Names {
					if _, exempt := packageLevelStateExemptions[ident.Name]; exempt {
						continue
					}
					var value ast.Expr
					if i < len(valueSpec.Values) {
						value = valueSpec.Values[i]
					}
					if isStaticLookupTable(valueSpec, value) {
						continue
					}
					if kind := mutableStateKind(valueSpec.Type, value); kind != "" {
						violations = append(violations, name+": "+ident.Name+" ("+kind+")")
					}
				}
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("本包不允许包级可变缓存状态（P3-7：权威必须在库/Redis，不能在进程里）：\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestPackageLevelStateExemptionsAreLive 豁免清单不能变成垃圾桶：登记了却已经没人用
// （或没写理由）的条目必须清理。
func TestPackageLevelStateExemptionsAreLive(t *testing.T) {
	if len(packageLevelStateExemptions) == 0 {
		return
	}
	dir := extensionsPackageDir(t)
	declared := map[string]struct{}{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR {
				continue
			}
			for _, spec := range genDecl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					for _, ident := range valueSpec.Names {
						declared[ident.Name] = struct{}{}
					}
				}
			}
		}
	}
	var stale []string
	for name, reason := range packageLevelStateExemptions {
		if strings.TrimSpace(reason) == "" {
			stale = append(stale, name+"（缺理由）")
			continue
		}
		if _, found := declared[name]; !found {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("豁免清单已过期：这些包级变量不再存在，请删除或更新：%v", stale)
	}
}

func extensionsPackageDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(thisFile)
}

// isStaticLookupTable 判定「非空复合字面量初始化的包级 map」——静态查找表，只读。
func isStaticLookupTable(spec *ast.ValueSpec, value ast.Expr) bool {
	literal, ok := value.(*ast.CompositeLit)
	if !ok || len(literal.Elts) == 0 {
		return false
	}
	if _, ok := literal.Type.(*ast.MapType); !ok {
		return false
	}
	if spec.Type == nil {
		return true
	}
	_, ok = spec.Type.(*ast.MapType)
	return ok
}

// mutableStateKind 返回该声明里出现的可变状态种类（"" 表示没有）。
func mutableStateKind(declared ast.Expr, value ast.Expr) string {
	for _, expr := range []ast.Expr{declared, value} {
		if expr == nil {
			continue
		}
		if kind := mutableStateInExpr(expr); kind != "" {
			return kind
		}
	}
	return ""
}

func mutableStateInExpr(expr ast.Expr) string {
	kind := ""
	ast.Inspect(expr, func(n ast.Node) bool {
		if kind != "" {
			return false
		}
		switch node := n.(type) {
		case *ast.MapType:
			kind = "map"
		case *ast.SelectorExpr:
			if ident, ok := node.X.(*ast.Ident); ok && ident.Name == "sync" && node.Sel.Name == "Map" {
				kind = "sync.Map"
			}
		}
		return true
	})
	return kind
}
