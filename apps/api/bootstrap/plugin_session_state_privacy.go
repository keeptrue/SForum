package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
)

// pluginSessionStatePrivacyContributionID is the Host-owned privacy
// contribution that owns plugin-namespaced session state. It is not an
// extension id: the state is Host storage, so the Host is the data controller
// and the Host erases it.
const pluginSessionStatePrivacyContributionID = "core.host.plugin-session-state"

// pluginSessionSessionLister is the narrow slice of Models/Identity the erase
// hook needs. It is declared here so the hook can be tested without PostgreSQL.
type pluginSessionSessionLister interface {
	ListUserSessions(ctx context.Context, userID int64, currentSID string, includeHistory bool, page int, perPage int) (identity.SessionListResult, error)
}

// pluginSessionStateEraser erases every plugin-namespaced session state a user
// owns. It walks the user's sessions (including revoked history, because a
// revoked sid's state is exactly the residue this exists to remove) and calls
// the store's idempotent InvalidateSession for each.
//
// It never touches or reports a value: the report carries counts only, so
// nothing that a plugin wrote can reach the audit ledger, the logs, or the
// export bundle.
type pluginSessionStateEraser struct {
	invalidator *productionPluginSessionState
	sessions    pluginSessionSessionLister
}

// eraseSessionPageSize bounds each listing page. The loop keeps paging until the
// reported total is consumed, so a user with more sessions than one page is
// still fully erased.
const eraseSessionPageSize = 100

func (e *pluginSessionStateEraser) erase(ctx context.Context, userID string) (privacy.EraseResult, error) {
	result := privacy.EraseResult{ExtensionID: pluginSessionStatePrivacyContributionID}
	if e == nil || e.invalidator == nil || e.sessions == nil {
		result.Error = "plugin session state eraser is not wired"
		return result, errors.New("bootstrap: plugin session state eraser is not wired")
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil || parsed <= 0 {
		result.Error = "user id is not a valid identity"
		return result, fmt.Errorf("bootstrap: privacy erase user id %q is invalid", userID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	erased := 0
	seen := make(map[string]struct{})
	for page := 1; ; page++ {
		listing, err := e.sessions.ListUserSessions(ctx, parsed, "", true, page, eraseSessionPageSize)
		if err != nil {
			result.Error = "session listing failed"
			return result, fmt.Errorf("bootstrap: list sessions for privacy erase: %w", err)
		}
		for _, session := range listing.Items {
			// SessionRecord.ID is the opaque sid (ListUserSessions selects `sid`).
			sid := strings.TrimSpace(session.ID)
			if sid == "" {
				continue
			}
			if _, duplicate := seen[sid]; duplicate {
				continue
			}
			seen[sid] = struct{}{}
			removed, err := e.invalidator.InvalidateSessionBySID(ctx, sid)
			if err != nil {
				result.Error = "session state invalidation failed"
				return result, fmt.Errorf("bootstrap: invalidate plugin session state during privacy erase: %w", err)
			}
			erased += removed
		}
		if len(listing.Items) == 0 || int64(page*listing.PerPage) >= listing.Total || listing.PerPage <= 0 {
			break
		}
	}
	result.Erased = true
	result.Warnings = append(result.Warnings,
		fmt.Sprintf("plugin session state keys removed: %d across %d sessions", erased, len(seen)))
	return result, nil
}

// registerPluginSessionStatePrivacyContribution installs the erase hook on the
// Host privacy registry. Export is deliberately not supported: session state is
// ephemeral workflow data, not a portable user record, and the Host would have
// to hand out opaque plugin bytes to satisfy an export.
func registerPluginSessionStatePrivacyContribution(
	registry *privacy.Registry,
	invalidator *productionPluginSessionState,
	sessions pluginSessionSessionLister,
) error {
	if registry == nil || invalidator == nil || sessions == nil {
		return fmt.Errorf("bootstrap: plugin session state privacy registration requires the registry, invalidator and session lister")
	}
	eraser := &pluginSessionStateEraser{invalidator: invalidator, sessions: sessions}
	return registry.Register(privacy.Contribution{
		ExtensionID: pluginSessionStatePrivacyContributionID,
		Inventory: []privacy.InventoryItem{{
			ID:   "plugin_session_state",
			Kind: privacy.KindPersonalData,
			Description: "Host-owned, plugin-namespaced session state: opaque bytes a plugin wrote for " +
				"one Core session. Erased by walking the user's sessions and invalidating each namespace; " +
				"values are never exported, logged, or reported.",
		}},
	}, nil, eraser.erase)
}
