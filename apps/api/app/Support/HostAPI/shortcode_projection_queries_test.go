package hostapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestShortcodeProjectionCatalogIsExactlyFrozenM4Surface(t *testing.T) {
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	want := ShortcodeProjectionQueryIDs()
	if len(want) != 7 {
		t.Fatalf("projection count=%d", len(want))
	}
	for _, queryID := range want {
		query, err := queryContributionByID(catalog.Publication().Queries, queryID)
		if err != nil {
			t.Fatal(err)
		}
		if query.ContractVersion != queryID+"@1" || query.PlanVersion != queryID+".plan@1" ||
			query.Pagination != queryregistry.PaginationOffset || len(query.Relations) != 0 || len(query.CacheTags) != 0 {
			t.Fatalf("projection contract drift for %s: %#v", queryID, query)
		}
		for _, field := range query.Fields {
			switch field {
			case "email", "raw_content", "html_content", "plain_text", "session", "ip_address", "moderation_notes", "avatar":
				t.Fatalf("projection exposed forbidden field: %#v", query)
			}
		}
	}
}

func TestShortcodeProjectionBatchUsesOneArrayPredicateAndRejectsOverLimit(t *testing.T) {
	ids := make([]int64, ShortcodeProjectionMaxBatch)
	for index := range ids {
		ids[index] = int64(index + 1)
	}
	canonical, err := canonicalShortcodeProjectionIDs(ids)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseShortcodeProjectionIDs(canonical)
	if err != nil || len(parsed) != ShortcodeProjectionMaxBatch {
		t.Fatalf("parsed=%v err=%v", parsed, err)
	}
	definition := shortcodeProjectionProtocolV2QueryDefinitions()[0]
	plan := protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields,
		Filters: []protocolV2QueryFilter{{Definition: definition.Filters[0], Value: parsed}},
		Sorts:   definition.DefaultSorts, FetchLimit: ShortcodeProjectionMaxBatch + 1,
	}
	statement, args := postgresProtocolV2Query(plan, 0)
	if strings.Count(statement, "ANY($") != 1 || strings.Count(statement, "SELECT ") != 2 ||
		len(args) != 3 || len(args[0].([]int64)) != ShortcodeProjectionMaxBatch {
		t.Fatalf("batch SQL=%q args=%#v", statement, args)
	}
	over := append(ids, int64(ShortcodeProjectionMaxBatch+1))
	overValue, _ := json.Marshal(over)
	if _, err := parseShortcodeProjectionIDs(string(overValue)); err == nil {
		t.Fatal("over-limit ID batch was accepted")
	}
	for _, invalid := range []string{"[2,1]", "[1,1]", "[0]", "[1.0]", "[1] "} {
		if _, err := parseShortcodeProjectionIDs(invalid); err == nil {
			t.Fatalf("invalid ID batch accepted: %s", invalid)
		}
	}
}

func TestProtocolV2QueryDelegationTokenKeepsActorServerSide(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(grant.Token, ".")
	if len(parts) != 3 {
		t.Fatal("delegation is not a compact token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"actor_user_id", "authenticated", "actor_fingerprint", "policy_fingerprint", "permissions", "session"} {
		if _, exists := claims[forbidden]; exists {
			t.Fatalf("delegation leaked %s: %s", forbidden, payload)
		}
	}
	stored, err := h.service.delegations.lookup(grant.Token)
	if err != nil || stored.Binding.Actor != h.actors.projection {
		t.Fatalf("server actor binding=%#v err=%v", stored.Binding.Actor, err)
	}
}

func queryContributionByID(queries []queryregistry.QueryDeclaration, id string) (queryregistry.QueryDeclaration, error) {
	for _, query := range queries {
		if query.ID == id {
			return query, nil
		}
	}
	return queryregistry.QueryDeclaration{}, queryregistry.ErrNotFound
}
