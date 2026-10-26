package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

type extensionBuildOptions struct {
	SkipInstall   bool
	AllowScaffold bool
	SDKDir        string
}

func newExtensionBuildCommand() *cobra.Command {
	var opts extensionBuildOptions
	cmd := &cobra.Command{
		Use:   "build [path]",
		Short: "Build author-side frontend assets and run exact package gates",
		Long: `Build frontend/admin with Bun when package.json is present, refresh exact
digests, validate the package, and run Host contract tests. This author command
executes package scripts locally; installation and production runtime never call it.

--sdk-dir is a development-only affordance: it builds the plugin frontend against
the @sforum SDK packages in a local checkout instead of the published registry
versions, so a new SDK version can be exercised before its Release publishes it.
The workspace package.json is restored afterwards.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runExtensionBuild(cmd, root, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.SkipInstall, "skip-install", false, "Skip bun install and use existing frontend dependencies")
	cmd.Flags().BoolVar(&opts.AllowScaffold, "allow-scaffold", false, "Allow contract tests before the backend binary exists")
	cmd.Flags().StringVar(&opts.SDKDir, "sdk-dir", os.Getenv("SFORUM_SDK_DIR"), "Development only: build against @sforum SDK packages found in this directory instead of the registry (env: SFORUM_SDK_DIR)")
	return cmd
}

func runExtensionBuild(cmd *cobra.Command, root string, opts extensionBuildOptions) error {
	abs, err := resolveExtensionPackageRoot(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("package root is a symlink: %s", abs)
	}
	if err := buildExtensionAdminFrontend(cmd, abs, opts); err != nil {
		return err
	}
	cmd.Println("refreshing exact package digests")
	if err := runExtensionDigest(cmd, abs, true); err != nil {
		return err
	}
	cmd.Println("validating extension package")
	if err := runExtensionValidate(cmd, abs, false); err != nil {
		return err
	}
	cmd.Println("running extension contract tests")
	return runExtensionTest(cmd, abs, pluginsdk.Options{SkipBackendBinary: opts.AllowScaffold}, false)
}

func buildExtensionAdminFrontend(cmd *cobra.Command, packageRoot string, opts extensionBuildOptions) error {
	frontendRoot := filepath.Join(packageRoot, "frontend", "admin")
	packageJSON := filepath.Join(frontendRoot, "package.json")
	info, err := os.Lstat(packageJSON)
	if errors.Is(err, fs.ErrNotExist) {
		cmd.Println("no frontend/admin/package.json; skipping frontend build")
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect admin frontend: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("admin frontend package is not a regular file: %s", packageJSON)
	}
	if opts.SDKDir != "" {
		restore, err := linkLocalSDKPackages(cmd, frontendRoot, opts.SDKDir)
		if err != nil {
			return err
		}
		defer restore()
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		return errors.New("Bun is required to build frontend/admin; install Bun or build the frontend before running this command")
	}

	if !opts.SkipInstall {
		installArgs := []string{"install"}
		hasLock, err := hasBunLock(frontendRoot)
		if err != nil {
			return err
		}
		if hasLock {
			installArgs = append(installArgs, "--frozen-lockfile")
		}
		cmd.Printf("running bun %s in %s\n", strings.Join(installArgs, " "), frontendRoot)
		if err := runExtensionBuildProcess(cmd, bun, frontendRoot, installArgs...); err != nil {
			return err
		}
	} else {
		cmd.Println("skipping frontend dependency installation")
	}
	cmd.Printf("running bun run build in %s\n", frontendRoot)
	return runExtensionBuildProcess(cmd, bun, frontendRoot, "run", "build")
}

// linkLocalSDKPackages 把工作区里声明的 @sforum 依赖改写成指向本地 SDK 包的
// `file:` 依赖，让尚未发布的 SDK 版本也能参与构建。这是开发期能力：它只在
// 显式传入 --sdk-dir（或 SFORUM_SDK_DIR）时生效，构建结束后恢复原文件；
// 发布与生产流程永远不设置它。
func linkLocalSDKPackages(cmd *cobra.Command, frontendRoot, sdkDir string) (func(), error) {
	absoluteSDK, err := filepath.Abs(sdkDir)
	if err != nil {
		return nil, fmt.Errorf("resolve --sdk-dir: %w", err)
	}
	info, err := os.Stat(absoluteSDK)
	if err != nil {
		return nil, fmt.Errorf("inspect --sdk-dir %s: %w", absoluteSDK, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--sdk-dir is not a directory: %s", absoluteSDK)
	}

	local, err := indexLocalSDKPackages(absoluteSDK)
	if err != nil {
		return nil, err
	}
	if len(local) == 0 {
		return nil, fmt.Errorf("--sdk-dir %s contains no package.json declaring a local package name", absoluteSDK)
	}

	packageJSON := filepath.Join(frontendRoot, "package.json")
	original, err := os.ReadFile(packageJSON)
	if err != nil {
		return nil, fmt.Errorf("read admin frontend package: %w", err)
	}
	document := map[string]any{}
	if err := json.Unmarshal(original, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", packageJSON, err)
	}

	linked := make([]string, 0, len(local))
	for _, field := range []string{"dependencies", "devDependencies"} {
		dependencies, ok := document[field].(map[string]any)
		if !ok {
			continue
		}
		for name := range dependencies {
			directory, ok := local[name]
			if !ok {
				continue
			}
			dependencies[name] = "file:" + directory
			linked = append(linked, name)
		}
	}
	if len(linked) == 0 {
		return nil, fmt.Errorf("--sdk-dir %s provides no package matching this workspace's dependencies", absoluteSDK)
	}

	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", packageJSON, err)
	}
	updated = append(updated, '\n')

	// 锁文件同样要还原：改写依赖后重装会把 file: 解析结果写进锁文件，
	// 留着它会让作者下一次普通的 bun install --frozen-lockfile 失败。
	locks, err := snapshotLockfiles(frontendRoot)
	if err != nil {
		return nil, err
	}

	// 先落一份备份再改写：构建被强杀时作者仍能拿回原文件。
	backup := packageJSON + ".sforum-sdk-backup"
	if err := os.WriteFile(backup, original, 0o644); err != nil {
		return nil, fmt.Errorf("write SDK backup: %w", err)
	}
	if err := os.WriteFile(packageJSON, updated, 0o644); err != nil {
		return nil, fmt.Errorf("write SDK-linked package.json: %w", err)
	}
	cmd.Printf("linking local SDK packages (development only): %s\n", strings.Join(linked, ", "))

	return func() {
		restoreLockfiles(cmd, locks)
		if err := os.WriteFile(packageJSON, original, 0o644); err != nil {
			cmd.PrintErrf("warning: restore %s from %s manually: %v\n", packageJSON, backup, err)
			return
		}
		_ = os.Remove(backup)
	}, nil
}

type lockfileSnapshot struct {
	path    string
	content []byte
	existed bool
}

func snapshotLockfiles(frontendRoot string) ([]lockfileSnapshot, error) {
	snapshots := make([]lockfileSnapshot, 0, 2)
	for _, name := range []string{"bun.lock", "bun.lockb"} {
		path := filepath.Join(frontendRoot, name)
		content, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			snapshots = append(snapshots, lockfileSnapshot{path: path})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		snapshots = append(snapshots, lockfileSnapshot{path: path, content: content, existed: true})
	}
	return snapshots, nil
}

func restoreLockfiles(cmd *cobra.Command, snapshots []lockfileSnapshot) {
	for _, snapshot := range snapshots {
		if snapshot.existed {
			if err := os.WriteFile(snapshot.path, snapshot.content, 0o644); err != nil {
				cmd.PrintErrf("warning: restore %s manually: %v\n", snapshot.path, err)
			}
			continue
		}
		_ = os.Remove(snapshot.path)
	}
}

func indexLocalSDKPackages(sdkDir string) (map[string]string, error) {
	entries, err := os.ReadDir(sdkDir)
	if err != nil {
		return nil, fmt.Errorf("read --sdk-dir %s: %w", sdkDir, err)
	}
	packages := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sdkDir, entry.Name(), "package.json"))
		if err != nil {
			continue
		}
		var meta struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Name == "" {
			continue
		}
		packages[meta.Name] = filepath.Join(sdkDir, entry.Name())
	}
	return packages, nil
}

func hasBunLock(root string) (bool, error) {
	for _, name := range []string{"bun.lock", "bun.lockb"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect Bun lockfile %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("Bun lockfile is not a regular file: %s", path)
		}
		return true, nil
	}
	return false, nil
}

func runExtensionBuildProcess(cmd *cobra.Command, executable, dir string, args ...string) error {
	process := exec.CommandContext(cmd.Context(), executable, args...)
	// 子进程必须可被整体回收：见 devConfigureChildProcess。
	devConfigureChildProcess(process)
	process.Dir = dir
	process.Stdin = cmd.InOrStdin()
	process.Stdout = cmd.OutOrStdout()
	process.Stderr = cmd.ErrOrStderr()
	if err := process.Run(); err != nil {
		return fmt.Errorf("%s %v failed: %w", filepath.Base(executable), args, err)
	}
	return nil
}
