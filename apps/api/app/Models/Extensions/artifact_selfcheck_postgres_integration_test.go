package extensions

import (
	"errors"
	"testing"

	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

// 启动期自检必须真的把 durable 行解析成「成员 + 路径」再判盘：一份 publication
// 里只要有一个成员的制品不可装载，就要返回带坐标的错误，让 bootstrap 直接进
// recovery-only，而不是把 runtime 拉起来再等 stage 失败。
func TestVerifyPluginRuntimePublicationArtifactsDetectsBrokenMember(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "artifact-selfcheck")
	member := fixture.firstMember()
	if _, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, []PluginRuntimeMember{member},
	); err != nil {
		t.Fatalf("publish fixture member: %v", err)
	}

	// 指向一个不存在的制品目录：等价于「行在、盘缺」。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions SET package_path = $2 WHERE id = $1
	`, member.ExtensionVersionID, t.TempDir()+"/absent"); err != nil {
		t.Fatalf("point fixture member at a missing artifact: %v", err)
	}

	err := fixture.store.VerifyPluginRuntimePublicationArtifacts(fixture.ctx, 0)
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("broken member must surface as an artifact error, got %v", err)
	}
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("broken member must carry coordinates, got %v", err)
	}
	if coordinates.ExtensionID != member.ExtensionID {
		t.Fatalf("coordinates=%#v want extension %s", coordinates, member.ExtensionID)
	}
	if coordinates.Reason != "artifact directory is missing" {
		t.Fatalf("reason=%q", coordinates.Reason)
	}
}

// 没有任何 publication（全新库）时自检必须放行，不能把首次启动拦下来。
func TestVerifyPluginRuntimePublicationArtifactsAllowsEmptyHistory(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "artifact-selfcheck-empty")
	if err := fixture.store.VerifyPluginRuntimePublicationArtifacts(fixture.ctx, 0); err != nil {
		t.Fatalf("empty publication history must verify, got %v", err)
	}
}
