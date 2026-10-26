package http

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionRouteGuardAuthorizerMapsTypedGuardFailures(t *testing.T) {
	authorizer := NewProductionRouteGuardAuthorizer()
	tests := []struct {
		guard      string
		permission string
		request    routes.DispatchRequest
		want       error
	}{
		{guard: extensionmanifest.GuardCoreLogin, want: ErrRouteLoginRequired},
		{guard: extensionmanifest.GuardCoreGuest, request: productionGuardRequest(), want: ErrRouteGuestRequired},
		{guard: extensionmanifest.GuardCorePermission, permission: "topic.create", request: productionGuardRequest(), want: ErrRoutePermissionDenied},
		// R1：Host 拥有的 core.guard.raw_request 已不再可发布，故本行改为验证生产授权器
		// 对仍携带该守卫的旧制品/伪造执行步骤保持 fail closed（见 productionDirectGuardPlan）。
		{guard: extensionmanifest.GuardCoreRaw, request: productionGuardRequest(), want: ErrRouteGuardUnavailable},
	}
	for index, test := range tests {
		plan, step := productionDirectGuardPlan(t, test.guard, test.permission)
		if step.Guard != test.guard {
			t.Fatalf("case %d step guard = %q, want %q", index, step.Guard, test.guard)
		}
		request := test.request
		request.Method, request.Path = plan.Method(), plan.Path()
		err := authorizer.Authorize(context.Background(), plan, step, request)
		if !errors.Is(err, test.want) {
			t.Fatalf("case %d error = %v, want %v", index, err, test.want)
		}
	}
}

func TestProductionRouteGuardAuthorizerRejectsForgedDirectPublicStep(t *testing.T) {
	plan, step := productionDirectGuardPlan(t, extensionmanifest.GuardCorePublic, "")
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
	step.RouteID = "guard.production.forged"
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged public guard error = %v", err)
	}
}

func TestProductionRouteGuardAuthorizerRunsExplicitContextualAdapters(t *testing.T) {
	authorizer := NewProductionRouteGuardAuthorizer()
	tests := []struct {
		name        string
		descriptor  routes.CoreGuardDescriptor
		permissions []string
		want        error
	}{
		{
			name: "profile self", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.profile.self",
			},
		},
		{
			name: "profile upload denied", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.profile.self",
			}, want: ErrRoutePermissionDenied,
		},
		{
			name: "profile upload", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.profile.self",
			}, permissions: []string{identity.PermissionAttachmentUpload},
		},
		{
			// own 权限需要冻结路由上的 topicID 与权威所有权证明；无资源上下文时 fail-closed。
			name: "topic own edit without resource fails closed", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, Permissions: []string{identity.PermissionTopicEditOwn, identity.PermissionTopicEditAny},
				EvaluatorID: "core.guard.forum.topic_edit",
			}, permissions: []string{identity.PermissionTopicEditOwn}, want: ErrRouteGuardUnavailable,
		},
		{
			name: "topic global edit", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, Permissions: []string{identity.PermissionTopicEditOwn, identity.PermissionTopicEditAny},
				EvaluatorID: "core.guard.forum.topic_edit",
			}, permissions: []string{identity.PermissionTopicEditAny},
		},
		{
			name: "unknown evaluator", descriptor: routes.CoreGuardDescriptor{
				Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.unregistered",
			}, want: ErrRouteGuardUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routeID := "core.route.profile.my_profile"
			if strings.Contains(test.name, "upload") {
				routeID = "core.route.profile.upload_avatar"
			}
			if strings.Contains(test.name, "topic") {
				routeID = "core.route.forum.update_topic"
			}
			if strings.Contains(test.name, "unknown") {
				routeID = "core.route.test.unknown"
			}
			plan, step := productionInheritedGuardPlan(t, routeID, test.descriptor)
			request := productionGuardRequest(test.permissions...)
			request.Method, request.Path = plan.Method(), plan.Path()
			err := authorizer.Authorize(context.Background(), plan, step, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProductionCoreGuardEvaluatorRegistryIsExplicitAndVersioned(t *testing.T) {
	registrations := productionCoreGuardEvaluatorRegistrations()
	registry, err := routes.NewCoreGuardEvaluatorRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	bindings := registry.Bindings()
	if len(bindings) != len(registrations) || len(bindings) != 32 {
		t.Fatalf("bindings = %#v", bindings)
	}
	for _, binding := range bindings {
		if binding.ContractVersion != routes.CoreGuardEvaluatorContractV1 || !strings.HasPrefix(binding.EvaluatorID, "core.guard.") {
			t.Fatalf("binding = %#v", binding)
		}
	}
}

func TestProductionForumReadGuardPartitionsCatalogByProvablePolicy(t *testing.T) {
	type expectedRoute struct {
		method     string
		body       string
		query      string
		cookieOnly bool
		supported  bool
	}
	expected := map[string]expectedRoute{
		"core.route.forum.composer_toolbar": {method: "GET", supported: true},

		"core.route.forum.categories":                  {method: "GET"},
		"core.route.forum.category_groups":             {method: "GET"},
		"core.route.forum.replies":                     {method: "GET"},
		"core.route.forum.search":                      {method: "GET"},
		"core.route.forum.tags":                        {method: "GET"},
		"core.route.forum.topics":                      {method: "GET"},
		"core.route.forum.topic":                       {method: "GET"},
		"core.route.forum.comments":                    {method: "GET"},
		"core.route.forum.topic_contribution_timeline": {method: "GET"},
		"core.route.forum.topic_by_slug":               {method: "GET"},
		"core.route.forum.comment_page":                {method: "GET"},
		// AI 依据读取面：评论的附属事实，与回复列表同 guest-read 策略。
		"core.route.forum.list_comment_citations": {method: "GET"},
		// 评论区实时信号：修订端点与 SSE 都按评论列表的 guest-read 策略裁决。
		"core.route.forum.topic_comment_revision": {method: "GET"},
		"core.route.forum.topic_comment_stream":   {method: "GET"},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.forum.read" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("forum read contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected forum read guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		if !want.supported {
			request := productionGuardRequest("*")
			request.Method, request.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s runtime-policy error = %v", route.ID, err)
			}
			continue
		}

		allowed := productionGuardRequest(identity.PermissionPostCreate)
		allowed.Method, allowed.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
			t.Fatalf("%s authenticated error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}
		forgedActor := allowed
		forgedActor.ActorID = 0
		if err := authorizer.Authorize(context.Background(), plan, step, forgedActor); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s forged actor error = %v", route.ID, err)
		}

		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
		forgedRequest := allowed
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged request error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing forum read guard routes = %#v", expected)
	}
}

func TestProductionForumReadGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.forum.read",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.forum.read.foreign", descriptor)
	request := productionGuardRequest("*")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign forum read route error = %v", err)
	}
}

func TestProductionForumReadPolicyClosesDynamicCatalogRoutes(t *testing.T) {
	policy := &testForumReadPolicy{guestRead: "public", softDeleteVisibility: "author_and_staff", revision: 1, ok: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumRead: policy})
	covered := 0
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID != "core.guard.forum.read" || route.ID == "core.route.forum.composer_toolbar" {
			continue
		}
		covered++
		plan, step := productionCatalogInheritedGuardPlan(t, route)
		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); err != nil {
			t.Fatalf("%s public error = %v", route.ID, err)
		}

		policy.guestRead = "login_required"
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s login-required anonymous error = %v", route.ID, err)
		}
		authenticated := productionGuardRequest()
		authenticated.Method, authenticated.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, authenticated); err != nil {
			t.Fatalf("%s login-required actor error = %v", route.ID, err)
		}
		policy.guestRead = "public"
	}
	if covered != 14 {
		t.Fatalf("dynamic forum read routes = %d, want 14", covered)
	}
}

func TestProductionForumReadPolicyPreservesSoftDeleteViewerBoundary(t *testing.T) {
	policy := &testForumReadPolicy{guestRead: "public", revision: 1, ok: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumRead: policy})
	for _, routeID := range []string{"core.route.forum.comments", "core.route.forum.replies"} {
		var target routes.CoreRoute
		for _, route := range routes.CoreRouteCatalog() {
			if route.ID == routeID {
				target = route
				break
			}
		}
		plan, step := productionCatalogInheritedGuardPlan(t, target)
		for _, visibility := range []string{"hidden", "staff_only", "author_and_staff"} {
			policy.softDeleteVisibility = visibility
			anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
			if err := authorizer.Authorize(context.Background(), plan, step, anonymous); err != nil {
				t.Fatalf("%s visibility %s public admission error = %v", routeID, visibility, err)
			}

			// 权限位不能在没有 Host actor 的情况下伪造 viewer；具体墓碑范围仍由
			// Forum Service 使用同一 actor snapshot 按 author/staff 过滤。
			policy.guestRead = "login_required"
			forgedStaff := routes.DispatchRequest{
				Method: plan.Method(), Path: plan.Path(),
				Permissions: map[string]bool{identity.PermissionPostDeleteAny: true},
			}
			if err := authorizer.Authorize(context.Background(), plan, step, forgedStaff); !errors.Is(err, ErrRouteLoginRequired) {
				t.Fatalf("%s visibility %s forged staff error = %v", routeID, visibility, err)
			}
			policy.guestRead = "public"
		}
	}
}

func TestProductionForumReadPolicyFailsClosedWhenUnavailableOrDrifted(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.forum.topics" {
			target = route
			break
		}
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
	policy := &testForumReadPolicy{guestRead: "public", softDeleteVisibility: "hidden", revision: 1, ok: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumRead: policy})
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatalf("initial policy error = %v", err)
	}
	forgedRequest := request
	forgedRequest.Path += "/forged"
	if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged request error = %v", err)
	}
	forgedStep := step
	forgedStep.RouteID += ".forged"
	if err := authorizer.Authorize(context.Background(), plan, forgedStep, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged step error = %v", err)
	}

	tests := []struct {
		name                 string
		guestRead            string
		softDeleteVisibility string
		revision             uint64
		ok                   bool
	}{
		{name: "unavailable", guestRead: "public", softDeleteVisibility: "hidden", revision: 2},
		{name: "zero revision", guestRead: "public", softDeleteVisibility: "hidden", ok: true},
		{name: "guest mode drift", guestRead: "members_only", softDeleteVisibility: "hidden", revision: 3, ok: true},
		{name: "soft delete drift", guestRead: "public", softDeleteVisibility: "everyone", revision: 4, ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy.guestRead = test.guestRead
			policy.softDeleteVisibility = test.softDeleteVisibility
			policy.revision = test.revision
			policy.ok = test.ok
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type testForumReadPolicy struct {
	guestRead            string
	softDeleteVisibility string
	revision             uint64
	ok                   bool
}

func (p *testForumReadPolicy) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	if p == nil {
		return "", "", 0, false
	}
	return p.guestRead, p.softDeleteVisibility, p.revision, p.ok
}

func TestProductionIdentitySelfCredentialsGuardPartitionsCatalogByProvablePolicy(t *testing.T) {
	type expectedRoute struct {
		method     string
		body       string
		query      string
		cookieOnly bool
		supported  bool
	}
	expected := map[string]expectedRoute{
		"core.route.identity.list_sessions":         {method: "GET", supported: true},
		"core.route.identity.revoke_other_sessions": {method: "POST", supported: true},
		// 外部身份列表：当前 Actor 的 redacted 绑定，允许 scope 收窄的 PAT。
		"core.route.identity.external_identities": {method: "GET", supported: true},
		// 自助设密：仅 cookie 会话；完整密码策略与 recent-auth 由 handler/service 门控。
		"core.route.identity.setup_password": {
			method: "POST", body: `{"password":"NewPassword1!"}`, cookieOnly: true, supported: true,
		},

		"core.route.identity.revoke_session":  {method: "DELETE"},
		"core.route.identity.list_apitokens":  {method: "GET", query: "includeRevoked=false", cookieOnly: true, supported: true},
		"core.route.identity.create_apitoken": {method: "POST", body: `{"name":"automation","scopes":["post.create"]}`, cookieOnly: true, supported: true},
		"core.route.identity.revoke_apitoken": {method: "DELETE"},
		"core.route.identity.rotate_apitoken": {method: "POST"},
		// 解绑依赖 link 所有权 / revision；无资源策略时 fail closed。
		"core.route.identity.external_identity_unlink": {method: "DELETE"},
		// 偏好更新依赖请求体校验；continuation link 还依赖 ticket、浏览器绑定和 recent-auth。
		// 当前 guard 输入不能完整复现 handler/service 策略，因此显式 fail closed。
		"core.route.identity.clear_current_user_appearance":   {method: "DELETE"},
		"core.route.identity.update_current_user_appearance":  {method: "PUT"},
		"core.route.identity.update_current_user_locale":      {method: "PUT"},
		"core.route.identity.link_external_auth_continuation": {method: "POST"},
		"core.route.site_chrome.account_navigation":           {method: "GET", supported: true},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.identity.self_credentials" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("identity self credentials catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected identity self credentials route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		if !want.supported {
			request := productionGuardRequest("*")
			request.Method, request.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s resource-dependent policy error = %v", route.ID, err)
			}
			continue
		}

		// 会话自服务允许 scope 收窄的 PAT；资源主体仍只能是 Host ActorID。
		allowed := productionGuardRequest(identity.PermissionPostCreate)
		allowed.Method, allowed.Path, allowed.Query, allowed.Body = plan.Method(), plan.Path(), want.query, []byte(want.body)
		if want.cookieOnly {
			allowed.CredentialSource = routes.DispatchCredentialCookie
		}
		if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
			t.Fatalf("%s authenticated PAT error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: want.query, Body: []byte(want.body)}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}
		forgedActor := allowed
		forgedActor.ActorID = 0
		if err := authorizer.Authorize(context.Background(), plan, step, forgedActor); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s forged actor error = %v", route.ID, err)
		}

		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
		forgedRequest := allowed
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged request error = %v", route.ID, err)
		}
		if want.cookieOnly {
			bearer := allowed
			bearer.CredentialSource = routes.DispatchCredentialBearer
			if err := authorizer.Authorize(context.Background(), plan, step, bearer); !errors.Is(err, ErrRoutePermissionDenied) {
				t.Fatalf("%s bearer credential error = %v", route.ID, err)
			}
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing identity self credentials routes = %#v", expected)
	}
}

func TestProductionIdentitySelfCredentialsGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.identity.self_credentials",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.identity.self_credentials.foreign", descriptor)
	request := productionGuardRequest("*")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign identity self credentials route error = %v", err)
	}
}

func TestProductionAPITokenSelfGuardRejectsCredentialAndPayloadDrift(t *testing.T) {
	targets := map[string]routes.CoreRoute{}
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.identity.list_apitokens" || route.ID == "core.route.identity.create_apitoken" {
			targets[route.ID] = route
		}
	}
	authorizer := NewProductionRouteGuardAuthorizer()
	listPlan, listStep := productionCatalogInheritedGuardPlan(t, targets["core.route.identity.list_apitokens"])
	for _, query := range []string{"includeRevoked=1", "includeRevoked=true&includeRevoked=false", "future=1", "%zz"} {
		request := productionGuardRequest()
		request.CredentialSource = routes.DispatchCredentialCookie
		request.Method, request.Path, request.Query = listPlan.Method(), listPlan.Path(), query
		if err := authorizer.Authorize(context.Background(), listPlan, listStep, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("query %q error = %v", query, err)
		}
	}

	createPlan, createStep := productionCatalogInheritedGuardPlan(t, targets["core.route.identity.create_apitoken"])
	for _, body := range []string{
		`{"name":"automation","scopes":[""]}`,
		`{"name":"automation","scopes":["post.create"],"future":true}`,
		`{"name":"automation","scopes":`,
	} {
		request := productionGuardRequest()
		request.CredentialSource = routes.DispatchCredentialCookie
		request.Method, request.Path, request.Body = createPlan.Method(), createPlan.Path(), []byte(body)
		if err := authorizer.Authorize(context.Background(), createPlan, createStep, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("body %s error = %v", body, err)
		}
	}
	request := productionGuardRequest()
	request.Method, request.Path = createPlan.Method(), createPlan.Path()
	request.Body = []byte(`{"name":"automation","scopes":["post.create"]}`)
	if err := authorizer.Authorize(context.Background(), createPlan, createStep, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("missing credential source error = %v", err)
	}
}

func TestProductionExtensionsMutationGuardPartitionsCatalogByProvablePolicy(t *testing.T) {
	type expectedRoute struct {
		method      string
		permissions []string
		body        string
		supported   bool
	}
	plugin := []string{identity.PermissionExtensionPluginManage, identity.PermissionExtensionManage}
	theme := []string{identity.PermissionExtensionThemeManage, identity.PermissionExtensionManage}
	superAdmin := []string{"*"}
	expected := map[string]expectedRoute{
		"core.route.extensions.activate":                         {method: "POST", supported: true, permissions: theme},
		"core.route.extensions.disable":                          {method: "POST", supported: true, permissions: plugin},
		"core.route.extensions.recover_lifecycle_operation":      {method: "POST", supported: true, permissions: plugin, body: `{"decision":"retry"}`},
		"core.route.extensions.rollback":                         {method: "POST", supported: true, permissions: plugin},
		"core.route.extensions.revoke_executable_trust":          {method: "DELETE", supported: true, permissions: superAdmin},
		"core.route.extensions.issue_executable_trust_challenge": {method: "POST", supported: true, permissions: superAdmin},
		// 开发期信任会话：创建/撤销/自动写入均为 super_admin only。
		"core.route.extensions.create_dev_trust_session":   {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.revoke_dev_trust_session":   {method: "DELETE", supported: true, permissions: superAdmin},
		"core.route.extensions.dev_trust_refresh":          {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.cleanup_missing_artifacts":  {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.finalize_uninstall_cleanup": {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.probe_provider_slot":        {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.select_provider_slot":       {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.reset_provider_slot":        {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.select_route_provider":      {method: "POST", supported: true, permissions: superAdmin},
		"core.route.extensions.reset_route_provider":       {method: "POST", supported: true, permissions: superAdmin},

		"core.route.extensions.install":                 {method: "POST"},
		"core.route.extensions.install_from_url":        {method: "POST"},
		"core.route.extensions.uninstall":               {method: "DELETE"},
		"core.route.extensions.enable":                  {method: "POST"},
		"core.route.extensions.restart":                 {method: "POST"},
		"core.route.extensions.apply_migrations":        {method: "POST"},
		"core.route.extensions.update_settings":         {method: "PUT"},
		"core.route.extensions.execute_settings_action": {method: "POST"},
		"core.route.extensions.reset_settings":          {method: "POST"},
		"core.route.extensions.upgrade":                 {method: "POST"},
		"core.route.extensions.verify":                  {method: "POST"},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.extensions.mutation" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("extensions mutation contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected extensions mutation guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		if !want.supported {
			request := productionGuardRequest("*")
			request.Method, request.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s artifact-dependent policy error = %v", route.ID, err)
			}
			continue
		}

		for _, permission := range want.permissions {
			allowed := productionGuardRequest(permission)
			allowed.Method, allowed.Path, allowed.Body = plan.Method(), plan.Path(), []byte(want.body)
			if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
				t.Fatalf("%s permission %s error = %v", route.ID, permission, err)
			}
		}

		denied := productionGuardRequest(identity.PermissionPostCreate)
		denied.Method, denied.Path, denied.Body = plan.Method(), plan.Path(), []byte(want.body)
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s permission denied error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Body: []byte(want.body)}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}

		allowed := productionGuardRequest(want.permissions[0])
		allowed.Method, allowed.Path, allowed.Body = plan.Method(), plan.Path(), []byte(want.body)
		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing extensions mutation guard routes = %#v", expected)
	}
}

func TestProductionExtensionsMutationGuardProtectsForcedRecovery(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.extensions.recover_lifecycle_operation" {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatal("lifecycle recovery route is missing")
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	authorizer := NewProductionRouteGuardAuthorizer()
	tests := []struct {
		name        string
		body        string
		permissions []string
		want        error
	}{
		{name: "ordinary recovery", body: `{"decision":"retry"}`, permissions: []string{identity.PermissionExtensionPluginManage}},
		{name: "forced denied", body: `{"decision":"retry","escalateForced":true}`, permissions: []string{identity.PermissionExtensionPluginManage}, want: ErrRoutePermissionDenied},
		{name: "forced super admin", body: `{"decision":"retry","escalateForced":true}`, permissions: []string{"*"}},
		{name: "unknown field closes", body: `{"decision":"retry","future":true}`, permissions: []string{identity.PermissionExtensionPluginManage}, want: ErrRouteGuardUnavailable},
		{name: "malformed closes", body: `{"decision":`, permissions: []string{identity.PermissionExtensionPluginManage}, want: ErrRouteGuardUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := productionGuardRequest(test.permissions...)
			request.Method, request.Path, request.Body = plan.Method(), plan.Path(), []byte(test.body)
			err := authorizer.Authorize(context.Background(), plan, step, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProductionExtensionsMutationGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.extensions.mutation",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.extensions.mutation.foreign", descriptor)
	request := productionGuardRequest("*")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign extensions mutation route error = %v", err)
	}
}

func TestProductionExtensionsReadGuardPartitionsCatalogByProvablePolicy(t *testing.T) {
	type expectedRoute struct {
		method      string
		permissions []string
		body        string
		supported   bool
	}
	viewer := []string{identity.PermissionExtensionView, identity.PermissionExtensionManage}
	migrations := []string{
		identity.PermissionExtensionView,
		identity.PermissionExtensionPluginManage,
		identity.PermissionExtensionManage,
	}
	trust := []string{
		identity.PermissionExtensionView,
		identity.PermissionExtensionPluginManage,
		identity.PermissionExtensionThemeManage,
		identity.PermissionExtensionManage,
	}
	expected := map[string]expectedRoute{
		"core.route.extensions.list":                    {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.events":                  {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.lifecycle_operations":    {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.lifecycle_operation":     {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.list_migrations":         {method: "GET", supported: true, permissions: migrations},
		"core.route.extensions.executable_trust_status": {method: "GET", supported: true, permissions: trust},
		"core.route.extensions.contribution_points":     {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.contributions":           {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.event_definitions":       {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.event_deliveries":        {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.navigation":              {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.inspect_provider_slots":  {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.provider_slot_events":    {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.inspect_asset":           {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.inspect_cache":           {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.inspect_route":           {method: "GET", supported: true, permissions: viewer},
		// 路由注册表目录：与其它检视器同一条已审核策略（extension.view 或 manage）。
		"core.route.extensions.route_catalog": {method: "GET", supported: true, permissions: viewer},
		// 密封 Core 查询目录：同一条已审核读取策略（extension.view 或 manage）。
		"core.route.extensions.query_catalog": {method: "GET", supported: true, permissions: viewer},
		// M5 隔离账本读取：与其它检视器同一条已审核策略（extension.view 或 manage）。
		"core.route.extensions.inspect_route_incidents":      {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.inspect_templates":            {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.component_inspector":          {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.navigation_inspector":         {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.openapi_aggregate":            {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.generated_client_metadata":    {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.route_provider_conflicts":     {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.route_provider_events":        {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.route_provider_selection":     {method: "GET", supported: true, permissions: viewer},
		"core.route.extensions.entity_import_export_dry_run": {method: "GET", supported: true, permissions: viewer},
		// 开发期信任会话列表：admin 扩展读取面。
		"core.route.extensions.list_dev_trust_sessions": {method: "GET", supported: true, permissions: viewer},
		// 版本 GC 候选清单：读守卫粗门 view/manage；服务层权威策略为 super_admin。
		"core.route.extensions.version_gc_candidates": {method: "GET", supported: true, permissions: viewer},

		"core.route.extensions.frontend_status":          {method: "GET"},
		"core.route.extensions.frontend_asset":           {method: "GET"},
		"core.route.extensions.frontend_component_asset": {method: "GET"},
		"core.route.extensions.page_bootstrap":           {method: "GET"},
		"core.route.extensions.settings":                 {method: "GET"},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.extensions.read" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("extensions read contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected extensions read guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		if !want.supported {
			request := productionGuardRequest("*")
			request.Method, request.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s target-dependent policy error = %v", route.ID, err)
			}
			continue
		}

		for _, permission := range want.permissions {
			allowed := productionGuardRequest(permission)
			allowed.Method, allowed.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
				t.Fatalf("%s permission %s error = %v", route.ID, permission, err)
			}
		}

		denied := productionGuardRequest(identity.PermissionPostCreate)
		denied.Method, denied.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s permission denied error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}

		allowed := productionGuardRequest(want.permissions[0])
		allowed.Method, allowed.Path = plan.Method(), plan.Path()
		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
		forgedRequest := allowed
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged request error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing extensions read guard routes = %#v", expected)
	}
}

func TestProductionExtensionsReadGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.extensions.read",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.extensions.read.foreign", descriptor)
	request := productionGuardRequest("*")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign extensions read route error = %v", err)
	}
}

func TestProductionExtensionPolicyClosesExactArtifactCatalogRoutes(t *testing.T) {
	entry := extensions.GuardPolicyEntry{
		ExtensionID: "guard.plugin", ExtensionType: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		Version: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		AdminFrontendDigest: strings.Repeat("b", 64), HasPrebuiltAdmin: true,
		FrontendArtifactTrusted: true, LifecycleV2: true,
		CurrentArtifactTrusted: true, ReviewArtifactTrusted: true,
		HasStagedArtifact: true, StagedVersion: "2.0.0",
		StagedPackageDigest: strings.Repeat("c", 64), StagedArtifactTrusted: true,
	}
	policy := &testExtensionGuardPolicy{lookup: extensions.GuardPolicyLookup{
		Revision: 1, TrustChallengesEnabled: true, Entry: entry, Found: true,
	}, ok: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Extensions: policy})
	expected := map[string]string{
		"core.route.extensions.install":                 identity.PermissionExtensionPluginManage,
		"core.route.extensions.uninstall":               identity.PermissionExtensionPluginManage,
		"core.route.extensions.enable":                  identity.PermissionExtensionPluginManage,
		"core.route.extensions.apply_migrations":        identity.PermissionExtensionPluginManage,
		"core.route.extensions.update_settings":         identity.PermissionExtensionPluginManage,
		"core.route.extensions.execute_settings_action": identity.PermissionExtensionPluginManage,
		"core.route.extensions.reset_settings":          identity.PermissionExtensionPluginManage,
		"core.route.extensions.upgrade":                 identity.PermissionExtensionPluginManage,
		"core.route.extensions.restart":                 identity.PermissionExtensionPluginManage,
		"core.route.extensions.verify":                  identity.PermissionExtensionPluginManage,
		"core.route.extensions.frontend_status":         identity.PermissionExtensionView,
		"core.route.extensions.frontend_asset":          identity.PermissionExtensionPluginManage,
		"core.route.extensions.settings":                identity.PermissionExtensionPluginManage,
	}
	covered := 0
	for _, route := range routes.CoreRouteCatalog() {
		permission, wanted := expected[route.ID]
		if !wanted {
			continue
		}
		covered++
		delete(expected, route.ID)
		plan, step := productionExtensionPolicyGuardPlan(t, route, entry)
		request := productionGuardRequest(permission)
		request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
			t.Fatalf("%s allowed error = %v", route.ID, err)
		}
		denied := productionGuardRequest(identity.PermissionPostCreate)
		denied.Method, denied.Path, denied.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s denied error = %v", route.ID, err)
		}
	}
	if covered != 13 || len(expected) != 0 {
		t.Fatalf("covered=%d missing=%#v", covered, expected)
	}
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID != "core.route.extensions.install" {
			continue
		}
		plan, step := productionExtensionPolicyGuardPlan(t, route, entry)
		themeManager := productionGuardRequest(identity.PermissionExtensionThemeManage)
		themeManager.Method, themeManager.Path, themeManager.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, themeManager); err != nil {
			t.Fatalf("theme manager inert upload guard error = %v", err)
		}
		return
	}
	t.Fatal("install route missing from Core catalog")
}

func TestProductionExtensionPolicyEnforcesTypeTrustSafeModeAndDrift(t *testing.T) {
	targets := map[string]routes.CoreRoute{}
	for _, route := range routes.CoreRouteCatalog() {
		targets[route.ID] = route
	}
	base := extensions.GuardPolicyEntry{
		ExtensionID: "guard.plugin", ExtensionType: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		Version: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		HasExecutableBackend: true, LifecycleV2: true,
		ReviewTrustRequired: true, HasStagedArtifact: true,
		StagedTrustRequired: true, StagedVersion: "2.0.0", StagedPackageDigest: strings.Repeat("c", 64),
	}
	policy := &testExtensionGuardPolicy{ok: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Extensions: policy})

	for _, routeID := range []string{"core.route.extensions.enable", "core.route.extensions.upgrade", "core.route.extensions.restart"} {
		plan, step := productionExtensionPolicyGuardPlan(t, targets[routeID], base)
		policy.lookup = extensions.GuardPolicyLookup{Revision: 1, TrustChallengesEnabled: true, Entry: base, Found: true}
		manager := productionGuardRequest(identity.PermissionExtensionPluginManage)
		manager.Method, manager.Path, manager.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, manager); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s untrusted manager error = %v", routeID, err)
		}
		superAdmin := productionGuardRequest("*")
		superAdmin.Method, superAdmin.Path, superAdmin.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, superAdmin); err != nil {
			t.Fatalf("%s super admin error = %v", routeID, err)
		}
	}

	theme := base
	theme.ExtensionType, theme.Source, theme.HasExecutableBackend = extensions.TypeTheme, extensions.SourceBuiltin, false
	theme.ReviewTrustRequired, theme.StagedTrustRequired = false, false
	theme.HasMailProvider = false
	policy.lookup = extensions.GuardPolicyLookup{Revision: 2, TrustChallengesEnabled: true, Entry: theme, Found: true}
	settingsPlan, settingsStep := productionExtensionPolicyGuardPlan(t, targets["core.route.extensions.settings"], theme)
	themeManager := productionGuardRequest(identity.PermissionExtensionThemeManage)
	themeManager.Method, themeManager.Path, themeManager.Params = settingsPlan.Method(), settingsPlan.Path(), settingsPlan.Params()
	if err := authorizer.Authorize(context.Background(), settingsPlan, settingsStep, themeManager); err != nil {
		t.Fatalf("theme settings error = %v", err)
	}

	policy.lookup.SafeMode = true
	enablePlan, enableStep := productionExtensionPolicyGuardPlan(t, targets["core.route.extensions.enable"], base)
	request := productionGuardRequest("*")
	request.Method, request.Path, request.Params = enablePlan.Method(), enablePlan.Path(), enablePlan.Params()
	if err := authorizer.Authorize(context.Background(), enablePlan, enableStep, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("safe mode error = %v", err)
	}

	policy.ok = false
	if err := authorizer.Authorize(context.Background(), settingsPlan, settingsStep, themeManager); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("stale policy error = %v", err)
	}
	policy.ok = true
	policy.lookup = extensions.GuardPolicyLookup{Revision: 3, Entry: theme, Found: true}
	policy.lookup.Entry.ExtensionID = "drifted.plugin"
	if err := authorizer.Authorize(context.Background(), settingsPlan, settingsStep, themeManager); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("identity drift error = %v", err)
	}
}

func TestProductionDeclaredExtensionRouteGuardEnforcesFrozenAccess(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.extensions.proxy_extension_route" {
			target = route
			break
		}
	}
	plan, step := productionWildcardInheritedGuardPlan(
		t, target, "POST", "/guard/extensions/:extensionId/*", "/guard/extensions/demo.plugin/reindex",
	)
	policy := &testDeclaredRoutePolicy{
		extensionID: "demo.plugin", method: "POST", routePath: "/reindex", ok: true,
		lookup: extensions.DeclaredRouteGuardLookup{
			Revision: 3, ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64),
		},
	}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{DeclaredRoutes: policy})
	tests := []struct {
		name       string
		access     string
		permission string
		request    routes.DispatchRequest
		want       error
	}{
		{name: "public", access: extensions.RouteAccessPublic},
		{name: "login anonymous", access: extensions.RouteAccessLogin, want: ErrRouteLoginRequired},
		{name: "login actor", access: extensions.RouteAccessLogin, request: productionGuardRequest()},
		{name: "permission anonymous", access: extensions.RouteAccessPermission, permission: identity.PermissionExtensionView, want: ErrRouteLoginRequired},
		{name: "permission denied", access: extensions.RouteAccessPermission, permission: identity.PermissionExtensionView, request: productionGuardRequest(), want: ErrRoutePermissionDenied},
		{name: "permission allowed", access: extensions.RouteAccessPermission, permission: identity.PermissionExtensionView, request: productionGuardRequest(identity.PermissionExtensionView)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy.lookup.Access, policy.lookup.Permission = test.access, test.permission
			request := test.request
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			err := authorizer.Authorize(context.Background(), plan, step, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if policy.calls != len(tests) {
		t.Fatalf("policy calls = %d", policy.calls)
	}
}

func TestProductionDeclaredExtensionRouteGuardRejectsStaleRawAndIdentityDrift(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.extensions.proxy_extension_route" {
			target = route
			break
		}
	}
	plan, step := productionWildcardInheritedGuardPlan(
		t, target, "GET", "/guard/extensions/:extensionId/*", "/guard/extensions/demo.plugin/public",
	)
	request := productionGuardRequest("*")
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	policy := &testDeclaredRoutePolicy{
		extensionID: "demo.plugin", method: "GET", routePath: "/public", ok: true,
		lookup: extensions.DeclaredRouteGuardLookup{
			Revision: 1, ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), Access: extensions.RouteAccessPublic,
		},
	}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{DeclaredRoutes: policy})
	policy.ok = false // raw/custom/inherit and stale snapshots are deliberately absent.
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("unpublished policy error = %v", err)
	}
	policy.ok = true
	policy.lookup.ExtensionID = "drifted.plugin"
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("identity drift error = %v", err)
	}
	policy.lookup.ExtensionID = "demo.plugin"
	policy.lookup.Revision = 0
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("revision drift error = %v", err)
	}
	policy.lookup.Revision = 1
	forged := request
	forged.Params = plan.Params()
	forged.Params["extensionId"] = "other.plugin"
	if err := authorizer.Authorize(context.Background(), plan, step, forged); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged resource error = %v", err)
	}
}

type testDeclaredRoutePolicy struct {
	extensionID string
	method      string
	routePath   string
	lookup      extensions.DeclaredRouteGuardLookup
	ok          bool
	calls       int
}

func (p *testDeclaredRoutePolicy) LookupDeclaredRoute(extensionID, method, routePath string) (extensions.DeclaredRouteGuardLookup, bool) {
	p.calls++
	if extensionID != p.extensionID || method != p.method || routePath != p.routePath {
		return extensions.DeclaredRouteGuardLookup{}, false
	}
	return p.lookup, p.ok
}

func TestProductionOptionsOwnerGuardClosesStaticPolicyCatalog(t *testing.T) {
	policy := &testOptionsOwnerPolicy{permissions: map[string]string{
		"site.name":                   identity.PermissionSettingsSiteManage,
		"forum.default_category_slug": identity.PermissionCategoryManage,
	}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Options: policy})
	targets := map[string]routes.CoreRoute{}
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.options.owner" {
			targets[route.ID] = route
		}
	}
	if len(targets) != 3 {
		t.Fatalf("options owner routes = %#v", targets)
	}

	listPlan, listStep := productionCatalogInheritedGuardPlan(t, targets["core.route.options.list_admin"])
	listRequest := productionGuardRequest(identity.PermissionCategoryManage)
	listRequest.Method, listRequest.Path = listPlan.Method(), listPlan.Path()
	if err := authorizer.Authorize(context.Background(), listPlan, listStep, listRequest); err != nil {
		t.Fatalf("list admin error = %v", err)
	}

	updatePlan, updateStep := productionCatalogInheritedGuardPlan(t, targets["core.route.options.update"])
	update := productionGuardRequest(identity.PermissionSettingsSiteManage)
	update.Method, update.Path = updatePlan.Method(), updatePlan.Path()
	update.Body = []byte(`{"name":"site.name","value":"Forum"}`)
	if err := authorizer.Authorize(context.Background(), updatePlan, updateStep, update); err != nil {
		t.Fatalf("single update error = %v", err)
	}

	batchPlan, batchStep := productionCatalogInheritedGuardPlan(t, targets["core.route.options.update_admin"])
	batchBody := []byte(`{"options":[{"name":"site.name","value":"Forum"},{"name":"forum.default_category_slug","value":"general"}]}`)
	batch := productionGuardRequest(identity.PermissionSettingsSiteManage, identity.PermissionCategoryManage)
	batch.Method, batch.Path, batch.Body = batchPlan.Method(), batchPlan.Path(), batchBody
	if err := authorizer.Authorize(context.Background(), batchPlan, batchStep, batch); err != nil {
		t.Fatalf("batch update error = %v", err)
	}
	onePermission := productionGuardRequest(identity.PermissionSettingsSiteManage)
	onePermission.Method, onePermission.Path, onePermission.Body = batchPlan.Method(), batchPlan.Path(), batchBody
	if err := authorizer.Authorize(context.Background(), batchPlan, batchStep, onePermission); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("partial batch permission error = %v", err)
	}

	for _, body := range []string{
		`{"name":"future.option","value":"x"}`,
		`{"name":"site.name","value":"x","future":true}`,
		`{"options":[]}`,
		`{"options":`,
	} {
		request := productionGuardRequest("*")
		plan, step := updatePlan, updateStep
		if strings.Contains(body, "options") {
			plan, step = batchPlan, batchStep
		}
		request.Method, request.Path, request.Body = plan.Method(), plan.Path(), []byte(body)
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("body %s error = %v", body, err)
		}
	}
	if policy.calls != 5 {
		// list + single + allowed batch + denied batch；非法 JSON/unknown option 仅 unknown option 进入 policy。
		// future/empty/malformed 在解码阶段关闭，因此总数额外包含 unknown option 一次。
		t.Fatalf("option policy calls = %d", policy.calls)
	}
}

func TestProductionPublicContextualGuardsCloseExactRoutes(t *testing.T) {
	tests := []struct {
		routeID string
		query   string
	}{
		{routeID: "core.route.identity.registration_status"},
		{routeID: "core.route.identity.human_verification_challenge", query: "purpose=register"},
		{routeID: "core.route.identity.human_verification_challenge", query: "purpose=email_verification"},
		{routeID: "core.route.pages.public_catalog"},
		{routeID: "core.route.seo.list"},
	}
	authorizer := NewProductionRouteGuardAuthorizer()
	for _, test := range tests {
		var target routes.CoreRoute
		for _, route := range routes.CoreRouteCatalog() {
			if route.ID == test.routeID {
				target = route
				break
			}
		}
		plan, step := productionCatalogInheritedGuardPlan(t, target)
		for _, request := range []routes.DispatchRequest{
			{Method: plan.Method(), Path: plan.Path(), Query: test.query},
			func() routes.DispatchRequest {
				r := productionGuardRequest(identity.PermissionPostCreate)
				r.Method, r.Path, r.Query = plan.Method(), plan.Path(), test.query
				return r
			}(),
		} {
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatalf("%s error = %v", test.routeID, err)
			}
		}
	}
}

func TestProductionHumanVerificationGuardRejectsPurposeDrift(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.identity.human_verification_challenge" {
			target = route
			break
		}
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	authorizer := NewProductionRouteGuardAuthorizer()
	for _, query := range []string{"", "purpose=future", "purpose=register&purpose=post_risk", "purpose=register&future=1", "%zz"} {
		request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: query}
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("query %q error = %v", query, err)
		}
	}
}

func TestProductionIdentityBootstrapGuardKeepsExecutableAuthFlowsHostOwned(t *testing.T) {
	authorizer := NewProductionRouteGuardAuthorizer()
	executable := map[string]bool{
		"core.route.identity.login":                           true,
		"core.route.identity.register":                        true,
		"core.route.identity.email_verification_request":      true,
		"core.route.identity.email_verification_confirm.get":  true,
		"core.route.identity.email_verification_confirm.post": true,
		"core.route.identity.password_reset_request":          true,
		"core.route.identity.password_reset_confirm":          true,
	}
	for _, route := range routes.CoreRouteCatalog() {
		if !executable[route.ID] {
			continue
		}
		delete(executable, route.ID)
		plan, step := productionCatalogInheritedGuardPlan(t, route)
		requests := []routes.DispatchRequest{
			{Method: plan.Method(), Path: plan.Path()},
			func() routes.DispatchRequest {
				request := productionGuardRequest()
				request.Method, request.Path = plan.Method(), plan.Path()
				return request
			}(),
			func() routes.DispatchRequest {
				request := productionGuardRequest("*")
				request.Method, request.Path = plan.Method(), plan.Path()
				request.Body, request.Query = []byte(`{"future":true}`), "future=1"
				return request
			}(),
		}
		for _, request := range requests {
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s error = %v", route.ID, err)
			}
		}
	}
	if len(executable) != 0 {
		t.Fatalf("missing executable bootstrap routes = %#v", executable)
	}
}

func TestProductionIdentityRegistrationStatusRemainsInertAndPublic(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.identity.registration_status" {
			target = route
			break
		}
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatal(err)
	}
}

func TestProductionThemeAssetGuardRequiresExactActiveArtifact(t *testing.T) {
	const extensionID = "sforum.theme"
	digest := strings.Repeat("d", 64)
	entry := extensions.GuardPolicyEntry{
		ExtensionID: extensionID, ExtensionType: extensions.TypeTheme,
		Status: extensions.StatusEnabled, Version: "1.0.0", PackageDigest: digest,
	}
	policy := &testExtensionGuardPolicy{lookup: extensions.GuardPolicyLookup{
		Revision: 7, Entry: entry, Found: true,
	}, ok: true}
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.pages.theme_asset" {
			target = route
			break
		}
	}
	plan, step := productionParameterizedInheritedGuardPlan(
		t, target, "/guard/theme/:extensionId/*", "/guard/theme/"+extensionID+"/styles/site.css",
	)
	request := routes.DispatchRequest{
		Method: plan.Method(), Path: plan.Path(), Params: plan.Params(), Query: "v=" + digest,
	}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Extensions: policy})
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatalf("exact theme asset error = %v params=%#v", err, plan.Params())
	}

	for name, mutate := range map[string]func(*routes.DispatchRequest){
		"digest drift":   func(r *routes.DispatchRequest) { r.Query = "v=" + strings.Repeat("e", 64) },
		"missing digest": func(r *routes.DispatchRequest) { r.Query = "" },
		"digest alias":   func(r *routes.DispatchRequest) { r.Query = "digest=" + digest },
		"asset type":     func(r *routes.DispatchRequest) { r.Params["path"] = "bin/plugin" },
		"path traversal": func(r *routes.DispatchRequest) { r.Params["path"] = "../site.css" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := request
			forged.Params = plan.Params()
			mutate(&forged)
			if err := authorizer.Authorize(context.Background(), plan, step, forged); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	policy.ok = false
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("stale policy error = %v", err)
	}
	policy.ok = true
	policy.lookup.Entry.ExtensionType = extensions.TypePlugin
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("type drift error = %v", err)
	}
}

func TestProductionPagesResolveGuardEnforcesImmutableAccessSnapshot(t *testing.T) {
	targets := map[string]routes.CoreRoute{}
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.pages.resolve" {
			targets[route.ID] = route
		}
	}
	if len(targets) != 2 {
		t.Fatalf("page resolve routes = %#v", targets)
	}
	policy := &testPageResolvePolicy{revision: 4, found: true}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Pages: policy})
	resolvePlan, resolveStep := productionCatalogInheritedGuardPlan(t, targets["core.route.pages.resolve"])

	tests := []struct {
		name    string
		access  pages.Access
		request routes.DispatchRequest
		want    error
	}{
		{name: "public", access: pages.AccessPublic},
		{name: "login anonymous", access: pages.AccessLogin, want: ErrRouteLoginRequired},
		{name: "login actor", access: pages.AccessLogin, request: productionGuardRequest()},
		{name: "guest anonymous", access: pages.AccessGuest},
		{name: "guest actor", access: pages.AccessGuest, request: productionGuardRequest(), want: ErrRouteGuestRequired},
		{name: "moderation denied", access: pages.AccessModeration, request: productionGuardRequest(), want: ErrRoutePermissionDenied},
		{name: "moderation allowed", access: pages.AccessModeration, request: productionGuardRequest(identity.PermissionModerationReview)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy.resolved = pages.ResolvedPage{Page: pages.PageDefinition{ID: "forum.home", Access: test.access}}
			request := test.request
			request.Method, request.Path, request.Query = resolvePlan.Method(), resolvePlan.Path(), "id=forum.home"
			err := authorizer.Authorize(context.Background(), resolvePlan, resolveStep, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	pathPlan, pathStep := productionCatalogInheritedGuardPlan(t, targets["core.route.pages.resolve_path"])
	policy.match = pages.RouteMatch{Contribution: pages.PageContribution{
		ID: "members", Access: pages.AccessPermission, Permission: identity.PermissionExtensionView,
	}}
	request := productionGuardRequest(identity.PermissionExtensionView)
	request.Method, request.Path, request.Query = pathPlan.Method(), pathPlan.Path(), "path=%2Fmembers"
	if err := authorizer.Authorize(context.Background(), pathPlan, pathStep, request); err != nil {
		t.Fatalf("resolve path error = %v", err)
	}
	request = productionGuardRequest(identity.PermissionPostCreate)
	request.Method, request.Path, request.Query = pathPlan.Method(), pathPlan.Path(), "path=%2Fmembers"
	if err := authorizer.Authorize(context.Background(), pathPlan, pathStep, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("resolve path denied error = %v", err)
	}
}

func TestProductionPagesResolveGuardRejectsQueryAndRevisionDrift(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.pages.resolve" {
			target = route
			break
		}
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	policy := &testPageResolvePolicy{
		revision: 2,
		resolved: pages.ResolvedPage{Page: pages.PageDefinition{ID: "forum.home", Access: pages.AccessPublic}},
		found:    true,
	}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Pages: policy})
	for _, query := range []string{"", "id=", "id=forum.home&id=forum.topic.show", "id=forum.home&future=1", "%zz"} {
		request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: query}
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("query %q error = %v", query, err)
		}
	}
	policy.drift = true
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: "id=forum.home"}
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("revision drift error = %v", err)
	}
	policy.drift = false
	policy.revision = 0
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("unpublished policy error = %v", err)
	}
}

func TestProductionPagesResolveGuardHotPathNeverReadsStore(t *testing.T) {
	store := &testPageGuardStore{}
	registry := pages.NewRegistry(store)
	if err := registry.RestoreBindings(context.Background()); err != nil {
		t.Fatal(err)
	}
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.pages.resolve" {
			target = route
			break
		}
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{Pages: registry})
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: "id=forum.home"}
	for range 100 {
		if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
			t.Fatal(err)
		}
	}
	if store.listCalls != 1 || store.otherCalls != 0 {
		t.Fatalf("page guard reached Store: list=%d other=%d", store.listCalls, store.otherCalls)
	}
}

func TestProductionEntityMetaPublicDefinitionsGuardValidatesEntityType(t *testing.T) {
	targets := map[string]routes.CoreRoute{}
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.entity_meta.read" {
			targets[route.ID] = route
		}
	}
	if len(targets) != 2 {
		t.Fatalf("entity meta read routes = %#v", targets)
	}
	authorizer := NewProductionRouteGuardAuthorizer()
	plan, step := productionCatalogInheritedGuardPlan(t, targets["core.route.entity_meta.list_public_definitions"])
	for _, entityType := range []string{"user", "topic"} {
		request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: "entityType=" + entityType}
		if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
			t.Fatalf("entity type %s error = %v", entityType, err)
		}
	}
	for _, query := range []string{"", "entityType=comment", "entityType=user&future=1", "entityType=user&entityType=topic"} {
		request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Query: query}
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("query %q error = %v", query, err)
		}
	}
	closedPlan, closedStep := productionCatalogInheritedGuardPlan(t, targets["core.route.entity_meta.list_values"])
	closed := productionGuardRequest("*")
	closed.Method, closed.Path = closedPlan.Method(), closedPlan.Path()
	if err := authorizer.Authorize(context.Background(), closedPlan, closedStep, closed); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("resource-dependent read error = %v", err)
	}
}

func TestProductionInboundWebhookGuardMatchesHostSkeletonBoundary(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.webhooks.inbound" {
			target = route
			break
		}
	}
	plan, step := productionParameterizedInheritedGuardPlan(
		t, target, "/guard/webhooks/inbound/:source", "/guard/webhooks/inbound/github",
	)
	authorizer := NewProductionRouteGuardAuthorizer()
	allowed := routes.DispatchRequest{
		Method: plan.Method(), Path: plan.Path(), Body: []byte(`{"event":"ping"}`),
		Params: map[string]string{"source": "github"},
	}
	if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
		t.Fatalf("anonymous inbound webhook error = %v", err)
	}
	for _, test := range []struct {
		name   string
		path   string
		body   []byte
		params map[string]string
	}{
		{name: "empty body", path: plan.Path(), params: map[string]string{"source": "github"}},
		{name: "blank source", path: plan.Path(), body: []byte(`{}`), params: map[string]string{"source": "   "}},
		{name: "oversized source", path: plan.Path(), body: []byte(`{}`), params: map[string]string{"source": strings.Repeat("x", 65)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := routes.DispatchRequest{Method: plan.Method(), Path: test.path, Body: test.body, Params: test.params}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	forgedPlan, forgedStep := productionInheritedGuardPlan(t, "core.route.webhooks.forged", routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.webhooks.inbound",
	})
	forged := routes.DispatchRequest{
		Method: forgedPlan.Method(), Path: forgedPlan.Path(), Body: []byte(`{}`),
		Params: map[string]string{"source": "github"},
	}
	if err := authorizer.Authorize(context.Background(), forgedPlan, forgedStep, forged); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged route error = %v", err)
	}
}

func TestProductionIdentityDeleteRoleGuardProtectsStaticRoles(t *testing.T) {
	var target, replaceTarget routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.identity.delete_role" {
			target = route
		}
		if route.ID == "core.route.identity.replace_role_permissions" {
			replaceTarget = route
		}
	}
	authorizer := NewProductionRouteGuardAuthorizer()
	for _, roleKey := range []string{
		identity.RoleMember, identity.RoleSuperAdmin, identity.RoleModerator,
		identity.RoleOperator, identity.RoleTechAdmin,
	} {
		plan, step := productionParameterizedInheritedGuardPlan(
			t, target, "/guard/roles/:roleKey", "/guard/roles/"+roleKey,
		)
		request := productionGuardRequest("*")
		request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("protected role %s error = %v", roleKey, err)
		}
	}
	for _, test := range []struct {
		roleKey string
		body    string
	}{
		{roleKey: identity.RoleSuperAdmin, body: `{"permissions":["post.create"]}`},
		{roleKey: "community_helper", body: `{"permissions":[""]}`},
		{roleKey: "community_helper", body: `{"permissions":["post.create"],"future":true}`},
		{roleKey: "community_helper", body: `{"permissions":`},
	} {
		plan, step := productionParameterizedInheritedGuardPlan(
			t, replaceTarget, "/guard/roles/:roleKey/permissions", "/guard/roles/"+test.roleKey+"/permissions",
		)
		request := productionGuardRequest("*")
		request.Method, request.Path, request.Params, request.Body = plan.Method(), plan.Path(), plan.Params(), []byte(test.body)
		if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("replace role %s body %s error = %v", test.roleKey, test.body, err)
		}
	}
}

type testPageResolvePolicy struct {
	revision uint64
	resolved pages.ResolvedPage
	match    pages.RouteMatch
	found    bool
	drift    bool
}

type testPageGuardStore struct {
	listCalls  int
	otherCalls int
}

func (s *testPageGuardStore) ListBindings(context.Context) ([]pages.ProviderBinding, error) {
	s.listCalls++
	return nil, nil
}

func (s *testPageGuardStore) GetBinding(context.Context, string) (pages.ProviderBinding, bool, error) {
	s.otherCalls++
	return pages.ProviderBinding{}, false, nil
}

func (s *testPageGuardStore) UpsertBinding(context.Context, pages.ProviderBinding) error {
	s.otherCalls++
	return nil
}

func (s *testPageGuardStore) DeleteBinding(context.Context, string) error {
	s.otherCalls++
	return nil
}

func (s *testPageGuardStore) ReplaceExtensionBindings(context.Context, []string, []pages.ProviderBinding) error {
	s.otherCalls++
	return nil
}

func (s *testPageGuardStore) ReconcileExtensionBindings(context.Context, string, []string, []pages.ProviderBinding) error {
	s.otherCalls++
	return nil
}

func (p *testPageResolvePolicy) Revision() uint64 { return p.revision }

func (p *testPageResolvePolicy) Resolve(context.Context, string) (pages.ResolvedPage, error) {
	if p.drift {
		p.revision++
	}
	return p.resolved, nil
}

func (p *testPageResolvePolicy) ResolveAddedPathMatch(string) (pages.RouteMatch, bool) {
	if p.drift {
		p.revision++
	}
	return p.match, p.found
}

type testOptionsOwnerPolicy struct {
	permissions map[string]string
	calls       int
}

func (p *testOptionsOwnerPolicy) OptionGuardManagePermissions(names []string) ([]string, bool) {
	p.calls++
	if names == nil {
		return []string{identity.PermissionSettingsSiteManage, identity.PermissionCategoryManage}, true
	}
	seen := map[string]bool{}
	result := []string{}
	for _, name := range names {
		permission := p.permissions[name]
		if permission == "" {
			return nil, false
		}
		if !seen[permission] {
			seen[permission] = true
			result = append(result, permission)
		}
	}
	return result, len(result) > 0
}

type testExtensionGuardPolicy struct {
	lookup extensions.GuardPolicyLookup
	ok     bool
	calls  int
}

func (p *testExtensionGuardPolicy) Lookup(extensionID string) (extensions.GuardPolicyLookup, bool) {
	if p == nil {
		return extensions.GuardPolicyLookup{}, false
	}
	p.calls++
	lookup := p.lookup
	if extensionID == "" {
		lookup.Found = false
		lookup.Entry = extensions.GuardPolicyEntry{}
	}
	return lookup, p.ok
}

func TestProductionIdentityAdminGuardPartitionsCatalogByProvablePolicy(t *testing.T) {
	type expectedRoute struct {
		method      string
		permissions []string
		body        string
		cookieOnly  bool
		supported   bool
	}
	expected := map[string]expectedRoute{
		"core.route.identity.list_permissions": {method: "GET", supported: true, permissions: []string{
			identity.PermissionRoleManage, identity.PermissionUserManage,
			identity.PermissionUserView, identity.PermissionUserPermissionOverride,
		}},
		"core.route.identity.permission_matrix": {method: "GET", supported: true, permissions: []string{
			identity.PermissionRoleManage, identity.PermissionUserManage,
			identity.PermissionUserView, identity.PermissionUserPermissionOverride,
		}},
		"core.route.identity.list_roles":  {method: "GET", supported: true, permissions: []string{identity.PermissionRoleManage}},
		"core.route.identity.create_role": {method: "POST", supported: true, permissions: []string{identity.PermissionRoleManage}},
		"core.route.identity.update_role": {method: "PATCH", supported: true, permissions: []string{identity.PermissionRoleManage}},
		"core.route.identity.list_role_suggestions": {
			method: "GET", supported: true, cookieOnly: true, permissions: []string{identity.PermissionRoleManage},
		},
		"core.route.identity.decide_role_suggestion": {
			method: "POST", supported: true, cookieOnly: true, permissions: []string{identity.PermissionRoleManage},
		},
		"core.route.identity.list_users": {method: "GET", supported: true, permissions: []string{identity.PermissionUserView, identity.PermissionUserManage}},
		"core.route.identity.get_user":   {method: "GET", supported: true, permissions: []string{identity.PermissionUserView, identity.PermissionUserManage}},

		"core.route.identity.delete_role":                       {method: "DELETE", supported: true, permissions: []string{identity.PermissionRoleManage}},
		"core.route.identity.replace_role_permissions":          {method: "PUT", supported: true, permissions: []string{identity.PermissionRoleManage}, body: `{"permissions":["post.create"]}`},
		"core.route.identity.create_user":                       {method: "POST"},
		"core.route.identity.update_user":                       {method: "PATCH"},
		"core.route.identity.admin_clear_user_client_ips":       {method: "POST"},
		"core.route.identity.set_user_email_verification":       {method: "PUT"},
		"core.route.identity.admin_set_user_password":           {method: "POST"},
		"core.route.identity.replace_user_permission_overrides": {method: "PUT"},
		"core.route.identity.replace_user_roles":                {method: "PUT"},
		"core.route.identity.admin_revoke_user_sessions":        {method: "POST"},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.identity.admin" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("identity admin contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected identity admin guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		if route.ID == "core.route.identity.delete_role" || route.ID == "core.route.identity.replace_role_permissions" {
			plan, step = productionParameterizedInheritedGuardPlan(
				t, route, "/guard/production/roles/:roleKey", "/guard/production/roles/community_helper",
			)
		}
		if !want.supported {
			request := productionGuardRequest("*")
			request.Method, request.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("%s unprovable policy error = %v", route.ID, err)
			}
			continue
		}

		for _, permission := range want.permissions {
			allowed := productionGuardRequest(permission)
			allowed.Method, allowed.Path, allowed.Params, allowed.Body = plan.Method(), plan.Path(), plan.Params(), []byte(want.body)
			if want.cookieOnly {
				allowed.CredentialSource = routes.DispatchCredentialCookie
			}
			if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
				t.Fatalf("%s permission %s error = %v", route.ID, permission, err)
			}
		}

		denied := productionGuardRequest(identity.PermissionPostCreate)
		denied.Method, denied.Path, denied.Params, denied.Body = plan.Method(), plan.Path(), plan.Params(), []byte(want.body)
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s permission denied error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params(), Body: []byte(want.body)}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}

		allowed := productionGuardRequest(want.permissions[0])
		allowed.Method, allowed.Path, allowed.Params, allowed.Body = plan.Method(), plan.Path(), plan.Params(), []byte(want.body)
		if want.cookieOnly {
			allowed.CredentialSource = routes.DispatchCredentialCookie
		}
		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
		forgedRequest := allowed
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged request error = %v", route.ID, err)
		}
		if want.cookieOnly {
			bearer := allowed
			bearer.CredentialSource = routes.DispatchCredentialBearer
			if err := authorizer.Authorize(context.Background(), plan, step, bearer); !errors.Is(err, ErrRoutePermissionDenied) {
				t.Fatalf("%s bearer credential error = %v", route.ID, err)
			}
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing identity admin guard routes = %#v", expected)
	}
}

func TestProductionIdentityAdminGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.identity.admin",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.identity.admin.foreign", descriptor)
	request := productionGuardRequest("*")
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign identity admin route error = %v", err)
	}
}

func TestProductionForumSettingsGuardClosesCatalogModule(t *testing.T) {
	expected := map[string]string{
		"core.route.forum.admin_settings":        "GET",
		"core.route.forum.admin_update_settings": "PUT",
		"core.route.forum.admin_reset_settings":  "POST",
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.forum.settings" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("forum settings contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		method, exists := expected[route.ID]
		if !exists || route.Method != method || route.Guard.Kind != routes.CoreGuardContextual ||
			!sameStringSet(route.Guard.Permissions, []string{
				identity.PermissionCategoryManage, identity.PermissionTagManage, identity.PermissionForumSettingsManage,
			}) {
			t.Fatalf("unexpected forum settings guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		allowed := productionGuardRequest(identity.PermissionCategoryManage)
		allowed.Method, allowed.Path = plan.Method(), plan.Path()
		if route.ID == "core.route.forum.admin_update_settings" {
			allowed.Body = []byte(`{"defaultCategorySlug":"general"}`)
		}
		if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
			t.Fatalf("%s allowed error = %v", route.ID, err)
		}

		denied := productionGuardRequest()
		denied.Method, denied.Path, denied.Body = plan.Method(), plan.Path(), allowed.Body
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s permission denied error = %v", route.ID, err)
		}

		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing forum settings guard routes = %#v", expected)
	}
}

func TestProductionForumSettingsGuardEnforcesFieldOwners(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.forum.admin_update_settings" {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatal("forum settings update route is missing")
	}
	plan, step := productionCatalogInheritedGuardPlan(t, target)
	authorizer := NewProductionRouteGuardAuthorizer()
	tests := []struct {
		name        string
		body        string
		permissions []string
		want        error
	}{
		{name: "category", body: `{"defaultCategorySlug":"general"}`, permissions: []string{identity.PermissionCategoryManage}},
		{name: "category denied", body: `{"defaultCategorySlug":"general"}`, permissions: []string{identity.PermissionTagManage}, want: ErrRoutePermissionDenied},
		{name: "tag", body: `{"tagCreationMode":"review","tagMaxPerTopic":5}`, permissions: []string{identity.PermissionTagManage}},
		{name: "tag denied", body: `{"tagPublicPages":true}`, permissions: []string{identity.PermissionCategoryManage}, want: ErrRoutePermissionDenied},
		{name: "runtime", body: `{"topicsPerPage":30}`, permissions: []string{identity.PermissionForumSettingsManage}},
		{name: "community runtime", body: `{"guestRead":"login_required","mentionsEnabled":false}`, permissions: []string{identity.PermissionForumSettingsManage}},
		{name: "runtime denied", body: `{"listDefaultSort":"hot"}`, permissions: []string{identity.PermissionTagManage}, want: ErrRoutePermissionDenied},
		{name: "mixed", body: `{"defaultCategorySlug":"general","tagMinPerTopic":1}`, permissions: []string{identity.PermissionCategoryManage, identity.PermissionTagManage}},
		{name: "mixed body escalation", body: `{"defaultCategorySlug":"general","tagMinPerTopic":1}`, permissions: []string{identity.PermissionCategoryManage}, want: ErrRoutePermissionDenied},
		{name: "empty requires module authority", body: `{}`, permissions: []string{identity.PermissionTagManage}},
		{name: "unknown field closes", body: `{"futureSetting":true}`, permissions: []string{identity.PermissionForumSettingsManage}, want: ErrRouteGuardUnavailable},
		{name: "malformed closes", body: `{"topicsPerPage":`, permissions: []string{identity.PermissionForumSettingsManage}, want: ErrRouteGuardUnavailable},
		{name: "trailing value closes", body: `{} {}`, permissions: []string{identity.PermissionForumSettingsManage}, want: ErrRouteGuardUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := productionGuardRequest(test.permissions...)
			request.Method, request.Path, request.Body = plan.Method(), plan.Path(), []byte(test.body)
			err := authorizer.Authorize(context.Background(), plan, step, request)
			if !errors.Is(err, test.want) || test.want == nil && err != nil {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProductionForumSettingsGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual,
		Permissions: []string{
			identity.PermissionCategoryManage, identity.PermissionTagManage, identity.PermissionForumSettingsManage,
		},
		EvaluatorID: "core.guard.forum.settings",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.forum.settings.foreign", descriptor)
	request := productionGuardRequest(identity.PermissionForumSettingsManage)
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign forum settings route error = %v", err)
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func TestProductionPagesAdminGuardClosesCatalogModule(t *testing.T) {
	type expectedRoute struct {
		method     string
		superAdmin bool
	}
	expected := map[string]expectedRoute{
		"core.route.pages.admin_list":       {method: "GET"},
		"core.route.pages.admin_get":        {method: "GET"},
		"core.route.pages.admin_approve":    {method: "POST", superAdmin: true},
		"core.route.pages.admin_restore":    {method: "POST", superAdmin: true},
		"core.route.pages.activate_preview": {method: "GET"},
		"core.route.pages.admin_added":      {method: "GET"},
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.pages.admin" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("pages admin contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		want, exists := expected[route.ID]
		if !exists || route.Method != want.method || route.Guard.Kind != routes.CoreGuardContextual ||
			len(route.Guard.Permissions) != 0 {
			t.Fatalf("unexpected pages admin guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		permission := identity.PermissionExtensionView
		if want.superAdmin {
			permission = "*"
		}
		allowed := productionGuardRequest(permission)
		allowed.Method, allowed.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
			t.Fatalf("%s allowed error = %v", route.ID, err)
		}

		denied := productionGuardRequest()
		denied.Method, denied.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, denied); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("%s permission denied error = %v", route.ID, err)
		}
		if want.superAdmin {
			themeManager := productionGuardRequest(identity.PermissionExtensionThemeManage)
			themeManager.Method, themeManager.Path = plan.Method(), plan.Path()
			if err := authorizer.Authorize(context.Background(), plan, step, themeManager); !errors.Is(err, ErrRoutePermissionDenied) {
				t.Fatalf("%s theme manager downgrade error = %v", route.ID, err)
			}
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}
		forgedActor := allowed
		forgedActor.ActorID = 0
		if err := authorizer.Authorize(context.Background(), plan, step, forgedActor); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s forged actor error = %v", route.ID, err)
		}

		forgedRequest := allowed
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged path error = %v", route.ID, err)
		}
		forgedRequest = allowed
		if forgedRequest.Method == "GET" {
			forgedRequest.Method = "POST"
		} else {
			forgedRequest.Method = "GET"
		}
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged method error = %v", route.ID, err)
		}

		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, allowed); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing pages admin guard routes = %#v", expected)
	}
}

func TestProductionPagesAdminGuardRequiresExactParsedResource(t *testing.T) {
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.pages.admin_get" {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatal("pages admin get route is missing")
	}
	plan, step := productionParameterizedInheritedGuardPlan(t, target,
		"/guard/production/pages/:pageId", "/guard/production/pages/forum.home")
	request := productionGuardRequest(identity.PermissionExtensionView)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatalf("exact resource error = %v", err)
	}

	request.Params["pageId"] = "admin.secret"
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("forged resource error = %v", err)
	}
}

func TestProductionPagesAdminGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.pages.admin",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.pages.foreign", descriptor)
	request := productionGuardRequest(identity.PermissionExtensionView)
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign pages admin route error = %v", err)
	}
}

func TestProductionNotificationRecipientGuardClosesCatalogModule(t *testing.T) {
	expected := map[string]string{
		"core.route.notifications.get_preferences":              "GET",
		"core.route.notifications.update_preferences":           "PUT",
		"core.route.notifications.restore_preferences":          "POST",
		"core.route.notifications.list":                         "GET",
		"core.route.notifications.detail":                       "GET",
		"core.route.notifications.mark_read":                    "PATCH",
		"core.route.notifications.mark_all_read":                "POST",
		"core.route.notifications.stream":                       "GET",
		"core.route.notifications.unread_count":                 "GET",
		"core.route.notifications.web_push_config":              "GET",
		"core.route.notifications.list_web_push_subscriptions":  "GET",
		"core.route.notifications.create_web_push_subscription": "POST",
		"core.route.notifications.revoke_web_push_subscription": "DELETE",
	}
	var catalog []routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.Guard.EvaluatorID == "core.guard.notifications.recipient" {
			catalog = append(catalog, route)
		}
	}
	if len(catalog) != len(expected) {
		t.Fatalf("notification contextual catalog = %#v", catalog)
	}

	authorizer := NewProductionRouteGuardAuthorizer()
	for _, route := range catalog {
		method, exists := expected[route.ID]
		if !exists || route.Method != method || route.Guard.Kind != routes.CoreGuardContextual {
			t.Fatalf("unexpected notification guard route = %#v", route)
		}
		delete(expected, route.ID)

		plan, step := productionCatalogInheritedGuardPlan(t, route)
		// 通知收件箱是登录主体能力，不要求额外 scope；PAT 权限仍保持收窄，
		// 资源所有权由核心通知查询的 recipient_user_id 条件继续强制。
		patRequest := productionGuardRequest(identity.PermissionPostCreate)
		patRequest.Method, patRequest.Path = plan.Method(), plan.Path()
		if err := authorizer.Authorize(context.Background(), plan, step, patRequest); err != nil {
			t.Fatalf("%s scoped PAT error = %v", route.ID, err)
		}

		anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path()}
		if err := authorizer.Authorize(context.Background(), plan, step, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
			t.Fatalf("%s anonymous error = %v", route.ID, err)
		}

		forgedRequest := patRequest
		forgedRequest.Path += "/forged"
		if err := authorizer.Authorize(context.Background(), plan, step, forgedRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged request error = %v", route.ID, err)
		}

		forgedStep := step
		forgedStep.RouteID += ".forged"
		if err := authorizer.Authorize(context.Background(), plan, forgedStep, patRequest); !errors.Is(err, ErrRouteGuardUnavailable) {
			t.Fatalf("%s forged step error = %v", route.ID, err)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing notification guard routes = %#v", expected)
	}
}

func TestProductionNotificationRecipientGuardRejectsForeignRouteID(t *testing.T) {
	descriptor := routes.CoreGuardDescriptor{
		Kind: routes.CoreGuardContextual, EvaluatorID: "core.guard.notifications.recipient",
	}
	plan, step := productionInheritedGuardPlan(t, "core.route.notifications.foreign", descriptor)
	request := productionGuardRequest()
	request.Method, request.Path = plan.Method(), plan.Path()
	if err := NewProductionRouteGuardAuthorizer().Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("foreign notification route error = %v", err)
	}
}

func productionCatalogInheritedGuardPlan(
	t *testing.T,
	target routes.CoreRoute,
) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	registry := routes.NewRegistry()
	alias := extensionmanifest.ManifestRoute{
		ID: "guard.production.catalog_alias", ContractVersion: "guard.production.catalog_alias@1",
		Action: extensionmanifest.RouteActionAlias, TargetID: target.ID,
		Path: "/guard/production/catalog", Methods: []string{target.Method}, Guard: extensionmanifest.GuardCoreInherit,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
	}
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{target},
		Plugins: []routes.PluginRouteSet{{
			Artifact: routes.PluginArtifact{
				ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("c", 64), RuntimeInstanceID: "runtime-c",
			},
			Routes: []extensionmanifest.ManifestRoute{alias},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(target.Method, alias.Path)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

func productionExtensionPolicyGuardPlan(
	t *testing.T,
	target routes.CoreRoute,
	entry extensions.GuardPolicyEntry,
) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	aliasPath := "/guard/production/extensions"
	requestPath := aliasPath
	if target.ID != "core.route.extensions.install" {
		aliasPath += "/:id"
		requestPath += "/" + entry.ExtensionID
	}
	switch target.ID {
	case "core.route.extensions.frontend_asset":
		aliasPath += "/:digest/:asset"
		requestPath += "/" + entry.AdminFrontendDigest + "/entry"
	case "core.route.extensions.execute_settings_action":
		aliasPath += "/:actionId"
		requestPath += "/probe"
	}
	return productionParameterizedInheritedGuardPlan(t, target, aliasPath, requestPath)
}

func productionWildcardInheritedGuardPlan(
	t *testing.T,
	target routes.CoreRoute,
	method string,
	aliasPath string,
	requestPath string,
) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	registry := routes.NewRegistry()
	alias := extensionmanifest.ManifestRoute{
		ID: "guard.production.wildcard_alias", ContractVersion: "guard.production.wildcard_alias@1",
		Action: extensionmanifest.RouteActionAlias, TargetID: target.ID,
		Path: aliasPath, Methods: []string{method}, Guard: extensionmanifest.GuardCoreInherit,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
	}
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{target},
		Plugins: []routes.PluginRouteSet{{
			Artifact: routes.PluginArtifact{
				ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("e", 64), RuntimeInstanceID: "runtime-e",
			},
			Routes: []extensionmanifest.ManifestRoute{alias},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(method, requestPath)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

func productionParameterizedInheritedGuardPlan(
	t *testing.T,
	target routes.CoreRoute,
	aliasPath string,
	requestPath string,
) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	registry := routes.NewRegistry()
	alias := extensionmanifest.ManifestRoute{
		ID: "guard.production.parameterized_alias", ContractVersion: "guard.production.parameterized_alias@1",
		Action: extensionmanifest.RouteActionAlias, TargetID: target.ID,
		Path: aliasPath, Methods: []string{target.Method}, Guard: extensionmanifest.GuardCoreInherit,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
	}
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{target},
		Plugins: []routes.PluginRouteSet{{
			Artifact: routes.PluginArtifact{
				ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("d", 64), RuntimeInstanceID: "runtime-d",
			},
			Routes: []extensionmanifest.ManifestRoute{alias},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(target.Method, requestPath)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

func productionInheritedGuardPlan(
	t *testing.T,
	targetID string,
	descriptor routes.CoreGuardDescriptor,
) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	const method = "PATCH"
	contract := "sforum.route." + strings.TrimPrefix(targetID, "core.route.") + "@1"
	descriptor.RouteID, descriptor.ContractVersion, descriptor.Method = targetID, contract, method
	registry := routes.NewRegistry()
	alias := extensionmanifest.ManifestRoute{
		ID: "guard.production.alias", ContractVersion: "guard.production.alias@1",
		Action: extensionmanifest.RouteActionAlias, TargetID: targetID,
		Path: "/guard/production", Methods: []string{method}, Guard: extensionmanifest.GuardCoreInherit,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
	}
	if _, err := registry.Publish(routes.Publication{
		Core: []routes.CoreRoute{{ID: targetID, ContractVersion: contract, Method: method, Path: "/guard/target", Guard: descriptor}},
		Plugins: []routes.PluginRouteSet{{
			Artifact: routes.PluginArtifact{
				ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-a",
			},
			Routes: []extensionmanifest.ManifestRoute{alias},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan(method, alias.Path)
	if err != nil {
		t.Fatal(err)
	}
	return plan, plan.Terminal()
}

// productionDirectGuardPlan 发布一条插件路由并返回其执行计划。
//
// 决定 R1（2026-10-16）：core.guard.raw_request 已不是合法守卫，注册表发布阶段就会
// 拒绝它，因此无法再为它构造执行计划。为了让授权器对「旧制品 / 伪造执行步骤」的
// fail closed 仍然可测，这里先构造等价的可发布路由，再把执行步骤上的守卫替换为已
// 退役的 Host raw 守卫（计划本身保持注册表产出的原始形态）。
func productionDirectGuardPlan(t *testing.T, guard, permission string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	retiredCoreRaw := guard == extensionmanifest.GuardCoreRaw
	if retiredCoreRaw {
		guard = "guard.production.custom"
	}
	registry := routes.NewRegistry()
	route := extensionmanifest.ManifestRoute{
		ID: "guard.production.direct", ContractVersion: "guard.production.direct@1",
		Action: extensionmanifest.RouteActionAdd, Path: "/guard/direct", Methods: []string{"GET"},
		Guard: guard, Permission: permission, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.handle", ResponseSchema: "guard.production.direct.response@1",
	}
	var guards []extensionmanifest.ManifestGuard
	if guard == "guard.production.custom" {
		guards = []extensionmanifest.ManifestGuard{{
			ID: guard, ContractVersion: guard + "@1", Kind: "custom",
			Entry: "backend/guard", Digest: strings.Repeat("c", 64),
		}}
	}
	if _, err := registry.Publish(routes.Publication{Plugins: []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: "guard.production", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("b", 64), RuntimeInstanceID: "runtime-b",
		},
		Routes: []extensionmanifest.ManifestRoute{route}, Guards: guards,
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := registry.BuildExecutionPlan("GET", route.Path)
	if err != nil {
		t.Fatal(err)
	}
	if retiredCoreRaw {
		step := plan.Terminal()
		step.Guard = extensionmanifest.GuardCoreRaw
		step.PluginGuard = routes.PluginGuardBinding{}
		return plan, step
	}
	return plan, plan.Terminal()
}

func productionGuardRequest(permissions ...string) routes.DispatchRequest {
	request := routes.DispatchRequest{ActorID: 42, Authenticated: true, Permissions: map[string]bool{}}
	for _, permission := range permissions {
		request.Permissions[permission] = true
	}
	return request
}
