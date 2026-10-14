package providers

import (
	"context"
	"io"

	attachments "github.com/zhuchunshu/sforum/apps/api/app/Models/Attachments"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
)

// AIReplyImageReader 把 attachments.Service 适配成 AI 回复的图片读取面。
//
// 读取走 OpenContent——与普通用户下载附件完全相同的授权、可见性与存储适配
// 路径。机器人不因「系统调用」获得任何额外可见性：帖子在私密板块或附件被
// 禁用时，这里拿到的错误与普通读者拿到的是同一个。
type AIReplyImageReader struct {
	attachments *attachments.Service
	users       identity.ActorStore
}

func NewAIReplyImageReader(attachmentsService *attachments.Service, users identity.ActorStore) *AIReplyImageReader {
	return &AIReplyImageReader{attachments: attachmentsService, users: users}
}

// ReadImage 以指定机器人账号身份读取附件，并用 LimitReader 强制 maxBytes
// 硬上限：size_bytes 是写入时的元数据，不能代替实际读取的边界。
func (r *AIReplyImageReader) ReadImage(ctx context.Context, actorUserID int64, publicID string, maxBytes int64) (io.ReadCloser, error) {
	if r == nil || r.attachments == nil || r.users == nil || actorUserID <= 0 || maxBytes <= 0 {
		return nil, identity.ErrPermissionDenied
	}
	actor, err := r.users.LoadActor(ctx, actorUserID)
	if err != nil {
		return nil, err
	}
	_, reader, err := r.attachments.OpenContent(ctx, actor, publicID)
	if err != nil {
		return nil, err
	}
	// 多读一个字节用于判定超限：恰好读到 maxBytes 与超出必须可区分。
	return &limitedImageReader{inner: reader, remaining: maxBytes + 1}, nil
}

// limitedImageReader 在到达上限后截断并返回 EOF；调用方读满 maxBytes+1
// 字节即知道内容超限。
type limitedImageReader struct {
	inner     io.ReadCloser
	remaining int64
}

func (l *limitedImageReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.inner.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func (l *limitedImageReader) Close() error {
	return l.inner.Close()
}
