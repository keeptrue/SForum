package plugin_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage/referencefixture"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// apps/api/sdk/plugin -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}

func fixtureRoot(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "extensions/fixtures", rel)
}

func TestFixtureEventsContract(t *testing.T) {
	root := fixtureRoot(t, "plugins/sforum-contract-events")
	report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected pass, got errors=%d checks=%#v", report.Errors, report.Checks)
	}
	if report.Manifest.ID != "sforum.contract.events" {
		t.Fatalf("id=%s", report.Manifest.ID)
	}
	// 事件与贡献点应出现在 ok checks 中。
	codes := checkCodes(report)
	for _, want := range []string{"manifest.ok", "event.known", "contribution.point_ok"} {
		if !codes[want] {
			t.Fatalf("missing check %s in %#v", want, report.Checks)
		}
	}
}

func TestFixtureSchedulesContract(t *testing.T) {
	root := fixtureRoot(t, "plugins/sforum-contract-schedules")
	report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected pass: %#v", report.Checks)
	}
	// 宿主 schedule 目录必须非空；fixture 本身不声明 cron。
	schedules := pluginsdk.CoreSchedules()
	if len(schedules) == 0 {
		t.Fatal("core schedules empty")
	}
	foundIdentity := false
	for _, s := range schedules {
		if s.ID == "identity.cleanup_sessions" {
			foundIdentity = true
			break
		}
	}
	if !foundIdentity {
		t.Fatalf("missing identity.cleanup_sessions in %#v", schedules)
	}
}

func TestAllStaticExtensionFixturesPassCurrentContract(t *testing.T) {
	patterns := []string{"plugins/*/sforum.extension.json", "themes/*/sforum.extension.json"}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(fixtureRoot(t, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, manifestPath := range paths {
			root := filepath.Dir(manifestPath)
			t.Run(filepath.Base(root), func(t *testing.T) {
				// 带可执行后端的夹具不提交二进制，摘要必须先在暂存副本里现场
				// 构建、重新对齐，再走整包契约校验。
				root = referencefixture.StageBackendFixture(t, repoRoot(t), root)
				report, err := pluginsdk.LoadAndTest(root, pluginsdk.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if !report.OK {
					t.Fatalf("fixture no longer satisfies the current extension contract: %#v", report.Checks)
				}
			})
		}
	}
}

func checkCodes(report pluginsdk.Report) map[string]bool {
	out := map[string]bool{}
	for _, c := range report.Checks {
		out[c.Code] = true
	}
	return out
}
