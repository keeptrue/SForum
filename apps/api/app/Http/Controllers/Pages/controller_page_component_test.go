package pagescontroller

import (
	"encoding/json"
	nethttp "net/http"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// M6（相邻任务书）阶段 3 的解析层证据：真实 fixture 包声明的 view=component 公开页
// 必须由解析响应把组件身份交给 Web 端，且不得被当成「无模板 ⇒ 运行时不可用」而回退；
// 同包的模板页必须保持原样，证明这条新路径没有改变既有行为。
func TestResolveAddedPathReturnsComponentForComponentViewPage(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 2)

	componentResp := performPages(t, app, nethttp.MethodGet,
		"/api/v1/pages/resolve-path?path=/page-component-reference/component", nil, cookie)
	if componentResp.StatusCode != nethttp.StatusOK {
		t.Fatalf("component page status = %d", componentResp.StatusCode)
	}
	var componentBody pagesEnvelope[struct {
		View         string `json:"view"`
		Component    string `json:"component"`
		Fallback     bool   `json:"fallback"`
		Reason       string `json:"reason"`
		TemplateHTML string `json:"templateHtml"`
		Provider     string `json:"provider"`
	}]
	if err := json.NewDecoder(componentResp.Body).Decode(&componentBody); err != nil {
		t.Fatal(err)
	}
	if componentBody.Data.View != string(pages.PageViewComponent) {
		t.Fatalf("view = %q; want %q", componentBody.Data.View, pages.PageViewComponent)
	}
	if componentBody.Data.Component != "sforum.page-component-reference.component.body" {
		t.Fatalf("component = %q", componentBody.Data.Component)
	}
	if componentBody.Data.Fallback {
		t.Fatalf("component page must not fall back (reason=%q)", componentBody.Data.Reason)
	}
	if componentBody.Data.TemplateHTML != "" {
		t.Fatalf("component page must not carry template html, got %q", componentBody.Data.TemplateHTML)
	}
	if componentBody.Data.Provider != "sforum.page-component-reference" {
		t.Fatalf("provider = %q", componentBody.Data.Provider)
	}

	templateResp := performPages(t, app, nethttp.MethodGet,
		"/api/v1/pages/resolve-path?path=/page-component-reference/template", nil, cookie)
	if templateResp.StatusCode != nethttp.StatusOK {
		t.Fatalf("template page status = %d", templateResp.StatusCode)
	}
	var templateBody pagesEnvelope[struct {
		View      string `json:"view"`
		Component string `json:"component"`
	}]
	if err := json.NewDecoder(templateResp.Body).Decode(&templateBody); err != nil {
		t.Fatal(err)
	}
	if templateBody.Data.View != string(pages.PageViewTemplate) || templateBody.Data.Component != "" {
		t.Fatalf("template page changed: view=%q component=%q", templateBody.Data.View, templateBody.Data.Component)
	}
}
