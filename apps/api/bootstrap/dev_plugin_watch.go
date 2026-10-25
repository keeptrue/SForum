package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
)

// devPluginWatcher 是「彻底 0 命令」dev 模式的 host 侧文件监视器：
// 监听 EXTERNAL_EXTENSION_ROOTS 下的包目录，去抖后触发 DevAutoRuntime.PromoteAll
// （重扫 → 构建 → digest → stage → grant → enable/upgrade）。
// 仅 development 且非 Safe Mode 装配；生产不创建本对象（fail closed）。
// 操作者拍板（2026-10-17）：dev 机器上「能写 dev 来源根的进程」即宿主全权执行体，
// 无任何登录/授权交互；决策记录见 knowledge/decisions/2026-10-17-dev-zero-command.md。
type devPluginWatcher struct {
	roots    []string
	promoter devPackagePromoter
	logger   *slog.Logger
	debounce time.Duration
	watcher  *fsnotify.Watcher
	stopped  chan struct{}
}

// devPackagePromoter 由 dev 装配注入：对变更的包根执行 go build + digest 写回。
type devPackagePromoter interface {
	PromoteAll(ctx context.Context)
}

func newDevPluginWatcher(
	runtime *extensions.DevAutoRuntime,
	promoter devPackagePromoter,
	roots []string,
	logger *slog.Logger,
) (*devPluginWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &devPluginWatcher{
		promoter: promoter, roots: roots,
		logger: logger, debounce: 600 * time.Millisecond,
		watcher: watcher, stopped: make(chan struct{}),
	}
	return w, nil
}

// devWatchExcludedDirectories 与 CLI dev 循环同一套排除规则：产物、依赖、VCS。
func devWatchExcludedDirectories() map[string]struct{} {
	return map[string]struct{}{
		"node_modules": {}, "dist": {}, "vendor": {}, ".git": {}, ".nuxt": {},
		"tmp": {}, "temp": {}, ".cache": {}, "coverage": {}, "__pycache__": {},
	}
}

// Start 递归登记监视目录并启动去抖循环。调用方负责在 ctx 取消时等待 Stop。
func (w *devPluginWatcher) Start(ctx context.Context) {
	registered := 0
	for _, root := range w.roots {
		if err := w.watchTree(root); err != nil {
			w.logger.WarnContext(ctx, "dev plugin watcher: watch root failed", "root", root, "error", err)
			continue
		}
		registered++
	}
	if registered == 0 {
		w.logger.InfoContext(ctx, "dev plugin watcher: no external roots to watch")
		_ = w.watcher.Close()
		close(w.stopped)
		return
	}
	go w.loop(ctx)
}

func (w *devPluginWatcher) watchTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root {
			if _, excluded := devWatchExcludedDirectories()[strings.ToLower(entry.Name())]; excluded {
				return filepath.SkipDir
			}
		}
		return w.watcher.Add(path)
	})
}

func (w *devPluginWatcher) loop(ctx context.Context) {
	defer close(w.stopped)
	// 启动对账与事件周期共用同一 goroutine：SyncExternalSources 快照必须与
	// buildRoot 的构建串行，否则快照会拍到 backend/plugin 的写中间态
	//（manifest 声明与实际字节不一致 → upgrade preflight invalid manifest）。
	w.promoteAllLogged(ctx)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			_ = w.watcher.Close()
			return
		case err, ok := <-w.watcher.Errors:
			if !ok {
				w.watcher.Errors = nil
				continue
			}
			w.logger.WarnContext(ctx, "dev plugin watcher error", "error", err)
		case event, ok := <-w.watcher.Events:
			if !ok {
				w.watcher.Events = nil
				continue
			}
			if !devWatchEventRelevant(event.Name) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(w.debounce)
			} else {
				timer.Reset(w.debounce)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			// 单事务：构建/摘要失败保留上一个健康制品（stage 未发生）。
			promoteCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			w.promoteAllLogged(promoteCtx)
			cancel()
		}
	}
}

func (w *devPluginWatcher) promoteAllLogged(ctx context.Context) {
	w.logger.InfoContext(ctx, "dev plugin watcher: change detected; rebuilding dev sources")
	// 构建阶段（go build / digest 写回）由公共 pipeline 对每个外部根执行。
	roots := append([]string(nil), w.roots...)
	sort.Strings(roots)
	var buildErrs []string
	for _, root := range roots {
		if err := w.buildRoot(ctx, root); err != nil {
			buildErrs = append(buildErrs, err.Error())
		}
	}
	if len(buildErrs) > 0 {
		// 构建失败：保留上一个健康制品，只记日志（dev 容错语义）。
		w.logger.WarnContext(ctx, "dev plugin watcher: build failed; keeping last healthy artifact",
			"errors", strings.Join(buildErrs, "; "))
	}
	w.promoter.PromoteAll(ctx)
}

func (w *devPluginWatcher) buildRoot(ctx context.Context, root string) error {
	_ = ctx
	pluginsDir := filepath.Join(root, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var buildErrs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packageRoot := filepath.Join(pluginsDir, entry.Name())
		digest, buildErr := extensionpackage.BuildAndDigestDevPackage(packageRoot)
		if buildErr != nil {
			buildErrs = append(buildErrs, entry.Name()+": "+buildErr.Error())
			continue
		}
		if digest != "" {
			w.logger.InfoContext(ctx, "dev plugin watcher: package built",
				"package", entry.Name(), "digest", digest)
		}
	}
	if len(buildErrs) > 0 {
		return fmt.Errorf("%s", strings.Join(buildErrs, "; "))
	}
	return nil
}

func devWatchEventRelevant(path string) bool {
	relative := filepath.ToSlash(strings.TrimSpace(path))
	if relative == "" {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		if _, excluded := devWatchExcludedDirectories()[strings.ToLower(segment)]; excluded {
			return false
		}
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, ".") && base != ".htaccess" {
		return false
	}
	// "-go-tmp-umask" 是 Go 链接器的中间产物后缀：`go build -o X` 会先写
	// `X-go-tmp-umask` 再改名成 X。BuildBackend 的输出是 backend/plugin.tmp，
	// 于是真实事件路径是 plugin.tmp-go-tmp-umask —— 它不以 ".tmp" 结尾，
	// 只靠下面那条 ".tmp" 规则拦不住，会自己触发自己形成稳态重建循环。
	for _, suffix := range []string{".sforum.zip", ".zip", ".tmp", ".swp", ".swx", ".swo", "~", ".log", ".map", "-go-tmp-umask"} {
		if strings.HasSuffix(base, suffix) {
			return false
		}
	}
	return true
}
