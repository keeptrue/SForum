package contentregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	businesscache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
)

const (
	ShortcodeUserID        = "sforum-shortcodes.user"
	ShortcodeTopicID       = "sforum-shortcodes.topic"
	ShortcodeCommentID     = "sforum-shortcodes.comment"
	ShortcodeCategoryID    = "sforum-shortcodes.category"
	ShortcodeFriendLinksID = "sforum-shortcodes.friend-links"
	ShortcodeLoginID       = "sforum-shortcodes.login"
	ShortcodeReplyID       = "sforum-shortcodes.reply"
	ShortcodeOnlyAuthorID  = "sforum-shortcodes.only-author"

	shortcodeExtensionID                = "sforum-shortcodes"
	shortcodePublicPolicyFingerprint    = "content.shortcode.public@1"
	shortcodeProtectedPolicyFingerprint = "content.shortcode.protected@1"
	shortcodeReferenceCacheTTL          = 2 * time.Minute
	shortcodeReferenceCacheKeyPrefix    = "forum:shortcode:render:v1:"
	shortcodeReferenceTagGenKeyPrefix   = "forum:shortcode:tag:v1:"
	shortcodeTopicVisibilityTag         = "forum.shortcode.topic-visibility"
)

type ShortcodeResourceKey struct {
	Type string
	ID   int64
}

func (k ShortcodeResourceKey) String() string {
	return k.Type + ":" + strconv.FormatInt(k.ID, 10)
}

func (k ShortcodeResourceKey) valid() bool {
	return (k.Type == "topic" || k.Type == "comment") && k.ID > 0
}

type ForumShortcodeRenderNode struct {
	Placeholder     string
	ID              string
	ContractVersion string
	Value           json.RawMessage
	Depth           int
}

type ForumShortcodeRenderDocument struct {
	Resource       ShortcodeResourceKey
	ReferenceStack []ShortcodeResourceKey
	TemplateHTML   string
	Nodes          []ForumShortcodeRenderNode
}

type ForumShortcodeRenderResult struct {
	HTML      string
	CacheTags []string
}

// ForumProtectedShortcodeRenderRequest is request-only input created only
// after Host authorization. AcceptedFragment is never cached, traced, logged,
// hashed into a cache key, or combined with another viewer's request.
type ForumProtectedShortcodeRenderRequest struct {
	ID               string
	ContractVersion  string
	AcceptedFragment json.RawMessage
}

type ForumProtectedShortcodeRenderResult struct {
	HTML     string
	Rendered bool
}

type ForumShortcodeDispatcherLimits struct {
	MaxDepth           int
	MaxTotalReferences int
	MaxBatchSize       int
	MaxOutputBytes     int
	TotalTimeout       time.Duration
	CallTimeout        time.Duration
}

type ForumShortcodeDispatcherConfig struct {
	Registry  *Registry
	Admission RuntimeAdmission
	Providers ContentProviderResolver
	Cache     businesscache.Cache
	Trace     *ContentTraceRing
	Limits    ForumShortcodeDispatcherLimits
}

type ForumShortcodeDispatcher struct {
	registry  *Registry
	admission RuntimeAdmission
	providers ContentProviderResolver
	cache     businesscache.Cache
	trace     *ContentTraceRing
	limits    ForumShortcodeDispatcherLimits

	mu       sync.Mutex
	revision uint64
	digest   string
	active   map[string]Contribution
	executor *Executor
}

type shortcodeRuntimeSpec struct {
	id              string
	contractVersion string
	schema          string
	argument        string
	targetType      string
	unavailableCode string
	protected       bool
}

var shortcodeRuntimeSpecs = map[string]shortcodeRuntimeSpec{
	ShortcodeUserID: {
		id: ShortcodeUserID, contractVersion: ShortcodeUserID + "@1",
		schema: ShortcodeUserID + ".schema@1", argument: "userId",
		unavailableCode: "shortcode.user.unavailable",
	},
	ShortcodeTopicID: {
		id: ShortcodeTopicID, contractVersion: ShortcodeTopicID + "@1",
		schema: ShortcodeTopicID + ".schema@1", argument: "topicId", targetType: "topic",
		unavailableCode: "shortcode.topic.unavailable",
	},
	ShortcodeCommentID: {
		id: ShortcodeCommentID, contractVersion: ShortcodeCommentID + "@1",
		schema: ShortcodeCommentID + ".schema@1", argument: "commentId", targetType: "comment",
		unavailableCode: "shortcode.comment.unavailable",
	},
	ShortcodeCategoryID: {
		id: ShortcodeCategoryID, contractVersion: ShortcodeCategoryID + "@1",
		schema: ShortcodeCategoryID + ".schema@1", argument: "categoryId",
		unavailableCode: "shortcode.category.unavailable",
	},
	ShortcodeFriendLinksID: {
		id: ShortcodeFriendLinksID, contractVersion: ShortcodeFriendLinksID + "@1",
		schema:          ShortcodeFriendLinksID + ".schema@1",
		unavailableCode: "shortcode.friend_links.omitted",
	},
	ShortcodeLoginID: {
		id: ShortcodeLoginID, contractVersion: ShortcodeLoginID + "@1",
		schema: ShortcodeLoginID + ".schema@1", unavailableCode: "shortcode.protected.unavailable", protected: true,
	},
	ShortcodeReplyID: {
		id: ShortcodeReplyID, contractVersion: ShortcodeReplyID + "@1",
		schema: ShortcodeReplyID + ".schema@1", unavailableCode: "shortcode.protected.unavailable", protected: true,
	},
	ShortcodeOnlyAuthorID: {
		id: ShortcodeOnlyAuthorID, contractVersion: ShortcodeOnlyAuthorID + "@1",
		schema: ShortcodeOnlyAuthorID + ".schema@1", unavailableCode: "shortcode.protected.unavailable", protected: true,
	},
}

func NewForumShortcodeDispatcher(config ForumShortcodeDispatcherConfig) (*ForumShortcodeDispatcher, error) {
	if config.Registry == nil || config.Admission == nil || config.Providers == nil {
		return nil, ErrExecutionInvalid
	}
	limits, err := normalizeForumShortcodeDispatcherLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	trace := config.Trace
	if trace == nil {
		trace = NewContentTraceRing(defaultForumPostBodyTraceCapacity)
	}
	dispatcher := &ForumShortcodeDispatcher{
		registry: config.Registry, admission: config.Admission, providers: config.Providers,
		cache: config.Cache, trace: trace, limits: limits,
	}
	if err := dispatcher.refreshExecutor(); err != nil {
		return nil, err
	}
	return dispatcher, nil
}

func normalizeForumShortcodeDispatcherLimits(input ForumShortcodeDispatcherLimits) (ForumShortcodeDispatcherLimits, error) {
	if input.MaxDepth == 0 {
		input.MaxDepth = 4
	}
	if input.MaxTotalReferences == 0 {
		input.MaxTotalReferences = 128
	}
	if input.MaxBatchSize == 0 {
		input.MaxBatchSize = 32
	}
	if input.MaxOutputBytes == 0 {
		input.MaxOutputBytes = 1 << 20
	}
	if input.TotalTimeout == 0 {
		input.TotalTimeout = 3 * time.Second
	}
	if input.CallTimeout == 0 {
		input.CallTimeout = 1500 * time.Millisecond
	}
	if input.MaxDepth < 1 || input.MaxDepth > 4 || input.MaxTotalReferences < 1 || input.MaxTotalReferences > 1024 ||
		input.MaxBatchSize < 1 || input.MaxBatchSize > 32 || input.MaxOutputBytes < 1024 || input.MaxOutputBytes > 8<<20 ||
		input.TotalTimeout < time.Millisecond || input.TotalTimeout > 10*time.Second ||
		input.CallTimeout < time.Millisecond || input.CallTimeout > input.TotalTimeout {
		return ForumShortcodeDispatcherLimits{}, ErrExecutionInvalid
	}
	return input, nil
}

func (d *ForumShortcodeDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.executor != nil {
		d.executor.Close()
		d.executor = nil
	}
}

func (d *ForumShortcodeDispatcher) RenderDocuments(
	ctx context.Context,
	documents []ForumShortcodeRenderDocument,
	locale string,
) ([]ForumShortcodeRenderResult, error) {
	if d == nil || ctx == nil || len(documents) == 0 {
		return nil, ErrExecutionInvalid
	}
	if err := d.refreshExecutor(); err != nil {
		return nil, err
	}
	workCtx, cancel := context.WithTimeout(ctx, d.limits.TotalTimeout)
	defer cancel()

	results := make([]ForumShortcodeRenderResult, len(documents))
	occurrences := make([]shortcodeOccurrence, 0)
	totalReferences := 0
	for documentIndex, document := range documents {
		results[documentIndex].HTML = document.TemplateHTML
		stack := normalizeShortcodeResourceStack(document.Resource, document.ReferenceStack)
		for nodeIndex, node := range document.Nodes {
			totalReferences++
			occurrence := shortcodeOccurrence{document: documentIndex, node: nodeIndex, input: node}
			if totalReferences > d.limits.MaxTotalReferences || node.Depth < 1 || node.Depth > d.limits.MaxDepth {
				occurrence.fallback = "shortcode.reference.unavailable"
				occurrences = append(occurrences, occurrence)
				continue
			}
			spec, target, key, err := decodeForumShortcodeRenderNode(node)
			if err != nil {
				occurrence.fallback = "shortcode.reference.unavailable"
				occurrences = append(occurrences, occurrence)
				continue
			}
			occurrence.spec, occurrence.target, occurrence.key = spec, target, key
			if target.valid() && shortcodeStackContains(stack, target) {
				occurrence.fallback = spec.unavailableCode
			}
			occurrences = append(occurrences, occurrence)
		}
	}

	renders, tags := d.resolveOccurrences(workCtx, occurrences, locale)
	totalOutput := 0
	for index, occurrence := range occurrences {
		html := renders[index]
		fallback := occurrence.fallback != ""
		if occurrence.fallback != "" {
			html = forumShortcodeFallbackHTML(occurrence.fallback, locale)
		}
		if len(html) > d.limits.MaxOutputBytes-totalOutput {
			html = forumShortcodeFallbackHTML("shortcode.reference.unavailable", locale)
			fallback = true
		}
		wrapped := WrapForumShortcodeHTML(occurrence.spec.id, html, fallback)
		if len(wrapped) > d.limits.MaxOutputBytes-totalOutput {
			wrapped = WrapForumShortcodeHTML(occurrence.spec.id,
				forumShortcodeFallbackHTML("shortcode.reference.unavailable", locale), true)
		}
		if len(wrapped) > d.limits.MaxOutputBytes-totalOutput {
			return results, ErrExecutionLimit
		}
		totalOutput += len(wrapped)
		result := &results[occurrence.document]
		result.HTML = strings.Replace(result.HTML, occurrence.input.Placeholder, wrapped, 1)
		result.CacheTags = append(result.CacheTags, tags[index]...)
	}
	for index := range results {
		results[index].CacheTags = uniqueSortedStrings(results[index].CacheTags)
	}
	return results, nil
}

// RenderProtectedFragments executes already-authorized fragments without any
// shared cache, cross-request dedupe, source-derived key, or Host delegation.
// Any batch/runtime/output failure returns closed results to the caller.
func (d *ForumShortcodeDispatcher) RenderProtectedFragments(
	ctx context.Context,
	requests []ForumProtectedShortcodeRenderRequest,
	locale string,
) ([]ForumProtectedShortcodeRenderResult, error) {
	if d == nil || ctx == nil || len(requests) == 0 || len(requests) > d.limits.MaxBatchSize {
		return nil, ErrExecutionInvalid
	}
	results := make([]ForumProtectedShortcodeRenderResult, len(requests))
	if err := d.refreshExecutor(); err != nil {
		return results, nil
	}
	executor := d.currentExecutor()
	if executor == nil {
		return results, nil
	}

	executionRequests := make([]ExecutionRequest, 0, len(requests))
	indexes := make([]int, 0, len(requests))
	for index, request := range requests {
		spec, ok := shortcodeRuntimeSpecs[request.ID]
		if !ok || !spec.protected || request.ContractVersion != spec.contractVersion ||
			len(request.AcceptedFragment) == 0 || !d.activeDeclaration(spec) {
			continue
		}
		value, err := json.Marshal(struct {
			Decision string          `json:"decision"`
			Fragment json.RawMessage `json:"fragment"`
		}{Decision: "allowed", Fragment: append(json.RawMessage(nil), request.AcceptedFragment...)})
		if err != nil || len(value) > d.limits.MaxOutputBytes {
			continue
		}
		executionRequests = append(executionRequests, ExecutionRequest{
			TargetID: spec.id, ContractVersion: spec.contractVersion,
			Document: EditorDocument{
				SchemaVersion: EditorDocumentSchemaVersion, ContentID: spec.id,
				ContractVersion: spec.contractVersion, Schema: spec.schema,
				StorageVersion: "1", Value: value,
			},
			Permission: PermissionInput{
				ActorFingerprint: "protected", PolicyFingerprint: shortcodeProtectedPolicyFingerprint,
				Recheck: forumShortcodePermissionRecheck{},
			},
			ResourceID: "protected:authorized", Locale: locale, Scope: "protected",
			Private: true, SuppressHostDelegations: true,
		})
		indexes = append(indexes, index)
	}
	if len(executionRequests) == 0 {
		return results, nil
	}
	workCtx, cancel := context.WithTimeout(ctx, d.limits.TotalTimeout)
	defer cancel()
	executions, err := executor.ExecuteBatch(workCtx, executionRequests)
	if err != nil || len(executions) != len(executionRequests) {
		return results, nil
	}
	for index, execution := range executions {
		if execution.CacheKey != "" || len(execution.CacheTags) != 0 {
			continue
		}
		html := renderSegmentsHTML(execution.Render)
		if strings.TrimSpace(html) == "" || len(html) > d.limits.MaxOutputBytes {
			continue
		}
		results[indexes[index]] = ForumProtectedShortcodeRenderResult{HTML: html, Rendered: true}
	}
	return results, nil
}

type shortcodeOccurrence struct {
	document int
	node     int
	input    ForumShortcodeRenderNode
	spec     shortcodeRuntimeSpec
	target   ShortcodeResourceKey
	key      string
	fallback string
}

func (d *ForumShortcodeDispatcher) resolveOccurrences(
	ctx context.Context,
	occurrences []shortcodeOccurrence,
	locale string,
) ([]string, [][]string) {
	renders := make([]string, len(occurrences))
	tags := make([][]string, len(occurrences))
	unique := make(map[string][]int)
	for index, occurrence := range occurrences {
		if occurrence.fallback != "" || occurrence.spec.id == "" {
			continue
		}
		unique[occurrence.key] = append(unique[occurrence.key], index)
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	type pendingCall struct {
		key        string
		occurrence shortcodeOccurrence
		request    ExecutionRequest
		cacheKey   string
		cacheTags  []string
	}
	pending := make([]pendingCall, 0, len(keys))
	for _, key := range keys {
		indices := unique[key]
		occurrence := occurrences[indices[0]]
		if !d.activeDeclaration(occurrence.spec) {
			for _, index := range indices {
				renders[index] = forumShortcodeFallbackHTML(occurrence.spec.unavailableCode, locale)
			}
			continue
		}
		cacheTags := shortcodeReferenceCacheTags(occurrence.target)
		cacheKey, cachedHTML, found := d.loadCachedRender(ctx, occurrence, locale, cacheTags)
		if found {
			for _, index := range indices {
				renders[index], tags[index] = cachedHTML, append([]string(nil), cacheTags...)
			}
			continue
		}
		pending = append(pending, pendingCall{
			key: key, occurrence: occurrence, cacheKey: cacheKey, cacheTags: cacheTags,
			request: ExecutionRequest{
				TargetID: occurrence.spec.id, ContractVersion: occurrence.spec.contractVersion,
				Document: EditorDocument{
					SchemaVersion: EditorDocumentSchemaVersion, ContentID: occurrence.spec.id,
					ContractVersion: occurrence.spec.contractVersion, Schema: occurrence.spec.schema,
					StorageVersion: "1", Value: append(json.RawMessage(nil), occurrence.input.Value...),
				},
				Permission: PermissionInput{
					ActorFingerprint: "public", PolicyFingerprint: shortcodePublicPolicyFingerprint,
					Recheck: forumShortcodePermissionRecheck{},
				},
				ResourceID: "public:shortcode", Locale: locale, Scope: "public", CacheTags: cacheTags,
			},
		})
	}

	for offset := 0; offset < len(pending); offset += d.limits.MaxBatchSize {
		end := offset + d.limits.MaxBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		requests := make([]ExecutionRequest, 0, end-offset)
		for _, call := range pending[offset:end] {
			requests = append(requests, call.request)
		}
		executor := d.currentExecutor()
		if executor == nil || ctx.Err() != nil {
			continue
		}
		batchResults, err := executor.ExecuteBatch(ctx, requests)
		if err != nil {
			continue
		}
		for batchIndex, execution := range batchResults {
			call := pending[offset+batchIndex]
			html := renderSegmentsHTML(execution.Render)
			if html == "" && call.occurrence.spec.id != ShortcodeFriendLinksID {
				continue
			}
			cacheTags := uniqueSortedStrings(append(call.cacheTags, execution.CacheTags...))
			d.storeCachedRender(ctx, call.cacheKey, html, cacheTags)
			for _, occurrenceIndex := range unique[call.key] {
				renders[occurrenceIndex], tags[occurrenceIndex] = html, append([]string(nil), cacheTags...)
			}
		}
	}

	for index, occurrence := range occurrences {
		if occurrence.fallback == "" && occurrence.spec.id != "" && renders[index] == "" && occurrence.spec.id != ShortcodeFriendLinksID {
			renders[index] = forumShortcodeFallbackHTML(occurrence.spec.unavailableCode, locale)
		}
	}
	return renders, tags
}

func (d *ForumShortcodeDispatcher) refreshExecutor() error {
	snapshot := d.registry.Snapshot()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.revision == snapshot.Revision && d.digest == snapshot.Digest {
		return nil
	}
	active := make(map[string]Contribution)
	bindings := make([]ExecutionBinding, 0, len(shortcodeRuntimeSpecs))
	if !snapshot.SafeMode {
		for _, contribution := range snapshot.Content {
			spec, expected := shortcodeRuntimeSpecs[contribution.ID]
			if !expected || contribution.ContractVersion != spec.contractVersion || contribution.Schema != spec.schema ||
				contribution.Kind != KindShortcode || contribution.Handler != contribution.ID ||
				contribution.Artifact.Core || contribution.Artifact.ExtensionID != shortcodeExtensionID {
				continue
			}
			providers, err := d.providers.ResolveContentProviders(contribution)
			if err != nil || providers.Renderer == nil {
				continue
			}
			active[contribution.ID] = contribution
			bindings = append(bindings, ExecutionBinding{
				TargetID: contribution.ID, TargetContractVersion: contribution.ContractVersion,
				DeclarationID: contribution.ID, ContractVersion: contribution.ContractVersion,
				Artifact: contribution.Artifact, Action: ActionAdd, Fallback: FallbackClosed,
				Providers: ProviderSet{Renderer: providers.Renderer},
			})
		}
	}
	var next *Executor
	if len(bindings) > 0 {
		var err error
		next, err = NewExecutor(
			d.registry, bindings, d.admission, forumShortcodeSchemaValidator{},
			ExecutionLimits{
				MaxBatchSize: d.limits.MaxBatchSize, MaxConcurrentCalls: 8,
				MaxOutputBytes: d.limits.MaxOutputBytes, CallTimeout: d.limits.CallTimeout,
			},
			WithContentTraceSink(d.trace),
		)
		if err != nil {
			return err
		}
	}
	if d.executor != nil {
		d.executor.Close()
	}
	d.executor, d.active = next, active
	d.revision, d.digest = snapshot.Revision, snapshot.Digest
	return nil
}

func (d *ForumShortcodeDispatcher) currentExecutor() *Executor {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.executor
}

func (d *ForumShortcodeDispatcher) activeDeclaration(spec shortcodeRuntimeSpec) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	contribution, ok := d.active[spec.id]
	return ok && contribution.ContractVersion == spec.contractVersion && contribution.Schema == spec.schema
}

type forumShortcodeValue struct {
	Type  string `json:"type"`
	Attrs struct {
		ID              string                     `json:"id"`
		ContractVersion string                     `json:"contractVersion"`
		Arguments       map[string]json.RawMessage `json:"arguments"`
	} `json:"attrs"`
}

func decodeForumShortcodeRenderNode(node ForumShortcodeRenderNode) (shortcodeRuntimeSpec, ShortcodeResourceKey, string, error) {
	spec, ok := shortcodeRuntimeSpecs[node.ID]
	if !ok || spec.protected || node.ContractVersion != spec.contractVersion || len(node.Value) == 0 || node.Placeholder == "" {
		return shortcodeRuntimeSpec{}, ShortcodeResourceKey{}, "", ErrExecutionInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(node.Value)))
	decoder.DisallowUnknownFields()
	var value forumShortcodeValue
	if err := decoder.Decode(&value); err != nil || value.Type != "sforumShortcodeRef" ||
		value.Attrs.ID != spec.id || value.Attrs.ContractVersion != spec.contractVersion {
		return shortcodeRuntimeSpec{}, ShortcodeResourceKey{}, "", ErrExecutionInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return shortcodeRuntimeSpec{}, ShortcodeResourceKey{}, "", ErrExecutionInvalid
	}
	targetID, err := validateForumShortcodeArguments(spec, value.Attrs.Arguments)
	if err != nil {
		return shortcodeRuntimeSpec{}, ShortcodeResourceKey{}, "", err
	}
	target := ShortcodeResourceKey{Type: spec.targetType, ID: targetID}
	key := spec.id
	if target.valid() {
		key += "\x00" + target.String()
	}
	return spec, target, key, nil
}

func validateForumShortcodeArguments(spec shortcodeRuntimeSpec, arguments map[string]json.RawMessage) (int64, error) {
	if spec.argument == "" {
		if len(arguments) != 0 {
			return 0, ErrExecutionInvalid
		}
		return 0, nil
	}
	if len(arguments) != 1 {
		return 0, ErrExecutionInvalid
	}
	raw, ok := arguments[spec.argument]
	if !ok || len(raw) == 0 || raw[0] == '-' || strings.ContainsRune(string(raw), '.') {
		return 0, ErrExecutionInvalid
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, ErrExecutionInvalid
	}
	return value, nil
}

type forumShortcodeSchemaValidator struct{}

func (forumShortcodeSchemaValidator) ValidateContentSchema(ctx context.Context, request SchemaValidationRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	spec, ok := shortcodeRuntimeSpecs[request.ContentID]
	if !ok || request.SchemaRef != spec.schema || request.ContractVersion != spec.contractVersion {
		return ErrSchemaRejected
	}
	body, err := json.Marshal(request.Value)
	if err != nil {
		return ErrSchemaRejected
	}
	if spec.protected {
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		var value struct {
			Decision string          `json:"decision"`
			Fragment json.RawMessage `json:"fragment"`
		}
		if err := decoder.Decode(&value); err != nil || value.Decision != "allowed" || len(value.Fragment) == 0 {
			return ErrSchemaRejected
		}
		var fragment struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		fragmentDecoder := json.NewDecoder(strings.NewReader(string(value.Fragment)))
		fragmentDecoder.DisallowUnknownFields()
		if err := fragmentDecoder.Decode(&fragment); err != nil || fragment.Type != "doc" || len(fragment.Content) == 0 {
			return ErrSchemaRejected
		}
		return nil
	}
	_, _, _, err = decodeForumShortcodeRenderNode(ForumShortcodeRenderNode{
		Placeholder: "schema", ID: spec.id, ContractVersion: spec.contractVersion, Value: body, Depth: 1,
	})
	if err != nil {
		return ErrSchemaRejected
	}
	return nil
}

type forumShortcodePermissionRecheck struct{}

func (forumShortcodePermissionRecheck) AuthorizeContent(_ context.Context, claim PermissionClaim) error {
	spec, ok := shortcodeRuntimeSpecs[claim.TargetID]
	expectedScope := "public"
	if spec.protected {
		expectedScope = "protected"
	}
	if !ok || !IsExactPermissionClaim(claim) || claim.TargetID != claim.ContentID ||
		claim.TargetContractVersion != spec.contractVersion || claim.ContractVersion != spec.contractVersion ||
		claim.TargetSchema != spec.schema || claim.Schema != spec.schema || claim.Artifact.Core ||
		claim.TargetArtifact.ExtensionID != shortcodeExtensionID || claim.Scope != expectedScope ||
		(claim.Operation != OperationSource && claim.Operation != OperationRenderer && claim.Operation != OperationRelease) {
		return ErrExecutionDenied
	}
	return nil
}

func normalizeShortcodeResourceStack(root ShortcodeResourceKey, input []ShortcodeResourceKey) []ShortcodeResourceKey {
	result := make([]ShortcodeResourceKey, 0, len(input)+1)
	for _, key := range input {
		if key.valid() {
			result = append(result, key)
		}
	}
	if root.valid() && !shortcodeStackContains(result, root) {
		result = append(result, root)
	}
	return result
}

func shortcodeStackContains(stack []ShortcodeResourceKey, target ShortcodeResourceKey) bool {
	for _, item := range stack {
		if item == target {
			return true
		}
	}
	return false
}

func forumShortcodeFallbackHTML(code, locale string) string {
	english := strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en")
	labels := map[string][2]string{
		"shortcode.reference.unavailable": {"短代码暂不可用", "Shortcode unavailable"},
		"shortcode.user.unavailable":      {"用户引用暂不可用", "User reference unavailable"},
		"shortcode.topic.unavailable":     {"主题引用暂不可用", "Topic reference unavailable"},
		"shortcode.comment.unavailable":   {"评论引用暂不可用", "Comment reference unavailable"},
		"shortcode.category.unavailable":  {"分类引用暂不可用", "Category reference unavailable"},
	}
	if code == "shortcode.friend_links.omitted" {
		return ""
	}
	label, ok := labels[code]
	if !ok {
		code, label = "shortcode.reference.unavailable", labels["shortcode.reference.unavailable"]
	}
	text := label[0]
	if english {
		text = label[1]
	}
	return `<span class="sf-editor-fallback" data-fallback="` + code + `">` + text + `</span>`
}

func shortcodeReferenceCacheTags(target ShortcodeResourceKey) []string {
	if !target.valid() {
		return nil
	}
	tags := []string{"forum.shortcode." + target.Type + "." + strconv.FormatInt(target.ID, 10)}
	if target.Type == "topic" || target.Type == "comment" {
		tags = append(tags, shortcodeTopicVisibilityTag)
	}
	return tags
}

type shortcodeCachedRender struct {
	HTML string `json:"html"`
}

func (d *ForumShortcodeDispatcher) loadCachedRender(
	ctx context.Context,
	occurrence shortcodeOccurrence,
	locale string,
	tags []string,
) (string, string, bool) {
	if d.cache == nil || len(tags) == 0 {
		return "", "", false
	}
	key, err := d.referenceCacheKey(ctx, occurrence.key, locale, tags)
	if err != nil {
		return "", "", false
	}
	body, found, err := d.cache.Get(ctx, key)
	if err != nil || !found {
		return key, "", false
	}
	var cached shortcodeCachedRender
	if json.Unmarshal(body, &cached) != nil || len(cached.HTML) > d.limits.MaxOutputBytes {
		return key, "", false
	}
	return key, cached.HTML, true
}

func (d *ForumShortcodeDispatcher) storeCachedRender(ctx context.Context, key, html string, tags []string) {
	if d.cache == nil || key == "" || len(tags) == 0 || len(html) > d.limits.MaxOutputBytes {
		return
	}
	body, err := json.Marshal(shortcodeCachedRender{HTML: html})
	if err == nil {
		_ = d.cache.Set(ctx, key, body, shortcodeReferenceCacheTTL)
	}
}

func (d *ForumShortcodeDispatcher) referenceCacheKey(ctx context.Context, nodeKey, locale string, tags []string) (string, error) {
	d.mu.Lock()
	registryDigest := d.digest
	d.mu.Unlock()
	material := []string{registryDigest, strings.ToLower(strings.TrimSpace(locale)), nodeKey}
	for _, tag := range tags {
		generation, found, err := d.cache.Get(ctx, shortcodeReferenceTagGenKeyPrefix+tag)
		if err != nil {
			return "", err
		}
		if !found {
			generation = []byte("0")
		}
		material = append(material, tag+"="+string(generation))
	}
	digest := sha256.Sum256([]byte(strings.Join(material, "\x00")))
	return shortcodeReferenceCacheKeyPrefix + hex.EncodeToString(digest[:]), nil
}

// InvalidateReferenceResource advances resource generations after authoritative
// Forum mutations. Topic changes also rotate the owning-topic visibility tag
// used by every comment-reference cache entry.
func (d *ForumShortcodeDispatcher) InvalidateReferenceResource(ctx context.Context, resourceType string, resourceID int64) {
	if d == nil || d.cache == nil || ctx == nil || resourceID <= 0 {
		return
	}
	tag := "forum.shortcode." + resourceType + "." + strconv.FormatInt(resourceID, 10)
	if resourceType != "topic" && resourceType != "comment" {
		return
	}
	d.bumpReferenceGeneration(ctx, tag)
	if resourceType == "topic" {
		d.bumpReferenceGeneration(ctx, shortcodeTopicVisibilityTag)
	}
}

func (d *ForumShortcodeDispatcher) InvalidateTopicVisibility(ctx context.Context) {
	if d == nil || d.cache == nil || ctx == nil {
		return
	}
	d.bumpReferenceGeneration(ctx, shortcodeTopicVisibilityTag)
}

func (d *ForumShortcodeDispatcher) bumpReferenceGeneration(ctx context.Context, tag string) {
	key := shortcodeReferenceTagGenKeyPrefix + tag
	generation, err := d.cache.Increment(ctx, key)
	if err != nil {
		return
	}
	// MemoryCache keeps counters separately from value entries while Redis
	// exposes INCR values through GET. Persisting the returned value gives both
	// backends the same generation-read contract and bounds stale generations.
	_ = d.cache.Set(ctx, key, []byte(strconv.FormatInt(generation, 10)), 24*time.Hour)
}

func uniqueSortedStrings(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, value := range input {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

var (
	_ SchemaValidator   = forumShortcodeSchemaValidator{}
	_ PermissionRecheck = forumShortcodePermissionRecheck{}
)
