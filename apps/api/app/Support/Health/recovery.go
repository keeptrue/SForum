package health

import "strings"

const PluginRuntimeRecoveryCode = "plugin_runtime_convergence_failed"

// PluginArtifactIncompleteCode 表示失败发生在制品装载阶段，而不是收敛时序或
// 子进程启动阶段：已发布修订钉住的某个制品在盘上不完整（缺 backend 入口、
// 入口路径被拒绝，或入口不是普通文件）。
//
// 它必须比 PluginRuntimeRecoveryCode 更精确：后者只说明「收敛失败」，运维无法
// 判断该去修制品、等时序，还是查进程启动。issue #253 的现场就是这一族失败被
// 归到 default 分支，message 只说「inspect API logs」。
const PluginArtifactIncompleteCode = "plugin_artifact_incomplete"

type RecoveryArtifact struct {
	ExtensionID string `json:"extensionId"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
}

// RecoveryRequirement describes a Host-owned recovery-only boot. It is
// immutable after bootstrap and never changes extension lifecycle authority.
type RecoveryRequirement struct {
	Code                string             `json:"code"`
	Component           string             `json:"component"`
	Message             string             `json:"message"`
	PublicationRevision int64              `json:"publicationRevision,omitempty"`
	Artifacts           []RecoveryArtifact `json:"artifacts,omitempty"`
}

func (r *RecoveryRequirement) Active() bool {
	return r != nil && strings.TrimSpace(r.Code) != ""
}

func ApplyRecoveryRequirement(report ReadyReport, requirement *RecoveryRequirement) ReadyReport {
	if !requirement.Active() {
		return report
	}
	report.Ready = false
	report.Status = "not_ready"
	report.Recovery = requirement
	report.Components = append(report.Components, ComponentResult{
		Name:     requirement.Component,
		Status:   StatusError,
		Required: true,
		Error:    requirement.Message,
	})
	return report
}
