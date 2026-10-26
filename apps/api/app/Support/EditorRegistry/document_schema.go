package editorregistry

import (
	"strings"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

// DocumentSchema projects the active Editor Registry graph into an
// EditorDocument Schema for Host Accept. Core types stay Host-owned; plugin
// node/mark ExtensionName values are admitted as atom placeholders so L2
// content survives storage without trusting client HTML.
func (r *Registry) DocumentSchema() editordocument.Schema {
	if r == nil {
		return editordocument.CoreSchema()
	}
	var nodes, marks []string
	for _, contribution := range r.Snapshot().Editor {
		name := strings.TrimSpace(contribution.ExtensionName)
		if name == "" {
			continue
		}
		switch contribution.Kind {
		case KindNode:
			nodes = append(nodes, name)
		case KindMark:
			marks = append(marks, name)
		}
	}
	if len(nodes) == 0 && len(marks) == 0 {
		return editordocument.CoreSchema()
	}
	return editordocument.SchemaFromEditorNames(nodes, marks)
}
