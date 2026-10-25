package bootstrap

import "testing"

// TestDevWatchEventRelevantIgnoresGoBuildTemporaryArtifacts 固定「构建产物不得反过来
// 触发构建」这条规则：Go 的 `-o X` 会先写 X-go-tmp-umask 再改名，而 BuildBackend 的
// X 是 backend/plugin.tmp，所以真实事件名是 plugin.tmp-go-tmp-umask。
// 同时确认这条规则没有误伤真实源码与最终制品。
func TestDevWatchEventRelevantIgnoresGoBuildTemporaryArtifacts(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "go linker temp beside plugin.tmp is ignored",
			path: "/repo/plugin/plugins/sforum.store/backend/plugin.tmp-go-tmp-umask",
			want: false,
		},
		{
			name: "build staging file is ignored",
			path: "/repo/plugin/plugins/sforum.store/backend/plugin.tmp",
			want: false,
		},
		{
			name: "go source edit still triggers a rebuild",
			path: "/repo/plugin/plugins/sforum.store/backend/main.go",
			want: true,
		},
		{
			name: "built plugin artifact still triggers a rebuild",
			path: "/repo/plugin/plugins/sforum.store/backend/plugin",
			want: true,
		},
		{
			name: "manifest edit still triggers a rebuild",
			path: "/repo/plugin/plugins/sforum.store/sforum.extension.json",
			want: true,
		},
		{
			name: "dependency directory stays excluded",
			path: "/repo/plugin/plugins/sforum.store/frontend/node_modules/pkg/index.js",
			want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := devWatchEventRelevant(testCase.path); got != testCase.want {
				t.Fatalf("devWatchEventRelevant(%q) = %v, want %v", testCase.path, got, testCase.want)
			}
		})
	}
}
