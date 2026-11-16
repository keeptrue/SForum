package moderation

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestCreateReportAllowsActiveUser(t *testing.T) {
	store := &fakeStore{}
	validator := &fakeValidator{topic: true}
	service := NewService(store, validator)
	actor := identity.Actor{ID: 5, Status: identity.UserStatusActive}

	report, err := service.CreateReport(context.Background(), actor, CreateReportInput{
		TargetType: TargetTypeTopic,
		TargetID:   10,
		ReasonCode: ReasonSpam,
		Body:       "spam content",
	})
	if err != nil {
		t.Fatalf("expected create success, got %v", err)
	}
	if report.TargetType != TargetTypeTopic || report.TargetID != 10 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if store.createdInput.ReporterUserID != 5 {
		t.Fatalf("expected reporter 5, got %d", store.createdInput.ReporterUserID)
	}
}

func TestCreateReportRejectsUnauthenticated(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeValidator{topic: true})
	_, err := service.CreateReport(context.Background(), identity.Actor{ID: 0}, CreateReportInput{
		TargetType: TargetTypeTopic, TargetID: 1, ReasonCode: ReasonSpam,
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestCreateReportRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		input CreateReportInput
	}{
		{name: "bad target type", input: CreateReportInput{TargetType: "user", TargetID: 1, ReasonCode: ReasonSpam}},
		{name: "bad reason", input: CreateReportInput{TargetType: TargetTypeTopic, TargetID: 1, ReasonCode: "bogus"}},
		{name: "zero target id", input: CreateReportInput{TargetType: TargetTypeTopic, TargetID: 0, ReasonCode: ReasonSpam}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := NewService(&fakeStore{}, &fakeValidator{topic: true})
			_, err := service.CreateReport(context.Background(), identity.Actor{ID: 5, Status: identity.UserStatusActive}, tc.input)
			if !errors.Is(err, ErrReportInvalid) {
				t.Fatalf("expected ErrReportInvalid, got %v", err)
			}
		})
	}
}

func TestCreateReportRejectsUnreportableTarget(t *testing.T) {
	service := NewService(&fakeStore{}, &fakeValidator{topic: false})
	_, err := service.CreateReport(context.Background(), identity.Actor{ID: 5, Status: identity.UserStatusActive}, CreateReportInput{
		TargetType: TargetTypeTopic, TargetID: 99, ReasonCode: ReasonSpam,
	})
	if !errors.Is(err, ErrReportTargetInvalid) {
		t.Fatalf("expected ErrReportTargetInvalid, got %v", err)
	}
}

func TestCreateReportPropagatesDuplicateError(t *testing.T) {
	store := &fakeStore{createErr: ErrReportDuplicate}
	service := NewService(store, &fakeValidator{topic: true})
	_, err := service.CreateReport(context.Background(), identity.Actor{ID: 5, Status: identity.UserStatusActive}, CreateReportInput{
		TargetType: TargetTypeTopic, TargetID: 10, ReasonCode: ReasonSpam,
	})
	if !errors.Is(err, ErrReportDuplicate) {
		t.Fatalf("expected ErrReportDuplicate, got %v", err)
	}
}

func TestListReportsRequiresReviewPermission(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, nil)
	_, err := service.ListReports(context.Background(), identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{}}, ReportListInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}

	reviewer := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}}
	list, err := service.ListReports(context.Background(), reviewer, ReportListInput{Page: 0, PerPage: 0})
	if err != nil {
		t.Fatalf("expected list success, got %v", err)
	}
	if list.Page != 1 || list.PerPage != 20 {
		t.Fatalf("expected normalized pagination, got page=%d perPage=%d", list.Page, list.PerPage)
	}
}

func TestUpdateReportRequiresReviewPermission(t *testing.T) {
	service := NewService(&fakeStore{}, nil)
	_, err := service.UpdateReport(context.Background(), identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{}}, UpdateReportInput{ReportID: 1, Status: StatusResolved})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestUpdateReportRejectsInvalidStatus(t *testing.T) {
	service := NewService(&fakeStore{}, nil)
	reviewer := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}}
	_, err := service.UpdateReport(context.Background(), reviewer, UpdateReportInput{ReportID: 1, Status: "bogus"})
	if !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("expected ErrReportInvalid, got %v", err)
	}
}

func TestUpdateReportRejectsNegativeExpectedRevision(t *testing.T) {
	service := NewService(&fakeStore{}, nil)
	reviewer := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}}
	_, err := service.UpdateReport(context.Background(), reviewer, UpdateReportInput{ReportID: 1, Status: StatusReviewing, ExpectedRevision: -1})
	if !errors.Is(err, ErrReportInvalid) {
		t.Fatalf("expected ErrReportInvalid, got %v", err)
	}
}

// 改派审核人是管理动作：review 只能认领空缺，manage 才能覆盖已有归属。
func TestUpdateReportGrantsReviewerOverrideOnlyToModerationManage(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, nil)
	reviewer := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}}

	if _, err := service.UpdateReport(context.Background(), reviewer, UpdateReportInput{ReportID: 1, Status: StatusReviewing}); err != nil {
		t.Fatalf("review-only update: %v", err)
	}
	if store.updatedInput.ReviewerUserID != reviewer.ID {
		t.Fatalf("reviewer attribution = %d, want %d", store.updatedInput.ReviewerUserID, reviewer.ID)
	}
	if store.updatedInput.AllowReviewerOverride {
		t.Fatal("moderation.review must not be allowed to reassign the reviewer")
	}

	manager := identity.Actor{ID: 11, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionModerationReview: true,
		identity.PermissionModerationManage: true,
	}}
	if _, err := service.UpdateReport(context.Background(), manager, UpdateReportInput{ReportID: 1, Status: StatusReviewing}); err != nil {
		t.Fatalf("manage update: %v", err)
	}
	if !store.updatedInput.AllowReviewerOverride {
		t.Fatal("moderation.manage must be allowed to reassign the reviewer")
	}
}

func TestUpdateReportPropagatesTerminalAndRevisionConflict(t *testing.T) {
	reviewer := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReportReview: true}}
	for _, want := range []error{ErrReportTerminal, ErrReportConflict} {
		store := &fakeStore{updateErr: want}
		service := NewService(store, nil)
		_, err := service.UpdateReport(context.Background(), reviewer, UpdateReportInput{ReportID: 1, Status: StatusResolved})
		if !errors.Is(err, want) {
			t.Fatalf("expected %v to reach the caller, got %v", want, err)
		}
	}
}

func TestModerationManagementAndReviewPermissionsRemainIndependent(t *testing.T) {
	store := &fakeWorkbenchStore{}
	service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store)
	manager := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationManage: true}}
	reviewer := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}

	if _, err := service.GetSettings(context.Background(), reviewer); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("review permission must not grant settings access: %v", err)
	}
	if _, err := service.QueueCounts(context.Background(), manager); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("manage permission must not grant review access: %v", err)
	}
	if _, err := service.GetSettings(context.Background(), manager); err != nil {
		t.Fatalf("manager should read settings: %v", err)
	}
	if _, err := service.QueueCounts(context.Background(), reviewer); err != nil {
		t.Fatalf("reviewer should read queue counts: %v", err)
	}
}

func TestSubmitDecisionRequiresNoteForDestructiveActions(t *testing.T) {
	store := &fakeWorkbenchStore{}
	service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store)
	reviewer := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}

	for _, action := range []string{ActionReject, ActionHideAndClose, ActionDeleteAndClose} {
		_, err := service.SubmitDecision(context.Background(), reviewer, DecisionInput{
			Source: SourcePrePublish, TargetType: TargetTypeTopic, TargetID: 10, Action: action,
		})
		if !errors.Is(err, ErrDecisionInvalid) {
			t.Fatalf("action %q should require a note, got %v", action, err)
		}
	}
}

func TestSubmitDecisionPropagatesTaskConflict(t *testing.T) {
	store := &fakeWorkbenchStore{decisionErr: ErrTaskConflict}
	service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store)
	reviewer := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}
	_, err := service.SubmitDecision(context.Background(), reviewer, DecisionInput{
		Source: SourcePrePublish, TargetType: TargetTypeTopic, TargetID: 10, Action: ActionApprove,
	})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("expected task conflict, got %v", err)
	}
}

func TestSubmitDecisionRefreshesSearchDerivatives(t *testing.T) {
	store := &fakeWorkbenchStore{reviewContext: ReviewContext{
		TargetType: TargetTypeComment, TargetID: 10, TopicID: 77,
		IPAddress: "203.0.113.1", LastEditIP: "203.0.113.2",
	}}
	indexer := &fakeDecisionIndexer{}
	service := NewServiceWithWorkbenchIndexer(&fakeStore{}, nil, store, store, indexer)
	reviewer := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}

	_, err := service.SubmitDecision(context.Background(), reviewer, DecisionInput{
		Source: SourcePrePublish, TargetType: TargetTypeComment, TargetID: 10, Action: ActionApprove,
	})
	if err != nil {
		t.Fatalf("submit decision: %v", err)
	}
	if len(indexer.indexed) != 1 || indexer.indexed[0] != 77 {
		t.Fatalf("approved comment should reindex topic 77, got %v", indexer.indexed)
	}
}

func TestSubmitDecisionInvalidatesPublishedForumReadModels(t *testing.T) {
	store := &fakeWorkbenchStore{reviewContext: ReviewContext{TargetType: TargetTypeComment, TargetID: 10, TopicID: 77}}
	invalidator := &fakeDecisionReadModelInvalidator{}
	service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store).WithDecisionReadModelInvalidator(invalidator)
	reviewer := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}

	if _, err := service.SubmitDecision(context.Background(), reviewer, DecisionInput{Source: SourcePrePublish, TargetType: TargetTypeComment, TargetID: 10, Action: ActionApprove}); err != nil {
		t.Fatalf("approve comment: %v", err)
	}
	if invalidator.topicID != 77 || invalidator.targetType != TargetTypeComment || invalidator.calls != 1 {
		t.Fatalf("approval invalidation = %+v", invalidator)
	}
	if _, err := service.SubmitDecision(context.Background(), reviewer, DecisionInput{Source: SourcePrePublish, TargetType: TargetTypeComment, TargetID: 10, Action: ActionReject, ReviewNote: "no"}); err != nil {
		t.Fatalf("reject comment: %v", err)
	}
	if invalidator.calls != 1 {
		t.Fatalf("rejection unexpectedly invalidated public reads: %+v", invalidator)
	}
}

// TestSubmitDecisionInvalidationMatrix 固定「任何改变公开可见性的处置都必须失效公开
// 读缓存」（P2-3）。
//
// 之前只有 pre_publish + approve 会调 InvalidateModerationPublication，举报处置的
// hide_and_close / delete_and_close 直接 UPDATE status，绕开 Forum.CachedStore，
// 被隐藏/删除的公开主题与评论在 TTL 窗口内（最长 taxonomy 60s）仍可被公开读取。
//
// 矩阵覆盖三种改变可见性的动作 × topic/comment 两类目标，以及 keep_and_close
// （只关工单、内容未变）不失效。
//
// 变异要求：把 invalidatesModerationPublication 改回「只有 pre_publish + approve」，
// 四条 report 改变可见性的用例必须红。
func TestSubmitDecisionInvalidationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		action     string
		targetType string
		wantCalls  int
	}{
		{
			name: "pre-publish approve topic", source: SourcePrePublish, action: ActionApprove,
			targetType: TargetTypeTopic, wantCalls: 1,
		},
		{
			name: "pre-publish approve comment", source: SourcePrePublish, action: ActionApprove,
			targetType: TargetTypeComment, wantCalls: 1,
		},
		{
			name: "report hide topic", source: SourceReport, action: ActionHideAndClose,
			targetType: TargetTypeTopic, wantCalls: 1,
		},
		{
			name: "report hide comment", source: SourceReport, action: ActionHideAndClose,
			targetType: TargetTypeComment, wantCalls: 1,
		},
		{
			name: "report delete topic", source: SourceReport, action: ActionDeleteAndClose,
			targetType: TargetTypeTopic, wantCalls: 1,
		},
		{
			name: "report delete comment", source: SourceReport, action: ActionDeleteAndClose,
			targetType: TargetTypeComment, wantCalls: 1,
		},
		{
			// 预审驳回：内容从 pending 进 rejected，从来没公开过，公开读缓存不变。
			name: "pre-publish reject", source: SourcePrePublish, action: ActionReject,
			targetType: TargetTypeTopic, wantCalls: 0,
		},
		{
			// 只关工单，内容状态没变：不该失效，否则等于白掉一次缓存。
			name: "report keep topic", source: SourceReport, action: ActionKeepAndClose,
			targetType: TargetTypeTopic, wantCalls: 0,
		},
		{
			name: "report keep comment", source: SourceReport, action: ActionKeepAndClose,
			targetType: TargetTypeComment, wantCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeWorkbenchStore{reviewContext: ReviewContext{
				TargetType: test.targetType, TargetID: 10, TopicID: 77,
			}}
			invalidator := &fakeDecisionReadModelInvalidator{}
			service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store).
				WithDecisionReadModelInvalidator(invalidator)
			reviewer := identity.Actor{
				ID: 8, Status: identity.UserStatusActive,
				Permissions: map[string]bool{identity.PermissionModerationReview: true},
			}
			input := DecisionInput{
				Source: test.source, TargetType: test.targetType, TargetID: 10,
				Action: test.action, ReportID: 3, ReviewNote: "处置说明",
			}
			if _, err := service.SubmitDecision(context.Background(), reviewer, input); err != nil {
				t.Fatalf("submit decision: %v", err)
			}
			if invalidator.calls != test.wantCalls {
				t.Fatalf("invalidation calls=%d want %d (%+v)", invalidator.calls, test.wantCalls, invalidator)
			}
			if test.wantCalls == 0 {
				return
			}
			if invalidator.topicID != 77 || invalidator.targetType != test.targetType {
				t.Fatalf("invalidation target = %+v, want topic 77 / %s", invalidator, test.targetType)
			}
		})
	}
}

func TestGetReviewContextStripsIPWithoutViewIPPermission(t *testing.T) {
	store := &fakeWorkbenchStore{reviewContext: ReviewContext{
		TargetType: TargetTypeTopic, TargetID: 3, TopicID: 3,
		IPAddress: "203.0.113.1", LastEditIP: "203.0.113.2",
	}}
	service := NewServiceWithWorkbench(&fakeStore{}, nil, store, store)
	reviewOnly := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionModerationReview: true},
	}
	item, err := service.GetReviewContext(context.Background(), reviewOnly, ReviewContextInput{
		Source: SourcePrePublish, TargetType: TargetTypeTopic, TargetID: 3,
	})
	if err != nil {
		t.Fatalf("GetReviewContext: %v", err)
	}
	if item.IPAddress != "" || item.LastEditIP != "" {
		t.Fatalf("expected IPs stripped without view_ip, got %+v", item)
	}

	withIP := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionModerationReview: true,
			identity.PermissionModerationViewIP: true,
		},
	}
	item, err = service.GetReviewContext(context.Background(), withIP, ReviewContextInput{
		Source: SourcePrePublish, TargetType: TargetTypeTopic, TargetID: 3,
	})
	if err != nil {
		t.Fatalf("GetReviewContext with view_ip: %v", err)
	}
	if item.IPAddress != "203.0.113.1" || item.LastEditIP != "203.0.113.2" {
		t.Fatalf("expected full IPs with view_ip, got %+v", item)
	}
}

func TestListPendingStripsIPWithoutViewIPPermission(t *testing.T) {
	service := NewServiceWithWorkbench(&fakeStore{}, nil, &fakeWorkbenchStore{}, &fakeWorkbenchStore{})
	reviewOnly := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionModerationReview: true},
	}
	list, err := service.ListPending(context.Background(), reviewOnly, WorkbenchListInput{Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(list.Items))
	}
	if list.Items[0].IPAddress != "" || list.Items[0].LastEditIP != "" {
		t.Fatalf("expected stripped IPs, got %+v", list.Items[0])
	}
}

type fakeStore struct {
	createdInput CreateReportInput
	updatedInput UpdateReportInput
	createErr    error
	updateErr    error
	listResult   ReportList
}

func (s *fakeStore) CreateReport(_ context.Context, input CreateReportInput) (Report, error) {
	if s.createErr != nil {
		return Report{}, s.createErr
	}
	s.createdInput = input
	return Report{ID: 1, TargetType: input.TargetType, TargetID: input.TargetID, ReasonCode: input.ReasonCode, Status: StatusOpen}, nil
}

func (s *fakeStore) ListReports(_ context.Context, input ReportListInput) (ReportList, error) {
	return ReportList{Items: []Report{}, Total: 0, Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *fakeStore) GetReport(context.Context, int64) (Report, error) {
	return Report{}, nil
}

func (s *fakeStore) UpdateReport(_ context.Context, input UpdateReportInput) (Report, error) {
	if s.updateErr != nil {
		return Report{}, s.updateErr
	}
	s.updatedInput = input
	return Report{ID: input.ReportID, Status: input.Status, ReviewNote: input.ReviewNote}, nil
}

type fakeValidator struct {
	topic   bool
	comment bool
}

type fakeWorkbenchStore struct {
	decisionErr   error
	reviewContext ReviewContext
}

func (s *fakeWorkbenchStore) GetSettings(context.Context) (Settings, error) {
	return RecommendedSettings(), nil
}

func (s *fakeWorkbenchStore) SaveSettings(_ context.Context, settings Settings, _ int64) (Settings, error) {
	return settings, nil
}

func (s *fakeWorkbenchStore) ResetSettings(_ context.Context, settings Settings, _ int64) (Settings, error) {
	return settings, nil
}

func (s *fakeWorkbenchStore) QueueCounts(context.Context) (QueueCounts, error) {
	return QueueCounts{}, nil
}

func (s *fakeWorkbenchStore) ListPending(context.Context, WorkbenchListInput) (PendingList, error) {
	return PendingList{Items: []PendingItem{{
		TargetType: TargetTypeTopic, TargetID: 1, Title: "t", AuthorID: 2,
		IPAddress: "203.0.113.9", LastEditIP: "203.0.113.10",
	}}}, nil
}

func (s *fakeWorkbenchStore) ListReportItems(context.Context, WorkbenchListInput) (ReportItemList, error) {
	return ReportItemList{}, nil
}

func (s *fakeWorkbenchStore) ListDecisions(context.Context, DecisionListInput) (DecisionList, error) {
	return DecisionList{}, nil
}

func (s *fakeWorkbenchStore) GetReviewContext(context.Context, ReviewContextInput) (ReviewContext, error) {
	return s.reviewContext, nil
}

type fakeDecisionIndexer struct {
	indexed []int64
	deleted []int64
}

type fakeDecisionReadModelInvalidator struct {
	calls      int
	topicID    int64
	targetType string
}

func (i *fakeDecisionReadModelInvalidator) InvalidateModerationPublication(_ context.Context, topicID int64, targetType string) {
	i.calls++
	i.topicID = topicID
	i.targetType = targetType
}

func (i *fakeDecisionIndexer) EnqueueIndex(_ context.Context, topicID int64) error {
	i.indexed = append(i.indexed, topicID)
	return nil
}

func (i *fakeDecisionIndexer) EnqueueDelete(_ context.Context, topicID int64) error {
	i.deleted = append(i.deleted, topicID)
	return nil
}

func (s *fakeWorkbenchStore) SubmitDecision(context.Context, DecisionInput) (Decision, error) {
	return Decision{}, s.decisionErr
}

func (v *fakeValidator) IsReportableTopic(context.Context, int64) (bool, error) {
	return v.topic, nil
}

func (v *fakeValidator) IsReportableComment(context.Context, int64) (bool, error) {
	return v.comment, nil
}
