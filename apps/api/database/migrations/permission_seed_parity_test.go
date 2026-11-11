package migrations

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEverySeededPermissionReachesTheDatabase 是这次「seeds.go 改了但库里没有」
// 事故的结构性修复。
//
// 事实：app/Models/Identity/seeds.go 的 SeedPermissions 只在测试与 CLI 里被读取，
// 启动期没有任何权限目录 upsert。权限目录的真值来源是迁移 SQL。所以代码里出现
// 一个权限键而迁移里没有，运行时不会有任何报错——角色挂不上、端点永远 403，
// 直到有人手工查库才会发现。
//
// 这条测试把两处钉在一起：seeds.go 里每个键，都必须在某条迁移的
// `INSERT INTO permissions` 里出现过。
func TestEverySeededPermissionReachesTheDatabase(t *testing.T) {
	seeded := seededPermissionKeys(t)
	if len(seeded) == 0 {
		t.Fatal("no seeded permission keys parsed; the parser is broken")
	}

	entries, err := fs.ReadDir(Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	inserted := map[string]struct{}{}
	// 允许 ( 与键之间出现换行/缩进：多行 VALUES 写法很常见。
	re := regexp.MustCompile(`\(\s*'([a-z][a-z0-9._-]*)',\s*'[a-z_]+',`)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := fs.ReadFile(Files(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		source := string(body)
		// 只看 INSERT INTO permissions 之后的 VALUES 段。
		for _, chunk := range strings.Split(source, "INSERT INTO permissions")[1:] {
			values := chunk
			if idx := strings.Index(values, ";"); idx >= 0 {
				values = values[:idx]
			}
			for _, match := range re.FindAllStringSubmatch(values, -1) {
				inserted[match[1]] = struct{}{}
			}
		}
	}

	var missing []string
	for key := range seeded {
		if _, ok := inserted[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("permissions declared in Identity/seeds.go but never inserted by any migration: %v\n"+
			"启动期没有权限目录 upsert，缺迁移等于该权限永远不存在于数据库。", missing)
	}
}

// seededPermissionKeys 从 Identity/seeds.go 里抽取权限键。
//
// 用源码解析而不是 import identity 包：database/migrations 不应该依赖模型层，
// 否则一个权限常量改名会在这里编译失败，掩盖掉真正的「迁移缺行」问题。
func seededPermissionKeys(t *testing.T) map[string]struct{} {
	t.Helper()
	path := filepath.Join("..", "..", "app", "Models", "Identity", "seeds.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeds.go: %v", err)
	}
	// 匹配 PermissionXxx = "key" 常量声明。
	re := regexp.MustCompile(`Permission[A-Za-z0-9]+\s*=\s*"([a-z][a-z0-9._-]*)"`)
	keys := map[string]struct{}{}
	for _, match := range re.FindAllStringSubmatch(string(body), -1) {
		keys[match[1]] = struct{}{}
	}
	return keys
}
