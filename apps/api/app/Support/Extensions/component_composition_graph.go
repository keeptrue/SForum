package extensionsruntime

import (
	"fmt"
	"sort"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// validateComponentCompositionGraph bounds recursive add targets at
// publication time. Runtime checks remain in place because a request may also
// use a stricter executor-specific depth limit.
func validateComponentCompositionGraph(
	contributions map[string]ComponentContribution,
	active map[string]bool,
	maxDepth int,
) error {
	edges := make(map[string][]string)
	for id, contribution := range contributions {
		if !active[id] || contribution.Action != extensionmanifest.ComponentActionAdd || contribution.TargetID == "" {
			continue
		}
		edges[contribution.TargetID] = append(edges[contribution.TargetID], contribution.ID)
	}
	for id := range edges {
		sort.Strings(edges[id])
	}
	visiting := make(map[string]bool)
	deepest := make(map[string]int)
	var walk func(string, int) error
	walk = func(id string, depth int) error {
		if depth >= maxDepth {
			return fmt.Errorf("%w: add graph", ErrComponentCompositionDepth)
		}
		if visiting[id] {
			return fmt.Errorf("%w: %s", ErrComponentCompositionCycle, id)
		}
		if previous, visited := deepest[id]; visited && depth <= previous {
			return nil
		}
		deepest[id] = depth
		visiting[id] = true
		for _, child := range edges[id] {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		delete(visiting, id)
		return nil
	}
	ids := make([]string, 0, len(edges))
	for id := range edges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := walk(id, 0); err != nil {
			return err
		}
	}
	return nil
}
