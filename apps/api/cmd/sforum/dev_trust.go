package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	devhygiene "github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 开发期信任会话 CLI：与 tests/admin-extension-activation.sh 同一套凭据约定。
//
//	SFORUM_API_BASE          默认 http://127.0.0.1:8080/api/v1
//	SFORUM_SESSION_COOKIE    super_admin 的 sforum_session 值
//	SFORUM_ADMIN_EMAIL/PASSWORD  未提供 cookie 时自动登录
const devTrustDefaultAPIBase = "http://127.0.0.1:8080/api/v1"

// devTrustSessionExistsCode 是服务端
// app/Models/Extensions.CodeDevTrustSessionExists 的字面值（HTTP 错误正文就是这段
// code，见 extensionscontroller.mapDevTrustError）；--ensure 的幂等判定用它。
// 客户端不反向 import Models 包，两处字面值需同步维护。
const devTrustSessionExistsCode = "extension.dev_trust_session_exists"

type devTrustClient struct {
	base      *url.URL
	http      *http.Client
	cookie    string
	csrfToken string
	// pat 非空时走 Authorization: Bearer sft_...（服务端 Bearer 中间件解析，
	// 同 cookie 会话同一 actor、CSRF 豁免）。优先级：显式 env > 登录 > 缓存。
	pat string
}

func newDevTrustClient() (*devTrustClient, error) {
	raw := strings.TrimSpace(os.Getenv("SFORUM_API_BASE"))
	if raw == "" {
		raw = devTrustDefaultAPIBase
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid SFORUM_API_BASE: %w", err)
	}
	jar, jarErr := cookiejar.New(nil)
	if jarErr != nil {
		return nil, fmt.Errorf("create cookie jar: %w", jarErr)
	}
	client := &devTrustClient{
		base:      base,
		http:      &http.Client{Timeout: 30 * time.Second, Jar: jar},
		cookie:    strings.TrimSpace(os.Getenv("SFORUM_SESSION_COOKIE")),
		csrfToken: strings.TrimSpace(os.Getenv("SFORUM_CSRF_TOKEN")),
	}
	if client.cookie == "" {
		email := strings.TrimSpace(os.Getenv("SFORUM_ADMIN_EMAIL"))
		password := os.Getenv("SFORUM_ADMIN_PASSWORD")
		if email == "" || password == "" {
			// dev 自动激活（host 内 watcher）落地后，本 CLI 是遗留辅助路径；
			// 日常 wrapper/agent 不再需要任何凭据。
			return nil, fmt.Errorf("dev trust requires SFORUM_SESSION_COOKIE or SFORUM_ADMIN_EMAIL+SFORUM_ADMIN_PASSWORD")
		}
		if err := client.login(email, password); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (c *devTrustClient) login(email, password string) error {
	// 预热：受管 GET 让中间件派发 csrf_ cookie；否则首个 POST /auth/login 的
	// 双提交校验拿不到头/cookie 对，直接 403（此前实测踩到）。
	c.warmCSRFHandshake()
	body, _ := json.Marshal(map[string]string{"login": email, "password": password})
	resp, err := c.request(http.MethodPost, "/auth/login", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		hint := ""
		if resp.StatusCode == http.StatusForbidden {
			hint = "（受管 CSRF 预热失败或 Origin/TrustedOrigins 不匹配；检查 SFORUM_API_BASE 与 APP_URL 同一来源后重试）"
		}
		return fmt.Errorf("login failed: %s%s %s", resp.Status, hint, strings.TrimSpace(string(payload)))
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "sforum_session" {
			c.cookie = cookie.Value
		}
		if strings.HasPrefix(cookie.Name, "csrf_") {
			c.csrfToken = cookie.Value
		}
	}
	if c.cookie == "" {
		return fmt.Errorf("login succeeded but no sforum_session cookie returned")
	}
	return nil
}

func (c *devTrustClient) request(method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base.String()+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", c.base.Scheme+"://"+c.base.Host)
	if c.cookie != "" && c.csrfToken != "" {
		// double-submit：头与 csrf_ cookie 值一致才被中间件接受。
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	// 显式带入的会话/CSRF 对进 jar（同名覆盖响应续发值）；其余依赖 jar 增量。
	c.seedCookieJar()
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	// 会话续期可能换发 cookie；从 jar 同步最新值。
	if respCookies := c.http.Jar.Cookies(c.base); respCookies != nil {
		for _, cookie := range respCookies {
			if cookie.Name == "sforum_session" {
				c.cookie = cookie.Value
			}
			if cookie.Name == "csrf_" {
				c.csrfToken = cookie.Value
			}
		}
	}
	return resp, nil
}

// seedCookieJar 把显式 cookie/csrf 值写入 jar（已有同名值覆盖），保证每个请求
// 都带全 double-submit 所需的头与 cookie 对。
func (c *devTrustClient) seedCookieJar() {
	if c.http.Jar == nil || (c.cookie == "" && c.csrfToken == "") {
		return
	}
	merged := c.http.Jar.Cookies(c.base)
	appendIf := func(name, value string) {
		if value == "" {
			return
		}
		for _, cookie := range merged {
			if cookie.Name == name {
				cookie.Value = value
				return
			}
		}
		merged = append(merged, &http.Cookie{Name: name, Value: value})
	}
	appendIf("sforum_session", c.cookie)
	appendIf("csrf_", c.csrfToken)
	c.http.Jar.SetCookies(c.base, merged)
}

func (c *devTrustClient) doJSON(method, path string, body []byte, out any) error {
	resp, err := c.request(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s -> %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// warmCSRFHandshake 请求一个受管 GET 路径，让 CSRF 中间件为空 jar 派发
// csrf_ cookie。失败 tolerated：有些装配可能关 CSRF（此时无需预热）。
func (c *devTrustClient) warmCSRFHandshake() {
	resp, err := c.request(http.MethodGet, "/api/v1/admin/extensions", nil)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// devTrustPackageRef 从包清单读取扩展 ID 与根路径（会话键）。
type devTrustPackageRef struct {
	ExtensionID string
	RootPath    string
	PackageSlug string
}

func readDevTrustPackageRef(root string) (devTrustPackageRef, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return devTrustPackageRef{}, err
	}
	manifestPath := filepath.Join(abs, extensionmanifest.ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return devTrustPackageRef{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return devTrustPackageRef{}, fmt.Errorf("parse manifest: %w", err)
	}
	if strings.TrimSpace(manifest.ID) == "" {
		return devTrustPackageRef{}, fmt.Errorf("manifest id is empty")
	}
	return devTrustPackageRef{
		ExtensionID: manifest.ID,
		RootPath:    abs,
		PackageSlug: manifest.ID + "@" + abs,
	}, nil
}

// devTrustRefresher 供 `sforum extension dev --auto-trust` 在每次构建成功后调用：
// 读取包清单得到 extensionId，POST /admin/extensions/:id/dev-trust-refresh。
type devTrustRefresher struct {
	client      *devTrustClient
	extensionID string
}

func newDevTrustRefresher(root string) (*devTrustRefresher, error) {
	ref, err := readDevTrustPackageRef(root)
	if err != nil {
		return nil, err
	}
	client, err := newDevTrustClient()
	if err != nil {
		return nil, err
	}
	return &devTrustRefresher{client: client, extensionID: ref.ExtensionID}, nil
}

// Refresh 幂等：digest 已授权时服务端直接成功。
func (r *devTrustRefresher) Refresh(ctx context.Context, packageDigest string) error {
	body, _ := json.Marshal(map[string]string{"packageDigest": packageDigest})
	return r.client.doJSON(http.MethodPost,
		"/admin/extensions/"+url.PathEscape(r.extensionID)+"/dev-trust-refresh", body, nil)
}

// Upgrade 在信任授予后把 staged 候选升级为活动版本（走既有 lifecycle upgrade 路由）。
// 服务端校验 exact-artifact 信任授予；dev trust 会话刚写入的 grant 即其权威来源。
// 常见失败：扩展处于 disabled（需先在管理端启用一次）、dev 会话过期/被吊销
// （重新 `sforum extension dev-trust create`）。
func (r *devTrustRefresher) Upgrade(ctx context.Context, packageDigest string) error {
	return r.client.doJSON(http.MethodPost,
		"/admin/extensions/"+url.PathEscape(r.extensionID)+"/upgrade", []byte("{}"), nil)
}

func newExtensionDevTrustCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev-trust",
		Short: "Development trust session helpers (requires a running dev API)",
	}
	cmd.AddCommand(newExtensionDevTrustCreateCommand())
	cmd.AddCommand(newExtensionDevTrustListCommand())
	cmd.AddCommand(newExtensionDevTrustRevokeCommand())
	return cmd
}

func newExtensionDevTrustCreateCommand() *cobra.Command {
	var ensure bool
	cmd := &cobra.Command{
		Use:   "create [path]",
		Short: "Create a dev trust session for one extensions/dev package",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			if !devhygiene.ShouldEnableDevelopmentOrphanReaper(os.Getenv("APP_ENV")) {
				return fmt.Errorf("%w: APP_ENV must be development", errExtensionDevDisabled)
			}
			abs, err := resolveDevPackageRoot(root)
			if err != nil {
				return err
			}
			ref, err := readDevTrustPackageRef(abs)
			if err != nil {
				return err
			}
			client, err := newDevTrustClient()
			if err != nil {
				return err
			}
			body, _ := json.Marshal(map[string]string{
				"extensionId": ref.ExtensionID,
				// packageSlug 已废弃：会话键由服务端按扩展 ID 派生，仅为旧版 API 兼容保留。
				"packageSlug": ref.ExtensionID,
				"rootPath":    ref.RootPath,
				"confirm":     "true",
			})
			var created map[string]any
			err = client.doJSON(http.MethodPost, "/admin/extensions/dev-trust-sessions", body, &created)
			if err != nil && ensure && strings.Contains(err.Error(), devTrustSessionExistsCode) {
				// 幂等确认（--ensure）：已有活跃会话即视为成功——wrapper 脚本可以
				// 「先 create 再启动 watch」一条命令完成，语义仍由服务端裁决。
				fmt.Fprintf(cmd.OutOrStdout(), "active dev trust session already exists for %s\n", ref.ExtensionID)
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dev trust session created for %s\n", ref.ExtensionID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ensure, "ensure", false, "Treat an already-active session as success (idempotent; 409 dev_trust_session_exists is tolerated)")
	return cmd
}

func newExtensionDevTrustListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List dev trust sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDevTrustClient()
			if err != nil {
				return err
			}
			var sessions []map[string]any
			if err := client.doJSON(http.MethodGet, "/admin/extensions/dev-trust-sessions?includeRevoked=true", nil, &sessions); err != nil {
				return err
			}
			encoded, _ := json.MarshalIndent(sessions, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
			return nil
		},
	}
	return cmd
}

func newExtensionDevTrustRevokeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <sessionID>",
		Short: "Revoke a dev trust session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDevTrustClient()
			if err != nil {
				return err
			}
			if err := client.doJSON(http.MethodDelete, "/admin/extensions/dev-trust-sessions/"+url.PathEscape(args[0]), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked %s\n", args[0])
			return nil
		},
	}
	return cmd
}
