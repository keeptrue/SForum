package extensionopenapi

import (
	"path/filepath"
	"runtime"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	"github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage/referencefixture"
)

// A1 / R2：带 loader 数据的参考包必须能在**构建期**通过路由 schema 目录校验。
//
// 为什么需要这条测试：`extension validate` / `extension test` 不会构建路由 schema 目录，
// 所以一个「路由声明了 responseSchema、但 OpenAPI 片段里没有可解析的 schema 定义」的包
// 会在**作者命令全绿**的情况下让 API 启动失败
// （`prepare startup route schema publication: ... route schema is missing`）。
// 这正是任务书 C 项「CLI 与运行时校验器不一致」的同一类缺口；对参考包而言，
// 这条测试把该契约钉在构建期，而不是等运维在 dev 重启时才发现。
func TestReferencePageLoaderRouteSchemasResolve(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	packageRoot := filepath.Join(root, "extensions/fixtures/plugins/sforum-page-loader-reference")

	// 夹具不提交 backend/plugin 二进制；暂存副本现场构建并把摘要对齐后再校验。
	packageRoot = referencefixture.StageBackendFixture(t, root, packageRoot)

	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load reference package: %v", err)
	}
	digest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{{
		Root: packageRoot, ExtensionID: manifest.ID, Version: manifest.Version,
		PackageDigest: digest, Manifest: manifest,
	}}}); err != nil {
		t.Fatalf("reference package route schema catalog: %v", err)
	}
}
