package forum

import (
	"context"
	"strings"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

// ContentPostFilter is an optional Host seam after authoritative RenderContent.
// Nil implementations and empty Content Registry graphs MUST leave content
// unchanged so ordinary markdown / editor-document posts never fail because a
// plugin surface is incomplete.
type ContentPostFilter interface {
	AfterHostRender(ctx context.Context, in ContentPostFilterInput) (RenderedContent, error)
}

// EditorDocumentSchemaProvider supplies the Host Accept schema for
// sourceFormat=editor-document. Nil keeps CoreSchema-only admission.
type EditorDocumentSchemaProvider interface {
	EditorDocumentSchema() editordocument.Schema
}

type DefaultLocaleResolver interface {
	DefaultLocale(context.Context) (string, error)
}

// ContentPostFilterInput carries Host-rendered content plus composition context.
type ContentPostFilterInput struct {
	Rendered   RenderedContent
	Resource   string // "topic" | "comment"
	ResourceID string // "new" or numeric id when known
	Scope      string // "public" | "author" | ...
}

// WithContentPostFilter injects the optional post-render content pipeline.
func (s *Service) WithContentPostFilter(filter ContentPostFilter) *Service {
	if s != nil {
		s.contentFilter = filter
	}
	return s
}

// WithEditorDocumentSchema injects Editor Registry → Accept schema projection.
func (s *Service) WithEditorDocumentSchema(provider EditorDocumentSchemaProvider) *Service {
	if s != nil {
		s.editorSchema = provider
	}
	return s
}

// renderContent is the Service write-path entry: editor-document admits plugin
// node/mark names from Editor Registry when wired.
func (s *Service) renderContent(input ContentInput, excerptLimit int, resourceKind string) (RenderedContent, error) {
	return renderContentWithContext(s, context.Background(), input, excerptLimit, resourceKind)
}

func renderContentWithContext(s *Service, ctx context.Context, input ContentInput, excerptLimit int, resourceKind string) (RenderedContent, error) {
	schema := editordocument.Schema{}
	locale := "zh-CN"
	if s != nil && s.editorSchema != nil {
		schema = s.editorSchema.EditorDocumentSchema()
	}
	if s != nil {
		if resolver, ok := s.settings.(DefaultLocaleResolver); ok {
			if resolved, err := resolver.DefaultLocale(ctx); err == nil && strings.TrimSpace(resolved) != "" {
				locale = strings.TrimSpace(resolved)
			}
		}
	}
	input.Locale = locale
	return RenderContentWithExcerptLimitAndSchemaForResource(input, excerptLimit, schema, resourceKind)
}

func (s *Service) applyContentPostFilter(ctx context.Context, content RenderedContent, resource, resourceID string) (RenderedContent, error) {
	if s == nil || s.contentFilter == nil {
		return content, nil
	}
	next, err := s.contentFilter.AfterHostRender(ctx, ContentPostFilterInput{
		Rendered:   content,
		Resource:   resource,
		ResourceID: resourceID,
		Scope:      "public",
	})
	if err != nil {
		return RenderedContent{}, err
	}
	// 空结果保护：过滤器不得擦除 Host 已验收正文。
	if strings.TrimSpace(next.HTMLContent) == "" && strings.TrimSpace(content.HTMLContent) != "" {
		return content, nil
	}
	return next, nil
}
