package forum

import (
	"context"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
)

// ContentRegistryPostFilter adapts ContentRegistry's filter surface to the
// forum ContentPostFilter interface without importing ContentRegistry types
// into every call site (provider injects a concrete adapter).
type ContentRegistryPostFilter interface {
	AfterHostRender(ctx context.Context, html, plain, resource, resourceID, scope string) (nextHTML, nextPlain string, err error)
}

// ContentRegistryBridge adapts ContentRegistryPostFilter → ContentPostFilter.
type ContentRegistryBridge struct {
	Inner ContentRegistryPostFilter
}

// EditorRegistrySchemaBridge projects EditorRegistry into forum Accept schema.
type EditorRegistrySchemaBridge struct {
	Registry *editorregistry.Registry
}

// EditorDocumentSchema implements EditorDocumentSchemaProvider.
func (b EditorRegistrySchemaBridge) EditorDocumentSchema() editordocument.Schema {
	if b.Registry == nil {
		return editordocument.CoreSchema()
	}
	return b.Registry.DocumentSchema()
}

func (b ContentRegistryBridge) AfterHostRender(ctx context.Context, in ContentPostFilterInput) (RenderedContent, error) {
	if b.Inner == nil {
		return in.Rendered, nil
	}
	html, plain, err := b.Inner.AfterHostRender(
		ctx,
		in.Rendered.HTMLContent,
		in.Rendered.PlainText,
		in.Resource,
		in.ResourceID,
		in.Scope,
	)
	if err != nil {
		return RenderedContent{}, err
	}
	out := in.Rendered
	// 默认只允许替换 HTML；PlainText/Excerpt/ContentHash 保持 Host 权威，避免搜索漂移。
	if html != "" {
		out.HTMLContent = html
	}
	_ = plain
	return out, nil
}
