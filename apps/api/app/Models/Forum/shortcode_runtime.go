package forum

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

const maxProtectedShortcodeHTMLBytes = 1 << 20

type PublicShortcodeSource struct {
	Resource     contentregistry.ShortcodeResourceKey
	RawContent   string
	SourceFormat string
}

// PublicShortcodeSourceStore is an internal, raw-bearing projection used only
// after the ordinary public read has already established visibility. Callers
// must convert it immediately to a bounded render plan and never serialize it.
type PublicShortcodeSourceStore interface {
	LoadPublicShortcodeSources(context.Context, []contentregistry.ShortcodeResourceKey) (map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource, error)
}

type PublicShortcodeDispatcher interface {
	RenderDocuments(context.Context, []contentregistry.ForumShortcodeRenderDocument, string) ([]contentregistry.ForumShortcodeRenderResult, error)
}

// ProtectedShortcodeDispatcher receives only Host-authorized accepted child
// fragments. It receives no actor, session, resource body, permission table,
// IP address, moderation data, or database/query authority.
type ProtectedShortcodeDispatcher interface {
	RenderProtectedFragments(context.Context, []contentregistry.ForumProtectedShortcodeRenderRequest, string) ([]contentregistry.ForumProtectedShortcodeRenderResult, error)
}

type ProtectedShortcodeAuthorizationRequest struct {
	Resource        contentregistry.ShortcodeResourceKey
	ID              string
	ContractVersion string
	Viewer          identity.Actor
}

type ProtectedShortcodeDecision struct {
	Allowed      bool
	FallbackCode string
}

// ProtectedShortcodeAuthorizer is the Host policy authority. M8 production
// wiring intentionally leaves it nil until M9 supplies product policies.
type ProtectedShortcodeAuthorizer interface {
	AuthorizeProtectedShortcode(context.Context, ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error)
}

type ProtectedShortcodeAuthorizerFunc func(context.Context, ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error)

func (f ProtectedShortcodeAuthorizerFunc) AuthorizeProtectedShortcode(ctx context.Context, request ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error) {
	if f == nil {
		return ProtectedShortcodeDecision{}, nil
	}
	return f(ctx, request)
}

type ReferenceRenderCacheInvalidator interface {
	InvalidateReferenceResource(context.Context, string, int64)
	InvalidateTopicVisibility(context.Context)
}

type publicRenderLocaleKey struct{}

type publicShortcodeRenderer struct {
	store      Store
	dispatcher PublicShortcodeDispatcher
	protected  ProtectedShortcodeDispatcher
	authorizer ProtectedShortcodeAuthorizer
}

func newPublicShortcodeRenderer(store Store) *publicShortcodeRenderer {
	if store == nil {
		return nil
	}
	return &publicShortcodeRenderer{store: store}
}

func WithPublicRenderLocale(ctx context.Context, locale string) context.Context {
	if ctx == nil || strings.TrimSpace(locale) == "" {
		return ctx
	}
	return context.WithValue(ctx, publicRenderLocaleKey{}, strings.TrimSpace(locale))
}

func publicRenderLocale(ctx context.Context) string {
	if ctx != nil {
		if locale, ok := ctx.Value(publicRenderLocaleKey{}).(string); ok && strings.TrimSpace(locale) != "" {
			return locale
		}
	}
	return "und"
}

type forumShortcodeRenderPlan struct {
	resource   contentregistry.ShortcodeResourceKey
	template   string
	references []contentregistry.ForumShortcodeRenderNode
	protected  []editordocument.ProtectedShortcodeFragment
}

func (r *publicShortcodeRenderer) renderTopic(ctx context.Context, topic TopicDetail, viewer identity.Actor) (TopicDetail, bool) {
	if r == nil || topic.Content.RenderVersion != RenderVersionEditorDocument {
		return topic, false
	}
	key := contentregistry.ShortcodeResourceKey{Type: "topic", ID: topic.ID}
	plans, indexes := r.renderPlans(ctx, []contentregistry.ShortcodeResourceKey{key})
	index, ok := indexes[key]
	if !ok || index < 0 || index >= len(plans) {
		return topic, false
	}
	html := r.renderPublicPlans(ctx, plans)[index]
	html, protected := r.composeProtected(ctx, plans[index], html, viewer)
	if strings.TrimSpace(html) != "" {
		topic.Content.HTMLContent = html
	}
	return topic, protected
}

func (r *publicShortcodeRenderer) renderComments(ctx context.Context, comments []Comment, viewer identity.Actor) ([]Comment, bool) {
	if r == nil || len(comments) == 0 {
		return comments, false
	}
	pointers := make([]*Comment, 0)
	collectRenderableComments(comments, &pointers)
	keys := make([]contentregistry.ShortcodeResourceKey, 0, len(pointers))
	byKey := make(map[contentregistry.ShortcodeResourceKey]*Comment, len(pointers))
	for _, comment := range pointers {
		if comment.Content.RenderVersion != RenderVersionEditorDocument || comment.Status != CommentStatusActive {
			continue
		}
		key := contentregistry.ShortcodeResourceKey{Type: "comment", ID: comment.ID}
		keys = append(keys, key)
		byKey[key] = comment
	}
	plans, indexes := r.renderPlans(ctx, keys)
	if len(plans) == 0 {
		return comments, false
	}
	publicHTML := r.renderPublicPlans(ctx, plans)
	hasProtected := false
	for key, index := range indexes {
		comment := byKey[key]
		if comment == nil || index < 0 || index >= len(plans) || index >= len(publicHTML) {
			continue
		}
		html, protected := r.composeProtected(ctx, plans[index], publicHTML[index], viewer)
		hasProtected = hasProtected || protected
		if strings.TrimSpace(html) != "" {
			comment.Content.HTMLContent = html
		}
	}
	return comments, hasProtected
}

func collectRenderableComments(comments []Comment, result *[]*Comment) {
	for index := range comments {
		*result = append(*result, &comments[index])
		collectRenderableComments(comments[index].Children, result)
	}
}

func (r *publicShortcodeRenderer) renderPlans(
	ctx context.Context,
	keys []contentregistry.ShortcodeResourceKey,
) ([]forumShortcodeRenderPlan, map[contentregistry.ShortcodeResourceKey]int) {
	store, ok := r.store.(PublicShortcodeSourceStore)
	if !ok || len(keys) == 0 {
		return nil, nil
	}
	sources, err := store.LoadPublicShortcodeSources(ctx, keys)
	if err != nil {
		slog.WarnContext(ctx, "forum: load shortcode render plans failed")
		return nil, nil
	}
	plans := make([]forumShortcodeRenderPlan, 0, len(keys))
	indexes := make(map[contentregistry.ShortcodeResourceKey]int, len(keys))
	seen := make(map[contentregistry.ShortcodeResourceKey]struct{}, len(keys))
	locale := publicRenderLocale(ctx)
	for _, key := range keys {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		source, found := sources[key]
		if !found || source.SourceFormat != SourceFormatEditorDocument || strings.TrimSpace(source.RawContent) == "" {
			continue
		}
		accepted, acceptErr := editordocument.Accept(editordocument.Input{
			NativeJSON: []byte(source.RawContent), Schema: editordocument.CoreSchema(), ResourceKind: key.Type, Locale: locale,
		})
		if acceptErr != nil {
			continue
		}
		template, slots, protected := editordocument.RenderHTMLWithAllShortcodeSlots(accepted.Native, editordocument.CoreSchema(), locale)
		if len(slots) == 0 && len(protected) == 0 {
			continue
		}
		nodes := make([]contentregistry.ForumShortcodeRenderNode, 0, len(slots))
		valid := true
		for _, slot := range slots {
			id, idOK := slot.Node.Attrs["id"].(string)
			version, versionOK := slot.Node.Attrs["contractVersion"].(string)
			value, marshalErr := json.Marshal(slot.Node)
			if !idOK || !versionOK || marshalErr != nil {
				valid = false
				break
			}
			nodes = append(nodes, contentregistry.ForumShortcodeRenderNode{
				Placeholder: slot.Placeholder, ID: id, ContractVersion: version,
				Value: value, Depth: slot.Depth,
			})
		}
		if !valid {
			continue
		}
		indexes[key] = len(plans)
		plans = append(plans, forumShortcodeRenderPlan{
			resource: key, template: template, references: nodes, protected: protected,
		})
	}
	return plans, indexes
}

func (r *publicShortcodeRenderer) renderPublicPlans(ctx context.Context, plans []forumShortcodeRenderPlan) []string {
	result := make([]string, len(plans))
	documents := make([]contentregistry.ForumShortcodeRenderDocument, 0, len(plans))
	indexes := make([]int, 0, len(plans))
	for index, plan := range plans {
		result[index] = closeReferenceSlots(plan.template, plan.references, publicRenderLocale(ctx))
		if r.dispatcher != nil && len(plan.references) > 0 {
			documents = append(documents, contentregistry.ForumShortcodeRenderDocument{
				Resource: plan.resource, TemplateHTML: plan.template, Nodes: plan.references,
			})
			indexes = append(indexes, index)
		} else if len(plan.references) == 0 {
			result[index] = plan.template
		}
	}
	if len(documents) == 0 {
		return result
	}
	rendered, err := r.dispatcher.RenderDocuments(ctx, documents, publicRenderLocale(ctx))
	if err != nil || len(rendered) != len(documents) {
		if err != nil {
			slog.WarnContext(ctx, "forum: public shortcode render fell back")
		}
		return result
	}
	for index, item := range rendered {
		if strings.TrimSpace(item.HTML) != "" {
			result[indexes[index]] = item.HTML
		}
	}
	return result
}

func closeReferenceSlots(template string, slots []contentregistry.ForumShortcodeRenderNode, locale string) string {
	result := template
	for _, slot := range slots {
		var node editordocument.Node
		if json.Unmarshal(slot.Value, &node) != nil {
			result = strings.Replace(result, slot.Placeholder, "", 1)
			continue
		}
		fallback := editordocument.RenderShortcodeFallbackHTML(node, editordocument.CoreSchema(), locale)
		result = strings.Replace(result, slot.Placeholder, fallback, 1)
	}
	return result
}

func (r *publicShortcodeRenderer) composeProtected(
	ctx context.Context,
	plan forumShortcodeRenderPlan,
	publicHTML string,
	viewer identity.Actor,
) (string, bool) {
	if len(plan.protected) == 0 {
		return publicHTML, false
	}
	locale := publicRenderLocale(ctx)
	result := publicHTML
	fallbacks := make([]string, len(plan.protected))
	rendered := make([]bool, len(plan.protected))
	requests := make([]contentregistry.ForumProtectedShortcodeRenderRequest, 0, len(plan.protected))
	indexes := make([]int, 0, len(plan.protected))
	for index, fragment := range plan.protected {
		fallbacks[index] = editordocument.RenderProtectedShortcodeFallbackHTML(locale)
		decision, allowed := r.authorizeProtectedTree(ctx, plan.resource, fragment, viewer)
		if !allowed || r.protected == nil {
			if strings.TrimSpace(decision.FallbackCode) != "" {
				fallbacks[index] = editordocument.RenderProtectedShortcodeFallbackHTMLForCode(decision.FallbackCode, locale)
			}
			continue
		}
		accepted, err := json.Marshal(fragment.Document)
		if err != nil || len(accepted) == 0 {
			continue
		}
		requests = append(requests, contentregistry.ForumProtectedShortcodeRenderRequest{
			ID: fragment.ID, ContractVersion: fragment.ContractVersion, AcceptedFragment: accepted,
		})
		indexes = append(indexes, index)
	}

	renders := make([]contentregistry.ForumProtectedShortcodeRenderResult, len(requests))
	if len(requests) > 0 {
		if rendered, err := r.protected.RenderProtectedFragments(ctx, requests, locale); err == nil && len(rendered) == len(requests) {
			renders = rendered
		}
	}
	for requestIndex, fragmentIndex := range indexes {
		candidate := renders[requestIndex]
		if !candidate.Rendered || strings.TrimSpace(candidate.HTML) == "" || len(candidate.HTML) > maxProtectedShortcodeHTMLBytes {
			continue
		}
		sanitized := editordocument.SanitizeHTML(candidate.HTML)
		if strings.TrimSpace(sanitized) != "" {
			fallbacks[fragmentIndex] = sanitized
			rendered[fragmentIndex] = true
		}
	}
	for index, fragment := range plan.protected {
		wrapped := contentregistry.WrapForumShortcodeHTML(fragment.ID, fallbacks[index], !rendered[index])
		result = strings.Replace(result, fragment.Placeholder, wrapped, 1)
	}
	return result, true
}

func (r *publicShortcodeRenderer) authorizeProtectedTree(
	ctx context.Context,
	resource contentregistry.ShortcodeResourceKey,
	fragment editordocument.ProtectedShortcodeFragment,
	viewer identity.Actor,
) (ProtectedShortcodeDecision, bool) {
	if r == nil || r.authorizer == nil {
		return ProtectedShortcodeDecision{FallbackCode: "shortcode.protected.unavailable"}, false
	}
	decision, err := r.authorizer.AuthorizeProtectedShortcode(ctx, ProtectedShortcodeAuthorizationRequest{
		Resource: resource, ID: fragment.ID, ContractVersion: fragment.ContractVersion, Viewer: viewer,
	})
	if err != nil || !decision.Allowed {
		return decision, false
	}
	var walk func([]editordocument.Node) bool
	walk = func(nodes []editordocument.Node) bool {
		for _, node := range nodes {
			if node.Type == editordocument.ShortcodeBlockNode {
				id, _ := node.Attrs["id"].(string)
				version, _ := node.Attrs["contractVersion"].(string)
				nested, nestedErr := r.authorizer.AuthorizeProtectedShortcode(ctx, ProtectedShortcodeAuthorizationRequest{
					Resource: resource, ID: id, ContractVersion: version, Viewer: viewer,
				})
				if nestedErr != nil || !nested.Allowed {
					return false
				}
			}
			if !walk(node.Content) {
				return false
			}
		}
		return true
	}
	if !walk(fragment.Document.Content) {
		return ProtectedShortcodeDecision{FallbackCode: "shortcode.protected.unavailable"}, false
	}
	return decision, true
}
