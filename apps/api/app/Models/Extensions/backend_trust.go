package extensions

import (
	"context"
	"errors"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// CodeUntrustedBackendRestricted 非 super_admin 试图引入或执行非内置后端插件时的稳定 reason。
const CodeUntrustedBackendRestricted = "extension.backend_execution_restricted"

// ErrUntrustedBackendRestricted 仅活跃 super_admin 可安装/升级/启用/校验/迁移/卸载
// 带后端入口的非内置插件。builtin 与前端-only 插件不受此限。
var ErrUntrustedBackendRestricted = errors.New("extensions: untrusted backend execution restricted to super_admin")

// hasExecutableBackend 宿主会通过 go-plugin 启动的后端入口是否声明。
func hasExecutableBackend(manifest Manifest) bool {
	return strings.TrimSpace(manifest.Backend.Entry) != ""
}

// isBuiltinSource 内置包（Git/镜像同步）视为受保护信任根，可由 plugin.manage 运维。
func isBuiltinSource(source string) bool {
	return strings.TrimSpace(source) == SourceBuiltin
}

// requireSuperAdminForUntrustedBackend 在引入或执行非内置后端代码前调用。
// 决策：上传/非内置后端插件 = 主机代码执行边界，仅 super_admin；
// extension.plugin.manage 可配置/禁用内置与前端-only 插件，不可引入任意后端二进制。
func requireSuperAdminForUntrustedBackend(actor identity.Actor, source string, manifest Manifest) error {
	if !hasExecutableBackend(manifest) {
		return nil
	}
	if isBuiltinSource(source) {
		return nil
	}
	if actor.IsSuperAdmin() {
		return nil
	}
	return ErrUntrustedBackendRestricted
}

// denyUntrustedBackend 记录拒绝审计（不含包内容/密钥），并写 extension_events（若有 id）。
func (s *serviceCore) denyUntrustedBackend(ctx context.Context, actor identity.Actor, extensionID, action string) {
	if s == nil {
		return
	}
	meta := map[string]any{
		"action": action,
		"reason": CodeUntrustedBackendRestricted,
	}
	if id := normalizeID(extensionID); id != "" {
		meta["extensionId"] = id
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: id,
			ActorUserID: actor.ID,
			Action:      EventEnableFailed,
			Message:     CodeUntrustedBackendRestricted,
		})
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionBackendDenied, meta)
}
