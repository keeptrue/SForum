package frontendtrust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// challengeAction 复用 extension_trust_challenges 既有的 action 取值域。
// 前端组件信任在语义上就是「导入前端制品」，因此不需要扩展 CHECK 约束。
const challengeAction = "frontend_import"

var hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PostgresStore 把挑战落进 migration 202607130006 的 extension_trust_challenges。
//
// 列映射（为什么既有表够用）：
//
//	token_hash         = sha256("sforum:frontend-trust-challenge:v1:" + ChallengeID)
//	                     —— 明文 challenge id 只回给发起者，与 V3 P1 口径一致
//	actor_user_id      = 挑战绑定的 actor（外键）
//	extension_id       = 目标扩展（外键）
//	extension_version  = 挑战创建时的扩展版本（版本变了即失效）
//	package_digest     = admin 前端产物摘要（sha256 hex，64 位，满足 CHECK）
//	action             = 'frontend_import'
//	artifact_digests   = {"trustClass":"frontend_component","apiVersion":..,"componentId":..}
//	                     —— trustClass 把本包的行与可执行信任那族隔离
//	impact_document    = {"confirmationCodeSha256":".."}，6 位确认码只存摘要
//	impact_digest      = sha256(impact_document 的 JSON 字节)
//	expires_at         = 创建时给出（5 分钟），过期即不可消费
//	consumed_at        = 单次消费标记；invalidated_at 由本包写 superseded
//
// 唯一权威是 PostgreSQL：进程内没有缓存，也没有内存兜底。多副本/蓝绿发布下
// A 副本建、B 副本消费是正常路径。
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Create 落一条挑战，并把同一 actor+extension 的旧前端组件挑战标为 superseded。
// 只失效带 trustClass 标记的行，绝不碰可执行信任挑战。
func (s *PostgresStore) Create(ctx context.Context, challenge Challenge) error {
	if err := challenge.validate(); err != nil {
		return err
	}
	artifactJSON, impactJSON, err := challengeArtifacts(challenge)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%w: begin challenge: %w", ErrStoreUnavailable, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE extension_trust_challenges
		SET invalidated_at = now(), invalidation_reason = 'superseded'
		WHERE actor_user_id = $1 AND extension_id = $2 AND action = $3
		  AND artifact_digests->>'trustClass' = $4
		  AND consumed_at IS NULL AND invalidated_at IS NULL
	`, challenge.ActorUserID, challenge.ExtensionID, challengeAction, TrustClass); err != nil {
		return fmt.Errorf("%w: supersede prior frontend challenges: %w", ErrStoreUnavailable, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO extension_trust_challenges (
			token_hash, actor_user_id, extension_id, extension_version,
			package_digest, action, artifact_digests, impact_document,
			impact_digest, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10)
	`, tokenHash(challenge.ChallengeID), challenge.ActorUserID, challenge.ExtensionID,
		challenge.Version, challenge.Digest, challengeAction,
		string(artifactJSON), string(impactJSON), sha256Hex(impactJSON), challenge.ExpiresAt); err != nil {
		return fmt.Errorf("%w: insert frontend challenge: %w", ErrStoreUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit frontend challenge: %w", ErrStoreUnavailable, err)
	}
	return nil
}

// Consume 原子地消费挑战：把全部绑定条件写进同一条 UPDATE 的 WHERE，命中即
// consumed_at=now()，未命中（不存在/已消费/已失效/已过期/绑定不符）一律
// ErrChallengeInvalid。并发两次消费只有一次能命中，这是单次消费的实现方式。
func (s *PostgresStore) Consume(ctx context.Context, input ConsumeInput) error {
	if err := input.validate(); err != nil {
		return ErrChallengeInvalid
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		UPDATE extension_trust_challenges
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND actor_user_id = $2
		  AND extension_id = $3
		  AND extension_version = $4
		  AND package_digest = $5
		  AND action = $6
		  AND artifact_digests->>'trustClass' = $7
		  AND artifact_digests->>'apiVersion' = $8
		  AND artifact_digests->>'componentId' = $9
		  AND impact_document->>'confirmationCodeSha256' = $10
		  AND consumed_at IS NULL
		  AND invalidated_at IS NULL
		  AND expires_at > now()
		RETURNING id
	`, tokenHash(input.ChallengeID), input.ActorUserID, input.ExtensionID, input.Version,
		input.Digest, challengeAction, TrustClass, strconv.Itoa(input.APIVersion), input.ComponentID,
		codeDigest(input.ChallengeID, input.Code)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChallengeInvalid
	}
	if err != nil {
		return fmt.Errorf("%w: consume frontend challenge: %w", ErrStoreUnavailable, err)
	}
	return nil
}

func (c Challenge) validate() error {
	switch {
	case strings.TrimSpace(c.ChallengeID) == "":
		return fmt.Errorf("%w: challenge id is required", ErrInvalidChallengeInput)
	case strings.TrimSpace(c.Code) == "":
		return fmt.Errorf("%w: confirmation code is required", ErrInvalidChallengeInput)
	case c.ActorUserID <= 0:
		return fmt.Errorf("%w: actor is required", ErrInvalidChallengeInput)
	case strings.TrimSpace(c.ExtensionID) == "":
		return fmt.Errorf("%w: extension id is required", ErrInvalidChallengeInput)
	case strings.TrimSpace(c.Version) == "":
		return fmt.Errorf("%w: extension version is required", ErrInvalidChallengeInput)
	case !hexDigestPattern.MatchString(c.Digest):
		return fmt.Errorf("%w: admin frontend digest must be 64 hex chars", ErrInvalidChallengeInput)
	case strings.TrimSpace(c.ComponentID) == "":
		return fmt.Errorf("%w: component id is required", ErrInvalidChallengeInput)
	case !c.ExpiresAt.After(time.Now().UTC()):
		return fmt.Errorf("%w: challenge must expire in the future", ErrInvalidChallengeInput)
	}
	return nil
}

func (i ConsumeInput) validate() error {
	if strings.TrimSpace(i.ChallengeID) == "" || strings.TrimSpace(i.Code) == "" || i.ActorUserID <= 0 {
		return ErrChallengeInvalid
	}
	return nil
}

// challengeArtifacts 构造落库的 JSONB 与 impact 摘要。确认码只以
// sha256(challengeID + code) 形式存在：单看库里那列无法跨挑战复用或对表。
func challengeArtifacts(challenge Challenge) ([]byte, []byte, error) {
	artifactJSON, err := json.Marshal(map[string]string{
		"trustClass":  TrustClass,
		"apiVersion":  strconv.Itoa(challenge.APIVersion),
		"componentId": challenge.ComponentID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode challenge artifacts: %w", ErrStoreUnavailable, err)
	}
	impactJSON, err := json.Marshal(map[string]string{
		"confirmationCodeSha256": codeDigest(challenge.ChallengeID, challenge.Code),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode challenge impact: %w", ErrStoreUnavailable, err)
	}
	return artifactJSON, impactJSON, nil
}

func tokenHash(challengeID string) string {
	return sha256Hex([]byte("sforum:frontend-trust-challenge:v1:" + challengeID))
}

func codeDigest(challengeID, code string) string {
	return sha256Hex([]byte("sforum:frontend-trust-code:v1:" + challengeID + ":" + code))
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
