package forum

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

var attachmentPublicIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// 附件引用专用 reason 与 sentinel，和下方的 typed error 定义同处一档；
// types.go 只保留通用错误码表。
const (
	// CodeAttachmentUnavailable 正文引用的附件不可用（已删除/已禁用/已转私有/非本人上传）。
	// 与笼统的 content_invalid 区分开，前端才能给出「重新上传这张图」的可操作提示。
	CodeAttachmentUnavailable = "forum.attachment_unavailable"
	// CodeAttachmentConflict 引用写入撞上唯一约束（schema 与代码假设不一致时的具名兜底，
	// 不让它冒泡成 internal_error）。
	CodeAttachmentConflict = "forum.attachment_conflict"
)

var (
	// ErrAttachmentUnavailable 表示正文提交的部分附件不可用；具体 id 见
	// AttachmentUnavailableError。errors.Is(err, ErrAttachmentUnavailable) 成立。
	ErrAttachmentUnavailable = errors.New("forum: attachment unavailable")
	// ErrAttachmentConflict 表示附件引用写入撞上唯一约束。
	ErrAttachmentConflict = errors.New("forum: attachment reference conflict")
)

func normalizeAndValidateContentAttachmentIDs(
	content RenderedContent,
	input *[]int64,
) ([]int64, bool, error) {
	attachmentIDs, submitted, err := normalizeContentAttachmentIDs(input)
	if err != nil {
		return nil, submitted, err
	}
	if err := validateEditorDocumentAttachmentIDs(content, attachmentIDs, submitted); err != nil {
		return nil, submitted, err
	}
	return attachmentIDs, submitted, nil
}

// validateEditorDocumentAttachmentIDs keeps native image identity and the
// transactional attachment reference list consistent. Other attachment kinds
// may still be supplied through attachmentIds by API clients.
func validateEditorDocumentAttachmentIDs(content RenderedContent, attachmentIDs []int64, submitted bool) error {
	if content.SourceFormat != SourceFormatEditorDocument {
		return nil
	}

	var document editordocument.Document
	if err := json.Unmarshal([]byte(content.RawContent), &document); err != nil {
		return ErrInvalidContent
	}

	nodeIDs, err := editorDocumentImageAttachmentIDs(document.Content)
	if err != nil {
		return err
	}
	if len(nodeIDs) == 0 {
		return nil
	}
	if !submitted {
		return ErrInvalidContent
	}

	allowed := make(map[int64]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		allowed[id] = struct{}{}
	}
	for _, id := range nodeIDs {
		if _, ok := allowed[id]; !ok {
			return ErrInvalidContent
		}
	}
	return nil
}

func editorDocumentImageAttachmentIDs(nodes []editordocument.Node) ([]int64, error) {
	seen := map[int64]struct{}{}
	ids := make([]int64, 0)
	var walk func([]editordocument.Node) error
	walk = func(items []editordocument.Node) error {
		for _, node := range items {
			if node.Type == "image" {
				idValue, hasID := node.Attrs["attachmentId"]
				publicValue, hasPublicID := node.Attrs["attachmentPublicId"]
				if hasID != hasPublicID {
					return ErrInvalidContent
				}
				if hasID {
					id, ok := positiveJSONInt64(idValue)
					publicID, publicOK := publicValue.(string)
					publicID = strings.TrimSpace(publicID)
					src, _ := node.Attrs["src"].(string)
					if !ok || !publicOK || !attachmentPublicIDPattern.MatchString(publicID) || !attachmentURLMatchesPublicID(src, publicID) {
						return ErrInvalidContent
					}
					if _, exists := seen[id]; !exists {
						seen[id] = struct{}{}
						ids = append(ids, id)
					}
				}
			}
			if err := walk(node.Content); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nodes); err != nil {
		return nil, err
	}
	return ids, nil
}

func positiveJSONInt64(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number <= 0 || number > math.MaxInt64 || math.Trunc(number) != number {
		return 0, false
	}
	return int64(number), true
}

func attachmentURLMatchesPublicID(rawURL, publicID string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	switch parsed.Path {
	case "/media/attachments/" + publicID,
		"/api/v1/attachments/" + publicID + "/content",
		"/api/v1/attachments/" + publicID + "/variants/display/content":
		return true
	default:
		return false
	}
}

// AttachmentUnavailableError 携带提交里不可用的附件 id，供控制器映射成具名 422。
// 与笼统的 ErrInvalidContent 分开，前端才能只针对失效图片给出「重新上传」提示。
type AttachmentUnavailableError struct {
	ResourceType  string
	ResourceID    int64
	AttachmentIDs []int64
}

func (e *AttachmentUnavailableError) Error() string {
	return fmt.Sprintf("forum: attachments %v unavailable for %s %d", e.AttachmentIDs, e.ResourceType, e.ResourceID)
}

func (e *AttachmentUnavailableError) Is(target error) bool {
	return target == ErrAttachmentUnavailable
}

// AttachmentConflictError 表示附件引用写入撞上唯一约束。它只应在 schema 与代码
// 假设不一致时出现，因此保留约束名用于审计。
type AttachmentConflictError struct {
	Constraint    string
	ResourceType  string
	ResourceID    int64
	AttachmentIDs []int64
}

func (e *AttachmentConflictError) Error() string {
	return fmt.Sprintf("forum: attachment reference conflict on %s for %s %d (attachments %v)", e.Constraint, e.ResourceType, e.ResourceID, e.AttachmentIDs)
}

func (e *AttachmentConflictError) Is(target error) bool {
	return target == ErrAttachmentConflict
}
