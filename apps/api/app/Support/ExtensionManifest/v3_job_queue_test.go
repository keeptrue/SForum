package extensionmanifest

import (
	"testing"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// 队列声明只在两条道之间选择：能通过清单校验的取值必须能被入队侧如实映射，
// 两侧的常量一旦漂移就会出现「清单允许、入队回退」的静默降级。
func TestManifestV3JobQueueDeclarations(t *testing.T) {
	if PluginJobQueueDefault != supportjobs.PluginJobQueueDefault ||
		PluginJobQueueAI != supportjobs.PluginJobQueueAI {
		t.Fatalf("queue vocabulary drifted: manifest=%q/%q jobs=%q/%q",
			PluginJobQueueDefault, PluginJobQueueAI,
			supportjobs.PluginJobQueueDefault, supportjobs.PluginJobQueueAI)
	}

	accepted := []string{"", PluginJobQueueDefault, PluginJobQueueAI}
	for _, queue := range accepted {
		manifest := completeV3Manifest()
		job := manifest.Jobs[0]
		job.Queue = queue
		manifest.Jobs = append(manifest.Jobs, job)
		manifest.Jobs[1].ID = "demo.v3.job.queued"
		manifest.Jobs[1].ContractVersion = "demo.v3.job.queued@1"
		manifest.Jobs[1].Name = "demo.v3.queued"
		if err := Validate(manifest); err != nil {
			t.Fatalf("queue %q should be accepted: %v", queue, err)
		}
	}

	for _, queue := range []string{"critical", "mail", "notifications", "search", "maintenance", "unknown", "ai2"} {
		manifest := completeV3Manifest()
		job := manifest.Jobs[0]
		job.Queue = queue
		manifest.Jobs = append(manifest.Jobs, job)
		manifest.Jobs[1].ID = "demo.v3.job.queued"
		manifest.Jobs[1].ContractVersion = "demo.v3.job.queued@1"
		manifest.Jobs[1].Name = "demo.v3.queued"
		if err := Validate(manifest); err == nil {
			t.Fatalf("queue %q must not be declarable by an extension", queue)
		}
	}
}

func TestManifestV3JobQueueNormalizesCase(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.Jobs[0].Queue = " AI "
	normalized := Normalize(manifest)
	if normalized.Jobs[0].Queue != PluginJobQueueAI {
		t.Fatalf("normalized queue = %q", normalized.Jobs[0].Queue)
	}
}
