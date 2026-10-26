// Package hostapi 实现 sforum.host/v1 宿主面（F2.2）。
//
// 插件不得 import 核心内部业务包；应通过 RPC 调用本面的方法。
// 每次调用都校验 extension 的 capability 授权。
package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

const (
	// Version 是 Host API 主版本标识。
	Version = "sforum.host/v1"

	// DefaultTimeout 单次 Host 调用默认超时。
	DefaultTimeout = 3 * time.Second
)

// 稳定错误码（插件侧可映射）。
var (
	ErrCapabilityDenied = errors.New("hostapi: capability denied")
	ErrInvalidRequest   = errors.New("hostapi: invalid request")
	ErrNotFound         = errors.New("hostapi: not found")
	ErrTimeout          = errors.New("hostapi: timeout")
	ErrUnavailable      = errors.New("hostapi: unavailable")
)

// Method 名常量，与 RPC 路径对齐。
const (
	MethodPing                   = "Ping"
	MethodCheckPermission        = "CheckPermission"
	MethodGetSettings            = "GetSettings"
	MethodEnqueueOwnJob          = "EnqueueOwnJob"
	MethodAppendAudit            = "AppendAudit"
	MethodGetUserSafe            = "GetUserSafe"
	MethodListExtensionInventory = "ListExtensionInventory"
)

// Request 是插件 → 宿主的统一信封。
type Request struct {
	Method      string `json:"method"`
	ExtensionID string `json:"extensionId"`
	TimeoutMS   int    `json:"timeoutMs,omitempty"`
	// Payload 为方法特定字段（扁平 map，避免反射复杂化）。
	Payload map[string]any `json:"payload,omitempty"`
}

// Response 是宿主 → 插件的统一信封。
type Response struct {
	OK      bool           `json:"ok"`
	Reason  string         `json:"reason,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// PermissionChecker 查询 actor 是否拥有权限。
type PermissionChecker interface {
	// HasPermission 在用户存在时返回是否持有 permission。
	HasPermission(ctx context.Context, userID int64, permission string) (bool, error)
}

// SettingsStore 读写扩展自身 settings。
type SettingsStore interface {
	ListSettings(ctx context.Context, extensionID string) (map[string]string, error)
}

// JobEnqueuer 入队插件声明的 job kind。
type JobEnqueuer interface {
	// EnqueuePluginJob 仅允许 extension 声明过的 kind。
	EnqueuePluginJob(ctx context.Context, extensionID, kind string, payload map[string]any) error
}

// UserReader 读取安全用户字段。
type UserReader interface {
	// GetUserSafe 返回非密钥用户字段；不存在时 ErrNotFound。
	// actorUserID 与 declaredFields 用于扩展用户字段的实时权限与 Schema 校验；
	// 核心字段不依赖 actor。无 actor 的扩展字段读取必须失败关闭。
	GetUserSafe(ctx context.Context, userID int64, actorUserID int64, declaredFields []string) (map[string]any, error)
}

// ExtensionInventoryReader 提供去敏扩展清单（extensions.read）。
type ExtensionInventoryReader interface {
	// ListRedactedInventory 返回不含 packagePath/settings/trust/凭证的清单行。
	ListRedactedInventory(ctx context.Context) ([]map[string]any, error)
}

// CapabilitySource 解析扩展当前授予的能力。
type CapabilitySource interface {
	// CapabilitiesFor 返回启用插件的有效能力集合。
	CapabilitiesFor(ctx context.Context, extensionID string) (capabilities.Set, error)
	// DeclaredJobKinds 返回插件 manifest 中声明的 job names。
	DeclaredJobKinds(ctx context.Context, extensionID string) ([]string, error)
}

// Service 是 Host API v1 实现。
type Service struct {
	capabilities     CapabilitySource
	permissions      PermissionChecker
	settings         SettingsStore
	jobs             JobEnqueuer
	jobAdmission     PluginJobEnqueueAdmission
	serviceAdmission ServiceProviderAdmission
	users            UserReader
	inventory        ExtensionInventoryReader
	auditor          audit.Writer
}

// Config 注入依赖；未注入的可选面在对应方法上返回 unavailable。
type Config struct {
	Capabilities     CapabilitySource
	Permissions      PermissionChecker
	Settings         SettingsStore
	Jobs             JobEnqueuer
	JobAdmission     PluginJobEnqueueAdmission
	ServiceAdmission ServiceProviderAdmission
	Users            UserReader
	Inventory        ExtensionInventoryReader
	Auditor          audit.Writer
}

func New(config Config) *Service {
	return &Service{
		capabilities:     config.Capabilities,
		permissions:      config.Permissions,
		settings:         config.Settings,
		jobs:             config.Jobs,
		jobAdmission:     config.JobAdmission,
		serviceAdmission: config.ServiceAdmission,
		users:            config.Users,
		inventory:        config.Inventory,
		auditor:          config.Auditor,
	}
}

// BindServiceProviderAdmission 在 runtime 构造完成后接入 exact-instance service admission。
func (s *Service) BindServiceProviderAdmission(admission ServiceProviderAdmission) {
	if s != nil {
		s.serviceAdmission = admission
	}
}

// BindPluginJobAdmission 在 runtime 构造完成后接入 exact-instance admission。
func (s *Service) BindPluginJobAdmission(admission PluginJobEnqueueAdmission) {
	if s != nil {
		s.jobAdmission = admission
	}
}

// BindCapabilitySource 在 bootstrap 二阶段注入（避免与 Extensions.Service 循环构造）。
func (s *Service) BindCapabilitySource(source CapabilitySource) {
	if s != nil {
		s.capabilities = source
	}
}

// BindExtensionInventory 在 bootstrap 二阶段注入去敏清单源。
func (s *Service) BindExtensionInventory(source ExtensionInventoryReader) {
	if s != nil {
		s.inventory = source
	}
}

// BindPermissions 注入权限查询。
func (s *Service) BindPermissions(checker PermissionChecker) {
	if s != nil {
		s.permissions = checker
	}
}

// BindUsers 注入安全用户读取。
func (s *Service) BindUsers(reader UserReader) {
	if s != nil {
		s.users = reader
	}
}

// Call 执行一次 Host API 调用。
func (s *Service) Call(ctx context.Context, req Request) Response {
	return s.call(ctx, req, Version)
}

func (s *Service) call(ctx context.Context, req Request, protocolVersion string) Response {
	if s == nil {
		return fail("host.unavailable", "Host API is not configured.")
	}
	extensionID := strings.TrimSpace(req.ExtensionID)
	if extensionID == "" {
		return fail("host.invalid_extension", "extensionId is required.")
	}
	method := strings.TrimSpace(req.Method)
	if method == "" {
		return fail("host.invalid_method", "method is required.")
	}

	timeout := DefaultTimeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 所有方法（含 Ping）都要求插件已启用且至少有 host.api 或其它能力来源。
	// Ping 仅需 host.api，用于探测。
	caps, err := s.loadCaps(callCtx, extensionID)
	if err != nil {
		return fail("host.extension_unavailable", err.Error())
	}

	switch method {
	case MethodPing:
		if err := caps.Require(capabilities.HostAPI); err != nil {
			return denied(capabilities.HostAPI)
		}
		return success(map[string]any{"version": protocolVersion, "extensionId": extensionID})
	case MethodCheckPermission:
		if err := caps.Require(capabilities.PermissionsCheck); err != nil {
			return denied(capabilities.PermissionsCheck)
		}
		return s.checkPermission(callCtx, req.Payload)
	case MethodGetSettings:
		if err := caps.Require(capabilities.SettingsOwn); err != nil {
			return denied(capabilities.SettingsOwn)
		}
		return s.getSettings(callCtx, extensionID)
	case MethodEnqueueOwnJob:
		if err := caps.Require(capabilities.JobsEnqueue); err != nil {
			return denied(capabilities.JobsEnqueue)
		}
		return s.enqueueOwnJob(callCtx, extensionID, req.Payload)
	case MethodAppendAudit:
		if err := caps.Require(capabilities.AuditAppend); err != nil {
			return denied(capabilities.AuditAppend)
		}
		return s.appendAudit(callCtx, extensionID, req.Payload, protocolVersion)
	case MethodGetUserSafe:
		if err := caps.Require(capabilities.UsersRead); err != nil {
			return denied(capabilities.UsersRead)
		}
		return s.getUserSafe(callCtx, req.Payload)
	case MethodListExtensionInventory:
		if err := caps.Require(capabilities.ExtensionsRead); err != nil {
			return denied(capabilities.ExtensionsRead)
		}
		return s.listExtensionInventory(callCtx)
	default:
		return fail("host.unknown_method", fmt.Sprintf("unknown method %q", method))
	}
}

func (s *Service) loadCaps(ctx context.Context, extensionID string) (capabilities.Set, error) {
	if s.capabilities == nil {
		return nil, fmt.Errorf("%w: capability source missing", ErrUnavailable)
	}
	return s.capabilities.CapabilitiesFor(ctx, extensionID)
}

func (s *Service) checkPermission(ctx context.Context, payload map[string]any) Response {
	if s.permissions == nil {
		return fail("host.unavailable", "Permission checker is not configured.")
	}
	userID, ok := int64From(payload, "userId")
	if !ok || userID <= 0 {
		return fail("host.invalid_payload", "userId is required.")
	}
	permission, _ := stringFrom(payload, "permission")
	if permission == "" {
		return fail("host.invalid_payload", "permission is required.")
	}
	allowed, err := s.permissions.HasPermission(ctx, userID, permission)
	if err != nil {
		return fail("host.permission_check_failed", err.Error())
	}
	return success(map[string]any{"allowed": allowed, "userId": userID, "permission": permission})
}

func (s *Service) getSettings(ctx context.Context, extensionID string) Response {
	if s.settings == nil {
		return fail("host.unavailable", "Settings store is not configured.")
	}
	values, err := s.settings.ListSettings(ctx, extensionID)
	if err != nil {
		return fail("host.settings_failed", err.Error())
	}
	// 转为 map[string]any 以符合 Response.Data。
	data := make(map[string]any, len(values))
	for key, value := range values {
		data[key] = value
	}
	return success(map[string]any{"settings": data})
}

func (s *Service) listExtensionInventory(ctx context.Context) Response {
	if s.inventory == nil {
		return fail("host.unavailable", "Extension inventory is not configured.")
	}
	items, err := s.inventory.ListRedactedInventory(ctx)
	if err != nil {
		return fail("host.inventory_failed", err.Error())
	}
	if items == nil {
		items = []map[string]any{}
	}
	return success(map[string]any{"extensions": items})
}

func (s *Service) enqueueOwnJob(ctx context.Context, extensionID string, payload map[string]any) Response {
	if s.jobs == nil {
		return fail("host.unavailable", "Job enqueuer is not configured.")
	}
	kind, _ := stringFrom(payload, "kind")
	if kind == "" {
		return fail("host.invalid_payload", "kind is required.")
	}
	// 校验 kind 在 manifest.jobs 中声明。
	if s.capabilities != nil {
		declared, err := s.capabilities.DeclaredJobKinds(ctx, extensionID)
		if err != nil {
			return fail("host.job_kinds_failed", err.Error())
		}
		if !containsString(declared, kind) {
			return fail("host.job_kind_forbidden", fmt.Sprintf("job kind %q is not declared by the extension", kind))
		}
	}
	jobPayload := mapFrom(payload, "payload")
	if err := s.jobs.EnqueuePluginJob(ctx, extensionID, kind, jobPayload); err != nil {
		return fail("host.enqueue_failed", err.Error())
	}
	return success(map[string]any{"kind": kind, "enqueued": true})
}

func (s *Service) appendAudit(ctx context.Context, extensionID string, payload map[string]any, protocolVersion string) Response {
	if s.auditor == nil {
		return fail("host.unavailable", "Audit writer is not configured.")
	}
	action, _ := stringFrom(payload, "action")
	action = strings.TrimSpace(action)
	if action == "" {
		return fail("host.invalid_payload", "action is required.")
	}
	// 强制命名空间，防止插件冒充核心 action。
	if !strings.HasPrefix(action, "extension.") && !strings.HasPrefix(action, extensionID+".") {
		action = "extension." + extensionID + "." + action
	}
	actorID, _ := int64From(payload, "actorUserId")
	targetID, _ := int64From(payload, "targetUserId")
	meta := mapFrom(payload, "metadata")
	if meta == nil {
		meta = map[string]any{}
	}
	meta["extensionId"] = extensionID
	meta["via"] = protocolVersion
	if err := s.auditor.Append(ctx, audit.Event{
		ActorUserID:  actorID,
		TargetUserID: targetID,
		Action:       action,
		Metadata:     meta,
	}); err != nil {
		return fail("host.audit_failed", err.Error())
	}
	return success(map[string]any{"action": action, "appended": true})
}

func (s *Service) getUserSafe(ctx context.Context, payload map[string]any) Response {
	if s.users == nil {
		return fail("host.unavailable", "User reader is not configured.")
	}
	userID, found := int64From(payload, "userId")
	if !found || userID <= 0 {
		return fail("host.invalid_payload", "userId is required.")
	}
	actorUserID, _ := int64From(payload, "actorUserId")
	var declaredFields []string
	if raw, ok := payload["declaredFields"].([]string); ok {
		declaredFields = raw
	} else if raw, ok := payload["declaredFields"].([]any); ok {
		declaredFields = make([]string, 0, len(raw))
		for _, item := range raw {
			if value, ok := item.(string); ok {
				declaredFields = append(declaredFields, value)
			}
		}
	}
	user, err := s.users.GetUserSafe(ctx, userID, actorUserID, declaredFields)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fail("host.user_not_found", "user not found")
		}
		return fail("host.user_read_failed", err.Error())
	}
	return success(map[string]any{"user": user})
}

func success(data map[string]any) Response {
	return Response{OK: true, Data: data}
}

func fail(reason, message string) Response {
	return Response{OK: false, Reason: reason, Message: message}
}

func denied(capability string) Response {
	return Response{
		OK:      false,
		Reason:  "host.capability_denied",
		Message: fmt.Sprintf("capability %s is not granted", capability),
		Data:    map[string]any{"capability": capability},
	}
}

func stringFrom(payload map[string]any, key string) (string, bool) {
	if payload == nil {
		return "", false
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return "", false
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value), true
	default:
		return strings.TrimSpace(fmt.Sprint(value)), true
	}
}

func int64From(payload map[string]any, key string) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case float64:
		return int64(value), true
	case jsonNumber:
		parsed, err := value.Int64()
		return parsed, err == nil
	default:
		var parsed int64
		_, err := fmt.Sscan(fmt.Sprint(value), &parsed)
		return parsed, err == nil
	}
}

// jsonNumber 兼容 encoding/json 的 Number 接口。
type jsonNumber interface {
	Int64() (int64, error)
}

func mapFrom(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	raw, ok := payload[key]
	if !ok || raw == nil {
		return nil
	}
	if asMap, ok := raw.(map[string]any); ok {
		return asMap
	}
	return nil
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
