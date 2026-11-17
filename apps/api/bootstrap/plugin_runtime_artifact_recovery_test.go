package bootstrap

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
)

const (
	artifactTestExtensionID = "sforum-shortcodes"
	artifactTestVersion     = "1.3.10"
	artifactTestDigest      = "b4f399bdbd8f82304dae57b7615293068c030bf42feed72c54dfd380525d6d74"
	artifactTestEntry       = "backend/plugin"
)

// 现场链路：装载点 → stage → apply。三层包装后恢复码与四坐标都必须还在。
func artifactTestWrappedCause() error {
	leaf := artifactintegrity.EntryUnavailable(
		artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry, fs.ErrNotExist)
	staged := fmt.Errorf("stage %s@%s: %w", artifactTestExtensionID, artifactTestVersion, leaf)
	return fmt.Errorf("apply plugin runtime revision %d attempt %d: %w", 1540, 1, staged)
}

// 每个能把 API 推进 recovery-only 的失败点都必须有类型化 code 与可读 message。
// 既有 6 个分支按原文比对（防回归），新增分支按四坐标比对。
func TestPluginRuntimeRecoveryClassification(t *testing.T) {
	cases := []struct {
		name            string
		cause           error
		wantCode        string
		wantMessage     string
		wantCoordinates []string
	}{
		{
			name:        "startup pending",
			cause:       errPluginRuntimeCoordinatorStartupPending,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin runtime did not converge before the startup deadline; an extension lifecycle operation is still open",
		},
		{
			name:        "bootstrap abi incompatible",
			cause:       pluginbootstrap.ErrBootstrapABIIncompatible,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin process bootstrap ABI is incompatible",
		},
		{
			name:        "executable architecture",
			cause:       pluginbootstrap.ErrExecutableArchitecture,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin executable architecture is incompatible",
		},
		{
			name:        "executable dependency",
			cause:       pluginbootstrap.ErrExecutableDependency,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin executable dependency is unavailable",
		},
		{
			name:        "executable permission",
			cause:       pluginbootstrap.ErrExecutablePermission,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin executable permission was denied",
		},
		{
			name:        "process start",
			cause:       pluginbootstrap.ErrProcessStart,
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin process failed to start",
		},
		{
			name:        "unclassified",
			cause:       errors.New("something nobody typed yet"),
			wantCode:    health.PluginRuntimeRecoveryCode,
			wantMessage: "plugin runtime convergence failed; inspect API logs for the exact cause",
		},
		{
			name:            "artifact entry missing through stage and apply",
			cause:           artifactTestWrappedCause(),
			wantCode:        health.PluginArtifactIncompleteCode,
			wantCoordinates: []string{artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry},
		},
		{
			name: "artifact entry path rejected",
			cause: artifactintegrity.EntryPathRejected(
				artifactTestExtensionID, artifactTestVersion, artifactTestDigest, "../escape/plugin",
				extensions.ErrInvalidManifest),
			wantCode:        health.PluginArtifactIncompleteCode,
			wantCoordinates: []string{artifactTestExtensionID, artifactTestVersion, artifactTestDigest, "../escape/plugin"},
		},
		{
			name: "artifact digest mismatch",
			cause: artifactintegrity.EntryDigestMismatch(
				artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry,
				"b29b4775581dc792d1135f1bfd8ecf4bc7bb02e6b03f77bd083f9ec26f1726f4",
				"d171bf2d74d7e1c088b1ea9bb46b858cabf7c3e04505282b01131143ab29533d"),
			wantCode:        health.PluginArtifactIncompleteCode,
			wantCoordinates: []string{artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry},
		},
		{
			// exec 路径的真实现场：OpenVerifiedRuntimeExecutable 的摘要不符 →
			// prepareVerifiedPluginExecutable 用 errors.Join 追加
			// ErrPluginExecutableUntrusted → stage → apply。
			name: "exec path digest mismatch behind errors.Join",
			cause: errors.Join(
				extensionsruntime.ErrPluginExecutableUntrusted,
				artifactintegrity.EntryDigestMismatch(
					artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry,
					"b29b4775581dc792d1135f1bfd8ecf4bc7bb02e6b03f77bd083f9ec26f1726f4",
					"d171bf2d74d7e1c088b1ea9bb46b858cabf7c3e04505282b01131143ab29533d",
					extensions.ErrFrontendPackageChanged),
			),
			wantCode:        health.PluginArtifactIncompleteCode,
			wantCoordinates: []string{artifactTestExtensionID, artifactTestVersion, artifactTestDigest, artifactTestEntry},
		},
		{
			// Models 侧 stagedArtifactUsable 包装同一个哨兵但不带坐标。
			name:        "artifact family without coordinates",
			cause:       fmt.Errorf("%w: sforum-shortcodes artifact directory is missing", extensions.ErrArtifactUnusable),
			wantCode:    health.PluginArtifactIncompleteCode,
			wantMessage: "plugin artifact is incomplete; inspect API logs for the exact cause",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := pluginRuntimeRecoveryCode(testCase.cause); code != testCase.wantCode {
				t.Fatalf("code=%q want %q (cause=%v)", code, testCase.wantCode, testCase.cause)
			}
			message := pluginRuntimeRecoveryMessage(testCase.cause)
			if testCase.wantMessage != "" && message != testCase.wantMessage {
				t.Fatalf("message=%q want %q", message, testCase.wantMessage)
			}
			for _, coordinate := range testCase.wantCoordinates {
				if !strings.Contains(message, coordinate) {
					t.Fatalf("message %q must carry coordinate %q", message, coordinate)
				}
			}
			if len(testCase.wantCoordinates) > 0 {
				if !strings.Contains(message, "extensionId=") || !strings.Contains(message, "version=") ||
					!strings.Contains(message, "digest=") || !strings.Contains(message, "path=") {
					t.Fatalf("artifact message must label all four coordinates, got %q", message)
				}
			}
		})
	}
}

// 恢复 message 必须带发布修订，否则运维无法定位是哪一次发布钉住了坏制品。
func TestPluginRuntimeRecoveryMessageCarriesPublicationRevision(t *testing.T) {
	message := pluginRuntimeRecoveryMessageWithRevision(artifactTestWrappedCause(), 1540)
	if !strings.Contains(message, "publicationRevision=1540") {
		t.Fatalf("message must carry the publication revision, got %q", message)
	}
	// 非制品族失败不追加修订：那些分支的文案是既有契约。
	if got := pluginRuntimeRecoveryMessageWithRevision(pluginbootstrap.ErrProcessStart, 1540); got != "plugin process failed to start" {
		t.Fatalf("non-artifact message must stay unchanged, got %q", got)
	}
}

// 制品错误不得泄露宿主绝对路径：message 进未认证的 /api/v1/ready 响应。
func TestPluginRuntimeRecoveryMessageOmitsAbsoluteArtifactPath(t *testing.T) {
	cause := fmt.Errorf("apply plugin runtime revision 1540 attempt 1: %w",
		artifactintegrity.EntryUnavailable(artifactTestExtensionID, artifactTestVersion, artifactTestDigest,
			artifactTestEntry, fmt.Errorf("stat /home/operator/storage/extensions/x/backend/plugin: %w", fs.ErrNotExist)))
	message := pluginRuntimeRecoveryMessage(cause)
	if strings.Contains(message, "/home/") || strings.Contains(message, "/storage/") {
		t.Fatalf("recovery message must stay relative, got %q", message)
	}
}

// 路径解析拒绝同时保留 ErrInvalidManifest，既有调用方判断不受影响。
func TestArtifactPathRejectedKeepsInvalidManifest(t *testing.T) {
	cause := artifactintegrity.EntryPathRejected(
		artifactTestExtensionID, artifactTestVersion, artifactTestDigest, "../escape/plugin",
		extensions.ErrInvalidManifest)
	if !errors.Is(cause, extensions.ErrInvalidManifest) {
		t.Fatalf("ErrInvalidManifest must stay reachable, got %v", cause)
	}
	if !errors.Is(cause, extensions.ErrArtifactUnusable) {
		t.Fatalf("ErrArtifactUnusable must match, got %v", cause)
	}
}
