package aiderived

import (
	"context"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 派生用途的权限面：复用 ai.manage，不注册新权限键。
//
// 手动触发派生是一次管理动作——它花站点配额、产出对运营者可见的文本——因此它
// 与「管理 AI 网关」是同一个权能。新开一个 `ai.derive` 键会让每个既有管理员
// 在升级后突然失去这个能力，而运营者无法从权限名判断两者的区别。
var errPermissionDenied = identity.ErrPermissionDenied

// EnqueueInput 是一次手动派生请求。
type EnqueueInput struct {
	Purpose string
	TopicID int64
	// RequestedBy 是发起者账号，写进产物的 created_by_user_id。
	RequestedBy int64
	// RequestID 是一次性触发标识，参与任务唯一键。空串表示由服务层生成。
	RequestID string
}

// Enqueuer 把一次派生任务放进持久队列。
//
// 它由 Jobs/AIDerived 实现，方向是 Jobs → Models（与 aireply.Enqueuer 同一条
// 依赖方向：领域包不认识队列，任务包认识领域）。
type Enqueuer interface {
	EnqueueDerive(ctx context.Context, input EnqueueInput) (string, error)
}

// Service 是派生内容的服务层：权限判定 + 触发 + 读取。
//
// 权限判定留在这里而不是控制器里，与 A4 的 Service.RunTimeline 同形：将来接入
// HTTP 端点时，控制器只做解码/编码，「谁能触发派生」与「谁能读产物」不可能
// 是两个答案。
type Service struct {
	artifacts ArtifactStore
	enqueuer  Enqueuer
}

// ServiceConfig 汇总服务层依赖。Artifacts 必填；Enqueuer 缺省时只能读、不能触发
// ——那是一个「只读装配」，不是错误（例如将来的控制台只做列表页）。
type ServiceConfig struct {
	Artifacts ArtifactStore
	Enqueuer  Enqueuer
}

func NewService(config ServiceConfig) *Service {
	return &Service{artifacts: config.Artifacts, enqueuer: config.Enqueuer}
}

// Enqueue 触发一次派生。返回本次触发的 request id，供调用方轮询产物。
//
// RequestID 由服务层兜底生成而不是要求调用方必须给：CLI 与将来的按钮都需要
// 「这次触发」的可追踪标识，而要求调用方自己拼一个（时间戳、UUID）会让两处
// 各自发明一种格式。
func (s *Service) Enqueue(ctx context.Context, actor identity.Actor, input EnqueueInput) (string, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return "", errPermissionDenied
	}
	if s == nil || s.enqueuer == nil {
		return "", ErrGeneratorUnavailable
	}
	purpose := strings.TrimSpace(input.Purpose)
	if !ValidArtifactPurpose(purpose) {
		return "", ErrArtifactInvalid
	}
	if input.TopicID < MinArtifactTopicID {
		return "", ErrArtifactInvalid
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = NewRequestID()
	}
	if _, err := s.enqueuer.EnqueueDerive(ctx, EnqueueInput{
		Purpose:     purpose,
		TopicID:     input.TopicID,
		RequestedBy: firstPositive(input.RequestedBy, actor.ID),
		RequestID:   requestID,
	}); err != nil {
		return "", err
	}
	return requestID, nil
}

// List 列取产物。零值 filter 表示不过滤，创建时间倒序。
func (s *Service) List(ctx context.Context, actor identity.Actor, filter ArtifactFilter) ([]Artifact, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, errPermissionDenied
	}
	if s == nil || s.artifacts == nil {
		return nil, ErrGeneratorUnavailable
	}
	if purpose := strings.TrimSpace(filter.Purpose); purpose != "" && !ValidArtifactPurpose(purpose) {
		return nil, ErrArtifactInvalid
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidArtifactStatus(status) {
		return nil, ErrArtifactInvalid
	}
	return s.artifacts.List(ctx, filter)
}

// Get 按 id 读一条产物。
func (s *Service) Get(ctx context.Context, actor identity.Actor, id int64) (Artifact, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return Artifact{}, errPermissionDenied
	}
	if s == nil || s.artifacts == nil {
		return Artifact{}, ErrGeneratorUnavailable
	}
	if id <= 0 {
		return Artifact{}, ErrArtifactNotFound
	}
	return s.artifacts.Get(ctx, id)
}

// firstPositive 取第一个正数。它让「调用方给的发起者」优先，缺失时才用 actor：
// 将来若有代他人触发的入口（例如 CLI 以某个账号身份批量补生成），产物归属
// 应该记真实发起者而不是转发者。
func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
