package http

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestOptionsErrorMappingHasSingleOwner 是 M12-a 的机械守卫：
// 「把 options.ErrInvalidOption 映射成 HTTP 状态」的函数全仓只允许有**一个**，
// 且必须就是 app/Http/options_error.go 里的 MapOptionsError。
//
// 为什么要有这条：`mapOptionsError` 曾经有两份（Controllers/Options 与
// Controllers/TrustLevels）。修好一份之后，另一份仍在把写库失败报成 422，
// 而两份代码看起来都「对」——M12-a 的原始症状恰好发生在没被修的那份上。
// 这类副本靠人记得守不住（本仓已复发过 T6/T7 的 fixture schema、白名单硬编码）。
//
// 断言可证伪：故意加回一份本地副本 → 本测试红（变异记录见 M12-a 报告）。
func TestOptionsErrorMappingHasSingleOwner(t *testing.T) {
	const wantOwner = "app/Http/options_error.go::MapOptionsError"

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析 apps/api 根目录失败：%v", err)
	}

	owners := make([]string, 0, 2)
	scanned := 0
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// 根目录本身要进（它的 base 名可能就是 ".."，不能按前缀跳过）。
			if path == root {
				return nil
			}
			name := entry.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		// 只扫生产文件：测试里的桩实现不算第二份真源。
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		scanned++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				return true
			}
			// 「映射」= 同一函数体内既提到 ErrInvalidOption，又产出 HTTP 状态。
			if !mentionsErrInvalidOption(decl.Body) || !producesHTTPStatus(decl.Body) {
				return true
			}
			owners = append(owners, rel+"::"+decl.Name.Name)
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描 apps/api 生产文件失败：%v", walkErr)
	}
	// 扫描本身要有效：0 个文件时「没有第二份副本」是假绿。
	if scanned < 500 {
		t.Fatalf("只扫到 %d 个生产 .go 文件，扫描根或过滤条件写错了", scanned)
	}

	if len(owners) != 1 || owners[0] != wantOwner {
		t.Fatalf("options 错误映射必须有且只有一个实现（期望 %s），实际 %d 个：%v",
			wantOwner, len(owners), owners)
	}
}

// mentionsErrInvalidOption 认 `options.ErrInvalidOption` 与裸 `ErrInvalidOption`
// （同包引用）两种写法。
func mentionsErrInvalidOption(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if typed.Sel != nil && typed.Sel.Name == "ErrInvalidOption" {
				found = true
			}
		case *ast.Ident:
			if typed.Name == "ErrInvalidOption" {
				found = true
			}
		}
		return true
	})
	return found
}

// producesHTTPStatus 认 fiber.NewError 与 fiber.Status* 两种写法。
func producesHTTPStatus(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if typed.Sel != nil && strings.HasPrefix(typed.Sel.Name, "Status") {
				found = true
			}
		case *ast.Ident:
			if typed.Name == "NewError" {
				found = true
			}
		}
		return true
	})
	return found
}
