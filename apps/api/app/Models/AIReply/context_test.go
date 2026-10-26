package aireply_test

import (
	"testing"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
)

// Label 是提示词里唯一生成作者标识的地方，所以它的每个分支都要有确定行为：
// 缺昵称、缺用户名、两者相同、两者都缺。
func TestAuthorRefLabelCoversEveryIdentityShape(t *testing.T) {
	cases := []struct {
		name string
		ref  aireply.AuthorRef
		want string
	}{
		{name: "both present", ref: aireply.AuthorRef{Username: "alice", DisplayName: "Alice"}, want: "Alice（@alice）"},
		{name: "username only", ref: aireply.AuthorRef{Username: "alice"}, want: "@alice"},
		{name: "display only", ref: aireply.AuthorRef{DisplayName: "张三"}, want: "张三"},
		{name: "identical", ref: aireply.AuthorRef{Username: "alice", DisplayName: "alice"}, want: "@alice"},
		{name: "blank display", ref: aireply.AuthorRef{Username: "alice", DisplayName: "   "}, want: "@alice"},
		{name: "trimmed", ref: aireply.AuthorRef{Username: " alice ", DisplayName: " Alice "}, want: "Alice（@alice）"},
		{name: "empty", ref: aireply.AuthorRef{}, want: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.ref.Label(); got != test.want {
				t.Fatalf("Label() = %q, want %q", got, test.want)
			}
		})
	}
}
