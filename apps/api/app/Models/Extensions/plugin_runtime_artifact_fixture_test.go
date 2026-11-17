package extensions

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testartifact"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 发布期 fail-closed 闸门（#254）落地后，任何会进入 publication 的成员都必须在
// 盘上有一份**自洽的真实制品**：extension_versions.package_path 指向它，
// package_digest 等于它的树摘要，manifest 声明的 backend 入口在位且摘要相符。
//
// 之前夹具直接写 repeat('b', 64) 并让 package_path 停在空串，那在新闸门下是
// 「不可装载」。所以测试不再自己编 digest：一律经由 testartifact 造制品，
// 拿造出来的坐标（构造手法只有一份，见那个包的包注释）。
//
// attachPluginRuntimeArtifacts 给共享 publication fixture 装上制品工厂与版本行坐标表。
func attachPluginRuntimeArtifacts(t *testing.T, fixture *pluginRuntimePublicationPGFixture) *pluginRuntimePublicationPGFixture {
	t.Helper()
	fixture.artifacts = testartifact.New(t)
	fixture.members = make(map[int64]PluginRuntimeMember, 8)
	fixture.paths = make(map[int64]string, 8)
	return fixture
}

// seedVersion 落一条 durable 版本行，并让它的 package_path / package_digest
// 指向一份真实自洽制品。返回与之一致的成员坐标（版本行 id 即 member.ExtensionVersionID）。
//
// 幂等：同一 versionID 只造一次制品，重复调用返回同一个坐标——测试里多处共享
// 101/102 这些固定版本行，不能因为调用顺序不同产生不同 digest。
func (f *pluginRuntimePublicationPGFixture) seedVersion(
	extensionID string,
	versionID int64,
	version string,
	extensionType string,
) PluginRuntimeMember {
	f.t.Helper()
	if existing, ok := f.members[versionID]; ok {
		return existing
	}
	coords := f.artifacts.Materialize(f.t, extensionID, version)
	backendEntry := ""
	if extensionType == TypePlugin {
		backendEntry = "backend/plugin"
	}
	manifest := runtimeManifestBody(f.t, extensionID, version, extensionType, backendEntry)
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_versions (id, extension_id, version, package_digest, package_path, manifest)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (id) DO UPDATE
		SET package_digest = EXCLUDED.package_digest,
		    package_path = EXCLUDED.package_path
	`, versionID, extensionID, version, coords.Digest, coords.Path, string(manifest)); err != nil {
		f.t.Fatalf("seed fixture version %s@%s: %v", extensionID, version, err)
	}
	// 显式 id 不推进 BIGSERIAL 序列：对齐到 max(id)，否则同一分片里别的包
	// （public schema 的 extension_versions）会拿到已被占用的号并撞 23505。
	testdb.AlignSerial(f.t, f.ctx, f.pool, "extension_versions")
	member := PluginRuntimeMember{
		ExtensionID: extensionID, ExtensionVersionID: versionID,
		ExtensionVersion: version, PackageDigest: coords.Digest,
	}
	f.members[versionID] = member
	f.paths[versionID] = coords.Path
	return member
}

// memberPath 取版本行在盘上的制品目录，供「坏制品」用例定点破坏。
func (f *pluginRuntimePublicationPGFixture) memberPath(t *testing.T, versionID int64) string {
	t.Helper()
	path, ok := f.paths[versionID]
	if !ok {
		t.Fatalf("version row %d has no artifact on disk", versionID)
	}
	return path
}

// builtinArtifact 幂等地为 (id, version) 造一份制品：SaveBuiltin 的重复同步用例
// 会在同一夹具里对同一版本保存多次，digest 漂移会让「不追加 revision」失真。
func (f *pluginRuntimePublicationPGFixture) builtinArtifact(extensionID, version string) (string, string) {
	f.t.Helper()
	coords := f.artifacts.MaterializeOnce(f.t, extensionID, version)
	return coords.Path, coords.Digest
}

// memberAt 取已经落盘的版本行坐标，供断言比对 digest。
func (f *pluginRuntimePublicationPGFixture) memberAt(t *testing.T, versionID int64) PluginRuntimeMember {
	t.Helper()
	member, ok := f.members[versionID]
	if !ok {
		t.Fatalf("version row %d was never seeded", versionID)
	}
	return member
}

// pluginRuntimeTransitionTarget 把成员坐标表达成 transition/authoritative 夹具
// 需要的 Extension 值：PackageDigest 与 ActiveVersionID 必须来自 durable 行，
// 否则发布期闸门会把它判成 digest 不一致。
func pluginRuntimeTransitionTarget(t *testing.T, member PluginRuntimeMember, backendEntry string) Extension {
	t.Helper()
	return transitionFixturePlugin(
		t, member.ExtensionID, member.ExtensionVersionID, member.ExtensionVersion,
		member.PackageDigest, backendEntry,
	)
}
