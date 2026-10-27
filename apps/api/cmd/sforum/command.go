package main

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type makeOptions struct {
	Kind             string
	ID               string
	Name             string
	Description      string
	URL              string
	AuthorName       string
	AuthorURL        string
	AuthorEmail      string
	Out              string
	Builtin          bool
	NoInteraction    bool
	Backend          bool
	PrebuiltSettings bool
	VueAdminPage     bool
	ProviderSlot     string
	// Complex 生成 multi-file manifest（includes + langs 目录 + settings 分片示例）。
	Complex bool
	// PublicPage 生成一个公开 Page Registry 页（add 路径 + L1 模板 + 精确声明）。
	PublicPage string
	// PublicPageLayout 是公开页的 layout：site（默认）| bare。
	PublicPageLayout string
	// SchemaPage 生成一个 view=schema 的公开页（有界描述符 + 包内契约），无 Vue/无 Go 模板。
	SchemaPage string
	// SchemaPageKind 是描述符种类：list（默认）| detail | form | notice。
	SchemaPageKind string
	// RouteMiddleware 生成 before/after 请求-响应补丁路由与示例处理器。
	RouteMiddleware bool
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sforum",
		Short:   "SForum developer console",
		Version: platformversion.Summary(),
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.AddCommand(
		newVersionCommand(),
		newMakeCommand("plugin"),
		newMakeCommand("theme"),
		newSeedCommand(),
		newSeedPerfCommand(),
		newAIEvalCommand(),
		newAIReplyCommand(),
		newAIDerivedCommand(),
		newUsersResetPasswordCommand(),
		newPrivacyEraseUserCommand(),
		newRevisionsCommand(),
		newExtensionCommand(),
		// 带外恢复也作为顶层 `sforum recovery` 暴露：事故现场最常敲的是这一条，
		// 与 `sforum extension recovery` 共享同一套子命令实现。
		newExtensionRecoveryCommand(),
		newDevCleanupOrphanPluginsCommand(),
	)
	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print SForum build information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Println(platformversion.Summary())
		},
	}
}

func newMakeCommand(kind string) *cobra.Command {
	opts := makeOptions{Kind: kind}
	commandName := "make:" + kind
	cmd := &cobra.Command{
		Use:   commandName,
		Short: fmt.Sprintf("Create an SForum %s scaffold", kind),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.NoInteraction {
				if err := promptMakeOptions(&opts); err != nil {
					return err
				}
			}
			target, err := GenerateExtensionScaffold(opts)
			if err != nil {
				return err
			}
			cmd.Printf("Created %s scaffold at %s\n", kind, target)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.ID, "id", "", "Extension id, such as acme.demo")
	cmd.Flags().StringVar(&opts.Name, "name", "", "Display name")
	cmd.Flags().StringVar(&opts.Description, "description", "", "Short description")
	cmd.Flags().StringVar(&opts.URL, "url", "", "Official website URL")
	cmd.Flags().StringVar(&opts.AuthorName, "author-name", "", "Author name")
	cmd.Flags().StringVar(&opts.AuthorURL, "author-url", "", "Author website URL")
	cmd.Flags().StringVar(&opts.AuthorEmail, "author-email", "", "Author email")
	cmd.Flags().StringVar(&opts.Out, "out", "", "Output directory for this package")
	cmd.Flags().BoolVar(&opts.Builtin, "builtin", false, "Create under extensions/builtin instead of extensions/dev")
	cmd.Flags().BoolVar(&opts.NoInteraction, "no-interaction", false, "Disable interactive prompts")
	cmd.Flags().StringVar(&opts.PublicPage, "public-page", "", "Add a public Page Registry page with this path slot, such as docs (path /docs)")
	cmd.Flags().StringVar(&opts.PublicPageLayout, "public-page-layout", "site", "Layout for --public-page: site (default) or bare")
	cmd.Flags().StringVar(&opts.SchemaPage, "schema-page", "", "Add a declarative view=schema public page with this path slot, such as orders (path /orders)")
	cmd.Flags().StringVar(&opts.SchemaPageKind, "schema-page-kind", "list", "Descriptor kind for --schema-page: list (default) | detail | form | notice")
	if kind == "plugin" {
		cmd.Flags().BoolVar(&opts.RouteMiddleware, "route-middleware", false, "Add a before request patch and an after response patch route (requires --backend)")
		cmd.Flags().BoolVar(&opts.Backend, "backend", false, "Include a backend plugin stub")
		cmd.Flags().BoolVar(&opts.Complex, "complex", false, "Scaffold multi-file manifest (includes + langs + settings shards)")
		cmd.Flags().BoolVar(&opts.PrebuiltSettings, "prebuilt-settings", false, "Include an author-prebuilt Admin Micro-frontend API v1 settings component with Schema fallback")
		cmd.Flags().BoolVar(&opts.VueAdminPage, "vue-admin-page", false, "Include a Vue admin page built with the Plugin UI SDK")
		cmd.Flags().StringVar(&opts.ProviderSlot, "provider-slot", "", "Declare a provider slot and host-rendered provider_probe settings action (requires --backend)")
	} else {
		cmd.Flags().BoolVar(&opts.PrebuiltSettings, "prebuilt-settings", false, "Include an author-prebuilt Admin Micro-frontend API v1 settings component with Schema fallback")
	}
	return cmd
}

func promptMakeOptions(opts *makeOptions) error {
	if opts == nil {
		return errors.New("missing make options")
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Extension ID").Description("Use a stable id such as acme.demo.").Value(&opts.ID),
			huh.NewInput().Title("Name").Value(&opts.Name),
			huh.NewInput().Title("Description").Value(&opts.Description),
			huh.NewInput().Title("Official URL").Value(&opts.URL),
			huh.NewInput().Title("Author name").Value(&opts.AuthorName),
			huh.NewInput().Title("Author URL").Value(&opts.AuthorURL),
			huh.NewInput().Title("Author email").Value(&opts.AuthorEmail),
		),
	)
	if opts.Kind == "plugin" {
		form = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("Extension ID").Description("Use a stable id such as acme.demo.").Value(&opts.ID),
				huh.NewInput().Title("Name").Value(&opts.Name),
				huh.NewInput().Title("Description").Value(&opts.Description),
				huh.NewInput().Title("Official URL").Value(&opts.URL),
				huh.NewInput().Title("Author name").Value(&opts.AuthorName),
				huh.NewInput().Title("Author URL").Value(&opts.AuthorURL),
				huh.NewInput().Title("Author email").Value(&opts.AuthorEmail),
				huh.NewConfirm().Title("Include backend stub?").Value(&opts.Backend),
				huh.NewConfirm().Title("Multi-file complex scaffold?").Description("Uses includes, per-locale langs, and settings shards.").Value(&opts.Complex),
				huh.NewConfirm().Title("Prebuilt settings component?").Description("Author-built .mjs with Schema fallback; operators do not rebuild SForum.").Value(&opts.PrebuiltSettings),
				huh.NewConfirm().Title("Vue admin page?").Description("Builds a first-class page with the Plugin UI SDK inside the Host admin shell.").Value(&opts.VueAdminPage),
				huh.NewInput().Title("Provider slot (optional)").Description("Adds a provider_probe action; requires backend stub.").Value(&opts.ProviderSlot),
				huh.NewInput().Title("Public page (optional)").Description("Adds a public Page Registry page at /<slug>; leave empty for none.").Value(&opts.PublicPage),
				huh.NewConfirm().Title("Route middleware?").Description("Adds a before request patch and an after response patch; requires backend stub.").Value(&opts.RouteMiddleware),
			),
		)
	} else {
		form = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("Extension ID").Description("Use a stable id such as acme.skin.").Value(&opts.ID),
				huh.NewInput().Title("Name").Value(&opts.Name),
				huh.NewInput().Title("Description").Value(&opts.Description),
				huh.NewInput().Title("Official URL").Value(&opts.URL),
				huh.NewInput().Title("Author name").Value(&opts.AuthorName),
				huh.NewInput().Title("Author URL").Value(&opts.AuthorURL),
				huh.NewInput().Title("Author email").Value(&opts.AuthorEmail),
				huh.NewConfirm().Title("Prebuilt settings component?").Description("Author-built .mjs with Schema fallback.").Value(&opts.PrebuiltSettings),
				huh.NewInput().Title("Public page (optional)").Description("Adds a public Page Registry page at /<slug>; leave empty for none.").Value(&opts.PublicPage),
				huh.NewSelect[string]().Title("Public page layout").Options(
					huh.NewOption("site (theme owns the shell)", "site"),
					huh.NewOption("bare (page owns its chrome)", "bare"),
				).Value(&opts.PublicPageLayout),
			),
		)
	}
	return form.Run()
}
