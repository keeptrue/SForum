package http

import (
	"context"
	"errors"
	"testing"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionIdentitySelfResourceGuardAllowsOwnedResources(t *testing.T) {
	policy := &testIdentitySelfResourcePolicy{
		session: identity.SessionGuardSubject{SID: "session-7", OwnerUserID: 42, Exists: true, Revoked: true},
		token:   apitokens.GuardSubject{TokenID: 7, OwnerUserID: 42, Exists: true, Revoked: true},
	}
	authorizer := identitySelfResourceAuthorizer(policy)
	tests := []struct {
		name       string
		routeID    string
		credential routes.DispatchCredentialSource
	}{
		{name: "session cookie", routeID: "core.route.identity.revoke_session", credential: routes.DispatchCredentialCookie},
		{name: "session PAT", routeID: "core.route.identity.revoke_session", credential: routes.DispatchCredentialBearer},
		{name: "token revoke cookie", routeID: "core.route.identity.revoke_apitoken", credential: routes.DispatchCredentialCookie},
		{name: "token rotate cookie", routeID: "core.route.identity.rotate_apitoken", credential: routes.DispatchCredentialCookie},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, step := productionIdentitySelfResourcePlan(t, test.routeID)
			request := productionGuardRequest()
			request.Method, request.Path, request.Params, request.CredentialSource = plan.Method(), plan.Path(), plan.Params(), test.credential
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionIdentitySelfResourceGuardRejectsMismatchedAndMissingResources(t *testing.T) {
	tests := []struct {
		name    string
		routeID string
		policy  *testIdentitySelfResourcePolicy
	}{
		{name: "session owner", routeID: "core.route.identity.revoke_session", policy: &testIdentitySelfResourcePolicy{session: identity.SessionGuardSubject{SID: "session-7", OwnerUserID: 99, Exists: true}}},
		{name: "session missing", routeID: "core.route.identity.revoke_session", policy: &testIdentitySelfResourcePolicy{sessionErr: identity.ErrSessionNotFound}},
		{name: "session identity", routeID: "core.route.identity.revoke_session", policy: &testIdentitySelfResourcePolicy{session: identity.SessionGuardSubject{SID: "forged", OwnerUserID: 42, Exists: true}}},
		{name: "token owner", routeID: "core.route.identity.revoke_apitoken", policy: &testIdentitySelfResourcePolicy{token: apitokens.GuardSubject{TokenID: 7, OwnerUserID: 99, Exists: true}}},
		{name: "token missing", routeID: "core.route.identity.rotate_apitoken", policy: &testIdentitySelfResourcePolicy{tokenErr: apitokens.ErrTokenNotFound}},
		{name: "token identity", routeID: "core.route.identity.rotate_apitoken", policy: &testIdentitySelfResourcePolicy{token: apitokens.GuardSubject{TokenID: 8, OwnerUserID: 42, Exists: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := identitySelfResourceAuthorizer(test.policy)
			plan, step := productionIdentitySelfResourcePlan(t, test.routeID)
			request := productionGuardRequest()
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			request.CredentialSource = routes.DispatchCredentialCookie
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionIdentitySelfResourceGuardEnforcesCredentialAndRequestContract(t *testing.T) {
	policy := &testIdentitySelfResourcePolicy{
		session: identity.SessionGuardSubject{SID: "session-7", OwnerUserID: 42, Exists: true},
		token:   apitokens.GuardSubject{TokenID: 7, OwnerUserID: 42, Exists: true},
	}
	authorizer := identitySelfResourceAuthorizer(policy)
	tokenPlan, tokenStep := productionIdentitySelfResourcePlan(t, "core.route.identity.revoke_apitoken")
	bearer := productionGuardRequest()
	bearer.Method, bearer.Path, bearer.Params, bearer.CredentialSource = tokenPlan.Method(), tokenPlan.Path(), tokenPlan.Params(), routes.DispatchCredentialBearer
	if err := authorizer.Authorize(context.Background(), tokenPlan, tokenStep, bearer); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("Bearer token management error = %v", err)
	}
	if policy.tokenCalls != 0 {
		t.Fatalf("Bearer token management performed %d Store reads", policy.tokenCalls)
	}

	sessionPlan, sessionStep := productionIdentitySelfResourcePlan(t, "core.route.identity.revoke_session")
	anonymous := routes.DispatchRequest{Method: sessionPlan.Method(), Path: sessionPlan.Path(), Params: sessionPlan.Params()}
	if err := authorizer.Authorize(context.Background(), sessionPlan, sessionStep, anonymous); !errors.Is(err, ErrRouteLoginRequired) {
		t.Fatalf("anonymous session error = %v", err)
	}
	if policy.sessionCalls != 0 {
		t.Fatalf("anonymous session performed %d Store reads", policy.sessionCalls)
	}

	for name, mutate := range map[string]func(*routes.DispatchRequest){
		"body":   func(request *routes.DispatchRequest) { request.Body = []byte(`{"hidden":true}`) },
		"query":  func(request *routes.DispatchRequest) { request.Query = "future=true" },
		"params": func(request *routes.DispatchRequest) { request.Params["sessionId"] = "forged" },
	} {
		t.Run(name, func(t *testing.T) {
			request := productionGuardRequest()
			request.Method, request.Path, request.Params = sessionPlan.Method(), sessionPlan.Path(), sessionPlan.Params()
			mutate(&request)
			if err := authorizer.Authorize(context.Background(), sessionPlan, sessionStep, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionIdentitySelfResourceGuardReloadsOwnershipEveryRequest(t *testing.T) {
	policy := &testIdentitySelfResourcePolicy{token: apitokens.GuardSubject{TokenID: 7, OwnerUserID: 42, Exists: true}}
	authorizer := identitySelfResourceAuthorizer(policy)
	plan, step := productionIdentitySelfResourcePlan(t, "core.route.identity.rotate_apitoken")
	request := productionGuardRequest()
	request.Method, request.Path, request.Params, request.CredentialSource = plan.Method(), plan.Path(), plan.Params(), routes.DispatchCredentialCookie
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatal(err)
	}
	policy.token.OwnerUserID = 99
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("stale ownership error = %v", err)
	}
	if policy.tokenCalls != 2 {
		t.Fatalf("token ownership loads = %d, want 2", policy.tokenCalls)
	}
}

type testIdentitySelfResourcePolicy struct {
	session      identity.SessionGuardSubject
	sessionErr   error
	sessionCalls int
	token        apitokens.GuardSubject
	tokenErr     error
	tokenCalls   int
}

func (p *testIdentitySelfResourcePolicy) LoadSessionGuardSubject(context.Context, string) (identity.SessionGuardSubject, error) {
	p.sessionCalls++
	return p.session, p.sessionErr
}

func (p *testIdentitySelfResourcePolicy) LoadGuardSubject(context.Context, int64) (apitokens.GuardSubject, error) {
	p.tokenCalls++
	return p.token, p.tokenErr
}

func identitySelfResourceAuthorizer(policy *testIdentitySelfResourcePolicy) ProductionRouteGuardAuthorizer {
	return NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
		IdentitySessions: policy, IdentityAPITokens: policy,
	})
}

func productionIdentitySelfResourcePlan(t *testing.T, routeID string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == routeID {
			target = route
			break
		}
	}
	if routeID == "core.route.identity.revoke_session" {
		return productionParameterizedInheritedGuardPlan(t, target, "/guard/production/sessions/:sessionId", "/guard/production/sessions/session-7")
	}
	return productionParameterizedInheritedGuardPlan(t, target, "/guard/production/tokens/:tokenID", "/guard/production/tokens/7")
}
