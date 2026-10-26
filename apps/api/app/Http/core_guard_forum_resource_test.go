package http

import (
	"context"
	"errors"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionForumResourceGuardAllowsGlobalAndOwnedAuthority(t *testing.T) {
	tests := []struct {
		name        string
		routeID     string
		resourceID  string
		actorID     int64
		permissions []string
		topic       *forum.TopicResourceGuardSubject
		comment     *forum.CommentResourceGuardSubject
	}{
		{
			name: "topic edit global", routeID: "core.route.forum.update_topic", resourceID: "7",
			permissions: []string{identity.PermissionTopicEditAny},
		},
		{
			name: "topic delete global", routeID: "core.route.forum.delete_topic", resourceID: "7",
			permissions: []string{identity.PermissionTopicDeleteAny},
		},
		{
			name: "topic edit super admin", routeID: "core.route.forum.update_topic", resourceID: "7",
			permissions: []string{"*"},
		},
		{
			name: "topic edit owner", routeID: "core.route.forum.update_topic", resourceID: "7", actorID: 42,
			permissions: []string{identity.PermissionTopicEditOwn},
			topic:       &forum.TopicResourceGuardSubject{TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusActive, Exists: true},
		},
		{
			name: "topic delete owner", routeID: "core.route.forum.delete_topic", resourceID: "7", actorID: 42,
			permissions: []string{identity.PermissionTopicDeleteOwn},
			topic:       &forum.TopicResourceGuardSubject{TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusLocked, Exists: true},
		},
		{
			name: "comment edit global", routeID: "core.route.forum.update_comment", resourceID: "9",
			permissions: []string{identity.PermissionPostEditAny},
		},
		{
			name: "comment delete global", routeID: "core.route.forum.delete_comment", resourceID: "9",
			permissions: []string{identity.PermissionPostDeleteAny},
		},
		{
			name: "comment edit owner", routeID: "core.route.forum.update_comment", resourceID: "9", actorID: 42,
			permissions: []string{identity.PermissionPostEditOwn},
			comment:     &forum.CommentResourceGuardSubject{CommentID: 9, AuthorUserID: 42, Status: forum.CommentStatusActive, Exists: true},
		},
		{
			name: "comment delete owner", routeID: "core.route.forum.delete_comment", resourceID: "9", actorID: 42,
			permissions: []string{identity.PermissionPostDeleteOwn},
			comment:     &forum.CommentResourceGuardSubject{CommentID: 9, AuthorUserID: 42, Status: forum.CommentStatusPending, Exists: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &testForumResourceGuardPolicy{}
			if test.topic != nil {
				policy.topic = *test.topic
			}
			if test.comment != nil {
				policy.comment = *test.comment
			}
			authorizer := forumResourceAuthorizer(policy)
			plan, step := productionForumResourcePlan(t, test.routeID, test.resourceID)
			request := productionGuardRequest(test.permissions...)
			if test.actorID > 0 {
				request.ActorID = test.actorID
			}
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
			// 全局 any/* 路径不得触发 Store 读取。
			if test.topic == nil && test.comment == nil {
				if policy.topicCalls != 0 || policy.commentCalls != 0 {
					t.Fatalf("global authority performed store loads topic=%d comment=%d", policy.topicCalls, policy.commentCalls)
				}
				return
			}
			if test.topic != nil && (policy.topicCalls != 1 || policy.topicID != 7) {
				t.Fatalf("topic loads=%d id=%d", policy.topicCalls, policy.topicID)
			}
			if test.comment != nil && (policy.commentCalls != 1 || policy.commentID != 9) {
				t.Fatalf("comment loads=%d id=%d", policy.commentCalls, policy.commentID)
			}
		})
	}
}

func TestProductionForumResourceGuardRejectsForeignAnonymousAndMissingAuthority(t *testing.T) {
	ownedTopic := forum.TopicResourceGuardSubject{TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusActive, Exists: true}
	ownedComment := forum.CommentResourceGuardSubject{CommentID: 9, AuthorUserID: 42, Status: forum.CommentStatusActive, Exists: true}
	tests := []struct {
		name        string
		routeID     string
		resourceID  string
		actorID     int64
		permissions []string
		policy      *testForumResourceGuardPolicy
		anonymous   bool
		want        error
		wantLoads   int
	}{
		{
			name: "topic foreign owner", routeID: "core.route.forum.update_topic", resourceID: "7", actorID: 99,
			permissions: []string{identity.PermissionTopicEditOwn},
			policy:      &testForumResourceGuardPolicy{topic: ownedTopic},
			want:        ErrRoutePermissionDenied, wantLoads: 1,
		},
		{
			name: "topic missing own permission", routeID: "core.route.forum.update_topic", resourceID: "7",
			permissions: []string{identity.PermissionTopicDeleteOwn},
			policy:      &testForumResourceGuardPolicy{topic: ownedTopic},
			want:        ErrRoutePermissionDenied,
		},
		{
			name: "topic delete wrong own key", routeID: "core.route.forum.delete_topic", resourceID: "7",
			permissions: []string{identity.PermissionTopicEditOwn},
			policy:      &testForumResourceGuardPolicy{topic: ownedTopic},
			want:        ErrRoutePermissionDenied,
		},
		{
			name: "comment foreign owner", routeID: "core.route.forum.delete_comment", resourceID: "9", actorID: 7,
			permissions: []string{identity.PermissionPostDeleteOwn},
			policy:      &testForumResourceGuardPolicy{comment: ownedComment},
			want:        ErrRoutePermissionDenied, wantLoads: 1,
		},
		{
			name: "comment edit wrong own key", routeID: "core.route.forum.update_comment", resourceID: "9",
			permissions: []string{identity.PermissionPostDeleteOwn},
			policy:      &testForumResourceGuardPolicy{comment: ownedComment},
			want:        ErrRoutePermissionDenied,
		},
		{
			name: "topic anonymous", routeID: "core.route.forum.update_topic", resourceID: "7", anonymous: true,
			policy: &testForumResourceGuardPolicy{topic: ownedTopic}, want: ErrRouteLoginRequired,
		},
		{
			name: "comment anonymous", routeID: "core.route.forum.update_comment", resourceID: "9", anonymous: true,
			policy: &testForumResourceGuardPolicy{comment: ownedComment}, want: ErrRouteLoginRequired,
		},
		{
			name: "stale actor", routeID: "core.route.forum.update_topic", resourceID: "7",
			permissions: []string{identity.PermissionTopicEditOwn},
			policy:      &testForumResourceGuardPolicy{topic: ownedTopic},
			want:        ErrRouteLoginRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := forumResourceAuthorizer(test.policy)
			plan, step := productionForumResourcePlan(t, test.routeID, test.resourceID)
			var request routes.DispatchRequest
			if test.anonymous {
				request = routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
			} else if test.name == "stale actor" {
				request = productionGuardRequest(test.permissions...)
				request.ActorID = 0
				request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			} else {
				request = productionGuardRequest(test.permissions...)
				if test.actorID > 0 {
					request.ActorID = test.actorID
				}
				request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			loads := test.policy.topicCalls + test.policy.commentCalls
			if loads != test.wantLoads {
				t.Fatalf("store loads=%d want=%d", loads, test.wantLoads)
			}
		})
	}
}

func TestProductionForumResourceGuardFailsClosedOnMissingDeletedAndContract(t *testing.T) {
	ownedTopic := forum.TopicResourceGuardSubject{TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusActive, Exists: true}
	ownedComment := forum.CommentResourceGuardSubject{CommentID: 9, AuthorUserID: 42, Status: forum.CommentStatusActive, Exists: true}
	tests := []struct {
		name       string
		routeID    string
		resourceID string
		permission string
		policy     *testForumResourceGuardPolicy
		mutate     func(*routes.DispatchRequest)
		wantLoads  int
	}{
		{
			name: "topic missing policy", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
		},
		{
			name: "topic lookup failure", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
			policy:     &testForumResourceGuardPolicy{topicErr: errors.New("database unavailable")},
			wantLoads:  1,
		},
		{
			name: "topic missing", routeID: "core.route.forum.delete_topic", resourceID: "7",
			permission: identity.PermissionTopicDeleteOwn,
			policy:     &testForumResourceGuardPolicy{topicErr: forum.ErrTopicNotFound},
			wantLoads:  1,
		},
		{
			name: "topic deleted", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
			policy: &testForumResourceGuardPolicy{topic: forum.TopicResourceGuardSubject{
				TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusDeleted, Exists: true,
			}},
			wantLoads: 1,
		},
		{
			name: "topic foreign subject", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
			policy: &testForumResourceGuardPolicy{topic: forum.TopicResourceGuardSubject{
				TopicID: 8, AuthorUserID: 42, Status: forum.TopicStatusActive, Exists: true,
			}},
			wantLoads: 1,
		},
		{
			name: "topic invalid id", routeID: "core.route.forum.update_topic", resourceID: "invalid",
			permission: identity.PermissionTopicEditOwn,
			policy:     &testForumResourceGuardPolicy{topic: ownedTopic},
		},
		{
			name: "topic zero id", routeID: "core.route.forum.update_topic", resourceID: "0",
			permission: identity.PermissionTopicEditOwn,
			policy:     &testForumResourceGuardPolicy{topic: ownedTopic},
		},
		{
			name: "topic forged params", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
			policy:     &testForumResourceGuardPolicy{topic: ownedTopic},
			mutate:     func(request *routes.DispatchRequest) { request.Params["topicID"] = "8" },
		},
		{
			name: "topic query", routeID: "core.route.forum.update_topic", resourceID: "7",
			permission: identity.PermissionTopicEditOwn,
			policy:     &testForumResourceGuardPolicy{topic: ownedTopic},
			mutate:     func(request *routes.DispatchRequest) { request.Query = "ownerId=42" },
		},
		{
			name: "comment missing", routeID: "core.route.forum.update_comment", resourceID: "9",
			permission: identity.PermissionPostEditOwn,
			policy:     &testForumResourceGuardPolicy{commentErr: forum.ErrCommentNotFound},
			wantLoads:  1,
		},
		{
			name: "comment deleted", routeID: "core.route.forum.delete_comment", resourceID: "9",
			permission: identity.PermissionPostDeleteOwn,
			policy: &testForumResourceGuardPolicy{comment: forum.CommentResourceGuardSubject{
				CommentID: 9, AuthorUserID: 42, Status: forum.CommentStatusDeleted, Exists: true,
			}},
			wantLoads: 1,
		},
		{
			name: "comment foreign subject", routeID: "core.route.forum.update_comment", resourceID: "9",
			permission: identity.PermissionPostEditOwn,
			policy: &testForumResourceGuardPolicy{comment: forum.CommentResourceGuardSubject{
				CommentID: 99, AuthorUserID: 42, Status: forum.CommentStatusActive, Exists: true,
			}},
			wantLoads: 1,
		},
		{
			name: "comment invalid id", routeID: "core.route.forum.delete_comment", resourceID: "bad",
			permission: identity.PermissionPostDeleteOwn,
			policy:     &testForumResourceGuardPolicy{comment: ownedComment},
		},
		{
			name: "comment forged params", routeID: "core.route.forum.update_comment", resourceID: "9",
			permission: identity.PermissionPostEditOwn,
			policy:     &testForumResourceGuardPolicy{comment: ownedComment},
			mutate:     func(request *routes.DispatchRequest) { request.Params["commentID"] = "1" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var policy ForumResourceGuardPolicy
			if test.policy != nil {
				policy = test.policy
			}
			authorizer := forumResourceAuthorizer(policy)
			plan, step := productionForumResourcePlan(t, test.routeID, test.resourceID)
			request := productionGuardRequest(test.permission)
			request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			if test.mutate != nil {
				test.mutate(&request)
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if test.policy != nil {
				loads := test.policy.topicCalls + test.policy.commentCalls
				if loads != test.wantLoads {
					t.Fatalf("store loads=%d want=%d", loads, test.wantLoads)
				}
			}
		})
	}
}

func TestProductionForumResourceGuardReloadsOwnershipEveryRequest(t *testing.T) {
	policy := &testForumResourceGuardPolicy{topic: forum.TopicResourceGuardSubject{
		TopicID: 7, AuthorUserID: 42, Status: forum.TopicStatusActive, Exists: true,
	}}
	authorizer := forumResourceAuthorizer(policy)
	plan, step := productionForumResourcePlan(t, "core.route.forum.update_topic", "7")
	request := productionGuardRequest(identity.PermissionTopicEditOwn)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
		t.Fatal(err)
	}
	policy.topic.AuthorUserID = 99
	if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("stale ownership error=%v", err)
	}
	if policy.topicCalls != 2 {
		t.Fatalf("topic ownership loads=%d want 2", policy.topicCalls)
	}
}

func TestProductionForumResourceGuardPartitionsCatalogRoutes(t *testing.T) {
	expected := map[string]string{
		"core.route.forum.update_topic":   "core.guard.forum.topic_edit",
		"core.route.forum.delete_topic":   "core.guard.forum.topic_delete",
		"core.route.forum.update_comment": "core.guard.forum.comment_write",
		"core.route.forum.delete_comment": "core.guard.forum.comment_write",
	}
	for _, route := range routes.CoreRouteCatalog() {
		evaluatorID, ok := expected[route.ID]
		if !ok {
			continue
		}
		if route.Guard.EvaluatorID != evaluatorID || route.Guard.Kind != routes.CoreGuardContextual {
			t.Fatalf("unexpected forum resource route = %#v", route)
		}
		delete(expected, route.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("missing forum resource guard routes = %#v", expected)
	}
}

type testForumResourceGuardPolicy struct {
	topic        forum.TopicResourceGuardSubject
	topicErr     error
	topicCalls   int
	topicID      int64
	comment      forum.CommentResourceGuardSubject
	commentErr   error
	commentCalls int
	commentID    int64
}

func (p *testForumResourceGuardPolicy) LoadTopicResourceGuardSubject(_ context.Context, topicID int64) (forum.TopicResourceGuardSubject, error) {
	p.topicCalls++
	p.topicID = topicID
	return p.topic, p.topicErr
}

func (p *testForumResourceGuardPolicy) LoadCommentResourceGuardSubject(_ context.Context, commentID int64) (forum.CommentResourceGuardSubject, error) {
	p.commentCalls++
	p.commentID = commentID
	return p.comment, p.commentErr
}

func forumResourceAuthorizer(policy ForumResourceGuardPolicy) ProductionRouteGuardAuthorizer {
	return NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumResources: policy})
}

func productionForumResourcePlan(t *testing.T, routeID, resourceID string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == routeID {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatalf("forum resource route %s is missing", routeID)
	}
	switch routeID {
	case "core.route.forum.update_topic", "core.route.forum.delete_topic":
		return productionParameterizedInheritedGuardPlan(
			t, target, "/guard/production/topics/:topicID", "/guard/production/topics/"+resourceID,
		)
	case "core.route.forum.update_comment", "core.route.forum.delete_comment":
		return productionParameterizedInheritedGuardPlan(
			t, target, "/guard/production/comments/:commentID", "/guard/production/comments/"+resourceID,
		)
	default:
		t.Fatalf("unsupported forum resource route %s", routeID)
		return routes.RouteExecutionPlan{}, routes.RouteExecutionStep{}
	}
}
