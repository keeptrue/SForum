package pluginv2

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	ShortcodeProjectionExtensionID = "sforum-shortcodes"
	ShortcodeProjectionMaxBatch    = 32
	shortcodeLoginContentID        = "sforum-shortcodes.login"
	shortcodeReplyContentID        = "sforum-shortcodes.reply"
	shortcodeOnlyAuthorContentID   = "sforum-shortcodes.only-author"

	ShortcodePublicUsersQueryID      = "core.query.shortcode.public_users.batch"
	ShortcodePublicTopicsQueryID     = "core.query.shortcode.public_topics.batch"
	ShortcodePublicCommentsQueryID   = "core.query.shortcode.public_comments.batch"
	ShortcodePublicCategoriesQueryID = "core.query.shortcode.public_categories.batch"
	ShortcodeFriendLinksQueryID      = "core.query.shortcode.friend_links.list"
	ShortcodeAuthorDecisionsQueryID  = "core.query.shortcode.author_decisions.batch"
	ShortcodeReplyEligibilityQueryID = "core.query.shortcode.reply_eligibility.batch"
)

var ErrShortcodeProjectionInvalid = errors.New("pluginv2: shortcode Host projection is invalid")

type shortcodeProjectionContract struct {
	QueryID      string
	ResultSchema string
}

var shortcodeProjectionContracts = []shortcodeProjectionContract{
	{ShortcodePublicUsersQueryID, "sforum.core.shortcode.public_user"},
	{ShortcodePublicTopicsQueryID, "sforum.core.shortcode.public_topic"},
	{ShortcodePublicCommentsQueryID, "sforum.core.shortcode.public_comment"},
	{ShortcodePublicCategoriesQueryID, "sforum.core.shortcode.public_category"},
	{ShortcodeFriendLinksQueryID, "sforum.core.shortcode.friend_link"},
	{ShortcodeAuthorDecisionsQueryID, "sforum.core.shortcode.author_decision"},
	{ShortcodeReplyEligibilityQueryID, "sforum.core.shortcode.reply_eligibility"},
}

func ShortcodeProjectionContractVersion(queryID string) string {
	return queryID + "@1"
}

func ShortcodeProjectionPlanVersion(queryID string) string {
	return queryID + ".plan@1"
}

// NewShortcodeProjectionIDsFilter deduplicates and orders IDs before encoding
// the sole bounded batch filter accepted by the M4 projections.
func NewShortcodeProjectionIDsFilter(ids ...int64) (*hostwire.QueryFilter, error) {
	if len(ids) == 0 || len(ids) > ShortcodeProjectionMaxBatch {
		return nil, ErrShortcodeProjectionInvalid
	}
	values := append([]int64(nil), ids...)
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	unique := values[:0]
	for _, id := range values {
		if id <= 0 {
			return nil, ErrShortcodeProjectionInvalid
		}
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	encoded, err := json.Marshal(unique)
	if err != nil {
		return nil, ErrShortcodeProjectionInvalid
	}
	document, err := NewHostQueryFilterValue(string(encoded))
	if err != nil {
		return nil, err
	}
	return &hostwire.QueryFilter{Field: "ids", Operator: "eq", Value: document}, nil
}

func preflightContentHostQueryDelegations(ctx *protocolwire.RequestContext) bool {
	if ctx.GetExtension().GetExtensionId() == ShortcodeProjectionExtensionID {
		return true
	}
	return len(ctx.GetHostQueryDelegations()) == 0
}

func validContentHostQueryDelegations(ctx *protocolwire.RequestContext, allowProtectedNone bool) bool {
	delegations := ctx.GetHostQueryDelegations()
	if ctx.GetExtension().GetExtensionId() != ShortcodeProjectionExtensionID {
		return len(delegations) == 0
	}
	if allowProtectedNone && len(delegations) == 0 {
		return true
	}
	if len(delegations) != len(shortcodeProjectionContracts) {
		return false
	}
	expected := make(map[string]shortcodeProjectionContract, len(shortcodeProjectionContracts))
	for _, contract := range shortcodeProjectionContracts {
		expected[contract.QueryID] = contract
	}
	for _, delegation := range delegations {
		contract, ok := expected[delegation.GetQueryId()]
		if !ok || delegation.GetContractVersion() != ShortcodeProjectionContractVersion(contract.QueryID) ||
			delegation.GetPlanVersion() != ShortcodeProjectionPlanVersion(contract.QueryID) ||
			delegation.GetResultSchemaId() != contract.ResultSchema || delegation.GetResultSchemaVersion() != "1" ||
			delegation.GetScope() != "content" || strings.TrimSpace(delegation.GetToken()) == "" {
			return false
		}
		delete(expected, contract.QueryID)
	}
	return len(expected) == 0
}
