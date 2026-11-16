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
// 目前只有一条：它按设计就是进程内状态，与缓存权威无关。
var packageLevelStateExemptions = map[string]string{
	"pluginRuntimeCoordinatorBootOwners": "进程内启动所有权登记（plugin_runtime_coordinator_ownership.go）：它按设计就是每进程一份——记录本进程已占用与已退役的 boot identity，跨进程共享反而会破坏「一次启动只有一个所有者」的语义。与缓存权威无关，不属于 P3-7 的范围。",
}

// TestExtensionsPackageHasNoPackageLevelMutableCacheState 结构性守卫（P3-7 复审）：
// `app/Models/Extensions` 及其**所有子包**不得出现「包级可变缓存状态」。
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
//     `make(map[...])`；把 map / sync.Map 藏进**匿名**结构体字面量的包级变量；
//     以及——**具名类型**里的 map 字段（`type holder struct{ m map[string]string }`
//     配 `var h = holder{}` / `&holder{}` / `new(holder)`）。
//
// 为什么必须连具名类型一起判：`ast.Inspect` 只看声明处的表达式，`holder{}` 这个
// 字面量里没有 map 节点，map 在 `type` 声明里。reviewer 实测 `var registry =
// &cacheHolder{}`（cacheHolder 带 map 字段）能整包全绿——而这正是守卫存在的目的。
// 具名类型的判定带**传递闭包**：`type outer struct{ h holder }` 也算带 map。
//
// 扫描根是递归的：`app/Models/Extensions/**` 每个子包都受约束。`frontendtrust`
// 现在是纯存储，但将来往那儿加包级缓存同样致命，没理由等它出事再补。
//
// 已知盲区（写清楚，别让它变成假声明）：它不做数据流分析——「先写成非空静态表、
// 再在运行期往里塞缓存」这种形态它看不出来；`var x any = holder{}` 这种经接口
// 逃逸的写法同样看不出。要覆盖那类需要 SSA 级分析，成本远高于收益；现实里的回归
// 形态是空 map / sync.Map / 结构体字段 / 具名持有者，这四类都被挡住。
//
// 变异自证（改完机制必须重跑）：任意非测试文件里加
//   - `var probeCache = sync.Map{}` → 红；
//   - `var probeGate = map[string]struct{}{}` → 红；
//   - `type zzHolder struct{ m map[string]string }` + `var h = zzHolder{}` → 红；
//   - 同上换成 `var h = &zzHolder{}` → 红；换成 `var h = new(zzHolder)` → 红；
//     删掉恢复绿。
func TestExtensionsPackageHasNoPackageLevelMutableCacheState(t *testing.T) {
	var violations []string
	for _, pkg := range extensionsPackages(t) {
		violations = append(violations, pkg.violations(t)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("本包（含子包）不允许包级可变缓存状态（P3-7：权威必须在库/Redis，不能在进程里）：\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestPackageLevelStateExemptionsAreLive 豁免清单不能变成垃圾桶：登记了却已经没人用
// （或没写理由）的条目必须清理。
func TestPackageLevelStateExemptionsAreLive(t *testing.T) {
	if len(packageLevelStateExemptions) == 0 {
		return
	}
	declared := map[string]struct{}{}
	for _, pkg := range extensionsPackages(t) {
		for _, file := range pkg.files {
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

type extensionPackage struct {
	rel   string
	files []*ast.File
	// names 是同包文件名字（与 files 一一对应），只用于报错点名。
	names []string
	// mapBearingTypes 是「带 map / sync.Map 字段的具名类型」集合（含传递闭包）。
	mapBearingTypes map[string]struct{}
}

func (p extensionPackage) violations(t *testing.T) []string {
	t.Helper()
	var out []string
	for i, file := range p.files {
		name := p.names[i]
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
				for valueIndex, ident := range valueSpec.Names {
					if _, exempt := packageLevelStateExemptions[ident.Name]; exempt {
						continue
					}
					var value ast.Expr
					if valueIndex < len(valueSpec.Values) {
						value = valueSpec.Values[valueIndex]
					}
					if isStaticLookupTable(valueSpec, value) {
						continue
					}
					if kind := mutableStateKind(valueSpec.Type, value); kind != "" {
						out = append(out, filepath.ToSlash(filepath.Join(p.rel, name))+": "+ident.Name+" ("+kind+")")
						continue
					}
					if kind := p.namedHolderKind(valueSpec.Type, value); kind != "" {
						out = append(out, filepath.ToSlash(filepath.Join(p.rel, name))+": "+ident.Name+" ("+kind+")")
					}
				}
			}
		}
	}
	return out
}

// namedHolderKind 判定「包级变量持有带 map 字段的具名类型」。
//
// 覆盖四种现实写法：`var h = holder{}`、`var h = &holder{}`、`var h = new(holder)`、
// `var h holder`（声明即零值，但后续随时可写）。
func (p extensionPackage) namedHolderKind(declared ast.Expr, value ast.Expr) string {
	if name, ok := p.typeNameOf(declared); ok {
		if _, found := p.mapBearingTypes[name]; found {
			return "named type with map field: " + name
		}
	}
	if value == nil {
		return ""
	}
	expr := value
	if unary, ok := expr.(*ast.UnaryExpr); ok {
		expr = unary.X
	}
	if name, ok := p.typeNameOf(expr); ok {
		if _, found := p.mapBearingTypes[name]; found {
			return "named type with map field: " + name
		}
	}
	return ""
}

// typeNameOf 从表达式里取出「具名类型」：T{} / &T{} / new(T) / 声明类型 T。
func (p extensionPackage) typeNameOf(expr ast.Expr) (string, bool) {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name, true
	case *ast.CompositeLit:
		if ident, ok := node.Type.(*ast.Ident); ok {
			return ident.Name, true
		}
	case *ast.CallExpr:
		if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "new" && len(node.Args) == 1 {
			if argIdent, ok := node.Args[0].(*ast.Ident); ok {
				return argIdent.Name, true
			}
		}
	}
	return "", false
}

func extensionsPackages(t *testing.T) []extensionPackage {
	t.Helper()
	root := extensionsPackageRoot(t)
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		// 跳过测试数据目录（fixture 里的 .go 不是本仓代码）。
		if name := entry.Name(); name == "testdata" || name == "node_modules" {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk extensions packages: %v", err)
	}

	var packages []extensionPackage
	for _, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read %s: %v", dir, readErr)
		}
		pkg := extensionPackage{rel: mustRel(t, root, dir)}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", name, parseErr)
			}
			pkg.files = append(pkg.files, file)
			pkg.names = append(pkg.names, name)
		}
		if len(pkg.files) == 0 {
			continue
		}
		pkg.mapBearingTypes = mapBearingNamedTypes(pkg.files)
		packages = append(packages, pkg)
	}
	if len(packages) == 0 {
		t.Fatal("没有解析到任何包：守卫会变成永远绿")
	}
	return packages
}

// mapBearingNamedTypes 求「带 map / sync.Map 字段的具名类型」的传递闭包。
//
// 例：`type holder struct{ m map[string]string }` 直接命中；
// `type outer struct{ h holder }` 通过闭包也命中。
func mapBearingNamedTypes(files []*ast.File) map[string]struct{} {
	structFields := map[string][]ast.Expr{}
	for _, file := range files {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				var fieldTypes []ast.Expr
				for _, field := range structType.Fields.List {
					fieldTypes = append(fieldTypes, field.Type)
				}
				structFields[typeSpec.Name.Name] = fieldTypes
			}
		}
	}

	bearing := map[string]struct{}{}
	for changed := true; changed; {
		changed = false
		for name, fields := range structFields {
			if _, done := bearing[name]; done {
				continue
			}
			for _, fieldType := range fields {
				if mutableStateInExpr(fieldType) != "" || namedTypeInExpr(fieldType, bearing) {
					bearing[name] = struct{}{}
					changed = true
					break
				}
			}
		}
	}
	return bearing
}

// namedTypeInExpr 判断字段类型里是否引用了已经判定为「带 map」的具名类型。
func namedTypeInExpr(expr ast.Expr, bearing map[string]struct{}) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok {
			if _, hit := bearing[ident.Name]; hit {
				found = true
			}
		}
		return true
	})
	return found
}

func extensionsPackageRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(thisFile)
}

func mustRel(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatalf("relative path for %s: %v", target, err)
	}
	return rel
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
