package options

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// PublicSurfaceRevisionTxBumper keeps a cross-domain mutation in the caller's
// transaction. SiteChrome uses this narrow capability instead of writing
// web_options directly.
type PublicSurfaceRevisionTxBumper interface {
	BumpPublicSurfaceRevisionTx(context.Context, pgx.Tx) (int64, error)
	Invalidate()
}

type publicSurfaceRevisionTxStore interface {
	BumpPublicSurfaceRevisionTx(context.Context, pgx.Tx) (int64, error)
}

type publicSurfaceRevisionTxBumper struct {
	store      publicSurfaceRevisionTxStore
	invalidate func()
}

// NewPublicSurfaceRevisionTxBumper keeps transaction-only mutation outside the
// legacy Options.Service method set while retaining its cache invalidation.
func NewPublicSurfaceRevisionTxBumper(service *Service) PublicSurfaceRevisionTxBumper {
	if service == nil {
		return nil
	}
	store, _ := service.store.(publicSurfaceRevisionTxStore)
	return &publicSurfaceRevisionTxBumper{store: store, invalidate: service.Invalidate}
}

func (b *publicSurfaceRevisionTxBumper) BumpPublicSurfaceRevisionTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	if b == nil || b.store == nil || tx == nil {
		return 0, fmt.Errorf("bump public surface revision: transaction support is required")
	}
	return b.store.BumpPublicSurfaceRevisionTx(ctx, tx)
}

func (b *publicSurfaceRevisionTxBumper) Invalidate() {
	if b != nil && b.invalidate != nil {
		b.invalidate()
	}
}

// 公开前端贡献面 revision 默认从 1 起；未写入时读路径也回落 1。
const publicSurfaceRevisionDefault = 1

func init() {
	optionDefinitions = append(optionDefinitions, optionDefinition{
		// public：Nuxt server 通过 GET /web-options/:name 读取；不进入运营可写路径。
		name:             NamePublicSurfaceRevision,
		public:           true,
		managePermission: identity.PermissionSettingsSiteManage,
	})
}

// PublicSurfaceRevision 返回当前公开前端贡献面 revision（至少为 1）。
func (s *Service) PublicSurfaceRevision(ctx context.Context) (int64, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return publicSurfaceRevisionDefault, err
	}
	return parsePublicSurfaceRevision(values[NamePublicSurfaceRevision]), nil
}

// BumpPublicSurfaceRevision 将 revision +1（从 1 起）。扩展设置变更且影响公开贡献时调用。
// 失败返回 error；调用方应记录但不阻塞设置保存主路径的业务结果（由扩展服务决定）。
func (s *Service) BumpPublicSurfaceRevision(ctx context.Context) (int64, error) {
	values, err := s.loadMapFresh(ctx)
	if err != nil {
		return 0, err
	}
	next := parsePublicSurfaceRevision(values[NamePublicSurfaceRevision]) + 1
	if next < publicSurfaceRevisionDefault+1 {
		next = publicSurfaceRevisionDefault + 1
	}
	value := strconv.FormatInt(next, 10)
	if _, err := s.store.Upsert(ctx, UpdateInput{Name: NamePublicSurfaceRevision, Value: value}); err != nil {
		return 0, fmt.Errorf("bump public surface revision: %w", err)
	}
	s.Invalidate()
	return next, nil
}

func parsePublicSurfaceRevision(raw string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || parsed < publicSurfaceRevisionDefault {
		return publicSurfaceRevisionDefault
	}
	return parsed
}

func normalizePublicSurfaceRevision(value string) (string, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < publicSurfaceRevisionDefault {
		return "", false
	}
	return strconv.FormatInt(parsed, 10), true
}

func mergePublicSurfaceRevisionDefaults(values map[string]string) {
	if _, ok := normalizePublicSurfaceRevision(values[NamePublicSurfaceRevision]); !ok {
		values[NamePublicSurfaceRevision] = strconv.Itoa(publicSurfaceRevisionDefault)
	}
}

func coercePublicSurfaceRevisionOptions(coerced, defaults map[string]string) {
	if value, ok := normalizePublicSurfaceRevision(coerced[NamePublicSurfaceRevision]); ok {
		coerced[NamePublicSurfaceRevision] = value
		return
	}
	if value, ok := normalizePublicSurfaceRevision(defaults[NamePublicSurfaceRevision]); ok {
		coerced[NamePublicSurfaceRevision] = value
		return
	}
	coerced[NamePublicSurfaceRevision] = strconv.Itoa(publicSurfaceRevisionDefault)
}
