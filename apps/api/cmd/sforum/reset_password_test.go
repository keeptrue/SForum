package main

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestRootCommandIncludesResetPasswordCommand(t *testing.T) {
	root := newRootCommand()
	command, _, err := root.Find([]string{"users:reset-password"})
	if err != nil || command == nil {
		t.Fatalf("find users:reset-password: command=%v err=%v", command, err)
	}
}

func TestNormalizeResetPasswordEmail(t *testing.T) {
	got, err := normalizeResetPasswordEmail(" Admin@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "admin@example.com" {
		t.Fatalf("email=%q", got)
	}
	if _, err := normalizeResetPasswordEmail("not an email"); err == nil {
		t.Fatal("expected invalid email error")
	}
}

func TestResetPasswordPolicyMessages(t *testing.T) {
	policy := identity.PasswordPolicy{
		MinLength:        12,
		MaxLength:        128,
		RequireLowercase: true,
		RequireUppercase: true,
		RequireNumber:    true,
		RequireSymbol:    true,
	}
	err := validateResetPasswordCandidate(policy, "short")
	if err == nil {
		t.Fatal("expected policy error")
	}
	for _, want := range []string{"密码长度不足", "需要包含大写字母", "需要包含数字", "需要包含符号"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestRecoverySuperAdminActorCanResetSuperAdmin(t *testing.T) {
	actor := recoverySuperAdminActor()
	if !actor.IsSuperAdmin() || !actor.Can(identity.PermissionUserManage) {
		t.Fatalf("unexpected actor: %+v", actor)
	}
}

// 非交互模式（自动化/发布镜像 smoke 用）：必须显式 --yes，且策略校验与交互式一致。
func TestResetPasswordNonInteractiveRequiresConfirmationAndBothFlags(t *testing.T) {
	cases := []struct {
		name string
		opts resetPasswordOptions
		want string
	}{
		{
			name: "password without yes",
			opts: resetPasswordOptions{Email: "admin@example.com", Password: "Str0ng-Passw0rd!"},
			want: "requires --yes",
		},
		{
			name: "email only",
			opts: resetPasswordOptions{Email: "admin@example.com", Confirm: true},
			want: "requires both --email and --password",
		},
		{
			name: "password only",
			opts: resetPasswordOptions{Password: "Str0ng-Passw0rd!", Confirm: true},
			want: "requires both --email and --password",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := runUsersResetPasswordCommand(context.Background(), testCase.opts, &cobra.Command{})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v; want %q", err, testCase.want)
			}
		})
	}
}

// 非交互模式也必须走站点密码策略，弱密码不得绕过。
func TestResetPasswordNonInteractiveEnforcesPasswordPolicy(t *testing.T) {
	policy := identity.PasswordPolicy{
		MinLength: 12, MaxLength: 128,
		RequireLowercase: true, RequireUppercase: true, RequireNumber: true, RequireSymbol: true,
	}
	if err := validateResetPasswordCandidate(policy, "short"); err == nil {
		t.Fatal("weak password must be rejected by the same policy as the interactive path")
	}
	if err := validateResetPasswordCandidate(policy, "Str0ng-Passw0rd!"); err != nil {
		t.Fatalf("strong password rejected: %v", err)
	}
}
