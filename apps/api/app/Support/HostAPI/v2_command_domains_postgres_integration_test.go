package hostapi

import (
	"fmt"
	"sync"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	"google.golang.org/protobuf/proto"
)

type postgresDomainCommandExercise struct {
	name             string
	spec             postgresDomainCommandSpec
	allowedInput     map[string]any
	allowedRevision  string
	rollbackInput    map[string]any
	rollbackRevision string
	assertAllowed    func(*testing.T)
	assertUnchanged  func(*testing.T)
}

func TestPostgresProtocolV2SixDomainCommandsAllowedDeniedReplayAndRollback(t *testing.T) {
	h := newPostgresDomainCommandHarness(t)

	t.Run("identity", func(t *testing.T) {
		allowedID := seedPostgresDomainCommandUserTarget(t, h, 2001, "identity-allowed")
		rollbackID := seedPostgresDomainCommandUserTarget(t, h, 2002, "identity-rollback")
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "identity",
			spec: postgresDomainCommandSpec{
				ID: CommandIdentityUserStatusSetID, Version: CommandIdentityUserStatusSetVersion,
				InputSchema: CommandIdentityUserStatusInputSchemaID, SchemaVersion: CommandIdentityUserStatusSchemaVersion,
				Delegated: true,
			},
			allowedInput: map[string]any{
				"userId": fmt.Sprintf("%d", allowedID), "status": "disabled", "reason": "domain command test",
			},
			allowedRevision: "0",
			rollbackInput: map[string]any{
				"userId": fmt.Sprintf("%d", rollbackID), "status": "disabled", "reason": "rollback test",
			},
			rollbackRevision: "0",
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainUserState(t, h, allowedID, "disabled", 1, true)
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainUserState(t, h, rollbackID, "active", 0, false)
			},
		})
	})

	t.Run("topic visibility", func(t *testing.T) {
		allowedID, allowedRevision := seedPostgresDomainCommandTopic(t, h, "topic-visibility-allowed", "active")
		rollbackID, rollbackRevision := seedPostgresDomainCommandTopic(t, h, "topic-visibility-rollback", "active")
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "topic",
			spec: postgresDomainCommandSpec{
				ID: CommandTopicVisibilitySetID, Version: CommandTopicVisibilitySetVersion,
				InputSchema: CommandTopicVisibilityInputSchemaID, SchemaVersion: CommandTopicVisibilitySchemaVersion,
				Delegated: true,
			},
			allowedInput: map[string]any{
				"topicId": fmt.Sprintf("%d", allowedID), "action": "hide",
			},
			allowedRevision: allowedRevision,
			rollbackInput: map[string]any{
				"topicId": fmt.Sprintf("%d", rollbackID), "action": "hide",
			},
			rollbackRevision: rollbackRevision,
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainTopicStatus(t, h, allowedID, "hidden")
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainTopicStatus(t, h, rollbackID, "active")
			},
		})
	})

	t.Run("entity meta", func(t *testing.T) {
		seedPostgresDomainCommandMetaField(t, h, "fixture.domain.allowed")
		seedPostgresDomainCommandMetaField(t, h, "fixture.domain.rollback")
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "meta",
			spec: postgresDomainCommandSpec{
				ID: CommandEntityMetaValuesUpsertID, Version: CommandEntityMetaValuesUpsertVersion,
				InputSchema: CommandEntityMetaValuesInputSchemaID, SchemaVersion: CommandEntityMetaValuesSchemaVersion,
				Delegated: true,
			},
			allowedInput:  postgresDomainMetaInput(postgresDomainCommandActorID, "fixture.domain.allowed", "committed"),
			rollbackInput: postgresDomainMetaInput(postgresDomainCommandActorID, "fixture.domain.rollback", "rolled-back"),
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainMetaValue(t, h, "fixture.domain.allowed", "committed", 1)
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainMetaValue(t, h, "fixture.domain.rollback", "", 0)
			},
		})
	})

	t.Run("moderation", func(t *testing.T) {
		allowedID, _ := seedPostgresDomainCommandTopic(t, h, "moderation-allowed", "pending")
		rollbackID, _ := seedPostgresDomainCommandTopic(t, h, "moderation-rollback", "pending")
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "moderation",
			spec: postgresDomainCommandSpec{
				ID: CommandModerationDecisionSubmitID, Version: CommandModerationDecisionSubmitVersion,
				InputSchema: CommandModerationDecisionInputSchemaID, SchemaVersion: CommandModerationDecisionSchemaVersion,
				Delegated: true,
			},
			allowedInput: map[string]any{
				"source": "pre_publish", "targetType": "topic", "targetId": fmt.Sprintf("%d", allowedID), "action": "approve",
			},
			rollbackInput: map[string]any{
				"source": "pre_publish", "targetType": "topic", "targetId": fmt.Sprintf("%d", rollbackID), "action": "approve",
			},
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainModerationState(t, h, allowedID, "active", 1)
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainModerationState(t, h, rollbackID, "pending", 0)
			},
		})
	})

	t.Run("entitlement", func(t *testing.T) {
		validFrom := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "entitlement",
			spec: postgresDomainCommandSpec{
				ID: CommandEntitlementsMutateID, Version: CommandEntitlementsMutateVersion,
				InputSchema: CommandEntitlementsMutationInputSchemaID, SchemaVersion: CommandEntitlementsMutationSchemaVersion,
			},
			allowedInput:  postgresDomainEntitlementInput("allowed-subject", validFrom),
			rollbackInput: postgresDomainEntitlementInput("rollback-subject", validFrom),
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainEntitlement(t, h, "allowed-subject", 1)
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainEntitlement(t, h, "rollback-subject", 0)
			},
		})
	})

	t.Run("attachment", func(t *testing.T) {
		allowedID, allowedRevision := seedPostgresDomainCommandAttachment(t, h, "attachment-allowed")
		rollbackID, rollbackRevision := seedPostgresDomainCommandAttachment(t, h, "attachment-rollback")
		exercisePostgresDomainCommand(t, h, postgresDomainCommandExercise{
			name: "attachment",
			spec: postgresDomainCommandSpec{
				ID: CommandAttachmentStatusSetID, Version: CommandAttachmentStatusSetVersion,
				InputSchema: CommandAttachmentStatusInputSchemaID, SchemaVersion: CommandAttachmentStatusSchemaVersion,
				Delegated: true,
			},
			allowedInput: map[string]any{
				"attachmentId": fmt.Sprintf("%d", allowedID), "status": "disabled",
			},
			allowedRevision: allowedRevision,
			rollbackInput: map[string]any{
				"attachmentId": fmt.Sprintf("%d", rollbackID), "status": "disabled",
			},
			rollbackRevision: rollbackRevision,
			assertAllowed: func(t *testing.T) {
				assertPostgresDomainAttachmentStatus(t, h, allowedID, "disabled")
			},
			assertUnchanged: func(t *testing.T) {
				assertPostgresDomainAttachmentStatus(t, h, rollbackID, "active")
			},
		})
	})
}

// 权益 Host Command 的完整原子性出口：并发同键回放、指纹冲突、expected-revision
// CAS、revoke 提交，以及 actorless 拒绝 actor delegation。
func TestPostgresProtocolV2EntitlementCommandConcurrentRevisionAndRevoke(t *testing.T) {
	h := newPostgresDomainCommandHarness(t)
	spec := postgresDomainCommandSpec{
		ID: CommandEntitlementsMutateID, Version: CommandEntitlementsMutateVersion,
		InputSchema: CommandEntitlementsMutationInputSchemaID, SchemaVersion: CommandEntitlementsMutationSchemaVersion,
	}
	validFrom := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	subject := "entitlement-concurrent-subject"
	grantKey := "entitlement-concurrent-grant"
	grantInput := postgresDomainEntitlementInput(subject, validFrom)

	// actorless 服务命令不得携带 Host-signed actor delegation。
	actorRequest := h.request(t, h.identity, spec, "entitlement-actor-token", grantInput, "", postgresDomainCommandActorID)
	actorResult, err := h.execute(h.identity, actorRequest)
	assertPostgresDomainCommandResult(t, actorResult, err, hostv2.CommandState_COMMAND_STATE_REJECTED, "host.command_actor_delegation_unexpected")
	assertPostgresDomainEntitlement(t, h, subject, 0)
	assertPostgresDomainCommandEvidence(t, h, "entitlement-actor-token", 0, 0)

	request := h.request(t, h.identity, spec, grantKey, grantInput, "", 0)
	const workers = 8
	start := make(chan struct{})
	results := make([]*hostv2.CommandResult, workers)
	errorsByWorker := make([]error, workers)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsByWorker[index] = h.execute(
				h.identity, proto.Clone(request).(*hostv2.CommandRequest),
			)
		}(index)
	}
	close(start)
	wait.Wait()

	states := map[hostv2.CommandState]int{}
	var committed *hostv2.CommandResult
	for index, workerErr := range errorsByWorker {
		if workerErr != nil {
			t.Fatalf("worker %d: %v", index, workerErr)
		}
		states[results[index].GetState()]++
		if results[index].GetState() == hostv2.CommandState_COMMAND_STATE_COMMITTED {
			committed = results[index]
		}
	}
	if states[hostv2.CommandState_COMMAND_STATE_COMMITTED] != 1 ||
		states[hostv2.CommandState_COMMAND_STATE_REPLAYED] != workers-1 {
		t.Fatalf("concurrent entitlement states = %#v", states)
	}
	if committed == nil {
		t.Fatal("expected one committed entitlement grant")
	}
	assertPostgresDomainEntitlement(t, h, subject, 1)
	assertPostgresDomainCommandEvidence(t, h, grantKey, 1, 0)
	entitlementID, revision := postgresDomainEntitlementIdentity(t, committed)
	if entitlementID == "" || revision != "1" {
		t.Fatalf("grant identity = %s@%s", entitlementID, revision)
	}

	// 同键但主体变更必须冲突，且不得产生第二行事实/回执。
	conflictInput := postgresDomainEntitlementInput(subject+"-changed", validFrom)
	conflictRequest := h.request(t, h.identity, spec, grantKey, conflictInput, "", 0)
	conflict, err := h.execute(h.identity, conflictRequest)
	assertPostgresDomainCommandResult(t, conflict, err, hostv2.CommandState_COMMAND_STATE_ROLLED_BACK, "host.command_idempotency_conflict")
	assertPostgresDomainEntitlement(t, h, subject, 1)
	assertPostgresDomainEntitlement(t, h, subject+"-changed", 0)
	assertPostgresDomainCommandEvidence(t, h, grantKey, 1, 0)

	// expected-revision 冲突 fail closed：事实保持 active，无额外 event/receipt。
	staleKey := "entitlement-stale-revision"
	staleRequest := h.request(t, h.identity, spec, staleKey, map[string]any{
		"action": "revoke", "entitlementId": entitlementID,
	}, "999", 0)
	stale, err := h.execute(h.identity, staleRequest)
	assertPostgresDomainCommandResult(t, stale, err, hostv2.CommandState_COMMAND_STATE_ROLLED_BACK, "host.entitlement_revision_conflict")
	assertPostgresDomainEntitlementStatus(t, h, entitlementID, "active", 1)
	assertPostgresDomainCommandEvidence(t, h, staleKey, 0, 0)
	if events := h.count(t, `SELECT count(*) FROM entitlement_events WHERE entitlement_id = $1::bigint`, entitlementID); events != 1 {
		t.Fatalf("stale revision events = %d, want 1", events)
	}

	// 正确 revision 的 revoke 与 Host receipt/audit 同事务提交。
	revokeKey := "entitlement-revoke"
	revokeRequest := h.request(t, h.identity, spec, revokeKey, map[string]any{
		"action": "revoke", "entitlementId": entitlementID,
	}, revision, 0)
	revoked, err := h.execute(h.identity, revokeRequest)
	assertPostgresDomainCommandResult(t, revoked, err, hostv2.CommandState_COMMAND_STATE_COMMITTED, "")
	assertPostgresDomainEntitlementStatus(t, h, entitlementID, "revoked", 2)
	assertPostgresDomainCommandEvidence(t, h, revokeKey, 1, 0)
	if events := h.count(t, `SELECT count(*) FROM entitlement_events WHERE entitlement_id = $1::bigint`, entitlementID); events != 2 {
		t.Fatalf("revoke events = %d, want 2", events)
	}

	// 同键 revoke 精确回放，不产生第二份事实。
	replayRevoke := h.request(t, h.identity, spec, revokeKey, map[string]any{
		"action": "revoke", "entitlementId": entitlementID,
	}, revision, 0)
	replayed, err := h.execute(h.identity, replayRevoke)
	assertPostgresDomainCommandResult(t, replayed, err, hostv2.CommandState_COMMAND_STATE_REPLAYED, "")
	if replayed.GetTransactionId() != revoked.GetTransactionId() {
		t.Fatalf("revoke replay transaction = %q, want %q", replayed.GetTransactionId(), revoked.GetTransactionId())
	}
	assertPostgresDomainEntitlementStatus(t, h, entitlementID, "revoked", 2)
	assertPostgresDomainCommandEvidence(t, h, revokeKey, 1, 0)
	if events := h.count(t, `SELECT count(*) FROM entitlement_events WHERE entitlement_id = $1::bigint`, entitlementID); events != 2 {
		t.Fatalf("replayed revoke events = %d, want 2", events)
	}
}

func exercisePostgresDomainCommand(t *testing.T, h *postgresDomainCommandHarness, exercise postgresDomainCommandExercise) {
	t.Helper()
	deniedIdentity := h.identity
	deniedActorID := postgresDomainCommandDeniedActorID
	deniedState := hostv2.CommandState_COMMAND_STATE_ROLLED_BACK
	deniedReason := "host.command_actor_permission_denied"
	if !exercise.spec.Delegated {
		deniedIdentity = h.deniedIdentity
		deniedActorID = 0
		deniedState = hostv2.CommandState_COMMAND_STATE_REJECTED
		deniedReason = "host.command_authority_denied"
	}
	deniedKey := "denied-" + exercise.name
	deniedRequest := h.request(
		t, deniedIdentity, exercise.spec, deniedKey,
		exercise.rollbackInput, exercise.rollbackRevision, deniedActorID,
	)
	denied, err := h.execute(deniedIdentity, deniedRequest)
	assertPostgresDomainCommandResult(t, denied, err, deniedState, deniedReason)
	exercise.assertUnchanged(t)
	assertPostgresDomainCommandEvidence(t, h, deniedKey, 0, 0)

	allowedKey := "allowed-" + exercise.name
	allowedActorID := int64(0)
	if exercise.spec.Delegated {
		allowedActorID = postgresDomainCommandActorID
	}
	allowedRequest := h.request(
		t, h.identity, exercise.spec, allowedKey,
		exercise.allowedInput, exercise.allowedRevision, allowedActorID,
	)
	committed, err := h.execute(h.identity, allowedRequest)
	assertPostgresDomainCommandResult(t, committed, err, hostv2.CommandState_COMMAND_STATE_COMMITTED, "")
	exercise.assertAllowed(t)
	wantConsumption := 0
	if exercise.spec.Delegated {
		wantConsumption = 1
	}
	assertPostgresDomainCommandEvidence(t, h, allowedKey, 1, wantConsumption)
	jobsAfterCommit := h.count(t, `SELECT count(*) FROM host_command_domain_jobs`)

	replayRequest := h.request(
		t, h.identity, exercise.spec, allowedKey,
		exercise.allowedInput, exercise.allowedRevision, allowedActorID,
	)
	replayed, err := h.execute(h.identity, replayRequest)
	assertPostgresDomainCommandResult(t, replayed, err, hostv2.CommandState_COMMAND_STATE_REPLAYED, "")
	if replayed.GetTransactionId() != committed.GetTransactionId() {
		t.Fatalf("%s replay transaction = %q, want %q", exercise.name, replayed.GetTransactionId(), committed.GetTransactionId())
	}
	exercise.assertAllowed(t)
	assertPostgresDomainCommandEvidence(t, h, allowedKey, 1, wantConsumption)
	if jobs := h.count(t, `SELECT count(*) FROM host_command_domain_jobs`); jobs != jobsAfterCommit {
		t.Fatalf("%s replay inserted jobs: got %d, want %d", exercise.name, jobs, jobsAfterCommit)
	}

	rollbackKey := "rollback-" + exercise.name
	rollbackRequest := h.request(
		t, h.identity, exercise.spec, rollbackKey,
		exercise.rollbackInput, exercise.rollbackRevision, allowedActorID,
	)
	rolledBack, err := h.execute(h.identity, rollbackRequest)
	assertPostgresDomainCommandResult(t, rolledBack, err, hostv2.CommandState_COMMAND_STATE_ROLLED_BACK, "host.command_rolled_back")
	exercise.assertUnchanged(t)
	assertPostgresDomainCommandEvidence(t, h, rollbackKey, 0, 0)
	if jobs := h.count(t, `SELECT count(*) FROM host_command_domain_jobs`); jobs != jobsAfterCommit {
		t.Fatalf("%s rollback retained jobs: got %d, want %d", exercise.name, jobs, jobsAfterCommit)
	}
}

func assertPostgresDomainCommandResult(
	t *testing.T,
	result *hostv2.CommandResult,
	err error,
	wantState hostv2.CommandState,
	wantReason string,
) {
	t.Helper()
	if err != nil || result.GetState() != wantState {
		t.Fatalf("command result = %#v, %v; want state %s", result, err, wantState)
	}
	if result.GetError().GetReason() != wantReason {
		t.Fatalf("command reason = %q, want %q", result.GetError().GetReason(), wantReason)
	}
}

func assertPostgresDomainCommandEvidence(
	t *testing.T,
	h *postgresDomainCommandHarness,
	key string,
	wantReceipt, wantConsumption int,
) {
	t.Helper()
	if got := h.count(t, `SELECT count(*) FROM extension_host_command_receipts WHERE idempotency_key = $1`, key); got != wantReceipt {
		t.Fatalf("receipt count for %s = %d, want %d", key, got, wantReceipt)
	}
	if got := h.count(t, `SELECT count(*) FROM extension_host_command_actor_delegation_consumptions WHERE idempotency_key = $1`, key); got != wantConsumption {
		t.Fatalf("delegation count for %s = %d, want %d", key, got, wantConsumption)
	}
	if got := h.count(t, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.host_command.committed'
		  AND metadata->>'idempotencyKey' = $1
	`, key); got != wantReceipt {
		t.Fatalf("Host Command audit count for %s = %d, want %d", key, got, wantReceipt)
	}
}

func seedPostgresDomainCommandUserTarget(t *testing.T, h *postgresDomainCommandHarness, id int64, username string) int64 {
	t.Helper()
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO users (
		  id, username, username_lower, email, email_lower, display_name, status
		) VALUES ($1, $2, $2, $2 || '@example.test', $2 || '@example.test', $2, 'active')
	`, id, username); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO user_sessions (user_id, sid, session_hash)
		VALUES ($1, $2 || '-sid', $2 || '-hash')
	`, id, username); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertPostgresDomainUserState(
	t *testing.T,
	h *postgresDomainCommandHarness,
	userID int64,
	status string,
	revision int64,
	revoked bool,
) {
	t.Helper()
	var gotStatus string
	var gotRevision int64
	var revokedAt *time.Time
	if err := h.pool.QueryRow(h.ctx, `
		SELECT users.status, users.current_token_version, user_sessions.revoked_at
		FROM users JOIN user_sessions ON user_sessions.user_id = users.id
		WHERE users.id = $1
	`, userID).Scan(&gotStatus, &gotRevision, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotRevision != revision || (revokedAt != nil) != revoked {
		t.Fatalf("user %d state = %s/%d/%v, want %s/%d/%v", userID, gotStatus, gotRevision, revokedAt != nil, status, revision, revoked)
	}
}

func seedPostgresDomainCommandTopic(t *testing.T, h *postgresDomainCommandHarness, slug, status string) (int64, string) {
	t.Helper()
	var postID int64
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO posts (
		  raw_content, html_content, plain_text, source_format, editor_type,
		  render_version, content_hash, created_by_user_id, updated_by_user_id
		) VALUES ('body', '<p>body</p>', 'body', 'markdown', 'markdown', '1', $1, $2, $2)
		RETURNING id
	`, slug, postgresDomainCommandActorID).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	var topicID int64
	var revision time.Time
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO topics (
		  category_id, author_user_id, content_id, title, slug, status, moderation_triggers
		) SELECT id, $1, $2, $3, $3, $4, '[]'::jsonb FROM categories WHERE slug = 'general'
		RETURNING id, updated_at
	`, postgresDomainCommandActorID, postID, slug, status).Scan(&topicID, &revision); err != nil {
		t.Fatal(err)
	}
	return topicID, revision.UTC().Format(time.RFC3339Nano)
}

func assertPostgresDomainTopicStatus(t *testing.T, h *postgresDomainCommandHarness, topicID int64, status string) {
	t.Helper()
	var got string
	if err := h.pool.QueryRow(h.ctx, `SELECT status FROM topics WHERE id = $1`, topicID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != status {
		t.Fatalf("topic %d status = %s, want %s", topicID, got, status)
	}
}

func seedPostgresDomainCommandMetaField(t *testing.T, h *postgresDomainCommandHarness, key string) {
	t.Helper()
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO entity_field_definitions (
		  field_key, entity_type, value_type, visibility, owner_extension_id,
		  required, enabled, constraints
		) VALUES ($1, 'user', 'string', 'admin', $2, false, true, '{"maxLength":200}'::jsonb)
	`, key, h.identity.GetExtensionId()); err != nil {
		t.Fatal(err)
	}
}

func postgresDomainMetaInput(userID int64, fieldKey, value string) map[string]any {
	return map[string]any{
		"entityType": "user", "entityId": fmt.Sprintf("%d", userID),
		"values": []any{map[string]any{"fieldKey": fieldKey, "value": value}},
	}
}

func assertPostgresDomainMetaValue(t *testing.T, h *postgresDomainCommandHarness, fieldKey, value string, count int) {
	t.Helper()
	var gotCount int
	var gotValue string
	err := h.pool.QueryRow(h.ctx, `
		SELECT count(*), COALESCE(max(value_text), '')
		FROM entity_meta_values WHERE field_key = $1
	`, fieldKey).Scan(&gotCount, &gotValue)
	if err != nil {
		t.Fatal(err)
	}
	if gotCount != count || gotValue != value {
		t.Fatalf("meta %s = %d/%q, want %d/%q", fieldKey, gotCount, gotValue, count, value)
	}
}

func assertPostgresDomainModerationState(t *testing.T, h *postgresDomainCommandHarness, topicID int64, status string, decisions int) {
	t.Helper()
	assertPostgresDomainTopicStatus(t, h, topicID, status)
	if got := h.count(t, `SELECT count(*) FROM moderation_decisions WHERE target_type = 'topic' AND target_id = $1`, topicID); got != decisions {
		t.Fatalf("moderation decisions for %d = %d, want %d", topicID, got, decisions)
	}
}

func postgresDomainEntitlementInput(subjectID, validFrom string) map[string]any {
	return map[string]any{
		"action":  "grant",
		"subject": map[string]any{"type": "user", "id": subjectID},
		"scope": map[string]any{
			"kind": "capability", "capability": "forum.priority-access",
		},
		"source":    map[string]any{"type": "fixture", "id": "domain-command"},
		"validFrom": validFrom,
	}
}

func assertPostgresDomainEntitlement(t *testing.T, h *postgresDomainCommandHarness, subjectID string, count int) {
	t.Helper()
	if got := h.count(t, `SELECT count(*) FROM entitlements WHERE subject_type = 'user' AND subject_id = $1`, subjectID); got != count {
		t.Fatalf("entitlements for %s = %d, want %d", subjectID, got, count)
	}
	if got := h.count(t, `
		SELECT count(*) FROM entitlement_events
		JOIN entitlements ON entitlements.id = entitlement_events.entitlement_id
		WHERE entitlements.subject_id = $1
	`, subjectID); got != count {
		t.Fatalf("entitlement events for %s = %d, want %d", subjectID, got, count)
	}
}

func postgresDomainEntitlementIdentity(t *testing.T, result *hostv2.CommandResult) (id, revision string) {
	t.Helper()
	values := result.GetOutput().GetValue().AsMap()
	entitlement, _ := values["entitlement"].(map[string]any)
	if entitlement == nil {
		t.Fatalf("missing entitlement output: %#v", values)
	}
	id, _ = entitlement["id"].(string)
	revision, _ = entitlement["revision"].(string)
	return id, revision
}

func assertPostgresDomainEntitlementStatus(
	t *testing.T,
	h *postgresDomainCommandHarness,
	entitlementID, status string,
	revision int,
) {
	t.Helper()
	var gotStatus string
	var gotRevision int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT status, revision FROM entitlements WHERE id = $1::bigint
	`, entitlementID).Scan(&gotStatus, &gotRevision); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotRevision != revision {
		t.Fatalf("entitlement %s = %s@%d, want %s@%d", entitlementID, gotStatus, gotRevision, status, revision)
	}
}

func seedPostgresDomainCommandAttachment(t *testing.T, h *postgresDomainCommandHarness, key string) (int64, string) {
	t.Helper()
	var id int64
	var revision time.Time
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name,
		  content_type, extension, size_bytes, sha256, visibility, status
		) VALUES ($1, $2, 'local', $1, $1 || '.txt', 'text/plain', 'txt', 4, $3, 'private', 'active')
		RETURNING id, updated_at
	`, key, postgresDomainCommandActorID, fmt.Sprintf("%064s", key)).Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	return id, revision.UTC().Format(time.RFC3339Nano)
}

func assertPostgresDomainAttachmentStatus(t *testing.T, h *postgresDomainCommandHarness, attachmentID int64, status string) {
	t.Helper()
	var got string
	if err := h.pool.QueryRow(h.ctx, `SELECT status FROM attachments WHERE id = $1`, attachmentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != status {
		t.Fatalf("attachment %d status = %s, want %s", attachmentID, got, status)
	}
}
