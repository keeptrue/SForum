package http

import (
	"context"
	"errors"
	"testing"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionAttachmentReadGuardAllowsCurrentResourceAuthority(t *testing.T) {
	publicForum := attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
		attachmentGuardReference("topic", "active", "active", "public", 42, true))
	tests := []struct {
		name        string
		routeID     string
		subject     attachments.ReadGuardSubject
		guestRead   string
		actorID     int64
		permissions []string
	}{
		{name: "site asset anonymous", routeID: "core.route.attachments.get", guestRead: "login_required",
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference(attachments.ResourceTypeSite, "", "", "", 0, true))},
		{name: "public forum anonymous", routeID: "core.route.attachments.content", guestRead: "public", subject: publicForum},
		{name: "public forum display variant anonymous", routeID: "core.route.attachments.variant_content", guestRead: "public", subject: publicForum},
		{name: "protected forum member", routeID: "core.route.attachments.get", guestRead: "login_required", actorID: 7, subject: publicForum},
		{name: "pending forum author", routeID: "core.route.attachments.get", guestRead: "login_required", actorID: 42,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "pending", "pending", "public", 42, true))},
		{name: "hidden forum moderator", routeID: "core.route.attachments.content", guestRead: "login_required", actorID: 7,
			permissions: []string{identity.PermissionModerationReview},
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "active", "active", "private", 42, true))},
		{name: "private owner", routeID: "core.route.attachments.get", actorID: 42,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPrivate)},
		{name: "disabled manager", routeID: "core.route.attachments.content", actorID: 7,
			permissions: []string{identity.PermissionAttachmentManage},
			subject:     attachmentReadSubject(42, attachments.StatusDisabled, attachments.VisibilityPrivate)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &testAttachmentReadGuardPolicy{subject: test.subject}
			authorizer := attachmentReadAuthorizer(policy, test.guestRead)
			plan, step := productionAttachmentReadPlan(t, test.routeID, test.subject.PublicID)
			request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
			if test.actorID > 0 {
				request = productionGuardRequest(test.permissions...)
				request.ActorID = test.actorID
				request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); err != nil {
				t.Fatal(err)
			}
			if policy.calls != 1 || policy.publicID != test.subject.PublicID {
				t.Fatalf("policy calls=%d publicID=%q", policy.calls, policy.publicID)
			}
		})
	}
}

func TestProductionAttachmentReadGuardRejectsUnauthorizedResourceState(t *testing.T) {
	tests := []struct {
		name        string
		routeID     string
		subject     attachments.ReadGuardSubject
		guestRead   string
		actorID     int64
		permissions []string
		want        error
	}{
		{name: "protected forum guest", routeID: "core.route.attachments.get", guestRead: "login_required", want: ErrRouteLoginRequired,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "active", "active", "public", 42, true))},
		{name: "protected forum display variant guest", routeID: "core.route.attachments.variant_content", guestRead: "login_required", want: ErrRouteLoginRequired,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "active", "active", "public", 42, true))},
		{name: "private foreign", routeID: "core.route.attachments.get", actorID: 7, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPrivate)},
		{name: "disabled owner", routeID: "core.route.attachments.get", actorID: 42, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusDisabled, attachments.VisibilityPrivate)},
		{name: "pending foreign", routeID: "core.route.attachments.get", actorID: 7, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "pending", "pending", "public", 42, true))},
		{name: "hidden category member", routeID: "core.route.attachments.get", actorID: 7, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "active", "active", "private", 42, true))},
		{name: "missing forum resource", routeID: "core.route.attachments.get", actorID: 7, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
				attachmentGuardReference("topic", "active", "active", "public", 42, false))},
		{name: "unreferenced public foreign", routeID: "core.route.attachments.get", actorID: 7, want: ErrRoutePermissionDenied,
			subject: attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := attachmentReadAuthorizer(&testAttachmentReadGuardPolicy{subject: test.subject}, test.guestRead)
			plan, step := productionAttachmentReadPlan(t, test.routeID, test.subject.PublicID)
			request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
			if test.actorID > 0 {
				request = productionGuardRequest(test.permissions...)
				request.ActorID = test.actorID
				request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestProductionAttachmentReadGuardFailsClosedOnForgedOrMissingState(t *testing.T) {
	subject := attachmentReadSubject(42, attachments.StatusActive, attachments.VisibilityPublic,
		attachmentGuardReference("topic", "active", "active", "public", 42, true))
	tests := []struct {
		name   string
		policy *testAttachmentReadGuardPolicy
		mutate func(*routes.DispatchRequest)
	}{
		{name: "missing policy"},
		{name: "lookup failure", policy: &testAttachmentReadGuardPolicy{err: errors.New("database unavailable")}},
		{name: "missing resource", policy: &testAttachmentReadGuardPolicy{subject: attachments.ReadGuardSubject{}}},
		{name: "foreign subject", policy: &testAttachmentReadGuardPolicy{subject: func() attachments.ReadGuardSubject {
			value := subject
			value.PublicID = "foreign"
			return value
		}()}},
		{name: "body", policy: &testAttachmentReadGuardPolicy{subject: subject}, mutate: func(request *routes.DispatchRequest) { request.Body = []byte(`{}`) }},
		{name: "query", policy: &testAttachmentReadGuardPolicy{subject: subject}, mutate: func(request *routes.DispatchRequest) { request.Query = "download=1" }},
		{name: "forged params", policy: &testAttachmentReadGuardPolicy{subject: subject}, mutate: func(request *routes.DispatchRequest) { request.Params["publicId"] = "forged" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := attachmentReadAuthorizer(test.policy, "public")
			plan, step := productionAttachmentReadPlan(t, "core.route.attachments.content", subject.PublicID)
			request := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
			if test.mutate != nil {
				test.mutate(&request)
			}
			if err := authorizer.Authorize(context.Background(), plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

type testAttachmentReadGuardPolicy struct {
	subject  attachments.ReadGuardSubject
	err      error
	calls    int
	publicID string
}

func (p *testAttachmentReadGuardPolicy) LoadReadGuardSubject(_ context.Context, publicID string) (attachments.ReadGuardSubject, error) {
	if p == nil {
		return attachments.ReadGuardSubject{}, errors.New("attachment read guard policy unavailable")
	}
	p.calls++
	p.publicID = publicID
	return p.subject, p.err
}

func attachmentReadAuthorizer(policy AttachmentReadGuardPolicy, guestRead string) ProductionRouteGuardAuthorizer {
	return NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
		AttachmentReads: policy,
		ForumRead:       &testForumReadPolicy{guestRead: guestRead, softDeleteVisibility: "staff_only", revision: 1, ok: true},
	})
}

func productionAttachmentReadPlan(t *testing.T, routeID, publicID string) (routes.RouteExecutionPlan, routes.RouteExecutionStep) {
	t.Helper()
	var target routes.CoreRoute
	for _, route := range routes.CoreRouteCatalog() {
		if route.ID == routeID {
			target = route
			break
		}
	}
	if target.ID == "" {
		t.Fatalf("attachment route %q is missing", routeID)
	}
	if routeID == "core.route.attachments.variant_content" {
		return productionParameterizedInheritedGuardPlan(
			t,
			target,
			"/guard/production/attachments/:publicId/variants/:variant/content",
			"/guard/production/attachments/"+publicID+"/variants/"+attachments.CompressionVariantDisplay+"/content",
		)
	}
	return productionParameterizedInheritedGuardPlan(
		t, target, "/guard/production/attachments/:publicId", "/guard/production/attachments/"+publicID,
	)
}

func attachmentReadSubject(ownerID int64, status, visibility string, references ...attachments.ReferenceAccess) attachments.ReadGuardSubject {
	return attachments.ReadGuardSubject{
		PublicID: "attachment-public-id", OwnerUserID: ownerID, Status: status,
		Visibility: visibility, Exists: true, References: references,
	}
}

func attachmentGuardReference(resourceType, resourceStatus, topicStatus, visibility string, authorID int64, exists bool) attachments.ReferenceAccess {
	contextName := "inline"
	if resourceType == attachments.ResourceTypeSite {
		contextName = attachments.ContextLogo
	}
	return attachments.ReferenceAccess{
		AttachmentReference: attachments.AttachmentReference{
			ID: 1, AttachmentID: 1, ResourceType: resourceType, ResourceID: 1, Context: contextName,
		},
		AuthorUserID: authorID, ResourceStatus: resourceStatus, TopicStatus: topicStatus,
		CategoryVisibility: visibility, Exists: exists,
	}
}
