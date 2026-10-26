package bootstrap

import (
	"log/slog"

	devhygiene "github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
	"github.com/keeptrue/sforum/apps/api/config"
)

// startDevelopmentOrphanPluginReaper 在 development 下延迟清理 air 热重载遗留的
// 扩展 backend plugin 孤儿。Air 会先停旧 API，再执行 pre_cmd/build 并启动新进程；
// 新 API 延迟扫描可以覆盖旧 API 刚退出时的进程状态收敛窗口。
func startDevelopmentOrphanPluginReaper(cfg config.Config, logger *slog.Logger) (stop func()) {
	return devhygiene.StartOrphanPluginReaper(devhygiene.ReaperConfig{
		Enabled: devhygiene.ShouldEnableDevelopmentOrphanReaper(cfg.AppEnv),
		Delays:  devhygiene.DefaultReaperDelays(),
		Logger:  logger,
	})
}
