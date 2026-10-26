package forum

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

const (
	// 分类/分组删除：目标不存在。
	CodeCategoryNotFound      = "forum.category_not_found"
	CodeCategoryGroupNotFound = "forum.category_group_not_found"
	// 分类/分组删除：仍被主题或下级分类占用，需要先清空。
	CodeCategoryNotEmpty      = "forum.category_not_empty"
	CodeCategoryGroupNotEmpty = "forum.category_group_not_empty"
	// 分类删除：该分类是当前默认发帖分类，删除会让发帖失去默认归属。
	CodeCategoryIsDefault = "forum.category_is_default"
)

var (
	// ErrCategoryNotFound / ErrCategoryGroupNotFound 表示删除目标不存在。
	ErrCategoryNotFound      = errors.New("forum: category not found")
	ErrCategoryGroupNotFound = errors.New("forum: category group not found")
	// ErrCategoryNotEmpty / ErrCategoryGroupNotEmpty 表示仍有下级资源引用，删除被拒绝。
	ErrCategoryNotEmpty      = errors.New("forum: category not empty")
	ErrCategoryGroupNotEmpty = errors.New("forum: category group not empty")
	// ErrCategoryIsDefault 表示目标是当前默认发帖分类，必须先改默认分类。
	ErrCategoryIsDefault = errors.New("forum: category is the default category")
)

// TaxonomyService 是版块分类维护的聚焦协作者：删除权限、默认发帖分类保护与删除事件。
// 与 EditableSourceService 一样复用 Service 的 store/settings/events，不向 God object 继续加方法。
type TaxonomyService struct {
	service *Service
}

func NewTaxonomyService(service *Service) *TaxonomyService {
	return &TaxonomyService{service: service}
}

// DeleteCategoryGroup 删除空分组。分组内仍有分类时由 store 返回
// ErrCategoryGroupNotEmpty，避免后台出现「删不掉又看不到」的死角。
func (s *TaxonomyService) DeleteCategoryGroup(ctx context.Context, actor identity.Actor, groupID int64) (CategoryGroup, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return CategoryGroup{}, identity.ErrPermissionDenied
	}
	deleted, err := s.service.store.DeleteCategoryGroup(ctx, groupID)
	if err != nil {
		return CategoryGroup{}, err
	}
	s.service.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryDeleted, map[string]any{
		"categoryGroupId": deleted.ID,
		"categorySlug":    deleted.Slug,
	}))
	return deleted, nil
}

// DeleteCategory 删除无主题的分类。
// 默认发帖分类被删除会让 createTopic 失去兜底归属，因此先要求运营切换默认分类。
func (s *TaxonomyService) DeleteCategory(ctx context.Context, actor identity.Actor, categoryID int64) (Category, error) {
	if !actor.Can(identity.PermissionCategoryManage) {
		return Category{}, identity.ErrPermissionDenied
	}
	target, err := s.categoryForDeletion(ctx, categoryID)
	if err != nil {
		return Category{}, err
	}
	settings, err := s.service.resolvedSettings(ctx)
	if err != nil {
		return Category{}, err
	}
	if settings.DefaultCategorySlug == target.Slug {
		return Category{}, ErrCategoryIsDefault
	}
	deleted, err := s.service.store.DeleteCategory(ctx, categoryID)
	if err != nil {
		return Category{}, err
	}
	s.service.events.Emit(ctx, appevents.NewEnvelope(appevents.CategoryDeleted, map[string]any{
		"categoryId":   deleted.ID,
		"categorySlug": deleted.Slug,
		"groupId":      deleted.GroupID,
	}))
	return deleted, nil
}

// categoryForDeletion 在删除前定位分类（含隐藏分类），供默认分类保护判定使用。
func (s *TaxonomyService) categoryForDeletion(ctx context.Context, categoryID int64) (Category, error) {
	categories, err := s.service.store.ListCategories(ctx, true)
	if err != nil {
		return Category{}, err
	}
	for _, category := range categories {
		if category.ID == categoryID {
			return category, nil
		}
	}
	return Category{}, ErrCategoryNotFound
}
