package http

import (
	"context"
	"errors"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionForumCommentCreateGuardRequiresPermissionAndActiveTopic(t *testing.T) {
	policy := &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{
		TopicID: 7, Status: forum.TopicStatusActive, Exists: true,
	}}
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumComments: policy})
	plan, step := productionForumCommentCreatePlan(t, "7")
	allowed := productionGuardRequest(identity.PermissionPostCreate)
	allowed.Method, allowed.Path, allowed.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(context.Background(), plan, step, allowed); err != nil {
		t.Fatal(err)
	}
	if policy.calls != 1 || policy.topicID != 7 {
		t.Fatalf("policy calls=%d topicID=%d", policy.calls, policy.topicID)
	}

	for name, request := range map[string]routes.DispatchRequest{
		"anonymous": {Method: plan.Method(), Path: plan.Path(), Params: plan.Params()},
		"denied": func() routes.DispatchRequest {
			value := productionGuardRequest(identity.PermissionTopicCreate)
			value.Method, value.Path, value.Params = plan.Method(), plan.Path(), plan.Params()
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			want := ErrRoutePermissionDenied
			if name == "anonymous" {
				want = ErrRouteLoginRequired
			}
			before := policy.calls
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			if policy.calls != before {
				t.Fatal("topic lookup ran before global permission rejection")
			}
		})
	}
}

func TestProductionForumCommentCreateGuardRejectsClosedOrForgedTopic(t *testing.T) {
	tests := []struct {
		name    string
		topicID string
		policy  *testForumCommentCreateGuardPolicy
		mutate  func(*routes.DispatchRequest)
		want    error
	}{
		{name: "locked", topicID: "7", policy: &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{TopicID: 7, Status: forum.TopicStatusLocked, Exists: true}}, want: ErrRoutePermissionDenied},
		{name: "pending", topicID: "7", policy: &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{TopicID: 7, Status: forum.TopicStatusPending, Exists: true}}, want: ErrRoutePermissionDenied},
		{name: "deleted", topicID: "7", policy: &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{TopicID: 7, Status: forum.TopicStatusDeleted, Exists: true}}, want: ErrRoutePermissionDenied},
		{name: "missing policy", topicID: "7", want: ErrRouteGuardUnavailable},
		{name: "lookup failure", topicID: "7", policy: &testForumCommentCreateGuardPolicy{err: errors.New("database unavailable")}, want: ErrRouteGuardUnavailable},
		{name: "missing topic", topicID: "7", policy: &testForumCommentCreateGuardPolicy{}, want: ErrRouteGuardUnavailable},
		{name: "foreign subject", topicID: "7", policy: &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{TopicID: 8, Status: forum.TopicStatusActive, Exists: true}}, want: ErrRouteGuardUnavailable},
		{name: "invalid id", topicID: "invalid", policy: &testForumCommentCreateGuardPolicy{}, want: ErrRouteGuardUnavailable},
		{name: "zero id", topicID: "0", policy: &testForumCommentCreateGuardPolicy{}, want: ErrRouteGuardUnavailable},
		{name: "forged params", topicID: "7", policy: &testForumCommentCreateGuardPolicy{subject: forum.CommentCreateGuardSubject{TopicID: 7, Status: forum.TopicStatusActive, Exists: true}}, mutate: func(request *routes.DispatchRequest) { request.Params["topicID"] = "8" }, want: ErrRouteGuardUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var policy ForumCommentCreateGuardPolicy
			if test.policy != nil {
				policy = test.policy
			}
			authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumComments: policy})
			plan, step := productionForumCommentCreatePlan(t, test.topicID)
			request := productionGuardRequest(identity.PermissionPostCreate)
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			if test.mutate != nil {
				test.mutate(&request)
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

type testForumCommentCreateGuardPolicy struct {
	subject forum.CommentCreateGuardSubject
	err     error
	calls   int
	topicID int64
}

func (p *testForumCommentCreateGuardPolicy) LoadCommentCreateGuardSubject(_ context.Context, topicID int64) (forum.CommentCreateGuardSubject, error) {
	p.calls++
	p.topicID = topicID
	return p.subject, p.err
}

func productionForumCommentCreatePlan(t *testing.T, topicID string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == "core.route.forum.create_comment" {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatal("forum create-comment route is missing")
	}
	return productionParameterizedInheritedGuardPlan(
		t, target, "/guard/production/topics/:topicID/comments", "/guard/production/topics/"+topicID+"/comments",
	)
}
