package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestWireAPIDomainServicesUsesForumProviderFinalize 是 P3-3 的机械守卫：
// 生产装配只能通过 finalizeAPIForumProvider 得到 forumProvider，不允许把
// `providers.NewForumProvider…` 直接赋给 forumProvider 再漏掉 post_risk 接线。
//
// 为什么需要它：finalizeAPIForumProvider 的用例能抓住「收口点内部少了一行」，
// 但抓不住「装配不再走收口点」——那是一次等价改写（把返回值换成 base 本身），
// 类型相同、编译通过、单元测试全绿，而 post_risk 又回到零校验。这跟
// app/Http/options_error_tripwire_test.go 守「映射只有一份」是同一类问题：
// 靠人记得守不住，用 AST 机械守住。
//
// 断言可证伪：把 api_assembly_domains.go 里的 finalizeAPIForumProvider 调用删掉、
// 直接使用 forumProviderBase → 本测试红。
func TestWireAPIDomainServicesUsesForumProviderFinalize(t *testing.T) {
	const assemblyFile = "api_assembly_domains.go"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, assemblyFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", assemblyFile, err)
	}

	assignments := 0
	finalizeCalls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "finalizeAPIForumProvider" {
			return true
		}
		finalizeCalls++
		// 第二个实参必须是 humanVerifier（bootstrap 从 infrastructure 取的那个实例）。
		if len(call.Args) != 2 {
			t.Fatalf("finalizeAPIForumProvider 参数个数 = %d，期望 2", len(call.Args))
		}
		verifierArg, ok := call.Args[1].(*ast.Ident)
		if !ok || verifierArg.Name != "humanVerifier" {
			t.Fatalf("finalizeAPIForumProvider 的 verifier 实参必须是 humanVerifier，实际 %T", call.Args[1])
		}
		return true
	})

	// 反向找赋值：`forumProvider, err := finalizeAPIForumProvider(...)`。
	ast.Inspect(file, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		usesFinalize := false
		for _, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "finalizeAPIForumProvider" {
				usesFinalize = true
			}
		}
		if !usesFinalize {
			return true
		}
		for _, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "forumProvider" {
				assignments++
			}
		}
		return true
	})

	if finalizeCalls != 1 {
		t.Fatalf("finalizeAPIForumProvider 调用点 = %d，期望恰好 1 处（唯一收口点）", finalizeCalls)
	}
	if assignments != 1 {
		t.Fatalf("forumProvider 未经 finalizeAPIForumProvider 赋值 = %d 处，期望 1 处；"+
			"生产装配必须走收口点，否则 post_risk 会静默变成零校验", assignments)
	}
}
