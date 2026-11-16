package frontendtrust_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/frontendtrust"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这些用例直接打真实 PostgreSQL：挑战的权威在库里，进程内替身证明不了
// 「跨实例可见」这件事本身。

func newChallengeFixture(t *testing.T) (*pgxpool.Pool, int64, string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Frontend Trust') RETURNING id
	`, "ftrust_"+suffix, "ftrust_"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	extensionID := "frontendtrust.integration." + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ($1, 'plugin', 'Frontend Trust')`, extensionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM extension_trust_challenges WHERE extension_id = $1`, extensionID)
		pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
		pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actorID)
	})
	return pool, actorID, extensionID
}

func challengeFor(actorID int64, extensionID string) frontendtrust.Challenge {
	return frontendtrust.Challenge{
		ChallengeID: fmt.Sprintf("challenge-%d", time.Now().UnixNano()),
		Code:        "246810",
		ActorUserID: actorID,
		ExtensionID: extensionID,
		Version:     "1.0.0",
		Digest:      strings.Repeat("a", 64),
		APIVersion:  1,
		ComponentID: "admin.settings",
		ExpiresAt:   time.Now().UTC().Add(5 * time.Minute),
	}
}

func consumeInputFor(challenge frontendtrust.Challenge) frontendtrust.ConsumeInput {
	return frontendtrust.ConsumeInput{
		ChallengeID: challenge.ChallengeID,
		Code:        challenge.Code,
		ActorUserID: challenge.ActorUserID,
		ExtensionID: challenge.ExtensionID,
		Version:     challenge.Version,
		Digest:      challenge.Digest,
		APIVersion:  challenge.APIVersion,
		ComponentID: challenge.ComponentID,
	}
}

// TestPostgresStoreSharesChallengesAcrossInstances 固定 P3-7 的核心：两个 store
// 实例（等价两个副本）连同一个库时，A 建的挑战必须能被 B 消费，且只能消费一次。
func TestPostgresStoreSharesChallengesAcrossInstances(t *testing.T) {
	pool, actorID, extensionID := newChallengeFixture(t)
	ctx := context.Background()
	nodeA := frontendtrust.NewPostgresStore(pool)
	nodeB := frontendtrust.NewPostgresStore(pool)

	challenge := challengeFor(actorID, extensionID)
	if err := nodeA.Create(ctx, challenge); err != nil {
		t.Fatalf("create on node A: %v", err)
	}
	if err := nodeB.Consume(ctx, consumeInputFor(challenge)); err != nil {
		t.Fatalf("consume on node B must accept node A's challenge, got %v", err)
	}
	if err := nodeA.Consume(ctx, consumeInputFor(challenge)); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
		t.Fatalf("replayed challenge must be rejected, got %v", err)
	}
}

// TestPostgresStoreRejectsExpiredActorMismatchAndWrongCode 覆盖 denied 侧。
func TestPostgresStoreRejectsExpiredActorMismatchAndWrongCode(t *testing.T) {
	pool, actorID, extensionID := newChallengeFixture(t)
	ctx := context.Background()
	store := frontendtrust.NewPostgresStore(pool)

	t.Run("expired", func(t *testing.T) {
		challenge := challengeFor(actorID, extensionID)
		if err := store.Create(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		// Create 要求过期时间在未来，所以过期只能靠时钟推进：直接把行拨到过去。
		// 表上有 expires_at > created_at 的 CHECK，所以 created_at 必须一起前移。
		if _, err := pool.Exec(ctx, `
			UPDATE extension_trust_challenges
			SET created_at = now() - interval '10 minutes',
			    expires_at = now() - interval '1 minute'
			WHERE actor_user_id = $1 AND extension_id = $2 AND consumed_at IS NULL
		`, actorID, extensionID); err != nil {
			t.Fatal(err)
		}
		if err := store.Consume(ctx, consumeInputFor(challenge)); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
			t.Fatalf("expired challenge must be rejected, got %v", err)
		}
	})

	t.Run("actor mismatch", func(t *testing.T) {
		challenge := challengeFor(actorID, extensionID)
		if err := store.Create(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		input := consumeInputFor(challenge)
		input.ActorUserID = actorID + 1000
		if err := store.Consume(ctx, input); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
			t.Fatalf("challenge must be actor bound, got %v", err)
		}
	})

	t.Run("wrong code", func(t *testing.T) {
		challenge := challengeFor(actorID, extensionID)
		if err := store.Create(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		input := consumeInputFor(challenge)
		input.Code = "000000"
		if input.Code == challenge.Code {
			input.Code = "111111"
		}
		if err := store.Consume(ctx, input); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
			t.Fatalf("wrong confirmation code must be rejected, got %v", err)
		}
	})

	t.Run("artifact mismatch", func(t *testing.T) {
		challenge := challengeFor(actorID, extensionID)
		if err := store.Create(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		input := consumeInputFor(challenge)
		input.Digest = strings.Repeat("b", 64)
		if err := store.Consume(ctx, input); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
			t.Fatalf("challenge must be bound to the exact artifact, got %v", err)
		}
	})
}

// TestPostgresStoreSupersedesOnlyOwnTrustClass 保证复用同一张表不会互相踩：
// 新建前端挑战只失效带 trustClass 标记的旧行，可执行信任那族的行保持可用。
func TestPostgresStoreSupersedesOnlyOwnTrustClass(t *testing.T) {
	pool, actorID, extensionID := newChallengeFixture(t)
	ctx := context.Background()
	store := frontendtrust.NewPostgresStore(pool)

	executableTokenHash := strings.Repeat("c", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_trust_challenges (
			token_hash, actor_user_id, extension_id, extension_version,
			package_digest, action, artifact_digests, impact_document,
			impact_digest, expires_at
		) VALUES ($1, $2, $3, '1.0.0', $4, 'frontend_import', '{}'::jsonb, '{}'::jsonb, $5, now() + interval '5 minutes')
	`, executableTokenHash, actorID, extensionID, strings.Repeat("d", 64), strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}

	first := challengeFor(actorID, extensionID)
	if err := store.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := challengeFor(actorID, extensionID)
	if err := store.Create(ctx, second); err != nil {
		t.Fatal(err)
	}

	// 旧的前端挑战被 superseded。
	if err := store.Consume(ctx, consumeInputFor(first)); !errors.Is(err, frontendtrust.ErrChallengeInvalid) {
		t.Fatalf("superseded challenge must be rejected, got %v", err)
	}
	// 新挑战仍然可用。
	if err := store.Consume(ctx, consumeInputFor(second)); err != nil {
		t.Fatalf("latest challenge must stay consumable, got %v", err)
	}
	// 可执行信任那一族的行没有被本包碰过。
	var invalidated bool
	if err := pool.QueryRow(ctx, `
		SELECT invalidated_at IS NOT NULL FROM extension_trust_challenges WHERE token_hash = $1
	`, executableTokenHash).Scan(&invalidated); err != nil {
		t.Fatal(err)
	}
	if invalidated {
		t.Fatal("frontend challenge creation must not invalidate executable trust challenges")
	}
}
