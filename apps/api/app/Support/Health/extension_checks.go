package health

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ContributionLister 列出已启用插件的有效贡献。
type ContributionLister interface {
	EffectiveContributions(ctx context.Context) ([]extensions.EffectiveContribution, error)
}

// RuntimeStatusReader 读取扩展运行时状态（不启动进程）。
type RuntimeStatusReader interface {
	Status(ctx context.Context, extension extensions.Extension) extensions.RuntimeStatus
}

// AppendExtensionHealthComponents 将 system.health.checks 贡献合并进 /ready 组件列表（F4.3）。
// 不调用插件 RPC：extensionRuntime 映射 runtime.state；static 在扩展 enabled 时固定 ok。
// 失败贡献被跳过，不拖垮核心探针。
func AppendExtensionHealthComponents(
	ctx context.Context,
	base []ComponentResult,
	contributions ContributionLister,
	runtimes RuntimeStatusReader,
) []ComponentResult {
	if contributions == nil {
		return base
	}
	items, err := contributions.EffectiveContributions(ctx)
	if err != nil || len(items) == 0 {
		return base
	}
	seenNames := map[string]struct{}{}
	for _, c := range base {
		seenNames[c.Name] = struct{}{}
	}
	out := append([]ComponentResult{}, base...)
	for _, contribution := range items {
		if contribution.Point != extensionmanifest.PointSystemHealthChecks {
			continue
		}
		var payload extensionmanifest.HealthCheckContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			continue
		}
		name := strings.TrimSpace(payload.Component)
		if name == "" {
			// 默认组件名：extension.<id>.health.<contributionId>
			name = "extension." + contribution.ExtensionID + "." + contribution.ID
		}
		// 避免覆盖 core 组件名（postgres/redis）。
		if _, clash := seenNames[name]; clash {
			name = "extension." + contribution.ExtensionID + "." + name
		}
		if _, clash := seenNames[name]; clash {
			continue
		}
		result := ComponentResult{
			Name:     name,
			Required: payload.Required,
			Status:   StatusOK,
			Latency:  (0 * time.Millisecond).String(),
		}
		switch strings.TrimSpace(payload.Type) {
		case "static":
			// 能出现在 EffectiveContributions 中即表示插件已启用。
			result.Status = StatusOK
		case "extensionRuntime":
			if runtimes == nil {
				result.Status = StatusSkipped
				result.Error = "runtime status unavailable"
			} else {
				// Status 仅依赖 Extension.ID / Manifest 钩子计数等；最小行即可。
				ext := extensions.Extension{ID: contribution.ExtensionID}
				status := runtimes.Status(ctx, ext)
				result.Status = mapRuntimeState(status.State)
				if status.LastError != "" && result.Status != StatusOK {
					result.Error = status.LastError
				}
			}
		default:
			continue
		}
		// required 失败由 Evaluate 汇总；此处只追加组件。
		seenNames[name] = struct{}{}
		out = append(out, result)
	}
	return out
}

func mapRuntimeState(state string) string {
	switch strings.TrimSpace(state) {
	case extensions.RuntimeRunning:
		return StatusOK
	case extensions.RuntimeDegraded, extensions.RuntimeStarting:
		return StatusDegraded
	case extensions.RuntimeFailed, extensions.RuntimeStopped:
		return StatusError
	default:
		return StatusDegraded
	}
}

// EvaluateWithExtensionContributions 在核心 checker 之后合并扩展 health 贡献。
func EvaluateWithExtensionContributions(
	ctx context.Context,
	checkers []Checker,
	contributions ContributionLister,
	runtimes RuntimeStatusReader,
) ReadyReport {
	// Evaluate core，再手动 merge 扩展组件并调整 Ready/Status（不在 ready 路径调插件 RPC）。
	report := Evaluate(ctx, checkers)
	extra := AppendExtensionHealthComponents(ctx, nil, contributions, runtimes)
	if len(extra) == 0 {
		return report
	}
	report.Components = append(report.Components, extra...)
	// 重新评估 required 失败与 degraded。
	report.Ready = true
	report.Status = "ready"
	for _, c := range report.Components {
		if c.Status == StatusError && c.Required {
			report.Ready = false
			report.Status = "not_ready"
			break
		}
	}
	if report.Ready {
		for _, c := range report.Components {
			if c.Status == StatusError || c.Status == StatusDegraded {
				report.Status = StatusDegraded
				break
			}
		}
	}
	return report
}
