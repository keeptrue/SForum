package plugin_test

import (
	"testing"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

// TestFixtureRegionDemoContract 锁定 forum.page.regions 三类放置声明
// (hostLink / 设置门控 extensionRoute / l2Widget)通过清单校验。
func TestFixtureRegionDemoContract(t *testing.T) {
	root := fixtureRoot(t, "plugins/sforum-region-demo")
	report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected pass, got errors=%d checks=%#v", report.Errors, report.Checks)
	}
	if report.Manifest.ID != "sforum.region.demo" {
		t.Fatalf("id=%s", report.Manifest.ID)
	}
	if len(report.Manifest.Contributions) != 3 {
		t.Fatalf("contributions=%d, want 3", len(report.Manifest.Contributions))
	}
	gated := 0
	for _, contribution := range report.Manifest.Contributions {
		if contribution.Point != "forum.page.regions" {
			t.Fatalf("unexpected point %s", contribution.Point)
		}
		if contribution.EnabledBySetting != "" {
			gated++
		}
	}
	if gated != 1 {
		t.Fatalf("gated contributions=%d, want 1", gated)
	}
}
