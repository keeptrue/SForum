package aireply

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"

	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

// 图片收集只发生在站点开启多模态时。收集范围刻意收窄：主楼与被回复那条评论
// 关联的公开图片附件。近期讨论窗口里的图片不收集——10 条评论的图全带上会把
// 单次请求打到不可解释的规模，而被回复对象才是机器人要正面回应的内容。

// ImageAttachment 是 reader 查出的图片附件定位信息。字节内容不在 SQL 里读：
// 附件可能几百 KB，SQL 行里塞字节数组只是徒增一次拷贝。PublicID 走附件的
// 正常读取路径（与普通用户同一套授权与存储适配），而不是绕开它直连存储。
type ImageAttachment struct {
	PublicID    string
	ContentType string
}

// ImageReader 读取附件字节。它由装配层用 attachments.Service 实现；接口保持
// 最小，回复模块不需要知道存储适配器、授权与禁用回落的细节。
type ImageReader interface {
	// ReadImage 以指定机器人账号的身份返回附件内容——读取走与普通用户完全
	// 相同的授权路径，机器人不因「系统调用」获得任何额外可见性。
	// maxBytes 是硬上限，超出按不可用处理。
	ReadImage(ctx context.Context, actorUserID int64, publicID string, maxBytes int64) (io.ReadCloser, error)
}

// collectImages 选出本次回复要带的图片：按 MaxImagesPerMessage 截断，单张
// 超过 MaxImageBytes 或读不出来的直接跳过——一张大图或一次存储抖动不该让
// 整次回复失败。
func collectImages(
	ctx context.Context,
	reader ImageReader,
	actorUserID int64,
	attachments []ImageAttachment,
) []supportai.Part {
	if reader == nil || len(attachments) == 0 {
		return nil
	}
	parts := make([]supportai.Part, 0, supportai.MaxImagesPerMessage)
	for _, attachment := range attachments {
		if len(parts) >= supportai.MaxImagesPerMessage {
			break
		}
		if !supportai.ValidImageMediaType(attachment.ContentType) {
			continue
		}
		part, err := readImagePart(ctx, reader, actorUserID, attachment)
		if err != nil {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

func readImagePart(ctx context.Context, reader ImageReader, actorUserID int64, attachment ImageAttachment) (supportai.Part, error) {
	stream, err := reader.ReadImage(ctx, actorUserID, attachment.PublicID, supportai.MaxImageBytes)
	if err != nil {
		return supportai.Part{}, err
	}
	defer stream.Close()
	// 多读一个字节用于判定超限：Attachment.size_bytes 是写入时的值，不能以
	// 它代替实际读取（存储内容可能被后台替换）。
	data, err := io.ReadAll(io.LimitReader(stream, supportai.MaxImageBytes+1))
	if err != nil {
		return supportai.Part{}, err
	}
	if len(data) == 0 || len(data) > supportai.MaxImageBytes {
		return supportai.Part{}, fmt.Errorf("image exceeds %d bytes", supportai.MaxImageBytes)
	}
	return supportai.Part{
		Type: supportai.PartImage,
		Image: &supportai.ImageContent{
			MediaType: attachment.ContentType,
			Data:      base64.StdEncoding.EncodeToString(data),
		},
	}, nil
}
