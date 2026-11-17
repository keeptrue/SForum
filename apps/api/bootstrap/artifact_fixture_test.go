package bootstrap

import (
	"os"
	"sync"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testartifact"
)

// bootstrapArtifactRoot 是整个测试二进制共享的制品根目录。
//
// 为什么不能挂 t.TempDir()：plugin_runtime_publications 是全局追加式账本，本包里
// 有些用例会直接钉一条不可变 publication（模拟生产历史），后续用例的 full-set 过渡
// 会把那些成员**继承**下来。发布期闸门（#254）会逐个回查盘上制品，所以被钉住的
// 成员制品必须活到进程结束——单个用例的临时目录会先被删掉，下一个用例就会撞上
// 「artifact directory is missing」。
var bootstrapArtifactRoot = sync.OnceValues(func() (string, error) {
	return os.MkdirTemp("", "sforum-bootstrap-artifacts-")
})

func bootstrapArtifacts(t *testing.T) *testartifact.Factory {
	t.Helper()
	root, err := bootstrapArtifactRoot()
	if err != nil {
		t.Fatalf("create bootstrap artifact root: %v", err)
	}
	return testartifact.NewWithRoot(t, root)
}
