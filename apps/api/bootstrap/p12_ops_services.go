package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
	runtimerollout "github.com/keeptrue/sforum/apps/api/app/Support/RuntimeRollout"
	systemtier "github.com/keeptrue/sforum/apps/api/app/Support/SystemTier"
	"github.com/keeptrue/sforum/apps/api/config"
)

// productionP12Ops holds durable P12 operations packages bound at API boot.
type productionP12Ops struct {
	Rollout    *runtimerollout.Service
	SystemTier *systemtier.Registry
	Privacy    *privacy.Registry
}

// bindProductionP12Ops wires RuntimeRollout / SystemTier / Privacy
// onto PostgreSQL authority so multi-node and CLI recovery share one store.
// Safe Mode does not load system-tier members (checked by callers via LoadOrder).
func bindProductionP12Ops(
	cfg config.Config,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) (*productionP12Ops, error) {
	if pool == nil {
		return nil, fmt.Errorf("bootstrap: P12 ops requires PostgreSQL")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rolloutStore, err := runtimerollout.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create runtime rollout store: %w", err)
	}
	rollout := runtimerollout.NewWithStore(rolloutStore)

	tierStore, err := systemtier.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create system tier store: %w", err)
	}
	tier := systemtier.NewWithStore(tierStore)

	// Privacy：真实 RBAC + PostgreSQL audit，禁止「actor 非空即允许」。
	// API 装配与带外 CLI 恢复共用同一个构造器，权限判定不出现第二份真相。
	privacyReg, err := NewPrivacyRegistry(pool)
	if err != nil {
		return nil, err
	}

	// Safe Mode 不在此加载 system extension 代码；仅暴露 registry 供调用方查询。
	if cfg.SafeMode {
		if order, err := tier.LoadOrder(context.Background(), true); err != nil {
			return nil, err
		} else if order != nil {
			return nil, fmt.Errorf("bootstrap: safe mode must bypass system tier")
		}
	}

	return &productionP12Ops{
		Rollout: rollout, SystemTier: tier, Privacy: privacyReg,
	}, nil
}
