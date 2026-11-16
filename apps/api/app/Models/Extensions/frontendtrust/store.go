// Package frontendtrust 持久化前端组件信任的「一次性确认挑战」。
//
// 为什么单独成包：`app/Models/Extensions` 的 receiver 方法数已顶到架构基线
// （tests/architecture-boundaries-baseline.json），往 `FrontendService` 上加方法
// 会直接推高基线。这里只放挑战记录的存储语义（创建/单次消费/过期/绑定校验），
// 装配与状态机仍在 `extensions.FrontendService`。
//
// 为什么复用 `extension_trust_challenges`（migration 202607130006）而不是新建表：
// 该表已经具备本需求全部不变量——只存 token 摘要、actor 外键、extension 外键、
// 精确制品摘要、过期时间、单次消费（consumed_at）与失效（invalidated_at）。
// 前端组件信任挑战需要的字段可以无损映射（见 postgres_store.go 的列映射注释），
// 新建一张同构表只会让两套挑战生命周期各自漂移。
//
// 进程内 map 不是权威状态：多副本/蓝绿发布下 challenge 落 A 副本、grant 打 B 副本
// 时旧实现必然 invalid-challenge。这里没有任何进程内缓存，读写的唯一权威是
// PostgreSQL。
package frontendtrust

import (
	"context"
	"errors"
	"time"
)

// TrustClass 是本包写入行的类别标记，用来把前端组件挑战与可执行信任挑战
// （同样使用 action='frontend_import'）在同一张表里区分开：本包只失效、只消费
// 带这个标记的行，绝不碰可执行信任那一族的行。
const TrustClass = "frontend_component"

var (
	// ErrChallengeInvalid 表示挑战不存在、已消费、已失效、已过期，或与本次提交的
	// 精确制品/actor 不匹配。这些情形对外统一为「挑战无效或已过期」，不向调用方
	// 泄露到底命中了哪一条（避免探测）。
	ErrChallengeInvalid = errors.New("frontendtrust: challenge is invalid, expired, or already used")
	// ErrStoreUnavailable 表示存储层故障（连接/超时/约束），与「挑战无效」区分开，
	// 免得把数据库故障报成用户提交错误。
	ErrStoreUnavailable = errors.New("frontendtrust: challenge store is unavailable")
)

// Challenge 是一次待确认的前端组件信任挑战。
//
// ChallengeID 与 Code 只以摘要形式落库（token_hash / impact_document），明文仅
// 返回给发起者，与 V3 P1「一次性确认挑战只持久化摘要」的口径一致。
type Challenge struct {
	ChallengeID string
	Code        string
	ActorUserID int64
	ExtensionID string
	Version     string
	// Digest 是扩展 admin 前端产物的摘要（sha256 hex），落 package_digest 列。
	Digest      string
	APIVersion  int
	ComponentID string
	ExpiresAt   time.Time
}

// ConsumeInput 是一次消费尝试：必须与创建时绑定的全部字段逐项一致。
type ConsumeInput struct {
	ChallengeID string
	Code        string
	ActorUserID int64
	ExtensionID string
	Version     string
	Digest      string
	APIVersion  int
	ComponentID string
}

// Store 是挑战存储的最小契约。实现必须是跨进程共享的持久化存储。
type Store interface {
	// Create 落一条新挑战；同一 actor+extension 的旧挑战会被标记 superseded。
	Create(ctx context.Context, challenge Challenge) error
	// Consume 原子地把挑战标记为已消费。任何一项绑定不匹配、已消费、已失效或
	// 已过期都返回 ErrChallengeInvalid，且不会让挑战保持可用。
	Consume(ctx context.Context, input ConsumeInput) error
}
