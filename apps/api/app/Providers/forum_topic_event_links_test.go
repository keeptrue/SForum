package providers

import (
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

func TestAbsoluteTopicEventURLUsesConfiguredModeAndBasePath(t *testing.T) {
	topic := forum.TopicSummary{ID: 42, Slug: "hello-world"}
	tests := []struct {
		mode string
		want string
	}{
		{mode: "id_slug", want: "https://forum.example/community/t/42/hello-world"},
		{mode: "id", want: "https://forum.example/community/t/42"},
		{mode: "slug", want: "https://forum.example/community/t/hello-world"},
	}
	for _, test := range tests {
		got, err := absoluteTopicEventURL("https://forum.example/community/?stale=1#fragment", test.mode, topic)
		if err != nil {
			t.Fatalf("absoluteTopicEventURL(%s): %v", test.mode, err)
		}
		if got != test.want {
			t.Fatalf("absoluteTopicEventURL(%s) = %q, want %q", test.mode, got, test.want)
		}
	}
}

func TestAbsoluteTopicEventURLRejectsInvalidSiteURL(t *testing.T) {
	if _, err := absoluteTopicEventURL("/relative", "id", forum.TopicSummary{ID: 42}); err == nil {
		t.Fatal("expected invalid site URL error")
	}
}
