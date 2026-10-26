package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// attachmentReadMaxBytes 是单次读取的硬上限。
	//
	// 它必须由 io.LimitReader 强制，而不是拿 attachment.SizeBytes 当边界：那是
	// 写入时记下的元数据，覆盖写、迁移、外部导入都会让它与现实脱节，用它当读取
	// 边界等于相信一个过期的数字。
	attachmentReadMaxBytes = 16 * 1024
	// attachmentTextRunes 是进入工具结果的正文长度上限（16 KiB 的文本仍可能
	// 远超一次工具调用的合理体积）。
	attachmentTextRunes = 3_000
	// attachmentNameRunes 是文件名进入结果的长度上限。
	attachmentNameRunes = 200
)

// AttachmentReadTool 读取附件：文本类返回正文（截断），其它类型只返回元数据。
//
// 它补的是一个明确的空洞：主楼与回复里的附件（配置片段、日志、CSV 导出）当前
// 完全不可读，模型只能凭文件名猜。
//
// 两条边界：
//   - 读取以提问者身份过 attachments.Service.OpenContent——与公开下载同一条授权
//     链（引用快照 + 主题可见性 + 访客登录策略），不得用机器人帐号身份；
//   - 二进制不返回字节。图片走多模态（PartImage），不是这里：把一段 PNG 当文本
//     塞进上下文既贵又无用。
type AttachmentReadTool struct {
	attachments AttachmentReader
	actors      identity.ActorStore
}

func NewAttachmentReadTool(attachments AttachmentReader, actors identity.ActorStore) *AttachmentReadTool {
	return &AttachmentReadTool{attachments: attachments, actors: actors}
}

func (t *AttachmentReadTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "forum-attachment-read",
		Description: "读取站内附件。文本类附件（txt/log/md/csv/json/xml/yaml 等）返回正文并截断；" +
			"图片等二进制只返回文件名、类型与大小。需要看配置片段、日志或导出数据时使用。" +
			"attachmentId 是附件链接里的 publicId（一段字符串），不是数字 id。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "attachmentId": {"type": "string", "description": "附件的 publicId（附件链接里的那段字符串）"}
  },
  "required": ["attachmentId"]
}`),
	}
}

type attachmentReadArguments struct {
	AttachmentID string `json:"attachmentId"`
}

func (t *AttachmentReadTool) Invoke(ctx context.Context, call supportai.ToolCall, env supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.attachments == nil {
		return supportai.ToolResult{Content: "附件读取在当前部署不可用。", IsError: true}, nil
	}
	var args attachmentReadArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	publicID := strings.TrimSpace(args.AttachmentID)
	if publicID == "" {
		return supportai.ToolResult{Content: "attachmentId 不能为空：它是附件链接里的 publicId 字符串。", IsError: true}, nil
	}
	viewer, err := viewerActor(ctx, t.actors, env)
	if err != nil {
		return supportai.ToolResult{}, err
	}
	attachment, reader, err := t.attachments.OpenContent(ctx, viewer, publicID)
	if err != nil {
		if result, ok := attachmentReadDenial(err); ok {
			return result, nil
		}
		return supportai.ToolResult{}, err
	}
	defer func() { _ = reader.Close() }()

	name := clip(strings.TrimSpace(attachment.OriginalName), attachmentNameRunes)
	if name == "" {
		name = clip(publicID, attachmentNameRunes)
	}
	contentType := strings.TrimSpace(attachment.ContentType)
	if contentType == "" {
		contentType = "未知"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "附件：%s ｜ 类型：%s ｜ 大小：%d 字节\n", name, contentType, attachment.SizeBytes)
	if !attachmentContentIsText(attachment) {
		builder.WriteString("这是二进制附件，不返回正文（图片内容由多模态输入承担，不在本工具范围）。")
		return supportai.ToolResult{Content: builder.String()}, nil
	}
	data, oversized, err := readAttachmentHead(reader, attachmentReadMaxBytes)
	if err != nil {
		return supportai.ToolResult{}, err
	}
	body := clip(textFromAttachmentBytes(data), attachmentTextRunes)
	if body == "" {
		builder.WriteString("附件是文本，但内容为空。")
		return supportai.ToolResult{Content: builder.String()}, nil
	}
	builder.WriteString("正文：\n")
	builder.WriteString(body)
	if oversized {
		builder.WriteString("\n（已达单次读取上限，后续内容未读取）")
	}
	return supportai.ToolResult{Content: builder.String()}, nil
}

// attachmentReadDenial 把授权层的拒绝翻成给模型看的话。
//
// 「不存在」与「无权限」必须是同一句话：分开回答会把附件 publicId 变成探测隐藏
// 资源的接口（与 TopicReadTool 同一条纪律）。第二个返回值只表示这是否是一个
// 「可以解释」的拒绝——内部故障必须向上抛，不能被翻译成一句温和的说明。
func attachmentReadDenial(err error) (supportai.ToolResult, bool) {
	switch {
	case errors.Is(err, attachments.ErrAttachmentNotFound),
		errors.Is(err, identity.ErrPermissionDenied):
		return supportai.ToolResult{Content: "附件不存在或不可见。", IsError: true}, true
	case errors.Is(err, attachments.ErrGuestLoginRequired):
		// 这是站点策略而不是资源可见性：说「不存在」会让模型以为文件被删了。
		return supportai.ToolResult{Content: "读取附件需要登录：当前以访客身份读取。", IsError: true}, true
	case errors.Is(err, attachments.ErrStorageUnavailable):
		return supportai.ToolResult{Content: "附件存储当前不可用，请基于已有信息回答。", IsError: true}, true
	default:
		return supportai.ToolResult{}, false
	}
}

// readAttachmentHead 读取开头至多 limit 字节，并报告是否还有剩余内容。
// 多读一字节是为了区分「刚好等于上限」与「被截断」。
func readAttachmentHead(reader io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// textFromAttachmentBytes 去掉截断点上的半个多字节字符。按字节读、再按 rune 截
// 断，少了这一步就会在结果里留下替换字符，而结果是要进提示词的。
func textFromAttachmentBytes(data []byte) string {
	return strings.TrimSpace(strings.ToValidUTF8(string(data), ""))
}

// attachmentTextExtensions 是「文本类」的兜底判据：上传时 MIME 经常被记成
// application/octet-stream，扩展名才是运营者真正的意图。
var attachmentTextExtensions = map[string]bool{
	"txt": true, "log": true, "md": true, "markdown": true, "csv": true, "tsv": true,
	"json": true, "ndjson": true, "xml": true, "yaml": true, "yml": true,
	"toml": true, "ini": true, "conf": true, "sql": true, "sh": true,
}

// attachmentContentIsText 判定一个附件是否按文本交付。
func attachmentContentIsText(attachment attachments.Attachment) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(attachment.ContentType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case contentType == "application/json",
		contentType == "application/xml",
		contentType == "application/x-yaml",
		contentType == "application/yaml",
		contentType == "application/toml",
		contentType == "application/x-ndjson",
		contentType == "application/csv",
		contentType == "application/javascript":
		return true
	case strings.HasSuffix(contentType, "+json"), strings.HasSuffix(contentType, "+xml"):
		return true
	}
	extension := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(attachment.Extension), "."))
	return attachmentTextExtensions[extension]
}
