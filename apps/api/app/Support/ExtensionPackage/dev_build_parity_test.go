package extensionpackage

import (
	"bytes"
	"testing"
)

// 同一棵插件树有两个写者：host dev watcher（本包）与发行脚本
// sforum-app-store 的 `plugin/scripts/build.sh` + `sync-digests.mjs`。
// 两边必须产出逐字节相同的二进制与 manifest——只要有一处口径不同，插件树就会
// 在两个 digest 之间永久抖动，发版树永远 `git status` 不干净。
//
// 这两条测试把口径钉在代码里：改任何一边都必须同时改另一边并更新断言。

func TestBackendBuildArgsMatchReleaseBuilder(t *testing.T) {
	// 对照 plugin/scripts/build.sh:
	//   CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin ./
	// 唯一允许不同的是输出路径（dev 先写 .tmp 再按字节比较替换）。
	want := []string{
		"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w",
		"-o", "backend/plugin.tmp", "./",
	}
	got := backendBuildArgs("backend/plugin.tmp")
	if len(got) != len(want) {
		t.Fatalf("backend build argv = %q, want %q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("backend build argv[%d] = %q, want %q (full: %q)", index, got[index], want[index], got)
		}
	}
}

func TestMarshalManifestJSONMatchesReleaseSerializer(t *testing.T) {
	// 同一份文档经 sync-digests.mjs 的 `JSON.stringify(manifest, null, 2) + "\n"`
	// 落盘时的字节形态：键排序、2 空格缩进、结尾换行，且 `<` `>` `&` 不转义。
	manifest := map[string]any{
		"sforumVersion": ">=1.0.0 <2.0.0",
		"description":   "supports a<b> & c",
		"packageFiles": []any{
			map[string]any{"path": "backend/plugin", "digest": "abc"},
		},
	}
	body, err := marshalManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "description": "supports a<b> & c",
  "packageFiles": [
    {
      "digest": "abc",
      "path": "backend/plugin"
    }
  ],
  "sforumVersion": ">=1.0.0 <2.0.0"
}
`
	if string(body) != want {
		t.Fatalf("manifest bytes diverged from the release serializer:\n got: %q\nwant: %q", body, want)
	}
	for _, escaped := range []string{`\u003e`, `\u003c`, `\u0026`} {
		if bytes.Contains(body, []byte(escaped)) {
			t.Fatalf("manifest still HTML-escapes %s: %s", escaped, body)
		}
	}
}
