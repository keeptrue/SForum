import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

// 呈现已迁到 body 岛；路由页仅为 SEO/outlet 壳。
const tagsPage = () => readFileSync(
  new URL('../../app/components/forum/SFTagIndexPage.vue', import.meta.url),
  'utf8'
)
const categoriesPage = () => readFileSync(
  new URL('../../app/components/forum/SFCategoryIndexPage.vue', import.meta.url),
  'utf8'
)
const tagsRoute = () => readFileSync(
  new URL('../../app/pages/tags/index.vue', import.meta.url),
  'utf8'
)
const homeNavigation = () => readFileSync(
  new URL('../../app/components/forum/SFHomeNavigation.vue', import.meta.url),
  'utf8'
)
const publicSidebarContent = () => readFileSync(
  new URL('../../app/components/forum/navigation/SFPublicSidebarContent.vue', import.meta.url),
  'utf8'
)
const categoryNavigationBlock = () => readFileSync(
  new URL('../../app/components/forum/navigation/SFCategoryNavigationBlock.vue', import.meta.url),
  'utf8'
)
const categoriesRoute = () => readFileSync(
  new URL('../../app/pages/categories/index.vue', import.meta.url),
  'utf8'
)
const taxonomyCss = () => readFileSync(
  new URL('../../app/assets/css/sforum-taxonomy.css', import.meta.url),
  'utf8'
)
const navbar = () => readFileSync(
  new URL('../../app/components/SFNavbar.vue', import.meta.url),
  'utf8'
)
const tagsCss = () => readFileSync(
  new URL('../../app/assets/css/sforum-tags.css', import.meta.url),
  'utf8'
)
const homeCss = () => readFileSync(
  new URL('../../app/assets/css/sforum-home.css', import.meta.url),
  'utf8'
)
const hostNuxtConfig = () => readFileSync(new URL('../../nuxt.config.ts', import.meta.url), 'utf8')
const zh = () => JSON.parse(readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8'))
const en = () => JSON.parse(readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8'))

describe('public taxonomy list pages (T02 + C04)', () => {
  test('taxonomy routes are thin outlet shells with island fallbacks', () => {
    expect(tagsRoute()).toContain('SFPageOutlet')
    expect(tagsRoute()).toContain('page="forum.tag.index"')
    expect(tagsRoute()).toContain('<SFTagIndexPage')
    expect(tagsRoute()).not.toContain('sforum-taxonomy__cloud')
    expect(categoriesRoute()).toContain('SFPageOutlet')
    expect(categoriesRoute()).toContain('page="forum.category.index"')
    expect(categoriesRoute()).toContain('<SFCategoryIndexPage')
    expect(categoriesRoute()).not.toContain('sforum-taxonomy__tile')
  })

  test('tags index uses listTags, public_pages gate, heat overview, drawers, and tag detail links', () => {
    const source = tagsPage()
    expect(source).toContain('forumApi.listTags()')
    // RV-35：分类目录走公开面共享 composable，不再自带一条 useAsyncData key。
    expect(source).toContain('useCategoryGroups()')
    expect(source).toContain("webOption('forum.tags.public_pages'")
    expect(source).toContain('parseForumTagPublicPagesOption')
    expect(source).toContain('filterTagIndexTags')
    expect(source).toContain('tagHeatEntries')
    expect(source).toContain('tagIndexOverview')
    expect(source).toContain('recentTagIndexTags')
    expect(source).toContain('forumTagPath(slug)')
    expect(source).toContain('forum-tags-index-rendered-at')
    expect(source).toContain("key: 'all'")
    expect(source).toContain("key: 'hot'")
    expect(source).toContain("key: 'week'")
    expect(source).toContain("key: 'az'")
    expect(source).toContain('TAG_FILTERS')
    // 三栏壳对齐首页 / 通知 / 分类目录
    expect(source).toContain('sforum-home__layout--with-right')
    expect(source).toContain('sforum-home__sidebar')
    expect(source).toContain('sforum-home__right')
    expect(source).toContain('data-layout="fullwidth-3col"')
    expect(source).toContain('forum.component.tag_index')
    expect(source).toContain(':show-categories="false"')
    expect(source).toContain('#after-navigation')
    expect(source).toContain('SFTagIndexRightRail')
    expect(source).toContain('SFContentColumnFooter')
    expect(source).toContain('sforum-tags-page__heat-board')
    expect(source).toContain('sforum-tags-page__directory')
    expect(source).toContain('<SFResponsivePublicSidebar')
    expect(source).toContain('owner-id="forum.tag.index"')
    expect(source).not.toContain('sforum-mobile-drawer--left')
    expect(source).toContain('sforum-mobile-drawer--right')
    expect(source).toContain('tagsError')
  })

  test('categories index uses category groups, directory layout, focus, filter, and category detail links', () => {
    const source = categoriesPage()
    expect(source).toContain('useCategoryGroups()')
    expect(source).toContain('visibleCategoryDirectoryGroups')
    expect(source).toContain('buildCategoryDirectoryDisplayGroups')
    expect(source).toContain('summarizeCategoryDirectory')
    expect(source).toContain('activeCategoryDirectoryCategories')
    expect(source).toContain('forumCategoryPath(category.slug)')
    expect(source).toContain('sforum-category-directory__board')
    expect(source).toContain('sforum-home__layout--with-right')
    expect(source).toContain('SFHomeNavigation')
    expect(source).toContain('<SFResponsivePublicSidebar')
    expect(source).toContain('owner-id="forum.category.index"')
    expect(source).not.toContain('mobileMenuOpen')
    expect(source).toContain('mobileInfoOpen')
    expect(source).toContain('route.query.group')
    expect(source).toContain('v-model="selectedGroupKey"')
    expect(source).toContain('groupOptions')
    expect(source).toContain("const ALL_GROUPS_VALUE = '__all_groups__'")
    expect(source).not.toContain("value: ''")
    expect(source).toContain('#after-navigation')
    expect(source).toContain('CATEGORY_FILTERS')
    expect(source).toContain("key: 'week'")
    expect(source).not.toContain('categoryDirectoryDistribution')
    expect(source).toContain('filterDraft')
    expect(source).toContain("filter === item.key")
    expect(source).toContain('setFilter(item.key, true)')
    expect(source).toContain('category.iconColor')
    expect(source).not.toContain('latestReply')
    expect(source).not.toContain('viewCount')
    expect(source).not.toContain('growth')
  })

  test('theme registers taxonomy CSS and taxonomy routes disable whole-page caching', () => {
    expect(hostNuxtConfig()).toContain('sforum-taxonomy.css')
    expect(hostNuxtConfig()).toContain('sforum-tags.css')
    expect(tagsCss()).toContain('.sforum-tags-page__heat-board')
    // 断点由 sforum-home 壳承担，标签页 CSS 只保留主列业务与右栏内容微调
    expect(tagsCss()).toContain('.sforum-tags-page__recent-list')
    expect(tagsCss()).toContain('.sforum-tags-page__filter-nav')
    expect(homeCss()).toContain('@media (max-width: 1180px)')
    expect(homeCss()).toContain('@media (max-width: 960px)')
    expect(taxonomyCss()).toContain('.sforum-taxonomy__tile')
    expect(taxonomyCss()).toContain('.sforum-category-directory__board-grid')
    expect(taxonomyCss()).toContain('.sforum-category-directory__facts')
    const config = hostNuxtConfig()
    for (const route of ['/categories', '/tags', '/c/**', '/tags/**']) {
      expect(config).toContain(`'${route}': { cache: false }`)
    }
    expect(config).not.toContain("'/categories': { swr:")
    expect(config).not.toContain("'/tags': { swr:")
  })

  test('topbar search remains the global search entry', () => {
    const source = navbar()
    expect(source).toContain('<SFSearch')
    expect(source).toContain("t('home.searchPlaceholder')")
    expect(source).toContain('@submit="submitSearch"')
    expect(source).toContain("path: localePath(normalizedQuery ? '/search' : '/')")
    expect(source).toContain('buildForumHomeQuery')
    expect(source).not.toContain('filterDraft')
    expect(source).not.toContain('taxonomy.categories.filterPlaceholder')
  })

  test('shared home navigation highlights taxonomy routes in route mode', () => {
    expect(homeNavigation()).toContain('<SFPublicSidebarContent')
    const source = publicSidebarContent()
    const categorySource = categoryNavigationBlock()
    expect(source).toContain("const target = String(navigationItemTo(item)).split('?')[0]")
    expect(source).toContain('routePath.value === target || routePath.value.startsWith(`${target}/`)')
    expect(source).toContain("if (item.sourceKey === 'core.home') return allTopicsActive.value")
    expect(categorySource).toContain("selectedCategorySlug === category.slug")
    expect(source).toContain("void router.push(slug ? categoryTo(slug) : allTopicsTo())")
    expect(homeCss()).toContain('.sf-home-navigation__foot')
    expect(homeCss()).toContain('.sf-home-navigation__foot a')
  })

  test('bilingual taxonomy copy is present', () => {
    expect(zh().taxonomy.tags.title).toBe('全部标签')
    expect(zh().taxonomy.tags.show.relatedNav).toBe('相关标签')
    expect(zh().taxonomy.tags.show.viewAll).toBe('全部标签')
    expect(zh().taxonomy.categories.title).toBe('全部分类')
    expect(zh().taxonomy.categories.filterLabel).toBe('分类筛选')
    expect(zh().taxonomy.categories.filters.week).toBe('本周')
    expect(zh().taxonomy.categories.filterPlaceholder).toContain('筛选分类')
    expect(en().taxonomy.tags.title).toBe('All tags')
    expect(en().taxonomy.tags.show.relatedNav).toBe('Related tags')
    expect(en().taxonomy.tags.show.viewAll).toBe('All tags')
    expect(en().taxonomy.categories.title).toBe('All categories')
    expect(en().taxonomy.categories.filterLabel).toBe('Category filters')
    expect(en().taxonomy.categories.filters.week).toBe('This week')
    expect(en().taxonomy.categories.filterPlaceholder).toContain('Filter category')
  })

  test('theme L1 taxonomy shells mark presentation ownership and chrome islands', () => {
    const root = new URL('../../../../extensions/builtin/themes/', import.meta.url)
    for (const theme of ['sforum-default']) {
      for (const [file, pageId] of [
        ['tag-index.html', 'forum.tag.index'],
        ['category-index.html', 'forum.category.index'],
        ['tag-show.html', 'forum.tag.show'],
        ['category-show.html', 'forum.category.show'],
      ] as const) {
        const tpl = readFileSync(new URL(`${theme}/templates/${file}`, root), 'utf8')
        expect(tpl).toContain('data-theme-owned="presentation"')
        expect(tpl).toContain(`data-page="${pageId}"`)
        expect(tpl).toContain('<sf-navbar')
        expect(tpl).toContain('<sf-footer')
      }
    }
  })
})
