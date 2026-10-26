package http

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionIdentityAdminSubjectGuardAllowsResourceRoutes(t *testing.T) {
	policy := &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{
		UserID: 7, Exists: true,
	}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
	tests := []struct {
		routeID    string
		permission string
		body       string
	}{
		{"core.route.identity.update_user", identity.PermissionUserManage, `{"displayName":"Managed"}`},
		{"core.route.identity.admin_clear_user_client_ips", identity.PermissionUserManage, ""},
		{"core.route.identity.set_user_email_verification", identity.PermissionUserManage, `{"verified":true}`},
		{"core.route.identity.admin_set_user_password", identity.PermissionUserManage, `{"password":"a-very-strong-password"}`},
		{"core.route.identity.replace_user_permission_overrides", identity.PermissionUserPermissionOverride, `{"allow":[],"deny":[]}`},
		{"core.route.identity.replace_user_roles", identity.PermissionUserManage, `{"roleKeys":["member"]}`},
		{"core.route.identity.admin_revoke_user_sessions", identity.PermissionUserManage, ""},
	}
	for _, test := range tests {
		t.Run(test.routeID, func(t *testing.T) {
			plan, step := productionIdentityAdminSubjectPlan(t, test.routeID)
			request := productionGuardRequest(test.permission)
			request.Method, request.Path, request.Params, request.Body = plan.Method(), plan.Path(), plan.Params(), []byte(test.body)
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionIdentityAdminSubjectGuardEnforcesTargetBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		routeID     string
		actorID     int64
		permissions []string
		body        string
		subject     identity.AdminGuardSubject
	}{
		{name: "ban requires permission", routeID: "core.route.identity.update_user", permissions: []string{identity.PermissionUserManage}, body: `{"status":"banned"}`},
		{name: "self status", routeID: "core.route.identity.update_user", actorID: 7, permissions: []string{identity.PermissionUserManage}, body: `{"status":"active"}`},
		{name: "initial disabled", routeID: "core.route.identity.update_user", permissions: []string{"*"}, body: `{"status":"disabled"}`, subject: identity.AdminGuardSubject{IsInitialSuperAdmin: true, IsSuperAdmin: true}},
		{name: "super user update", routeID: "core.route.identity.update_user", permissions: []string{identity.PermissionUserManage}, body: `{"displayName":"No"}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "super ip clear", routeID: "core.route.identity.admin_clear_user_client_ips", permissions: []string{identity.PermissionUserManage}, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "super email verification", routeID: "core.route.identity.set_user_email_verification", permissions: []string{identity.PermissionUserManage}, body: `{"verified":true}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "super password", routeID: "core.route.identity.admin_set_user_password", permissions: []string{identity.PermissionUserManage}, body: `{"password":"a-very-strong-password"}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "self override", routeID: "core.route.identity.replace_user_permission_overrides", actorID: 7, permissions: []string{identity.PermissionUserPermissionOverride}},
		{name: "super override", routeID: "core.route.identity.replace_user_permission_overrides", permissions: []string{identity.PermissionUserPermissionOverride}, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "self roles", routeID: "core.route.identity.replace_user_roles", actorID: 7, permissions: []string{identity.PermissionUserManage}, body: `{"roleKeys":["member"]}`},
		{name: "initial demotion", routeID: "core.route.identity.replace_user_roles", permissions: []string{"*"}, body: `{"roleKeys":["member"]}`, subject: identity.AdminGuardSubject{IsInitialSuperAdmin: true, IsSuperAdmin: true}},
		{name: "super role mutation", routeID: "core.route.identity.replace_user_roles", permissions: []string{identity.PermissionUserManage}, body: `{"roleKeys":["member"]}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "self revoke", routeID: "core.route.identity.admin_revoke_user_sessions", actorID: 7, permissions: []string{identity.PermissionUserManage}},
		{name: "super revoke", routeID: "core.route.identity.admin_revoke_user_sessions", permissions: []string{identity.PermissionUserManage}, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.subject.UserID, test.subject.Exists = 7, true
			policy := &testIdentityAdminGuardPolicy{subject: test.subject}
			authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
			plan, step := productionIdentityAdminSubjectPlan(t, test.routeID)
			request := productionGuardRequest(test.permissions...)
			if test.actorID > 0 {
				request.ActorID = test.actorID
			}
			request.Method, request.Path, request.Params, request.Body = plan.Method(), plan.Path(), plan.Params(), []byte(test.body)
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionIdentityAdminSubjectGuardAllowsProtectedChangesForSuperAdmin(t *testing.T) {
	tests := []struct {
		name    string
		routeID string
		body    string
		subject identity.AdminGuardSubject
	}{
		{name: "ban", routeID: "core.route.identity.update_user", body: `{"status":"banned"}`},
		{name: "update super", routeID: "core.route.identity.update_user", body: `{"displayName":"Managed"}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "verify super email", routeID: "core.route.identity.set_user_email_verification", body: `{"verified":true}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "password super", routeID: "core.route.identity.admin_set_user_password", body: `{"password":"a-very-strong-password"}`, subject: identity.AdminGuardSubject{IsSuperAdmin: true}},
		{name: "retain initial role", routeID: "core.route.identity.replace_user_roles", body: `{"roleKeys":["member","super_admin"]}`, subject: identity.AdminGuardSubject{IsInitialSuperAdmin: true, IsSuperAdmin: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.subject.UserID, test.subject.Exists = 7, true
			policy := &testIdentityAdminGuardPolicy{subject: test.subject}
			authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
			plan, step := productionIdentityAdminSubjectPlan(t, test.routeID)
			request := productionGuardRequest("*")
			request.Method, request.Path, request.Params, request.Body = plan.Method(), plan.Path(), plan.Params(), []byte(test.body)
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionIdentityAdminSubjectGuardFailsClosedOnMissingAndForgery(t *testing.T) {
	plan, step := productionIdentityAdminSubjectPlan(t, "core.route.identity.admin_revoke_user_sessions")
	base := productionGuardRequest(identity.PermissionUserManage)
	base.Method, base.Path, base.Params = plan.Method(), plan.Path(), plan.Params()
	tests := []struct {
		name   string
		policy *testIdentityAdminGuardPolicy
		mutate func(*routes.DispatchRequest)
	}{
		{name: "missing", policy: &testIdentityAdminGuardPolicy{err: identity.ErrUserNotFound}},
		{name: "forged subject", policy: &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{UserID: 8, Exists: true}}},
		{name: "forged params", policy: &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{UserID: 7, Exists: true}}, mutate: func(request *routes.DispatchRequest) { request.Params["userID"] = "8" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Params = plan.Params()
			if test.mutate != nil {
				test.mutate(&request)
			}
			authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: test.policy})
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionIdentityAdminSubjectGuardReloadsAuthoritativeStateAcrossNodes(t *testing.T) {
	policy := &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{UserID: 7, Exists: true}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
	plan, step := productionIdentityAdminSubjectPlan(t, "core.route.identity.admin_revoke_user_sessions")
	request := productionGuardRequest(identity.PermissionUserManage)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatalf("node A member snapshot error = %v", err)
	}

	// 模拟 node B 已把目标提升为 super_admin。node A 的下一次 Guard 必须重新
	// 读取权威 Store，而不是继续使用第一次请求留下的进程内身份。
	policy.subject.IsSuperAdmin = true
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("node B role update remained stale on node A: %v", err)
	}
	if policy.calls != 2 {
		t.Fatalf("authoritative loads = %d, want 2", policy.calls)
	}
}

func TestProductionIdentityAdminSubjectGuardRejectsBeforeStoreForUnauthorizedActor(t *testing.T) {
	policy := &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{UserID: 7, Exists: true}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
	plan, step := productionIdentityAdminSubjectPlan(t, "core.route.identity.admin_clear_user_client_ips")
	request := productionGuardRequest(identity.PermissionPostCreate)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("error = %v", err)
	}
	if policy.calls != 0 {
		t.Fatalf("unauthorized request performed %d Store reads", policy.calls)
	}
}

func TestProductionIdentityAdminSetPasswordGuardAllowsSelfAndDeniesMissingBody(t *testing.T) {
	policy := &testIdentityAdminGuardPolicy{subject: identity.AdminGuardSubject{UserID: 7, Exists: true}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{IdentityAdmins: policy})
	plan, step := productionIdentityAdminSubjectPlan(t, "core.route.identity.admin_set_user_password")

	// 改密是恢复入口：允许管理员重置自己的密码（与 revoke sessions 不同）。
	self := productionGuardRequest(identity.PermissionUserManage)
	self.ActorID = 7
	self.Method, self.Path, self.Params = plan.Method(), plan.Path(), plan.Params()
	self.Body = []byte(`{"password":"a-very-strong-password"}`)
	if err := authorizer.Authorize(context.Background(), plan, step, self); err != nil {
		t.Fatalf("self password reset error = %v", err)
	}

	// 空密码体无法在 Guard 层复现服务校验细节，fail-closed。
	empty := productionGuardRequest(identity.PermissionUserManage)
	empty.Method, empty.Path, empty.Params = plan.Method(), plan.Path(), plan.Params()
	empty.Body = []byte(`{"password":""}`)
	if err := authorizer.Authorize(context.Background(), plan, step, empty); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("empty password error = %v", err)
	}
}

type testIdentityAdminGuardPolicy struct {
	subject identity.AdminGuardSubject
	err     error
	calls   int
}

func (p *testIdentityAdminGuardPolicy) LoadAdminGuardSubject(context.Context, int64) (identity.AdminGuardSubject, error) {
	p.calls++
	return p.subject, p.err
}

func productionIdentityAdminSubjectPlan(t *testing.T, routeID string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == routeID {
			target = route
			break
		}
	}
	return productionParameterizedInheritedGuardPlan(t, target, "/guard/production/users/:userID", "/guard/production/users/7")
}
