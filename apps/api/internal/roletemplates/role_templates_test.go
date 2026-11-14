package roletemplates_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/internal/roletemplates"
)

// TestRenderIsStable 保证渲染是纯函数：同一份 seeds 必得同一份字节。
func TestRenderIsStable(t *testing.T) {
	first := roletemplates.Render(identity.SeedRoleTemplates)
	second := roletemplates.Render(identity.SeedRoleTemplates)
	if first != second {
		t.Fatal("Render is not deterministic for the same seeds")
	}
	if first != roletemplates.Current() {
		t.Fatal("Current() must equal Render(SeedRoleTemplates)")
	}
	// 首行是生成物标记：手改这个文件必须显眼，评审也要能一眼看出来。
	if line, _, _ := strings.Cut(first, "\n"); !strings.Contains(line, "Code generated") || !strings.Contains(line, "DO NOT EDIT") {
		t.Fatalf("first line is not the generated marker: %q", line)
	}
}

// TestCheckedInCatalogMatchesSeeds 是生成物一致性闸门：磁盘上的 roleTemplates.ts
// 必须逐字节等于渲染结果。手改或忘记重跑生成命令都会在这里红。
func TestCheckedInCatalogMatchesSeeds(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, roletemplates.DefaultOutput)
	checkedIn, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated catalog: %v", err)
	}
	if rendered := roletemplates.Current(); string(checkedIn) != rendered {
		t.Fatalf("%s is stale; run `cd apps/api && go run ./cmd/sforum roles:templates --write`", roletemplates.DefaultOutput)
	}
}

// TestCatalogCarriesTheDriftedGrants 盯住 PRD2-04 的具体漂移：生成物必须给出
// operator 的 trust.manage / attachment.upload_policy.manage 与 tech_admin 的 ai.manage。
func TestCatalogCarriesTheDriftedGrants(t *testing.T) {
	rendered := roletemplates.Current()
	for _, want := range []string{
		"'trust.manage'",
		"'attachment.upload_policy.manage'",
		"'ai.manage'",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated catalog missing %s", want)
		}
	}

	for _, template := range identity.SeedRoleTemplates {
		switch template.Key {
		case identity.RoleOperator:
			for _, permission := range []string{
				identity.PermissionTrustManage,
				identity.PermissionAttachmentUploadPolicyManage,
				identity.PermissionEntityMetaManage,
			} {
				if !slices.Contains(template.PermissionKeys, permission) {
					t.Fatalf("operator seeds missing %q", permission)
				}
			}
		case identity.RoleTechAdmin:
			if !slices.Contains(template.PermissionKeys, identity.PermissionAIManage) {
				t.Fatal("tech_admin seeds missing ai.manage")
			}
		}
	}
}

// repoRoot 从包目录向上找到仓库根（含 apps/web/app/config/roleTemplates.ts 的那一层）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, roletemplates.DefaultOutput)); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root above %s", dir)
		}
		dir = parent
	}
}
