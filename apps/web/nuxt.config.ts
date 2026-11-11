import type { NuxtPage } from 'nuxt/schema'
import { execFileSync } from 'node:child_process'
import {
  LEGACY_ADMIN_ROUTE_PREFIX,
  normalizeAdminRoutePrefix
} from './app/utils/admin/adminRoutePrefix'

const appName = process.env.APP_NAME || 'SForum'
const appUrl =
  process.env.NUXT_PUBLIC_I18N_BASE_URL ||
  process.env.APP_URL ||
  'http://127.0.0.1:3000'
const adminRoutePrefix = normalizeAdminRoutePrefix(
  process.env.NUXT_PUBLIC_ADMIN_ROUTE_PREFIX ||
  process.env.ADMIN_ROUTE_PREFIX
)
// 公开主题经 Page Registry 运行时注入；管理端预构建组件按 digest 动态加载。
const nitroOutputDir = process.env.SFORUM_NITRO_OUTPUT_DIR?.trim()
const devtoolsEnabled = process.env.NUXT_DEVTOOLS === 'true'
const sforumVersion = process.env.NUXT_PUBLIC_SFORUM_VERSION?.trim() || 'dev'
const sforumCommit = resolveSForumCommit(process.env.NUXT_PUBLIC_SFORUM_COMMIT)
// Nuxt 4.4 的提取式 payload 依赖路由缓存保持开启；会话页在请求内切到
// no-store 后，`_payload.json` 会退化成 HTML，进而中断客户端导航。
// SSR 数据内联可继续保留 SPA 导航，同时让开发与生产行为一致。
const payloadExtractionEnabled = false
const publicHomepageRouteRule = {
  cache: false,
  headers: {
    'cache-control': 'no-store'
  }
} as const
// 公开只读资源（hash 定址、immutable）必须允许任意来源读取：QQ X5 云加速、
// CDN、企业代理会把文档或子资源换源，浏览器随后按跨源校验。构建产物是模块
// 脚本，模块脚本无条件走 CORS，只能由我们发 ACAO；CORP 同步放开到
// cross-origin，否则 no-cors 资源（样式、图标）仍会被 same-origin 拦掉。
// 私有资产 `/_sforum/private-assets/**` 不在此列，保持 same-origin。
const publicAssetCorsHeaders = {
  'access-control-allow-origin': '*',
  'cross-origin-resource-policy': 'cross-origin',
  'timing-allow-origin': '*'
} as const
const nuxtGeneratedIgnores = [
  '.nuxt/**',
  '.nuxt-build/**',
  '.nuxt-typecheck/**',
  '.output/**',
  '.nitro/**',
  '.vite/**',
  '.cache/**',
  'coverage/**',
  'playwright-report/**',
  'test-results/**'
]

function resolveSForumCommit(injected: string | undefined) {
  const commit = injected?.trim()
  if (commit) return commit

  try {
    return execFileSync('git', ['rev-parse', 'HEAD'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore']
    }).trim()
  } catch {
    return ''
  }
}

const generatedOutputWatchIgnores = [
  '**/.nuxt/**',
  '**/.nuxt-build/**',
  '**/.nuxt-typecheck/**',
  '**/.output/**',
  '**/.nitro/**',
  '**/.vite/**',
  '**/.cache/**',
  '**/dist/**',
  '**/coverage/**',
  '**/playwright-report/**',
  '**/test-results/**'
]

function rewriteAdminPageRoutes(pages: NuxtPage[]) {
  for (const page of pages) {
    if (
      page.path === LEGACY_ADMIN_ROUTE_PREFIX ||
      page.path.startsWith(`${LEGACY_ADMIN_ROUTE_PREFIX}/`)
    ) {
      page.path = page.path.replace(
        LEGACY_ADMIN_ROUTE_PREFIX,
        adminRoutePrefix
      )
    }

    if (page.children?.length) {
      rewriteAdminPageRoutes(page.children)
    }
  }
}

export default defineNuxtConfig({
  modules: ['@nuxt/ui', '@nuxtjs/i18n', '@nuxtjs/seo', '@nuxt/image'],
  ssr: true,
  buildDir: process.env.NUXT_BUILD_DIR || '.nuxt',
  experimental: {
    // 开发态 HMR 后的 SWR payload 可能短暂指向旧路由并返回 HTML/404；内联数据可保持导航与 SSR 一致。
    payloadExtraction: payloadExtractionEnabled
  },
  nitro: {
    // 生产运行时为 Bun（见 Dockerfile prod 阶段）；node-server 产物的 srvx
    // exports 会把 Bun 解析到被 tree-shake 掉的 bun adapter，故显式使用 bun preset。
    preset: 'bun',
    // 静态资源（带 hash 的 _nuxt 文件）压缩为 brotli + gzip。
    compressPublicAssets: { brotli: true, gzip: true },
    // 路由级渲染模式与缓存：全部页面保持 SSR，只有不依赖会话的公开页面才允许共享 SWR。
    // i18n strategy=no_prefix：URL 不含语言前缀，无需 /en/** 镜像规则。
    // 非默认语 cookie 由 server/middleware/locale-cache.ts 绕过共享 SWR，避免串语言。
    routeRules: {
      // 公开只读资源：换源链路下仍需可加载，见 publicAssetCorsHeaders 注释。
      '/_nuxt/**': { headers: publicAssetCorsHeaders },
      '/_sforum/assets/**': { headers: publicAssetCorsHeaders },
      '/_sforum/icons/**': { headers: publicAssetCorsHeaders },
      // 首页发布状态必须在审核通过后立即可见；API 的 Redis generation 缓存承担读压。
      '/': publicHomepageRouteRule,
      // 分类、标签、个人主页与主题的 SSR 内容依赖会话、权限或实时数据，不能共享整页缓存。
      // 分类/标签详情还包含分页 query；Nuxt payload 路径不携带该 query，同样不能共享 SWR。
      '/c/**': { cache: false },
      '/categories': { cache: false },
      '/tags': { cache: false },
      '/tags/**': { cache: false },
      '/u/**': { cache: false },
      // 主题详情含实时评论与会话权限，整页缓存会让 SSR payload 在写入后继续水合旧数据。
      // 主题和评论读路径由 API 的 topic-scoped Redis generation 缓存承担。
      '/t/**': { cache: false },
      // 登录/注册/密码找回与受保护用户页保持 SSR，受保护页显式禁缓存，避免继承公开内容页 SWR。
      // 站外链接中转页：纯跳转中间页，禁缓存且永不索引（robots 由页面 SEO 再兜一层）。
      '/outbound': { cache: false, robots: { index: false } },
      // 账号安全整合后的旧路径兼容：登录方式 / 密码已收进 /settings/security 子路由，
      // 302 保住收藏夹与外部文档；in-app 链接已全部指向新路径，仅外部入口命中。
      '/settings/login-methods': { redirect: { to: '/settings/security/login-methods', statusCode: 302 } },
      '/settings/password': { redirect: { to: '/settings/security/password', statusCode: 302 } },
      '/settings/**': { cache: false, robots: { index: false } },
      '/topics/new': { cache: false, robots: { index: false } },
      // 管理后台：SSR + 禁缓存 + 禁止索引。未登录由 admin 中间件服务端重定向到 /login，不再返回空壳。
      [`${adminRoutePrefix}/**`]: { cache: false, robots: { index: false } },
      // 组件预览页：SSR（生产环境直接渲染 404 错误页，不再先返回空壳）。
      '/components': { robots: { index: false } },
      // 静态图标目录：数据完全静态，长缓存。
      '/api/icon-collections/**': { cache: { maxAge: 86400 } },
      '/api/_sitemap-urls': { cache: { maxAge: 600 } }
    },
    ...(nitroOutputDir ? { output: { dir: nitroOutputDir } } : {})
  },
  ignore: nuxtGeneratedIgnores,
  css: [
    '~/assets/css/main.css',
    '~/assets/css/sforum-components.css',
    '~/assets/css/sforum-content-semantics.css',
    '~/assets/css/sforum-mention.css',
    '~/assets/css/sforum-altcha.css',
    '~/assets/css/sforum-theme.css',
    '~/assets/css/sforum-home.css',
    '~/assets/css/sforum-home-pinned.css',
    '~/assets/css/sforum-moderation.css',
    '~/assets/css/sforum-topic.css',
    '~/assets/css/sforum-taxonomy.css',
    '~/assets/css/sforum-tags.css',
    '~/assets/css/sforum-profile.css',
    // 档位配色 token（D-C5）：只定义 --sf-tl-*，必须在资料页/主题 token 之后加载。
    '~/assets/css/sforum-trust-colors.css',
    // 排序/分段控件基线必须在 home / taxonomy / tags 之后加载（同级声明后置覆盖）
    '~/assets/css/sforum-sort-control.css',
    '~/assets/css/highlight-theme.css',
    // 富内容容器依赖 highlight-theme 的 .sf-code-block，必须排在它之后。
    '~/assets/css/rich-content.css'
  ],
  // DevTools 会扩大开发期依赖扫描和常驻内存；需要调试时用 NUXT_DEVTOOLS=true 显式开启。
  devtools: { enabled: devtoolsEnabled },
  ui: {
    fonts: false
  },
  colorMode: {
    classSuffix: ''
  },
  icon: {
    provider: 'server',
    fallbackToApi: false,
    collections: ['lucide', 'tabler'],
    serverBundle: {
      collections: ['lucide', 'tabler']
    }
  },
  image: {
    // 头像与用户上传图片优化：默认 webp 格式，惰性加载。
    format: ['webp'],
    quality: 80,
    // 头像尺寸预设，SFAvatar 按 size prop 选用对应宽度。
    screens: {
      xs: 48,
      sm: 96,
      md: 128,
      lg: 256
    }
  },
  vite: {
    resolve: {
      dedupe: ['vue', 'vue-router', 'nuxt', '@nuxt/ui']
    },
    // 预声明会被运行时 import 的依赖，让 Vite 冷启动就预打包好。
    // 否则浏览器首次打开页面时才扫描发现这些 CJS 依赖（altcha、highlight、tiptap）
    // 或 devtools 子依赖，触发 full page reload，叠加成肉眼可见的“网页卡住”。
    optimizeDeps: {
      include: [
        ...(devtoolsEnabled ? ['@vue/devtools-core', '@vue/devtools-kit'] : []),
        'altcha',
        'altcha/i18n/en',
        'altcha/i18n/zh-cn',
        'dompurify',
        'parse5',
        // 富文本渲染器只在遇到对应围栏时才 import()：不预打包的话，用户第一次看到
        // 公式/图表/流程图就会触发一次运行时发现 + 整页 reload。
        'katex',
        'mermaid',
        'chart.js/auto',
        'highlight.js/lib/common',
        'highlight.js/lib/languages/dart',
        'highlight.js/lib/languages/dockerfile',
        'highlight.js/lib/languages/http',
        'highlight.js/lib/languages/nginx',
        'highlight.js/lib/languages/powershell',
        '@tiptap/vue-3',
        '@tiptap/vue-3/menus',
        '@tiptap/extension-character-count',
        '@tiptap/extension-image',
        '@tiptap/extension-link',
        '@tiptap/extension-placeholder',
        '@tiptap/extension-underline',
        '@tiptap/markdown',
        '@tiptap/starter-kit'
      ]
    },
    server: {
      watch: {
        ignored: generatedOutputWatchIgnores
      }
    }
  },
  vue: {
    compilerOptions: {
      isCustomElement: (tag) => tag === 'altcha-widget'
    }
  },
  // nuxt-seo-utils 的 useSeoMeta tree-shaking 需要 @unhead/bundler，而该包只有 v3
  // 系列（本仓仍随 Nuxt 4.4 使用 Unhead v2），装不上也跑不了，只会每次启动告警。
  // 关掉它：useSeoMeta 不再按字段裁剪，体积差异可忽略。Nuxt 升到自带 unhead v3
  // vite 插件（>=4.5）后可移除此项恢复裁剪。
  seo: {
    treeShakeUseSeoMeta: false
  },
  schemaOrg: {
    enabled: false
  },
  robots: {
    credits: false,
    header: false,
    sitemap: '/sitemap.xml',
    disallow: [
      '/api/',
      '/login',
      '/register',
      '/components',
      adminRoutePrefix,
      `${adminRoutePrefix}/`
    ]
  },
  // multi-sitemaps 下顶层 includeAppSources 会被忽略；子 sitemap 默认不启用
  // app sources（opt-in），我们只用自定义 API sources，故不在此设置。
  sitemap: {
    credits: false,
    sitemaps: {
      static: { sources: ['/api/_sitemap-urls'] },
      categories: { sources: ['/api/_sitemap-categories'] },
      tags: { sources: ['/api/_sitemap-tags'] },
      topics: { sources: ['/api/_sitemap-topics'], chunks: true },
      profiles: { sources: ['/api/_sitemap-profiles'] }
    },
    exclude: [
      '/api/**',
      '/login',
      '/register',
      '/components',
      `${adminRoutePrefix}/**`
    ]
  },
  runtimeConfig: {
    // nuxt-site-config 只有在 runtimeConfig 中声明过键，才会在容器启动时
    // 接受 NUXT_SITE_URL 覆盖。保留运行时槽位，避免反代内部 Host 成为 SEO URL。
    site: {
      url: appUrl
    },
    public: {
      apiBaseUrl: process.env.NUXT_PUBLIC_API_BASE_URL || '/api/v1',
      adminRoutePrefix,
      appLocale: process.env.APP_LOCALE || 'zh-CN',
      supportedLocales: process.env.SUPPORTED_LOCALES || 'zh-CN,en-US',
      sforumBuild: {
        version: sforumVersion,
        commit: sforumCommit,
        builtAt: process.env.NUXT_PUBLIC_SFORUM_BUILD_TIME || ''
      },
      i18n: {
        baseUrl: appUrl
      }
    }
  },
  site: {
    name: appName
  },
  app: {
    head: {
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' }
      ],
      script: [
        {
          // 富内容水合前的骨架标记：SSR 输出的是普通代码块，先把它遮成骨架，避免
          // 「先看到源码、再变成图」的闪屏。客户端 pass 起来后改写为 ready；若 8 秒
          // 仍未接管（脚本被文档 CSP 拦、bundle 加载失败），自动摘掉标记让源码照常可见。
          innerHTML: "document.documentElement.dataset.sfRich='pending';setTimeout(function(){if(document.documentElement.dataset.sfRich==='pending')delete document.documentElement.dataset.sfRich},8000)"
        }
      ]
    }
  },
  // baseUrl 只从 runtimeConfig.public.i18n 注入。写在这里会被
  // nuxt-site-config 烘焙进镜像，并在启动时覆盖 NUXT_SITE_URL。
  i18n: {
    defaultLocale: 'zh-CN',
    // 无感切换：URL 不带语言前缀；locale 由 cookie / setLocale 决定。
    strategy: 'no_prefix',
    langDir: 'locales',
    // i18n 10.4 的 Nitro context 实验路径在 dev 冷启动/HMR 窗口可能先进入
    // render:before、后执行 request 初始化，导致首页偶发 500。常规路由检测保留。
    experimental: {
      nitroContextDetection: false
    },
    locales: [
      {
        code: 'zh-CN',
        language: 'zh-CN',
        name: '简体中文',
        file: 'zh-CN.json'
      },
      {
        code: 'en',
        language: 'en-US',
        name: 'English',
        file: 'en-US.json'
      }
    ],
    detectBrowserLanguage: {
      useCookie: true,
      cookieKey: 'sforum_locale',
      // no_prefix 下没有可跳转的 locale 路径；只做 cookie/浏览器协商，不 302。
      alwaysRedirect: false,
      fallbackLocale: 'zh-CN'
    }
  },
  hooks: {
    'pages:extend'(pages) {
      rewriteAdminPageRoutes(pages)
    },
    // Nitro 已处理 sitemap 的重定向和 XML 响应；Nuxt 为 redirect rule 注入的空页面组件会触发 Vue SSR 警告。
    'pages:resolved'(pages) {
      const sitemapRouteIndex = pages.findIndex(page => page.path === '/sitemap.xml')
      if (sitemapRouteIndex !== -1) {
        pages.splice(sitemapRouteIndex, 1)
      }
    },
    'prepare:types'(options) {
      options.tsConfig.compilerOptions ||= {}
      options.tsConfig.compilerOptions.paths ||= {}
      // 主题 layer 位于 apps/web 外，TypeScript 不会从 layer 路径向下找到宿主依赖。
      options.tsConfig.compilerOptions.paths.altcha = [
        '../node_modules/altcha/dist/types/generic.d.ts'
      ]
    }
  },
  ogImage: {
    enabled: false
  },
  compatibilityDate: '2026-07-03'
})
