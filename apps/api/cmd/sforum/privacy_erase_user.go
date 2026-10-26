package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
	"github.com/keeptrue/sforum/apps/api/bootstrap"
)

// privacy:erase-user 是 `privacy.Registry.EraseUser` 的生产入口（D2 决定）：
// 遍历全部已注册 Privacy contribution 的擦除钩子（含宿主侧插件命名空间会话
// 状态），把一个用户的可擦除数据清掉。权限门是真实 RBAC——执行者必须是一个
// 真实账号，且持有 super_admin 或 `user.manage`；执行者与目标都写进审计台账。
// 报告只含扩展 id、成败与外部保留警告，任何值/键都不会出现在输出里。
type privacyEraseUserOptions struct {
	DatabaseURL   string
	ActorUserID   int64
	TargetEmail   string
	TargetID      int64
	Confirm       bool
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

func newPrivacyEraseUserCommand() *cobra.Command {
	opts := privacyEraseUserOptions{}
	cmd := &cobra.Command{
		Use:   "privacy:erase-user",
		Short: "擦除一个用户的隐私数据（super_admin / user.manage）",
		Long: `遍历所有已注册的 Privacy contribution 擦除钩子，擦除一个用户的隐私数据。

这是 privacy.Registry.EraseUser 的生产入口。权限由真实 RBAC 把守：
--actor-user-id 指向的账号必须是 super_admin 或持有 user.manage 权限；
执行者（actor）与目标都会写入 privacy 审计台账。报告只含扩展 id、成败与
外部保留警告——任何数据值或命名空间键都不会被打印。

目标二选一：--user-id 或 --user-email。非交互自动化必须带 --yes。

由于 config.Load() 只读环境变量、不读 .env，运行前需先把 .env 导入环境：

  set -a; . ./.env; set +a
  go run ./cmd/sforum privacy:erase-user --actor-user-id 1 --user-email user@example.com

自动化（CI、运维脚本）显式确认：

  go run ./cmd/sforum privacy:erase-user \
    --database-url "$DATABASE_URL" --actor-user-id 1 \
    --user-id 42 --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrivacyEraseUserCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL)")
	cmd.Flags().Int64Var(&opts.ActorUserID, "actor-user-id", 0, "执行擦除的 super_admin/user.manage 账号 id（写入审计）")
	cmd.Flags().Int64Var(&opts.TargetID, "user-id", 0, "目标用户 id（与 --user-email 二选一）")
	cmd.Flags().StringVar(&opts.TargetEmail, "user-email", "", "目标用户邮箱（与 --user-id 二选一）")
	cmd.Flags().StringVar(&opts.RedisAddr, "redis-addr", "", "Redis 地址（默认取 REDIS_ADDR，再退 127.0.0.1:6379）；插件会话状态擦除必需")
	cmd.Flags().StringVar(&opts.RedisPassword, "redis-password", "", "Redis 密码（默认取 REDIS_PASSWORD）")
	cmd.Flags().IntVar(&opts.RedisDB, "redis-db", -1, "Redis DB（默认取 REDIS_DB，再退 0）")
	cmd.Flags().BoolVar(&opts.Confirm, "yes", false, "非交互模式：跳过确认提示")
	return cmd
}

func runPrivacyEraseUserCommand(ctx context.Context, opts privacyEraseUserOptions, cmd *cobra.Command) error {
	// 用法契约先于连接配置校验。
	if opts.ActorUserID <= 0 {
		return errors.New("privacy:erase-user requires --actor-user-id (the super_admin/user.manage account recorded in the audit ledger)")
	}
	if (opts.TargetID > 0) == (strings.TrimSpace(opts.TargetEmail) != "") {
		return errors.New("privacy:erase-user needs exactly one of --user-id or --user-email")
	}

	databaseURL := strings.TrimSpace(opts.DatabaseURL)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		return fmt.Errorf("database url is empty: set DATABASE_URL or pass --database-url")
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 2)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	identityStore := identity.NewPostgresStore(pool)

	// 执行者必须真实存在（RBAC 门还会再次校验 super_admin / user.manage）。
	if _, err := identityStore.LoadActor(ctx, opts.ActorUserID); err != nil {
		return fmt.Errorf("load actor %d: %w (the actor must be a real super_admin or user.manage account)", opts.ActorUserID, err)
	}

	targetID := opts.TargetID
	targetSummary := fmt.Sprintf("user_id=%d", targetID)
	if opts.TargetID <= 0 {
		user, err := identityStore.GetCurrentUserByEmail(ctx, strings.ToLower(strings.TrimSpace(opts.TargetEmail)))
		if err != nil {
			if errors.Is(err, identity.ErrUserNotFound) {
				return fmt.Errorf("user not found for email %q", opts.TargetEmail)
			}
			return err
		}
		targetID = user.ID
		targetSummary = fmt.Sprintf("user_id=%d username=%s", user.ID, user.Username)
	} else {
		// LoadActor 对不存在的用户返回错误，兼作目标存在性检查。
		if _, err := identityStore.LoadActor(ctx, targetID); err != nil {
			return fmt.Errorf("load target user %d: %w", targetID, err)
		}
	}

	if !opts.Confirm {
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("确认擦除这个用户的隐私数据？").
					Description("执行者: user:" + strconv.FormatInt(opts.ActorUserID, 10) +
						"\n目标: " + targetSummary +
						"\n\n将遍历全部已注册擦除钩子；操作会写入审计台账，且不可撤销。").
					Affirmative("确认擦除").
					Negative("取消").
					Value(&confirmed).
					Validate(func(value bool) error {
						if !value {
							return errors.New("已取消擦除")
						}
						return nil
					}),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
	}

	redisAddr := strings.TrimSpace(opts.RedisAddr)
	if redisAddr == "" {
		redisAddr = strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	}
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	redisPassword := opts.RedisPassword
	if redisPassword == "" {
		redisPassword = os.Getenv("REDIS_PASSWORD")
	}
	redisDB := opts.RedisDB
	if redisDB < 0 {
		if raw := strings.TrimSpace(os.Getenv("REDIS_DB")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil {
				return fmt.Errorf("parse REDIS_DB %q: %w", raw, parseErr)
			}
			redisDB = parsed
		} else {
			redisDB = 0
		}
	}
	registry, err := bootstrap.NewPrivacyRegistryForOutband(ctx, pool, redisAddr, redisPassword, redisDB)
	if err != nil {
		return err
	}
	report, err := registry.EraseUser(ctx, fmt.Sprintf("user:%d", opts.ActorUserID), strconv.FormatInt(targetID, 10))
	if err != nil && !errors.Is(err, privacy.ErrPartial) {
		// partial 也照样打印报告再返回非零；完全失败没有报告可打。
		if report.UserID == "" {
			return err
		}
	}
	cmd.Printf("隐私擦除完成：target=%s actor=user:%d partial=%t\n", report.UserID, opts.ActorUserID, report.Partial)
	for _, result := range report.Results {
		line := fmt.Sprintf("  %-40s erased=%t", result.ExtensionID, result.Erased)
		if result.Error != "" {
			line += " error=" + result.Error
		}
		if len(result.RetainedExternal) > 0 {
			line += " retained_external=" + strings.Join(result.RetainedExternal, ",")
		}
		cmd.Println(line)
	}
	if len(report.Failures) > 0 {
		cmd.Printf("failures: %s\n", strings.Join(report.Failures, "; "))
	}
	if len(report.RetainedExternal) > 0 {
		cmd.Printf("retained external resources: %s\n", strings.Join(report.RetainedExternal, ", "))
	}
	if report.Partial {
		return errors.New("privacy erase finished partially; see failures above")
	}
	return nil
}
