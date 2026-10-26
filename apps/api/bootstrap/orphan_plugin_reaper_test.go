package bootstrap

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/config"
)

func TestStartDevelopmentOrphanPluginReaperDisabledOutsideDevelopment(t *testing.T) {
	stop := startDevelopmentOrphanPluginReaper(config.Config{AppEnv: "production"}, nil)
	if stop == nil {
		t.Fatal("expected non-nil stop")
	}
	stop()
	stop = startDevelopmentOrphanPluginReaper(config.Config{AppEnv: "test"}, nil)
	stop()
}

func TestStartDevelopmentOrphanPluginReaperEnabledInDevelopment(t *testing.T) {
	stop := startDevelopmentOrphanPluginReaper(config.Config{AppEnv: "development"}, nil)
	if stop == nil {
		t.Fatal("expected non-nil stop")
	}
	// 立即 stop，避免测试进程在 3s/6s 后扫真实机器。
	stop()
}
