package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// 未过期 lease 必须拒绝且**一行都不写**：这是 fail-closed 的核心承诺，
// 只断言返回 errLifecycleCloseLeaseActive 证明不了"没写库"。
func TestLifecycleCloseRejectsUnexpiredLeaseWithoutWriting(t *testing.T) {
	fixture := newLifecycleClosePostgresFixture(t)
	ctx := fixture.ctx
	operationID := insertLifecycleCloseOperation(
		t, ctx, fixture.pool, "close.lease.plugin", "disable:unexpired-lease",
	)
	insertLifecycleCloseStep(t, ctx, fixture.pool, operationID, "disable.stop", "+30 minutes")

	opts := lifecycleCloseOptions{
		OperationID: operationID,
		Reason:      "operator tried to close while the step lease is still live",
	}
	if _, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts); !errors.Is(err, errLifecycleCloseLeaseActive) {
		t.Fatalf("unexpired lease close error = %v, want errLifecycleCloseLeaseActive", err)
	}

	_, terminal, _, revision, completedAt := loadLifecycleCloseOperation(t, ctx, fixture.pool, operationID)
	if completedAt != nil || terminal != "" || revision != 1 {
		t.Fatalf("rejected close must write nothing: terminal=%q revision=%d completedAt=%v",
			terminal, revision, completedAt)
	}
	if running := countLifecycleCloseSteps(t, ctx, fixture.pool, operationID, "running"); running != 1 {
		t.Fatalf("step state after rejected close = %d running, want 1", running)
	}
	var audits int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.cli_recovery'
		  AND metadata->>'mode' = 'lifecycle_close'
		  AND (metadata->>'operationId')::bigint = $1
	`, operationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("rejected close wrote %d audit row(s), want 0", audits)
	}

	// 反向确认：--force 是唯一放行口，并且会把 force 记进审计。
	opts.Force = true
	if _, err := closeLifecycleOperationOutOfBand(ctx, fixture.pool, opts); err != nil {
		t.Fatalf("forced close must succeed: %v", err)
	}
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.cli_recovery'
		  AND metadata->>'mode' = 'lifecycle_close'
		  AND (metadata->>'force')::boolean = TRUE
		  AND (metadata->>'operationId')::bigint = $1
	`, operationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("forced close audit rows = %d, want 1", audits)
	}
}

// 非法制品参数不得触库：给一个不可达地址，失败原因必须是参数校验。
func TestRecoveryQuarantineRejectsInvalidArtifactArgumentsBeforeConnecting(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the sforum CLI")
	}
	binary := buildSforumCLI(t)
	unreachable := "postgres://nobody:nobody@127.0.0.1:1/never?sslmode=disable"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing artifact arguments",
			args: []string{"extension", "quarantine", "sforum.some-plugin", "--database-url", unreachable},
			want: "--expect-version and --expect-digest are required",
		},
		{
			name: "malformed digest",
			args: []string{"extension", "quarantine", "sforum.some-plugin",
				"--expect-version", "1.0.0", "--expect-digest", "not-a-digest",
				"--database-url", unreachable},
			want: "must be a 64-character lowercase SHA-256 digest",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command := exec.Command(binary, testCase.args...)
			output, err := command.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("%s must exit 1, got %v\n%s", testCase.name, err, output)
			}
			text := string(output)
			if !strings.Contains(text, testCase.want) {
				t.Fatalf("%s must fail on argument validation, got:\n%s", testCase.name, text)
			}
			if strings.Contains(text, "connect recovery database") {
				t.Fatalf("%s must be rejected before connecting, got:\n%s", testCase.name, text)
			}
		})
	}
}
