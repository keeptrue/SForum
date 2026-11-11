package options

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// TestFeatureFlagCatalogCoversEveryConstant 是 F9 的完整性测试：
// features.* 常量数必须等于 FeatureFlagCatalog() 条目数，且逐个同名。
//
// 用 AST 读本包源文件，而不是在手写第二份清单里再列一遍：手写清单漏登记时
// 测试仍然绿，而 features.badges 那次事故正是「常量在、目录里没有」。
func TestFeatureFlagCatalogCoversEveryConstant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "feature_flags.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 feature_flags.go 失败：%v", err)
	}

	constantValues := make([]string, 0, 16)
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "NameFeature") {
				continue
			}
			if index >= len(spec.Values) {
				t.Fatalf("常量 %s 没有字面量值，无法参与完整性比对", name.Name)
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("常量 %s 不是字符串字面量，无法参与完整性比对", name.Name)
			}
			constantValues = append(constantValues, strings.Trim(literal.Value, `"`))
		}
		return true
	})

	catalog := FeatureFlagCatalog()
	catalogNames := make([]string, 0, len(catalog))
	for _, definition := range catalog {
		catalogNames = append(catalogNames, definition.Name)
	}

	if len(constantValues) != len(catalogNames) {
		t.Fatalf("features.* 常量数 = %d，目录条目数 = %d：常量与目录必须一一对应",
			len(constantValues), len(catalogNames))
	}

	slices.Sort(constantValues)
	sortedCatalog := slices.Clone(catalogNames)
	slices.Sort(sortedCatalog)
	if !slices.Equal(constantValues, sortedCatalog) {
		t.Fatalf("常量集合 %v 与目录集合 %v 不一致", constantValues, sortedCatalog)
	}

	// 目录内部不得重复：两个常量取同一个值时条目数会虚高，上面的计数比对照样绿。
	if len(slices.Compact(slices.Clone(sortedCatalog))) != len(catalogNames) {
		t.Fatalf("FeatureFlagCatalog 有重复条目：%v", catalogNames)
	}
}
