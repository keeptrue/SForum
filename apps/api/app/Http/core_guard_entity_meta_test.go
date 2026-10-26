package http

import (
	"context"
	"errors"
	"testing"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionEntityMetaValueGuardAllowsCurrentReadAndWriteAuthority(t *testing.T) {
	fields := map[string]entitymeta.ValueGuardField{
		"profile.note": {FieldKey: "profile.note", Visibility: entitymeta.VisibilityOwner, Enabled: true},
		"admin.note":   {FieldKey: "admin.note", Visibility: entitymeta.VisibilityAdmin, Enabled: true},
	}
	tests := []struct {
		name        string
		routeID     string
		entityType  string
		actorID     int64
		permissions []string
		body        string
		fields      map[string]entitymeta.ValueGuardField
	}{
		{name: "anonymous user read", routeID: "core.route.entity_meta.list_values", entityType: entitymeta.EntityUser},
		{name: "anonymous topic read", routeID: "core.route.entity_meta.list_values", entityType: entitymeta.EntityTopic},
		{name: "user owner write", routeID: "core.route.entity_meta.upsert_values", entityType: entitymeta.EntityUser, actorID: 7, body: `{"values":[{"fieldKey":"profile.note","value":"x"}]}`, fields: fields},
		{name: "topic owner write", routeID: "core.route.entity_meta.upsert_values", entityType: entitymeta.EntityTopic, actorID: 42, permissions: []string{identity.PermissionTopicEditOwn}, body: `{"values":[{"fieldKey":"profile.note","value":"x"}]}`, fields: fields},
		{name: "topic owner edit any", routeID: "core.route.entity_meta.upsert_values", entityType: entitymeta.EntityTopic, actorID: 42, permissions: []string{identity.PermissionTopicEditAny}, body: `{"values":[{"fieldKey":"profile.note","value":"x"}]}`, fields: fields},
		{name: "manager admin write", routeID: "core.route.entity_meta.upsert_values", entityType: entitymeta.EntityTopic, actorID: 7, permissions: []string{identity.PermissionEntityMetaManage}, body: `{"values":[{"fieldKey":"admin.note","value":"x"}]}`, fields: fields},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ownerID := int64(42)
			if test.entityType == entitymeta.EntityUser {
				ownerID = 7
			}
			policy := &testEntityMetaValueGuardPolicy{subject: entitymeta.ValueGuardSubject{
				EntityType: test.entityType, EntityID: 7, OwnerUserID: ownerID, Exists: true, Fields: test.fields,
			}}
			authorizer := entityMetaValueAuthorizer(policy)
			plan, step := productionEntityMetaValuePlan(t, test.routeID, test.entityType)
			request := routes.DispatchRequest{}
			if test.actorID > 0 {
				request = productionGuardRequest(test.permissions...)
				request.ActorID = test.actorID
			}
			request.Method, request.Path, request.Params, request.Body = plan.Method(), plan.Path(), plan.Params(), []byte(test.body)
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionEntityMetaValueWriteGuardRejectsForeignAndProtectedFields(t *testing.T) {
	tests := []struct {
		name        string
		entityType  string
		actorID     int64
		permissions []string
		field       entitymeta.ValueGuardField
	}{
		{name: "foreign user", entityType: entitymeta.EntityUser, actorID: 42, field: entitymeta.ValueGuardField{FieldKey: "profile.note", Visibility: entitymeta.VisibilityPublic, Enabled: true}},
		{name: "topic owner without edit own", entityType: entitymeta.EntityTopic, actorID: 42, field: entitymeta.ValueGuardField{FieldKey: "profile.note", Visibility: entitymeta.VisibilityPublic, Enabled: true}},
		{name: "owner admin field", entityType: entitymeta.EntityUser, actorID: 7, field: entitymeta.ValueGuardField{FieldKey: "profile.note", Visibility: entitymeta.VisibilityAdmin, Enabled: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ownerID := int64(42)
			if test.entityType == entitymeta.EntityUser {
				ownerID = 7
			}
			policy := &testEntityMetaValueGuardPolicy{subject: entitymeta.ValueGuardSubject{
				EntityType: test.entityType, EntityID: 7, OwnerUserID: ownerID, Exists: true,
				Fields: map[string]entitymeta.ValueGuardField{"profile.note": test.field},
			}}
			authorizer := entityMetaValueAuthorizer(policy)
			plan, step := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", test.entityType)
			request := productionGuardRequest(test.permissions...)
			request.ActorID = test.actorID
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			request.Body = []byte(`{"values":[{"fieldKey":"profile.note","value":"x"}]}`)
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionEntityMetaValueGuardFailsClosedOnInvalidTargetFieldAndBody(t *testing.T) {
	baseSubject := entitymeta.ValueGuardSubject{
		EntityType: entitymeta.EntityUser, EntityID: 7, OwnerUserID: 7, Exists: true,
		Fields: map[string]entitymeta.ValueGuardField{
			"profile.note": {FieldKey: "profile.note", Visibility: entitymeta.VisibilityOwner, Enabled: true},
		},
	}
	tests := []struct {
		name   string
		policy *testEntityMetaValueGuardPolicy
		mutate func(*routes.DispatchRequest)
	}{
		{name: "missing entity", policy: &testEntityMetaValueGuardPolicy{err: entitymeta.ErrEntityNotFound}},
		{name: "forged subject", policy: &testEntityMetaValueGuardPolicy{subject: entitymeta.ValueGuardSubject{EntityType: entitymeta.EntityTopic, EntityID: 7, OwnerUserID: 42, Exists: true}}},
		{name: "missing field", policy: &testEntityMetaValueGuardPolicy{subject: entitymeta.ValueGuardSubject{EntityType: entitymeta.EntityUser, EntityID: 7, OwnerUserID: 7, Exists: true, Fields: map[string]entitymeta.ValueGuardField{}}}},
		{name: "disabled field", policy: &testEntityMetaValueGuardPolicy{subject: func() entitymeta.ValueGuardSubject {
			value := baseSubject
			value.Fields = map[string]entitymeta.ValueGuardField{"profile.note": {FieldKey: "profile.note", Visibility: entitymeta.VisibilityOwner}}
			return value
		}()}},
		{name: "unknown body", policy: &testEntityMetaValueGuardPolicy{subject: baseSubject}, mutate: func(request *routes.DispatchRequest) {
			request.Body = []byte(`{"values":[{"fieldKey":"profile.note"}],"future":true}`)
		}},
		{name: "empty body", policy: &testEntityMetaValueGuardPolicy{subject: baseSubject}, mutate: func(request *routes.DispatchRequest) { request.Body = []byte(`{"values":[]}`) }},
		{name: "blank field", policy: &testEntityMetaValueGuardPolicy{subject: baseSubject}, mutate: func(request *routes.DispatchRequest) { request.Body = []byte(`{"values":[{"fieldKey":" "}]}`) }},
		{name: "query", policy: &testEntityMetaValueGuardPolicy{subject: baseSubject}, mutate: func(request *routes.DispatchRequest) { request.Query = "future=true" }},
		{name: "forged params", policy: &testEntityMetaValueGuardPolicy{subject: baseSubject}, mutate: func(request *routes.DispatchRequest) { request.Params["entityID"] = "8" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := entityMetaValueAuthorizer(test.policy)
			plan, step := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", entitymeta.EntityUser)
			request := productionGuardRequest()
			request.ActorID = 7
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			request.Body = []byte(`{"values":[{"fieldKey":"profile.note","value":"x"}]}`)
			if test.mutate != nil {
				test.mutate(&request)
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProductionEntityMetaValueWriteGuardRejectsAnonymousBeforeStore(t *testing.T) {
	policy := &testEntityMetaValueGuardPolicy{}
	authorizer := entityMetaValueAuthorizer(policy)
	plan, step := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", entitymeta.EntityUser)
	request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params(), Body: []byte(`{"values":[{"fieldKey":"profile.note"}]}`)}
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteLoginRequired) {
		t.Fatalf("error = %v", err)
	}
	if policy.calls != 0 {
		t.Fatalf("anonymous write performed %d Store reads", policy.calls)
	}
}

func TestProductionEntityMetaValueGuardRejectsInvalidTypeIDAndReadPayloadBeforeStore(t *testing.T) {
	policy := &testEntityMetaValueGuardPolicy{}
	authorizer := entityMetaValueAuthorizer(policy)
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.entity_meta.list_values" {
			target = route
			break
		}
	}
	tests := []struct {
		name        string
		requestPath string
		body        string
		query       string
	}{
		{name: "entity type", requestPath: "/guard/production/entity-meta/comment/7"},
		{name: "entity id zero", requestPath: "/guard/production/entity-meta/user/0"},
		{name: "entity id syntax", requestPath: "/guard/production/entity-meta/user/not-a-number"},
		{name: "read body", requestPath: "/guard/production/entity-meta/user/7", body: `{"hidden":true}`},
		{name: "read query", requestPath: "/guard/production/entity-meta/user/7", query: "future=true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, step := productionParameterizedInheritedGuardPlan(
				t, target, "/guard/production/entity-meta/:entityType/:entityID", test.requestPath,
			)
			request := routes.DispatchRequest{
				Method: plan.Method(), Path: plan.Path(), Params: plan.Params(),
				Body: []byte(test.body), Query: test.query,
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if policy.calls != 0 {
		t.Fatalf("invalid read requests performed %d Store reads", policy.calls)
	}
}

func TestProductionEntityMetaValueGuardReloadsOwnerAndVisibility(t *testing.T) {
	policy := &testEntityMetaValueGuardPolicy{subject: entitymeta.ValueGuardSubject{
		EntityType: entitymeta.EntityTopic, EntityID: 7, OwnerUserID: 42, Exists: true,
		Fields: map[string]entitymeta.ValueGuardField{"profile.note": {FieldKey: "profile.note", Visibility: entitymeta.VisibilityOwner, Enabled: true}},
	}}
	authorizer := entityMetaValueAuthorizer(policy)
	plan, step := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", entitymeta.EntityTopic)
	request := productionGuardRequest(identity.PermissionTopicEditOwn)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	request.Body = []byte(`{"values":[{"fieldKey":"profile.note","value":"x"}]}`)
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatal(err)
	}
	policy.subject.OwnerUserID = 99
	policy.subject.Fields["profile.note"] = entitymeta.ValueGuardField{FieldKey: "profile.note", Visibility: entitymeta.VisibilityAdmin, Enabled: true}
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("stale authority error = %v", err)
	}
	if policy.calls != 2 {
		t.Fatalf("authority loads = %d, want 2", policy.calls)
	}
}

type testEntityMetaValueGuardPolicy struct {
	subject entitymeta.ValueGuardSubject
	err     error
	calls   int
}

func (p *testEntityMetaValueGuardPolicy) LoadValueGuardSubject(_ context.Context, _ string, _ int64, fieldKeys []string) (entitymeta.ValueGuardSubject, error) {
	p.calls++
	subject := p.subject
	if len(fieldKeys) > 0 {
		subject.Fields = make(map[string]entitymeta.ValueGuardField, len(fieldKeys))
		for _, key := range fieldKeys {
			if field, ok := p.subject.Fields[key]; ok {
				subject.Fields[key] = field
			}
		}
	}
	return subject, p.err
}

func entityMetaValueAuthorizer(policy EntityMetaValueGuardPolicy) ProductionRouteGuardAuthorizer {
	return NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{EntityMetaValues: policy})
}

func productionEntityMetaValuePlan(t *testing.T, routeID, entityType string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == routeID {
			target = route
			break
		}
	}
	return productionParameterizedInheritedGuardPlan(
		t, target,
		"/guard/production/entity-meta/:entityType/:entityID",
		"/guard/production/entity-meta/"+entityType+"/7",
	)
}
