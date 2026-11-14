package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keeptrue/sforum/apps/api/internal/roletemplates"
)

// newRoleTemplatesCommand 暴露角色模板生成物（apps/web/app/config/roleTemplates.ts）的
// 渲染 / 写盘 / 校验三条路径。唯一权威源是 Identity/seeds.go，本命令不引入第二份定义。
func newRoleTemplatesCommand() *cobra.Command {
	var (
		write bool
		check bool
		out   string
	)
	cmd := &cobra.Command{
		Use:   "roles:templates",
		Short: "Render the web role-template catalog from Identity/seeds.go",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if write && check {
				return fmt.Errorf("--write and --check are mutually exclusive")
			}
			path, explicit, err := roleTemplatesOutputPath(out)
			if err != nil {
				return err
			}
			rendered := roletemplates.Current()

			switch {
			case write:
				// 只有显式 --out 才建目录：默认路径必须落在已存在的仓库目录里，
				// 否则说明 repo root 解析出了问题，此时宁可失败也不要凭空造目录。
				if explicit {
					if dir := filepath.Dir(path); dir != "" {
						if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
							return mkErr
						}
					}
				}
				if writeErr := os.WriteFile(path, []byte(rendered), 0o644); writeErr != nil {
					return writeErr
				}
				cmd.Printf("wrote %s (%d bytes)\n", path, len(rendered))
				return nil
			case check:
				existing, readErr := os.ReadFile(path)
				if readErr != nil {
					return fmt.Errorf("read %s: %w", path, readErr)
				}
				if string(existing) == rendered {
					cmd.Printf("%s is up to date\n", path)
					return nil
				}
				cmd.PrintErr(diffHint(string(existing), rendered))
				return fmt.Errorf("%s is stale; run `cd apps/api && go run ./cmd/sforum roles:templates --write`", path)
			default:
				// 无参数：只把渲染结果打到 stdout，方便评审或重定向对比。
				cmd.Print(rendered)
				return nil
			}
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write the generated catalog to disk")
	cmd.Flags().BoolVar(&check, "check", false, "fail when the on-disk catalog is stale")
	cmd.Flags().StringVar(&out, "out", "", "output path (cwd-relative; default: <repo root>/"+roletemplates.DefaultOutput+")")
	return cmd
}

// roleTemplatesOutputPath 解析写盘/校验目标。
//
// 默认路径按**仓库根**解析，不按 cwd：从仓库根误跑 `go run ./apps/api/cmd/sforum` 时，
// cwd 相对路径会落到 repo 之外（旧的 "../web/..." 更是会 MkdirAll 出一个新目录），
// 生成物静默丢在错误位置，而 --check 又因为找不到文件而报一个与真实问题无关的错。
// 显式 --out 保持 cwd 相对语义：调用方自己指定了位置，就该按他说的位置写。
func roleTemplatesOutputPath(out string) (path string, explicit bool, err error) {
	if out != "" {
		return out, true, nil
	}
	root, err := findRepoRoot()
	if err != nil {
		return "", false, err
	}
	if err := requireAppsLayout(root); err != nil {
		return "", false, err
	}
	return filepath.Join(root, filepath.FromSlash(roletemplates.DefaultOutput)), false, nil
}

// requireAppsLayout 确认探测到的仓库根确实是 SForum 仓库：apps/api/go.mod 与 apps/web 必须存在。
//
// 仓库根探测复用 generator.go 的 findRepoRoot（全 CLI 一套约定：AGENTS.md + extensions）；
// 这里再校验 apps 布局，是因为生成物的默认落点就在 apps/web 下——探测成功但布局不对时
// 宁可报错，也不要往一个似是而非的目录里写文件。
func requireAppsLayout(root string) error {
	if _, err := os.Stat(filepath.Join(root, "apps", "api", "go.mod")); err != nil {
		return fmt.Errorf("repo root %s has no apps/api/go.mod; pass --out to choose a path explicitly", root)
	}
	info, err := os.Stat(filepath.Join(root, "apps", "web"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repo root %s has no apps/web directory; pass --out to choose a path explicitly", root)
	}
	return nil
}

// diffHint 打印前几处行级差异：CI 里只看得到「stale」时，这几行足够判断是漏跑生成还是手改。
func diffHint(existing, rendered string) string {
	got := strings.Split(existing, "\n")
	want := strings.Split(rendered, "\n")
	var b strings.Builder
	b.WriteString("role template catalog differs from Identity/seeds.go:\n")
	shown := 0
	for i := 0; i < len(want) || i < len(got); i++ {
		var wantLine, gotLine string
		if i < len(want) {
			wantLine = want[i]
		}
		if i < len(got) {
			gotLine = got[i]
		}
		if wantLine == gotLine {
			continue
		}
		fmt.Fprintf(&b, "  line %d\n    file:     %s\n    rendered: %s\n", i+1, gotLine, wantLine)
		shown++
		if shown == 5 {
			b.WriteString("  ...\n")
			break
		}
	}
	return b.String()
}
