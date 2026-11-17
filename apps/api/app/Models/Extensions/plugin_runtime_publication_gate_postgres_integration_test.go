package extensions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

// 发布期 fail-closed 闸门（#254）的行为契约：写入 publication 之前，成员的制品
// 必须自洽；任一成员不可装载就整次拒绝，且一行都不写。
//
// 这组用例直接打共享插入口（insertPluginRuntimePublication），因此覆盖 9 个生产
// 发布调用点共同继承的那道判据。判定本身在 ArtifactIntegrity.Verify，这里只钉
// 发布路径是否真的调用它、以及错误坐标是否原样透出。

func assertPluginRuntimeArtifactRejection(
	t *testing.T,
	err error,
	member PluginRuntimeMember,
	wantPath, wantReasonPrefix string,
) {
	t.Helper()
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("rejection must be an artifact error, got %v", err)
	}
	coordinates, ok := artifactintegrity.Coordinates(err)
	if !ok {
		t.Fatalf("rejection must carry coordinates, got %v", err)
	}
	if coordinates.ExtensionID != member.ExtensionID ||
		coordinates.Version != member.ExtensionVersion ||
		coordinates.Digest != member.PackageDigest {
		t.Fatalf("coordinates lost durable identity: %#v member=%#v", coordinates, member)
	}
	if coordinates.Path != wantPath {
		t.Fatalf("coordinates path=%q want %q", coordinates.Path, wantPath)
	}
	if !strings.HasPrefix(coordinates.Reason, wantReasonPrefix) {
		t.Fatalf("coordinates reason=%q want prefix %q", coordinates.Reason, wantReasonPrefix)
	}
}

func TestPublishPluginRuntimePublicationRejectsMissingBackendEntry(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_missing_entry")
	member := fixture.firstMember()
	entry := filepath.Join(fixture.memberPath(t, member.ExtensionVersionID), "backend", "plugin")
	if err := os.Remove(entry); err != nil {
		t.Fatalf("remove backend entry: %v", err)
	}

	_, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 7, []PluginRuntimeMember{member},
	)
	assertPluginRuntimeArtifactRejection(t, err, member, "backend/plugin", "missing")
	// 拒绝必须发生在写入之前：既没有 publication，也没有 member 行。
	assertPluginRuntimePublicationCount(t, fixture, 0)
	var memberRows int
	if err := fixture.pool.QueryRow(
		fixture.ctx, `SELECT count(*) FROM plugin_runtime_publication_members`,
	).Scan(&memberRows); err != nil {
		t.Fatal(err)
	}
	if memberRows != 0 {
		t.Fatalf("rejected publication wrote %d member rows", memberRows)
	}
}

// 成员行的 package_path 为空（durable 行没记住制品位置，或该行已不存在）时必须
// 报带坐标的「package path is empty」，而不是放行或塌成无坐标的发布冲突。
func TestPublishPluginRuntimePublicationRejectsEmptyPackagePath(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_empty_path")
	member := fixture.firstMember()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions SET package_path = '' WHERE id = $1
	`, member.ExtensionVersionID); err != nil {
		t.Fatalf("blank package path: %v", err)
	}

	_, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 7, []PluginRuntimeMember{member},
	)
	assertPluginRuntimeArtifactRejection(t, err, member, ".", "package path is empty")
	assertPluginRuntimePublicationCount(t, fixture, 0)
}

func TestPublishPluginRuntimePublicationRejectsBackendEntryDigestMismatch(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_entry_digest")
	member := fixture.firstMember()
	entry := filepath.Join(fixture.memberPath(t, member.ExtensionVersionID), "backend", "plugin")
	// 文件名对、内容被换掉：只 stat 的判据会放行，必须由摘要比对拦下。
	if err := os.WriteFile(entry, []byte("substituted binary"), 0o755); err != nil {
		t.Fatalf("substitute backend entry: %v", err)
	}

	_, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 7, []PluginRuntimeMember{member},
	)
	assertPluginRuntimeArtifactRejection(t, err, member, "backend/plugin", "digest mismatch: want ")
	assertPluginRuntimePublicationCount(t, fixture, 0)
}

func TestPublishPluginRuntimePublicationRejectsTreeDigestMismatch(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_tree_digest")
	member := fixture.firstMember()
	// 入口与 manifest 都自洽，只有 durable 行冻结的树摘要与盘上不符。
	member.PackageDigest = flippedDigest(member.PackageDigest)

	_, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationRecovery, 0, []PluginRuntimeMember{member},
	)
	assertPluginRuntimeArtifactRejection(t, err, member, ".", "tree digest mismatch: want ")
	assertPluginRuntimePublicationCount(t, fixture, 0)
}

// 正例：全部成员自洽时必须照常发布，闸门不能把健康发布打成永久拒绝。
func TestPublishPluginRuntimePublicationAcceptsIntactArtifacts(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_accepts_intact")
	members := []PluginRuntimeMember{fixture.secondMember(), fixture.firstMember()}

	publication, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 11, members,
	)
	if err != nil {
		t.Fatalf("intact members must publish, got %v", err)
	}
	if publication.Revision <= 0 || publication.MemberCount != 2 {
		t.Fatalf("publication=%#v", publication)
	}
	assertPluginRuntimePublicationMembers(t, publication, members...)

	// 已发布的成员被带外删除：下一次发布必须拒绝，旧 revision 继续当权。
	if err := os.Remove(filepath.Join(
		fixture.memberPath(t, fixture.firstMember().ExtensionVersionID), "backend", "plugin",
	)); err != nil {
		t.Fatalf("remove published entry: %v", err)
	}
	_, err = fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationUpgrade, 12, members,
	)
	assertPluginRuntimeArtifactRejection(t, err, fixture.firstMember(), "backend/plugin", "missing")
	latest, latestErr := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if latestErr != nil || !samePluginRuntimePublication(latest, publication) {
		t.Fatalf("rejected publish disturbed current revision: latest=%#v err=%v", latest, latestErr)
	}
}

// genesis：空成员集没有制品可校验，必须继续可发布（否则全新库起不来）。
func TestPublishPluginRuntimePublicationAcceptsEmptyMemberSet(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "gate_empty_set")
	publication, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	)
	if err != nil {
		t.Fatalf("empty member set must publish, got %v", err)
	}
	if publication.MemberCount != 0 || publication.Revision <= 0 {
		t.Fatalf("publication=%#v", publication)
	}
}

// flippedDigest 换掉 hex 摘要的一位，保持长度合法（64 hex）。
func flippedDigest(digest string) string {
	if digest == "" {
		return strings.Repeat("a", 64)
	}
	replacement := "0"
	last := len(digest) - 1
	if digest[last] == '0' {
		replacement = "1"
	}
	return digest[:last] + replacement
}
