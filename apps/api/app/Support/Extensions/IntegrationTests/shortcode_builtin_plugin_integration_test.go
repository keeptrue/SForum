package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	businesscache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const shortcodeBuiltinExtensionID = "sforum-shortcodes"

// TestShortcodeBuiltinPluginRealSubprocessRenderAndSanitize proves M5 through
// the production Host chain: real Go plugin binary, real Manager + Protocol V2
// runtime, real HostAPI Query Registry outlet, real PostgreSQL frozen
// projections, and the Host Content Registry executor with strict schemas,
// sanitizer, and deadline. This is runtime evidence, not a stub callback.
func TestShortcodeBuiltinPluginRealSubprocessRenderAndSanitize(t *testing.T) {
	fixture := newShortcodeBuiltinRuntimeFixture(t)
	executor := fixture.executor(t)

	userResult := fixture.executeShortcode(t, executor, shortcodeBuiltinUserDoc(t, 1), "zh-CN")
	userHTML := fixture.finalHTML(userResult)
	userDOM := parseShortcodeHTMLFragment(t, userHTML)
	userAnchor := requireShortcodeElements(t, userDOM, "a", 1)[0]
	if shortcodeAttribute(userAnchor, "href") != "/u/author" || shortcodeText(userAnchor) != "Author" ||
		!strings.Contains(shortcodeText(userDOM[0]), "@author") || strings.Contains(userHTML, `href=\"`) {
		t.Fatalf("real user card DOM = %q", userHTML)
	}
	if fixture.lastRender.ContentID != "sforum-shortcodes.user" ||
		fixture.lastRender.ContractVersion != "sforum-shortcodes.user@1" {
		t.Fatalf("typed segment identity drifted: %#v", fixture.lastRender)
	}
	escapedUser := fixture.executeShortcode(t, executor, shortcodeBuiltinUserDoc(t, 5), "en-US")
	escapedUserDOM := parseShortcodeHTMLFragment(t, fixture.finalHTML(escapedUser))
	if href := shortcodeAttribute(requireShortcodeElements(t, escapedUserDOM, "a", 1)[0], "href"); href != "/u/path%2Fuser%3Fx=1%23fragment" {
		t.Fatalf("sanitized escaped profile href = %q", href)
	}

	categoryResult := fixture.executeShortcode(t, executor, shortcodeBuiltinCategoryDoc(t, 1), "en-US")
	categoryHTML := fixture.finalHTML(categoryResult)
	categoryDOM := parseShortcodeHTMLFragment(t, categoryHTML)
	categoryAnchor := requireShortcodeElements(t, categoryDOM, "a", 1)[0]
	categoryLabels := requireShortcodeElements(t, categoryDOM, "p", 1)
	if shortcodeAttribute(categoryAnchor, "href") != "/c/general" || shortcodeText(categoryAnchor) != "General" ||
		shortcodeText(categoryLabels[0]) != "Category" {
		t.Fatalf("real category card DOM = %q", categoryHTML)
	}
	escapedCategory := fixture.executeShortcode(t, executor, shortcodeBuiltinCategoryDoc(t, 3), "en-US")
	escapedCategoryDOM := parseShortcodeHTMLFragment(t, fixture.finalHTML(escapedCategory))
	if href := shortcodeAttribute(requireShortcodeElements(t, escapedCategoryDOM, "a", 1)[0], "href"); href != "/c/nested%2Fcategory%3Fx%23fragment" {
		t.Fatalf("sanitized escaped category href = %q", href)
	}

	linksResult := fixture.executeShortcode(t, executor, shortcodeBuiltinFriendLinksDoc(t), "zh-CN")
	linksHTML := fixture.finalHTML(linksResult)
	linksDOM := parseShortcodeHTMLFragment(t, linksHTML)
	linkItems := requireShortcodeElements(t, linksDOM, "li", 4)
	linkAnchors := requireShortcodeElements(t, linksDOM, "a", 2)
	// 站外链接经 Host 链接策略改写为 /outbound?u=<base64url> 并强制新标签页
	//（Support/ContentLinks）；插件渲染段与正文走同一条策略。
	if shortcodeAttribute(linkAnchors[0], "href") != contentlinks.InterstitialHref("https://first.example.com") ||
		shortcodeText(linkAnchors[0]) != "First" ||
		shortcodeAttribute(linkAnchors[1], "href") != contentlinks.InterstitialHref("https://later.example.com") ||
		shortcodeText(linkAnchors[1]) != "Later" ||
		shortcodeAttribute(linkAnchors[0], "target") != "_blank" ||
		shortcodeAttribute(linkAnchors[0], "rel") != "nofollow noreferrer noopener" ||
		!strings.Contains(shortcodeText(linkItems[2]), "See us") {
		t.Fatalf("real friend links DOM = %q", linksHTML)
	}
	if len(shortcodeElements(linkItems[0], "a")) != 0 || shortcodeText(linkItems[0]) != "Bad" ||
		len(shortcodeElements(linkItems[1], "a")) != 0 || shortcodeText(linkItems[1]) != "Credentials" ||
		strings.Contains(linksHTML, "Disabled") || strings.Contains(linksHTML, "javascript:") ||
		strings.Contains(linksHTML, "Blank") || strings.Contains(linksHTML, "user:secret") ||
		strings.Contains(linksHTML, "alert(1)") || strings.Contains(linksHTML, `href=\"`) {
		t.Fatalf("friend links leaked disabled or unsafe content: %q", linksHTML)
	}
	// 每个短代码调用 = 恰好一次 Host 查询（批量用户、批量分类、一条列表查询）。
	fixture.assertQueryCounts(t, map[string]int{
		"core.query.shortcode.public_users.batch":      2,
		"core.query.shortcode.public_categories.batch": 2,
		"core.query.shortcode.friend_links.list":       1,
	})
}

// TestShortcodeBuiltinPluginVisibilityAndXSSFallback covers the frozen Fallback
// Matrix through the same real chain: non-public target, XSS projection data,
// invalid strict input, and empty friend links.
func TestShortcodeBuiltinPluginVisibilityAndXSSFallback(t *testing.T) {
	fixture := newShortcodeBuiltinRuntimeFixture(t)
	executor := fixture.executor(t)

	// 不可见用户（id 999 无行）-> 通用不可用 label，且无任何目标元数据。
	missing := fixture.executeShortcode(t, executor, shortcodeBuiltinUserDoc(t, 999), "zh-CN")
	missingHTML := fixture.finalHTML(missing)
	if missingHTML != `<p>该内容暂不可用</p>` {
		t.Fatalf("non-public user fallback = %q", missingHTML)
	}
	if strings.Contains(missingHTML, "999") || strings.Contains(missingHTML, "/u/") {
		t.Fatalf("non-public fallback leaked target metadata: %q", missingHTML)
	}

	// XSS 投影数据：Host sanitizer 必须剥离脚本，标签渲染为纯文本。
	xss := fixture.executeShortcode(t, executor, shortcodeBuiltinUserDoc(t, 3), "en-US")
	xssHTML := fixture.finalHTML(xss)
	if strings.Contains(xssHTML, "<script") || strings.Contains(xssHTML, "<b>Unsafe</b>") {
		t.Fatalf("sanitizer retained raw XSS markup: %q", xssHTML)
	}
	if !strings.Contains(xssHTML, "&lt;script&gt;") {
		t.Fatalf("XSS display name must render as escaped text: %q", xssHTML)
	}

	// 非法 declaration 输入（type/版本漂移）必须失败关闭而不是泄漏输出。
	invalid := contentregistry.EditorDocument{
		SchemaVersion: contentregistry.EditorDocumentSchemaVersion,
		ContentID:     "sforum-shortcodes.user", ContractVersion: "sforum-shortcodes.user@1",
		Schema: "sforum-shortcodes.user.schema@1", StorageVersion: "1",
		Value: mustJSONRaw(t, map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.user", "contractVersion": "sforum-shortcodes.user@2",
			"arguments": map[string]any{"userId": 1},
		}}),
	}
	if _, err := executor.Execute(t.Context(), contentregistry.ExecutionRequest{
		TargetID: fixture.shortcodeTarget.ID, ContractVersion: fixture.shortcodeTarget.ContractVersion,
		Document: invalid,
		Permission: contentregistry.PermissionInput{
			ActorFingerprint: "anonymous", PolicyFingerprint: "content.shortcode.public@1",
			Recheck: shortcodeBuiltinPermissionRecheck{},
		},
		ResourceID: "topic:42", Locale: "zh-CN", Scope: "public",
	}); err == nil {
		t.Fatalf("invalid declaration input must fail closed")
	}

	// 无 friend-links 数据时不得产生任何公开内容或占位标签。
	if _, err := fixture.pool.Exec(t.Context(), "DELETE FROM site_friend_links"); err != nil {
		t.Fatal(err)
	}
	emptyLinks := fixture.executeShortcode(t, executor, shortcodeBuiltinFriendLinksDoc(t), "zh-CN")
	if len(emptyLinks.Render.Segments) != 0 || emptyLinks.Render.PlainText != "" {
		t.Fatalf("empty friend links produced public output: %#v", emptyLinks.Render)
	}
	if nodes := parseShortcodeHTMLFragment(t, fixture.finalHTML(emptyLinks)); len(nodes) != 0 {
		t.Fatalf("empty friend links produced DOM nodes: %#v", nodes)
	}
	fixture.assertQueryCounts(t, map[string]int{
		"core.query.shortcode.public_users.batch": 2,
		"core.query.shortcode.friend_links.list":  1,
	})
}

func TestShortcodeBuiltinTopicCommentAndProductionDispatcher(t *testing.T) {
	fixture := newShortcodeBuiltinRuntimeFixture(t)
	executor := fixture.executor(t)

	topic := fixture.executeShortcode(t, executor, shortcodeBuiltinTopicDoc(t, 10), "en-US")
	topicHTML := fixture.finalHTML(topic)
	topicAnchor := requireShortcodeElements(t, parseShortcodeHTMLFragment(t, topicHTML), "a", 1)[0]
	if shortcodeAttribute(topicAnchor, "href") != "/t/10/public" || shortcodeText(topicAnchor) != "Public" ||
		!strings.Contains(topicHTML, "public excerpt 1") {
		t.Fatalf("real topic card = %q", topicHTML)
	}
	comment := fixture.executeShortcode(t, executor, shortcodeBuiltinCommentDoc(t, 100), "en-US")
	commentHTML := fixture.finalHTML(comment)
	commentAnchor := requireShortcodeElements(t, parseShortcodeHTMLFragment(t, commentHTML), "a", 1)[0]
	if shortcodeAttribute(commentAnchor, "href") != "/t/10/public#comment-100" ||
		!strings.Contains(commentHTML, "public excerpt 10") {
		t.Fatalf("real comment card = %q", commentHTML)
	}

	for _, topicID := range []int64{11, 12, 13, 14, 16, 17} {
		html := fixture.finalHTML(fixture.executeShortcode(t, executor, shortcodeBuiltinTopicDoc(t, topicID), "en-US"))
		if html != `<p>This topic does not exist or is not public</p>` || strings.Contains(html, fmt.Sprint(topicID)) || strings.Contains(html, "/t/") {
			t.Fatalf("topic %d visibility fallback=%q", topicID, html)
		}
	}
	for _, commentID := range []int64{101, 102, 103, 104, 105, 107, 108} {
		html := fixture.finalHTML(fixture.executeShortcode(t, executor, shortcodeBuiltinCommentDoc(t, commentID), "en-US"))
		if html != `<p>This comment does not exist or is not public</p>` || strings.Contains(html, fmt.Sprint(commentID)) || strings.Contains(html, "/t/") {
			t.Fatalf("comment %d visibility fallback=%q", commentID, html)
		}
	}

	dispatcher, err := contentregistry.NewForumShortcodeDispatcher(contentregistry.ForumShortcodeDispatcherConfig{
		Registry: fixture.registry, Admission: hostapi.NewContentRegistryAdmission(fixture.runtime),
		Providers: fixture.runtime, Cache: businesscache.NewMemoryCache(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	topicDoc := shortcodeBuiltinTopicDoc(t, 10)
	queryCountBefore := len(fixture.queryTrace.events)
	results, err := dispatcher.RenderDocuments(t.Context(), []contentregistry.ForumShortcodeRenderDocument{{
		Resource:     contentregistry.ShortcodeResourceKey{Type: "topic", ID: 20},
		TemplateHTML: "{{one}}{{two}}{{three}}",
		Nodes: []contentregistry.ForumShortcodeRenderNode{
			{Placeholder: "{{one}}", ID: topicDoc.ContentID, ContractVersion: topicDoc.ContractVersion, Value: topicDoc.Value, Depth: 1},
			{Placeholder: "{{two}}", ID: topicDoc.ContentID, ContractVersion: topicDoc.ContractVersion, Value: topicDoc.Value, Depth: 1},
			{Placeholder: "{{three}}", ID: topicDoc.ContentID, ContractVersion: topicDoc.ContractVersion, Value: topicDoc.Value, Depth: 1},
		},
	}}, "en-US")
	if err != nil || len(results) != 1 || strings.Count(results[0].HTML, "/t/10/public") != 3 {
		t.Fatalf("production dispatcher result=%#v err=%v", results, err)
	}
	if delta := len(fixture.queryTrace.events) - queryCountBefore; delta != 1 {
		t.Fatalf("three repeated topic IDs executed %d Host queries, want 1", delta)
	}

	queryCountBefore = len(fixture.queryTrace.events)
	self, err := dispatcher.RenderDocuments(t.Context(), []contentregistry.ForumShortcodeRenderDocument{{
		Resource:     contentregistry.ShortcodeResourceKey{Type: "topic", ID: 10},
		TemplateHTML: "{{self}}", Nodes: []contentregistry.ForumShortcodeRenderNode{{
			Placeholder: "{{self}}", ID: topicDoc.ContentID, ContractVersion: topicDoc.ContractVersion,
			Value: topicDoc.Value, Depth: 1,
		}},
	}}, "en-US")
	if err != nil || len(self) != 1 || !strings.Contains(self[0].HTML, "Topic reference unavailable") ||
		len(fixture.queryTrace.events) != queryCountBefore {
		t.Fatalf("self-reference reached plugin: result=%#v err=%v", self, err)
	}
}

func TestShortcodeBuiltinProtectedProductionDispatcher(t *testing.T) {
	fixture := newShortcodeBuiltinRuntimeFixture(t)
	trace := contentregistry.NewContentTraceRing(16)
	dispatcher, err := contentregistry.NewForumShortcodeDispatcher(contentregistry.ForumShortcodeDispatcherConfig{
		Registry: fixture.registry, Admission: hostapi.NewContentRegistryAdmission(fixture.runtime),
		Providers: fixture.runtime, Cache: businesscache.NewMemoryCache(), Trace: trace,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	fragment := mustJSONRaw(t, map[string]any{
		"type": "doc", "content": []any{map[string]any{
			"type": "paragraph", "content": []any{map[string]any{
				"type": "text", "text": "PROTECTED_RUNTIME_MARKER",
			}},
		}},
	})
	value := mustJSONRaw(t, map[string]any{"decision": "allowed", "fragment": json.RawMessage(fragment)})
	contribution, err := fixture.registry.Resolve(contentregistry.ShortcodeLoginID)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := fixture.runtime.ResolveContentProviders(contribution)
	if err != nil {
		t.Fatal(err)
	}
	direct, directErr := providers.Renderer.RenderContent(t.Context(), contentregistry.RendererProviderRequest{
		Target: contribution, Provider: contribution, Action: contentregistry.ActionAdd,
		Document: contentregistry.EditorDocument{
			SchemaVersion: contentregistry.EditorDocumentSchemaVersion,
			ContentID:     contribution.ID, ContractVersion: contribution.ContractVersion,
			Schema: contribution.Schema, StorageVersion: "1", Value: value,
		},
		ResourceID: "protected:authorized", Locale: "en-US", Scope: "protected",
		SuppressHostDelegations: true,
	})
	if directErr != nil {
		t.Fatalf("direct protected production provider result=%#v err=%+v", direct, directErr)
	}
	results, err := dispatcher.RenderProtectedFragments(t.Context(), []contentregistry.ForumProtectedShortcodeRenderRequest{{
		ID: contentregistry.ShortcodeLoginID, ContractVersion: contentregistry.ShortcodeLoginID + "@1",
		AcceptedFragment: fragment,
	}}, "en-US")
	if err != nil || len(results) != 1 || !results[0].Rendered || results[0].HTML != "<p>PROTECTED_RUNTIME_MARKER</p>" {
		t.Fatalf("protected production dispatcher result=%#v trace=%#v err=%v", results, trace.ContentTraces(16), err)
	}
}

func (f *shortcodeBuiltinFixture) assertQueryCounts(t *testing.T, expected map[string]int) {
	t.Helper()
	actual := map[string]int{}
	for _, event := range f.queryTrace.events {
		actual[event.QueryID]++
	}
	for queryID, count := range expected {
		if actual[queryID] != count {
			t.Fatalf("query %s executed %d times, want %d; total events=%#v", queryID, actual[queryID], count, actual)
		}
	}
}

type shortcodeBuiltinFixture struct {
	t               *testing.T
	extension       extensions.Extension
	manager         *extensionsruntime.Manager
	pool            *pgxpool.Pool
	gateway         *hostapi.Gateway
	registry        *contentregistry.Registry
	publication     contentregistry.Publication
	runtime         *extensionsruntime.ContentRegistryProtocolRuntime
	shortcodeTarget contentregistry.Contribution
	lastRender      contentregistry.RenderSegments
	trace           *contentregistry.ContentTraceRing
	queryTrace      *shortcodeBuiltinQueryTrace
}

func newShortcodeBuiltinRuntimeFixture(t *testing.T) *shortcodeBuiltinFixture {
	t.Helper()
	ctx, pool := newShortcodeBuiltinPostgresHarness(t)
	_ = ctx
	extension, _ := buildShortcodeBuiltinExtension(t)

	queryTrace := &shortcodeBuiltinQueryTrace{events: []queryregistry.ExecutionTrace{}}
	gateway := newShortcodeBuiltinGateway(t, pool, extension, queryTrace)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "builtin", ImpactDigest: extension.PackageDigest,
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start shortcode builtin runtime: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication := contentregistry.Publication{Artifact: contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	}}
	for _, declaration := range extension.Manifest.Content {
		publication.Content = append(publication.Content, contentregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Kind: declaration.Kind, Handler: declaration.Handler, Schema: declaration.Schema,
			Renderer: declaration.Renderer, Migration: declaration.Migration,
		})
	}
	registry := contentregistry.New()
	core, err := contentregistry.ForumPostBodyCorePublication()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReplaceAll([]contentregistry.Publication{core, publication}, false); err != nil {
		t.Fatal(err)
	}
	runtimeAdapter, err := extensionsruntime.NewContentRegistryProtocolRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	target, err := registry.Resolve("sforum-shortcodes.user")
	if err != nil {
		t.Fatal(err)
	}
	return &shortcodeBuiltinFixture{
		t: t, extension: extension, manager: manager, pool: pool, gateway: gateway,
		registry: registry, publication: publication, runtime: runtimeAdapter,
		shortcodeTarget: target, queryTrace: queryTrace,
	}
}

func (f *shortcodeBuiltinFixture) executor(t *testing.T) *contentregistry.Executor {
	t.Helper()
	bindings := make([]contentregistry.ExecutionBinding, 0, len(f.publication.Content))
	for _, declaration := range f.publication.Content {
		contribution, err := f.registry.Resolve(declaration.ID)
		if err != nil {
			t.Fatal(err)
		}
		providers, err := f.runtime.ResolveContentProviders(contribution)
		if err != nil || providers.Renderer == nil {
			t.Fatalf("resolve providers for %s: %v", declaration.ID, err)
		}
		bindings = append(bindings, contentregistry.ExecutionBinding{
			TargetID: contribution.ID, TargetContractVersion: contribution.ContractVersion,
			DeclarationID: contribution.ID, ContractVersion: contribution.ContractVersion,
			Artifact: contribution.Artifact, Action: contentregistry.ActionAdd, Priority: 0,
			Fallback: contentregistry.FallbackClosed,
			// 只注入 Renderer：短代码 dispatch 不经过 filter/editor/validator 传输。
			Providers: contentregistry.ProviderSet{Renderer: providers.Renderer},
		})
	}
	if f.trace == nil {
		f.trace = contentregistry.NewContentTraceRing(64)
	}
	executor, err := contentregistry.NewExecutor(
		f.registry, bindings,
		hostapi.NewContentRegistryAdmission(f.runtime),
		shortcodeBuiltinSchemaValidator(t, f.extension.PackagePath),
		contentregistry.ExecutionLimits{CallTimeout: 4 * time.Second},
		contentregistry.WithContentTraceSink(f.trace),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(executor.Close)
	return executor
}

func (f *shortcodeBuiltinFixture) executeShortcode(
	t *testing.T,
	executor *contentregistry.Executor,
	document contentregistry.EditorDocument,
	locale string,
) contentregistry.ExecutionResult {
	t.Helper()
	target, err := f.registry.Resolve(document.ContentID)
	if err != nil {
		t.Fatalf("resolve target %s: %v", document.ContentID, err)
	}
	result, err := executor.Execute(t.Context(), contentregistry.ExecutionRequest{
		TargetID: target.ID, ContractVersion: target.ContractVersion,
		Document: document,
		Permission: contentregistry.PermissionInput{
			ActorFingerprint: "anonymous", PolicyFingerprint: "content.shortcode.public@1",
			Recheck: shortcodeBuiltinPermissionRecheck{},
		},
		ResourceID: "topic:42", Locale: locale, Scope: "public",
	})
	if err != nil {
		if f.trace != nil {
			for _, record := range f.trace.ContentTracesForTarget(f.shortcodeTarget.ID, 32) {
				t.Logf("execution trace: %#v", record)
			}
		}
		t.Fatalf("shortcode execute: %+v", err)
	}
	f.lastRender = result.Render
	return result
}

func (f *shortcodeBuiltinFixture) finalHTML(result contentregistry.ExecutionResult) string {
	var builder strings.Builder
	for _, segment := range result.Render.Segments {
		builder.WriteString(segment.HTML)
	}
	return builder.String()
}

func parseShortcodeHTMLFragment(t *testing.T, value string) []*xhtml.Node {
	t.Helper()
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(value), contextNode)
	if err != nil {
		t.Fatalf("parse sanitized shortcode HTML: %v", err)
	}
	return nodes
}

func requireShortcodeElements(t *testing.T, roots []*xhtml.Node, tag string, count int) []*xhtml.Node {
	t.Helper()
	var found []*xhtml.Node
	for _, root := range roots {
		found = append(found, shortcodeElements(root, tag)...)
	}
	if len(found) != count {
		t.Fatalf("sanitized HTML has %d <%s> elements, want %d", len(found), tag, count)
	}
	return found
}

func shortcodeElements(root *xhtml.Node, tag string) []*xhtml.Node {
	var found []*xhtml.Node
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && node.Data == tag {
			found = append(found, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return found
}

func shortcodeAttribute(node *xhtml.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func shortcodeText(root *xhtml.Node) string {
	var builder strings.Builder
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			builder.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return builder.String()
}

// shortcodeBuiltinSchemaValidator compiles the strict declaration schemas that
// ship inside the exact plugin artifact and applies them to the call value.
func shortcodeBuiltinSchemaValidator(t *testing.T, packageRoot string) contentregistry.SchemaValidator {
	t.Helper()
	files := map[string]string{
		"sforum-shortcodes.user.schema@1":         "user.json",
		"sforum-shortcodes.topic.schema@1":        "topic.json",
		"sforum-shortcodes.comment.schema@1":      "comment.json",
		"sforum-shortcodes.category.schema@1":     "category.json",
		"sforum-shortcodes.friend-links.schema@1": "friend-links.json",
	}
	compiler := jsonschema.NewCompiler()
	for schemaRef, fileName := range files {
		body, err := os.ReadFile(filepath.Join(packageRoot, "schemas", fileName))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		resource := "file:///" + fileName
		if err := compiler.AddResource(resource, document); err != nil {
			t.Fatal(err)
		}
		_ = schemaRef
	}
	compiled := map[string]*jsonschema.Schema{}
	for schemaRef, fileName := range files {
		schema, err := compiler.Compile("file:///" + fileName)
		if err != nil {
			t.Fatal(err)
		}
		compiled[schemaRef] = schema
	}
	return contentregistry.SchemaValidatorFunc(func(ctx context.Context, request contentregistry.SchemaValidationRequest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		schema, ok := compiled[request.SchemaRef]
		if !ok || request.ContentID == "" || !strings.HasPrefix(request.ContentID, shortcodeBuiltinExtensionID+".") {
			return contentregistry.ErrSchemaRejected
		}
		if err := schema.Validate(request.Value); err != nil {
			return contentregistry.ErrSchemaRejected
		}
		return nil
	})
}

type shortcodeBuiltinPermissionRecheck struct{}

func (shortcodeBuiltinPermissionRecheck) AuthorizeContent(_ context.Context, claim contentregistry.PermissionClaim) error {
	if !contentregistry.IsExactPermissionClaim(claim) || claim.TargetID != claim.ContentID ||
		!strings.HasPrefix(claim.TargetID, shortcodeBuiltinExtensionID+".") ||
		claim.Artifact.Core || claim.TargetArtifact.ExtensionID != shortcodeBuiltinExtensionID ||
		(claim.Operation != contentregistry.OperationSource && claim.Operation != contentregistry.OperationRenderer &&
			claim.Operation != contentregistry.OperationRelease) ||
		claim.Scope != "public" || claim.ResourceID == "" {
		return contentregistry.ErrExecutionDenied
	}
	return nil
}

func shortcodeBuiltinUserDoc(t *testing.T, userID int64) contentregistry.EditorDocument {
	t.Helper()
	return shortcodeBuiltinDoc(t, "sforum-shortcodes.user", "sforum-shortcodes.user@1",
		"sforum-shortcodes.user.schema@1", map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.user", "contractVersion": "sforum-shortcodes.user@1",
			"arguments": map[string]any{"userId": userID},
		}})
}

func shortcodeBuiltinCategoryDoc(t *testing.T, categoryID int64) contentregistry.EditorDocument {
	t.Helper()
	return shortcodeBuiltinDoc(t, "sforum-shortcodes.category", "sforum-shortcodes.category@1",
		"sforum-shortcodes.category.schema@1", map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.category", "contractVersion": "sforum-shortcodes.category@1",
			"arguments": map[string]any{"categoryId": categoryID},
		}})
}

func shortcodeBuiltinTopicDoc(t *testing.T, topicID int64) contentregistry.EditorDocument {
	t.Helper()
	return shortcodeBuiltinDoc(t, "sforum-shortcodes.topic", "sforum-shortcodes.topic@1",
		"sforum-shortcodes.topic.schema@1", map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.topic", "contractVersion": "sforum-shortcodes.topic@1",
			"arguments": map[string]any{"topicId": topicID},
		}})
}

func shortcodeBuiltinCommentDoc(t *testing.T, commentID int64) contentregistry.EditorDocument {
	t.Helper()
	return shortcodeBuiltinDoc(t, "sforum-shortcodes.comment", "sforum-shortcodes.comment@1",
		"sforum-shortcodes.comment.schema@1", map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.comment", "contractVersion": "sforum-shortcodes.comment@1",
			"arguments": map[string]any{"commentId": commentID},
		}})
}

func shortcodeBuiltinFriendLinksDoc(t *testing.T) contentregistry.EditorDocument {
	t.Helper()
	return shortcodeBuiltinDoc(t, "sforum-shortcodes.friend-links", "sforum-shortcodes.friend-links@1",
		"sforum-shortcodes.friend-links.schema@1", map[string]any{"type": "sforumShortcodeRef", "attrs": map[string]any{
			"id": "sforum-shortcodes.friend-links", "contractVersion": "sforum-shortcodes.friend-links@1",
			"arguments": map[string]any{},
		}})
}

func shortcodeBuiltinDoc(
	t *testing.T, id, contractVersion, schema string, value map[string]any,
) contentregistry.EditorDocument {
	t.Helper()
	return contentregistry.EditorDocument{
		SchemaVersion: contentregistry.EditorDocumentSchemaVersion, ContentID: id,
		ContractVersion: contractVersion, Schema: schema, StorageVersion: "1",
		Value: mustJSONRaw(t, value),
	}
}

func mustJSONRaw(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func newShortcodeBuiltinPostgresHarness(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("shortcode_builtin_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	setupShortcodeBuiltinSchema(t, ctx, pool)
	return ctx, pool
}

// setupShortcodeBuiltinSchema mirrors the frozen M4 projection tables plus the
// narrow extension visibility rows the stable Postgres query authority checks.
func setupShortcodeBuiltinSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT, display_name TEXT, status TEXT)`,
		`CREATE TABLE category_groups (id BIGINT PRIMARY KEY, visibility TEXT)`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, group_id BIGINT, slug TEXT, name TEXT, description TEXT, icon TEXT, icon_color TEXT, visibility TEXT)`,
		`CREATE TABLE posts (id BIGINT PRIMARY KEY, plain_text TEXT)`,
		`CREATE TABLE topics (id BIGINT PRIMARY KEY, category_id BIGINT, content_id BIGINT, author_user_id BIGINT, title TEXT, slug TEXT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE comments (id BIGINT PRIMARY KEY, topic_id BIGINT, content_id BIGINT, author_user_id BIGINT, status TEXT, deleted_at TIMESTAMPTZ, created_at TIMESTAMPTZ)`,
		`CREATE TABLE site_friend_links (id BIGINT PRIMARY KEY, name TEXT, url TEXT, description TEXT, logo_url TEXT, position INTEGER, enabled BOOLEAN)`,
		`CREATE TABLE extensions (id TEXT PRIMARY KEY, type TEXT, status TEXT, source TEXT, is_system BOOLEAN, active_version_id BIGINT)`,
		`CREATE TABLE extension_versions (id BIGINT PRIMARY KEY, version TEXT, package_digest TEXT, manifest JSONB)`,
		`INSERT INTO users VALUES (1,'author','Author','active'),(2,'commenter','Commenter','active'),(3,'xss-user','<script>alert(1)</script><b>Unsafe</b>','active'),(4,'blocked','Blocked','banned'),(5,'path/user?x=1#fragment','Path User','active')`,
		`INSERT INTO category_groups VALUES (1,'public'),(2,'hidden')`,
		`INSERT INTO categories VALUES (1,1,'general','General','First category','','', 'public'),(2,1,'hidden','Hidden','','','', 'hidden'),(3,1,'nested/category?x#fragment','Nested','Escaped path','','','public'),(4,2,'hidden-group','Hidden Group','','','','public')`,
		`INSERT INTO posts SELECT value, 'public excerpt ' || value FROM generate_series(1,30) AS value`,
		`INSERT INTO topics VALUES
			(10,1,1,1,'Public','public','active',NULL),(11,1,2,1,'Hidden','hidden','hidden',NULL),
			(12,1,3,1,'Deleted','deleted','deleted',now()),(13,1,4,1,'Pending','pending','pending',NULL),
			(14,1,5,1,'Rejected','rejected','rejected',NULL),(15,1,6,1,'Locked','locked','locked',NULL),
			(16,2,7,1,'Hidden category','hidden-category','active',NULL),(17,4,8,1,'Hidden group','hidden-group','active',NULL)`,
		`INSERT INTO comments VALUES
			(100,10,10,2,'active',NULL,now()),(101,10,11,2,'hidden',NULL,now()),
			(102,10,12,2,'deleted',now(),now()),(103,10,13,2,'pending',NULL,now()),
			(104,10,14,2,'rejected',NULL,now()),(105,11,15,2,'active',NULL,now()),
			(106,15,16,2,'active',NULL,now()),(107,16,17,2,'active',NULL,now()),
			(108,17,18,2,'active',NULL,now()),(109,15,19,2,'active',NULL,now())`,
		`INSERT INTO site_friend_links VALUES (1,'Later','https://later.example.com','','',20,TRUE),(2,'First','https://first.example.com','See us','',10,TRUE),(3,'Disabled','https://disabled.example.com','','',0,FALSE),(4,'Bad','javascript:alert(1)','','',5,TRUE),(5,'Credentials','https://user:secret@example.com','','',6,TRUE),(6,'   ','https://blank.example.com','','',7,TRUE)`,
		`INSERT INTO extensions VALUES ('` + shortcodeBuiltinExtensionID + `','plugin','enabled','builtin',TRUE,1)`,
		`INSERT INTO extension_versions VALUES (1, '1.1.0', '` + strings.Repeat("0", 64) + `', '{"database":{"authority":""}}'::jsonb)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup shortcode builtin schema: %v\n%s", err, statement)
		}
	}
}

func newShortcodeBuiltinGateway(t *testing.T, pool *pgxpool.Pool, extension extensions.Extension, traceSink *shortcodeBuiltinQueryTrace) *hostapi.Gateway {
	t.Helper()
	registry, catalog, err := hostapi.NewQueryRegistryCoreRegistry(hostapi.QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stableRuntime, err := hostapi.NewPostgresProtocolV2QueryRuntime(
		pool, hostapi.NewPostgresProtocolV2QueryAuthorityResolver(pool),
	)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := hostapi.NewProtocolV2QueryRegistryProviderResolver(stableRuntime, catalog.Bindings())
	if err != nil {
		t.Fatal(err)
	}
	coreSchemas, err := queryregistry.NewJSONResultSchemaCatalog(catalog.Schemas())
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := queryregistry.NewCompositeResultSchemaValidator(registry, coreSchemas)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Trace: traceSink,
		Admission: queryregistry.ContextualExecutionAdmissionFunc(func(ctx context.Context, _ queryregistry.Artifact) (queryregistry.ExecutionAdmissionLease, error) {
			return queryregistry.ExecutionAdmissionLease{Context: ctx, Release: func() {}}, nil
		}),
		Schemas: schemas,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := hostapi.NewProtocolV2QueryRegistryService(
		registry, execution, shortcodeBuiltinActorAuthority{}, shortcodeBuiltinCallerAdmission{extension: extension},
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := hostapi.NewGateway(hostapi.New(hostapi.Config{
		Capabilities: protocolV2HostCapabilities{set: capabilities.NewSet([]string{capabilities.HostAPI})},
		Settings:     protocolV2HostSettings{}, Permissions: protocolV2HostPermissions{},
		Users: protocolV2HostUsers{}, Jobs: &protocolV2HostState{}, JobAdmission: protocolV2HostJobAdmission{},
		Auditor: &protocolV2HostState{},
	}))
	if err := gateway.BindProtocolV2QueryRegistryService(service); err != nil {
		t.Fatal(err)
	}
	tCleanup := func() { _ = gateway.Close() }
	_ = tCleanup
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	return gateway
}

type shortcodeBuiltinQueryTrace struct {
	events []queryregistry.ExecutionTrace
}

func (s *shortcodeBuiltinQueryTrace) AppendExecutionTrace(e queryregistry.ExecutionTrace) {
	s.events = append(s.events, e)
}

type shortcodeBuiltinCallerAdmission struct {
	extension extensions.Extension
}

func (a shortcodeBuiltinCallerAdmission) AuthorizeProtocolV2QueryCaller(_ context.Context, runtime *protocolwire.ExtensionIdentity) error {
	if runtime.GetExtensionId() != shortcodeBuiltinExtensionID ||
		runtime.GetExtensionVersion() != a.extension.Version ||
		runtime.GetArtifactDigest() != a.extension.PackageDigest ||
		runtime.GetTrustGrantId() != "builtin" || runtime.GetInstanceId() == "" {
		return fmt.Errorf("shortcode query caller is not the exact builtin runtime")
	}
	return nil
}

type shortcodeBuiltinActorAuthority struct{}

func (shortcodeBuiltinActorAuthority) ResolveProtocolV2QueryActor(_ context.Context, actorUserID int64) (hostapi.ProtocolV2QueryActorProjection, error) {
	if actorUserID < 0 {
		return hostapi.ProtocolV2QueryActorProjection{}, fmt.Errorf("invalid actor")
	}
	if actorUserID == 0 {
		return hostapi.ProtocolV2QueryActorProjection{
			ActorUserID: 0, Authenticated: false,
			ActorFingerprint: "anonymous", PolicyFingerprint: "public:v1",
		}, nil
	}
	return hostapi.ProtocolV2QueryActorProjection{
		ActorUserID: actorUserID, Authenticated: true,
		ActorFingerprint: "actor:" + fmt.Sprint(actorUserID), PolicyFingerprint: "session:v1",
	}, nil
}

func (shortcodeBuiltinActorAuthority) AuthorizeProtocolV2QueryActor(ctx context.Context, actorUserID int64, _ queryregistry.PermissionClaim) (hostapi.ProtocolV2QueryActorProjection, error) {
	return shortcodeBuiltinActorAuthority{}.ResolveProtocolV2QueryActor(ctx, actorUserID)
}

// buildShortcodeBuiltinExtension builds the real protected built-in backend and
// materializes exact manifest digests into a temp package copy.
func buildShortcodeBuiltinExtension(t *testing.T) (extensions.Extension, string) {
	t.Helper()
	sourceRoot := shortcodeBuiltinSourceRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum-shortcodes")
	if err := copyShortcodeBuiltinTree(sourceRoot, packageRoot, "plugin"); err != nil {
		t.Fatalf("copy shortcode builtin package: %v", err)
	}
	goModPath := filepath.Join(packageRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(sourceRoot, "../../../.."))
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repositoryRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", binaryPath, ".")
	build.Dir = filepath.Join(packageRoot, "backend")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shortcode builtin backend: %v\n%s", err, output)
	}
	if err := refreshShortcodeBuiltinManifestDigests(t, packageRoot, binaryPath); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact shortcode builtin package: %v", err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		PackagePath: packageRoot, PackageDigest: digest, Manifest: manifest, ActiveVersionID: 801,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}, packageRoot
}

func refreshShortcodeBuiltinManifestDigests(t *testing.T, packageRoot, binaryPath string) error {
	t.Helper()
	manifestPath := filepath.Join(packageRoot, extensionmanifest.ManifestFileName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		return err
	}
	digest := shortcodeBuiltinFileSHA256(t, binaryPath)
	if backend, ok := manifest["backend"].(map[string]any); ok {
		backend["digest"] = digest
	}
	if files, ok := manifest["packageFiles"].([]any); ok {
		for _, raw := range files {
			file, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := strings.TrimSpace(fmt.Sprint(file["path"]))
			if path == "" {
				continue
			}
			if path == "backend/plugin" {
				file["digest"] = digest
				continue
			}
			file["digest"] = shortcodeBuiltinFileSHA256(t, filepath.Join(packageRoot, filepath.FromSlash(path)))
		}
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(encoded, '\n'), 0o600)
}

func shortcodeBuiltinFileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func copyShortcodeBuiltinTree(source, target, skipName string) error {
	return fs.WalkDir(os.DirFS(source), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(".", path)
		destination := filepath.Join(target, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if relative == "." || entry.Name() == skipName {
			return nil
		}
		body, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, 0o600)
	})
}

func shortcodeBuiltinSourceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../..", "extensions", "builtin/plugins/sforum-shortcodes"))
	if _, err := os.Stat(filepath.Join(root, "sforum.extension.json")); err != nil {
		t.Fatalf("shortcode builtin source root %s is invalid: %v", root, err)
	}
	return root
}
