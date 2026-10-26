package plugin

import (
	"context"
	"fmt"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

// Host API 方法名与版本（与宿主 sforum.host/v1 对齐）。
const (
	HostAPIVersion = hostapi.Version

	MethodPing            = hostapi.MethodPing
	MethodCheckPermission = hostapi.MethodCheckPermission
	MethodGetSettings     = hostapi.MethodGetSettings
	MethodEnqueueOwnJob   = hostapi.MethodEnqueueOwnJob
	MethodAppendAudit     = hostapi.MethodAppendAudit
	MethodGetUserSafe     = hostapi.MethodGetUserSafe
)

// Host 是插件侧 Host API 客户端。
// 生产环境用 HostFromEnv；测试可直接构造。
type Host = hostapi.Client

// HostResponse 是宿主统一响应信封。
type HostResponse = hostapi.Response

// HostFromEnv 从 SFORUM_HOST_API_* / SFORUM_EXTENSION_ID 构造客户端。
// 宿主在启动子进程时注入这些变量。
func HostFromEnv() (*Host, error) {
	return hostapi.ClientFromEnv()
}

// Ping 探测 Host API 与能力授权（需要 host.api）。
func Ping(ctx context.Context, h *Host) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodPing, nil)
}

// GetSettings 读取本扩展 settings（需要 settings.own）。
func GetSettings(ctx context.Context, h *Host) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodGetSettings, nil)
}

// CheckPermission 询问 actor 是否持有 permission（需要 permissions.check）。
func CheckPermission(ctx context.Context, h *Host, userID int64, permission string) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodCheckPermission, map[string]any{
		"userId":     userID,
		"permission": permission,
	})
}

// EnqueueOwnJob 入队本扩展声明的 job kind（需要 jobs.enqueue）。
func EnqueueOwnJob(ctx context.Context, h *Host, kind string, payload map[string]any) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodEnqueueOwnJob, map[string]any{
		"kind":    kind,
		"payload": payload,
	})
}

// AppendAudit 追加命名空间化的审计事件（需要 audit.append）。
func AppendAudit(ctx context.Context, h *Host, action string, actorUserID, targetUserID int64, metadata map[string]any) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodAppendAudit, map[string]any{
		"action":       action,
		"actorUserId":  actorUserID,
		"targetUserId": targetUserID,
		"metadata":     metadata,
	})
}

// GetUserSafe 读取非密钥用户字段（需要 users.read）。
func GetUserSafe(ctx context.Context, h *Host, userID int64) (HostResponse, error) {
	if h == nil {
		return HostResponse{}, fmt.Errorf("plugin: host client is nil")
	}
	return h.Call(ctx, MethodGetUserSafe, map[string]any{"userId": userID})
}
