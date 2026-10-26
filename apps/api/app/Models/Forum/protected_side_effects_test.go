package forum

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

const protectedSideEffectMarker = "M8_SIDE_EFFECT_SECRET_MARKER_93D6E1"

type protectedLinkTrustPolicy struct{}

func (protectedLinkTrustPolicy) NewUserTrustDays(context.Context) (int, error) { return 7, nil }
func (protectedLinkTrustPolicy) NewUserTopicCooldownSeconds(context.Context) (int, error) {
	return 0, nil
}
func (protectedLinkTrustPolicy) NewUserCommentCooldownSeconds(context.Context) (int, error) {
	return 0, nil
}
func (protectedLinkTrustPolicy) NewUserDailyTopicLimit(context.Context) (int, error)   { return 0, nil }
func (protectedLinkTrustPolicy) NewUserDailyCommentLimit(context.Context) (int, error) { return 0, nil }
func (protectedLinkTrustPolicy) NewUserForbidOutboundLinks(context.Context) (bool, error) {
	return true, nil
}

type protectedModerationCapture struct {
	input PublicationInput
}

func (p *protectedModerationCapture) EvaluatePublication(_ context.Context, input PublicationInput) (PublicationDecision, error) {
	p.input = input
	return PublicationDecision{Pending: true, Triggers: []string{"protected_child_policy"}}, nil
}

func protectedSideEffectSourceJSON() string {
	return `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"public text @publicuser"}]},{"type":"sforumShortcodeBlock","attrs":{"id":"sforum-shortcodes.login","contractVersion":"sforum-shortcodes.login@1","arguments":{}},"content":[{"type":"paragraph","content":[{"type":"text","text":"` + protectedSideEffectMarker + ` @hiddenuser https://secret.invalid/path"}]}]}]}`
}

func TestProtectedShortcodeWriteSideEffectsUseOnlyPublicProjection(t *testing.T) {
	t.Parallel()

	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{}
	indexer := &fakeIndexer{}
	service := NewService(ServiceConfig{
		Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher, Indexer: indexer,
	})
	created, err := service.CreateTopic(t.Context(), topicCreator(), CreateTopicInput{
		CategorySlug: "general",
		Title:        "M8 protected side effects",
		Content: ContentInput{
			RawContent: protectedSideEffectSourceJSON(), SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap,
		},
	})
	if err != nil {
		t.Fatalf("create protected topic: %v", err)
	}

	if got := store.createdTopic.MentionedUsernames; len(got) != 1 || got[0] != "publicuser" {
		t.Fatalf("mention fanout included protected child: %#v", got)
	}
	if len(indexer.indexedIDs) != 1 || indexer.indexedIDs[0] != created.ID {
		t.Fatalf("search scheduling must carry only the topic id: %#v", indexer.indexedIDs)
	}
	for name, value := range map[string]string{
		"public html":    created.Content.HTMLContent,
		"public plain":   created.Content.PlainText,
		"public excerpt": created.Content.Excerpt,
	} {
		if strings.Contains(value, protectedSideEffectMarker) || strings.Contains(value, "hiddenuser") || strings.Contains(value, "secret.invalid") {
			t.Fatalf("%s leaked protected child: %q", name, value)
		}
	}

	for _, envelope := range publisher.envelopes {
		if envelope.Kind != appevents.KindObserve {
			continue
		}
		body, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		serialized := string(body)
		for _, forbidden := range []string{protectedSideEffectMarker, "hiddenuser", "secret.invalid", "rawContent", "contentHash"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("observe/webhook projection leaked %q: %s", forbidden, serialized)
			}
		}
	}
}

func TestProtectedShortcodeChildStillReceivesOutboundLinkPolicy(t *testing.T) {
	t.Parallel()

	service := NewService(ServiceConfig{Store: newServiceFakeStore(), TrustPolicy: protectedLinkTrustPolicy{}})
	actor := identity.Actor{
		ID: 77, Status: identity.UserStatusActive, CreatedAt: time.Now().Add(-time.Hour),
		Permissions: map[string]bool{identity.PermissionTopicCreate: true},
	}
	_, err := service.CreateTopic(context.Background(), actor, CreateTopicInput{
		Title: "M8 protected link policy",
		Content: ContentInput{
			RawContent: protectedSideEffectSourceJSON(), SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap,
		},
	})
	if err != ErrOutboundLinkForbidden {
		t.Fatalf("protected child bypassed outbound-link trust policy: %v", err)
	}
}

func TestProtectedShortcodeChildStillReceivesModerationPolicy(t *testing.T) {
	t.Parallel()

	store := newServiceFakeStore()
	policy := &protectedModerationCapture{}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{
		Store: store, Publisher: publisher, PublicationPolicy: policy,
	})
	created, err := service.CreateTopic(t.Context(), topicCreator(), CreateTopicInput{
		Title: "M8 protected moderation",
		Content: ContentInput{
			RawContent: protectedSideEffectSourceJSON(), SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap,
		},
	})
	if err != nil {
		t.Fatalf("create moderated protected topic: %v", err)
	}
	if !strings.Contains(policy.input.RawContent, protectedSideEffectMarker) || !strings.Contains(policy.input.RawContent, "secret.invalid") {
		t.Fatalf("Host moderation did not inspect protected child: %#v", policy.input)
	}
	if created.Status != TopicStatusPending || store.createdTopic.Status != TopicStatusPending ||
		len(store.createdTopic.ModerationTriggers) != 1 || store.createdTopic.ModerationTriggers[0] != "protected_child_policy" {
		t.Fatalf("protected child moderation decision was not enforced: created=%#v record=%#v", created, store.createdTopic)
	}
	for _, envelope := range publisher.envelopes {
		if envelope.Kind != appevents.KindObserve {
			continue
		}
		body, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(body), protectedSideEffectMarker) {
			t.Fatalf("moderation observe projection leaked protected child: %s", body)
		}
	}
}
