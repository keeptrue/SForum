package pluginv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var (
	ErrInvalidSEODefinition = errors.New("invalid plugin SEO definition")
	seoIDPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
)

const (
	seoProviderSlot      = "sforum.seo"
	seoProviderOperation = "apply"
	seoRequestSchema     = "sforum.seo.apply.request@1"
	seoResponseSchema    = "sforum.seo.apply.response@1"
)

// SEODocument is the closed, typed Host SEO payload available to plugin
// authors. It deliberately has no raw HTML or arbitrary head representation.
type SEODocument = seoregistry.Document

// SEODefinition mirrors one frozen Manifest SEO declaration and supplies its
// author handler. The Host remains authoritative for composition and policy.
type SEODefinition struct {
	ID              string
	ContractVersion string
	Scope           string
	Kind            string
	Action          string
	Handler         string
	Priority        int
	FailurePolicy   string
	TimeoutMS       int
	Execute         SEOHandler
}

// SEOContribution is the wire projection of one frozen SEO declaration.
// Exact artifact and trust identity remain bound by RequestContext.
type SEOContribution struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Scope           string `json:"scope"`
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Handler         string `json:"handler"`
	Priority        int    `json:"priority,omitempty"`
	FailurePolicy   string `json:"failurePolicy"`
	TimeoutMS       int    `json:"timeoutMs"`
}

type seoApplyRequest struct {
	Scope        string          `json:"scope"`
	Contribution SEOContribution `json:"contribution"`
	Current      SEODocument     `json:"current"`
}

type seoApplyResponse struct {
	Document SEODocument `json:"document"`
}

type SEOCall struct {
	Context      *protocolwire.RequestContext
	Scope        string
	Contribution SEOContribution
	Current      seoregistry.Document
}

type SEOHandler func(context.Context, *SEOCall) (seoregistry.Document, error)

type SEORegistry struct {
	byID  map[string]SEODefinition
	order []SEODefinition
}

func NewSEORegistry(definitions ...SEODefinition) (*SEORegistry, error) {
	registry := &SEORegistry{byID: make(map[string]SEODefinition, len(definitions))}
	for _, input := range definitions {
		definition, err := normalizeSEODefinition(input)
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.byID[definition.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate declaration %q", ErrInvalidSEODefinition, definition.ID)
		}
		registry.byID[definition.ID] = definition
		registry.order = append(registry.order, definition)
	}
	sort.Slice(registry.order, func(i, j int) bool { return registry.order[i].ID < registry.order[j].ID })
	return registry, nil
}

func (r *SEORegistry) Definitions() []SEODefinition {
	if r == nil {
		return nil
	}
	result := make([]SEODefinition, len(r.order))
	copy(result, r.order)
	for index := range result {
		result[index].Execute = nil
	}
	return result
}

func (r *SEORegistry) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: responseContext(providerRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, input, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	current := cloneSEODocument(input.Current)
	document, err := definition.Execute(handlerCtx, &SEOCall{
		Context: cloneRequestContext(request.GetContext()), Scope: input.Scope,
		Contribution: input.Contribution, Current: current,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "seo.handler_failed", "Plugin SEO handler failed.")
		return response, nil
	}
	values, err := strictSEOMap(seoApplyResponse{Document: document})
	if err != nil {
		response.Error = familyErrorDetail(err, "seo.output_invalid", "Plugin SEO output is invalid.")
		return response, nil
	}
	output, err := NewTypedDocument(seoResponseSchema, values)
	if err != nil {
		response.Error = familyErrorDetail(err, "seo.output_invalid", "Plugin SEO output is invalid.")
		return response, nil
	}
	response.Output = output
	return response, nil
}

func (r *SEORegistry) resolve(request *pluginwire.ProviderCallRequest) (SEODefinition, seoApplyRequest, *protocolwire.ErrorDetail) {
	invalid := func(reason, message string) (SEODefinition, seoApplyRequest, *protocolwire.ErrorDetail) {
		return SEODefinition{}, seoApplyRequest{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason, Message: message,
		}
	}
	if r == nil || request == nil {
		return invalid("seo.request_required", "A plugin SEO request is required.")
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "seo"); detail != nil {
		return SEODefinition{}, seoApplyRequest{}, detail
	}
	// Public SEO resolution is actor-independent. Any actor projection here is
	// a Host transport bug and must fail closed rather than leak personalization.
	if request.GetContext().GetActor() != nil {
		return invalid("seo.actor_forbidden", "SEO provider calls cannot carry actor or session authority.")
	}
	if request.GetSlotId() != seoProviderSlot || request.GetOperation() != seoProviderOperation {
		return invalid("seo.transport_mismatch", "SEO providers require the declared sforum.seo apply transport.")
	}
	definition, found := r.byID[strings.TrimSpace(request.GetDeclarationId())]
	if !found {
		return SEODefinition{}, seoApplyRequest{}, &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "seo.declaration_not_found",
			Message: "The requested SEO declaration is not registered.",
		}
	}
	if request.GetContractVersion() != definition.ContractVersion {
		return invalid("seo.contract_mismatch", "SEO declaration contract version mismatch.")
	}
	if err := validateBoundDocument(request.GetInput(), seoRequestSchema, "seo", "input"); err != nil {
		return SEODefinition{}, seoApplyRequest{}, familyErrorDetail(err, "seo.schema_mismatch", "SEO input schema mismatch.")
	}
	input := seoApplyRequest{}
	if err := strictSEODecode(TypedDocumentValues(request.GetInput()), &input); err != nil {
		return invalid("seo.input_invalid", "SEO input has unknown or invalid fields.")
	}
	contribution := input.Contribution
	if input.Scope != definition.Scope || contribution.ID != definition.ID ||
		contribution.ContractVersion != definition.ContractVersion || contribution.Scope != definition.Scope ||
		contribution.Kind != definition.Kind || contribution.Action != definition.Action ||
		contribution.Handler != definition.Handler || contribution.Priority != definition.Priority ||
		contribution.FailurePolicy != definition.FailurePolicy || contribution.TimeoutMS != definition.TimeoutMS {
		return invalid("seo.declaration_drift", "SEO input does not match the frozen declaration.")
	}
	return definition, input, nil
}

func normalizeSEODefinition(input SEODefinition) (SEODefinition, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.Handler = strings.ToLower(strings.TrimSpace(input.Handler))
	input.FailurePolicy = strings.ToLower(strings.TrimSpace(input.FailurePolicy))
	if !seoIDPattern.MatchString(input.ID) || input.ContractVersion != input.ID+"@1" ||
		(input.Scope != seoregistry.GlobalScope && !seoIDPattern.MatchString(input.Scope)) ||
		!validSEOFamily(input.Kind, input.Action, input.FailurePolicy) || !seoIDPattern.MatchString(input.Handler) ||
		input.Priority < -1_000_000 || input.Priority > 1_000_000 || input.TimeoutMS < 1 || input.TimeoutMS > 5000 ||
		input.Execute == nil {
		return SEODefinition{}, ErrInvalidSEODefinition
	}
	return input, nil
}

func validSEOFamily(kind, action, failure string) bool {
	switch kind {
	case seoregistry.KindTitle, seoregistry.KindMeta, seoregistry.KindCanonical, seoregistry.KindRobots,
		seoregistry.KindHreflang, seoregistry.KindSitemap, seoregistry.KindJSONLD:
	default:
		return false
	}
	if action != seoregistry.ActionAdd && action != seoregistry.ActionFilter && action != seoregistry.ActionReplace {
		return false
	}
	return failure == seoregistry.FailurePolicyFailClosed || failure == seoregistry.FailurePolicyFallback
}

func strictSEODecode(values map[string]any, target any) error {
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("SEO typed document has trailing data")
	}
	return nil
}

func strictSEOMap(value any) (map[string]any, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloneSEODocument(value seoregistry.Document) seoregistry.Document {
	values, err := strictSEOMap(value)
	if err != nil {
		return seoregistry.Document{}
	}
	result := seoregistry.Document{}
	if err := strictSEODecode(values, &result); err != nil {
		return seoregistry.Document{}
	}
	return result
}
