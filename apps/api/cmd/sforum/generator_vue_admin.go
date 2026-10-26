package main

import (
	"path/filepath"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func scaffoldAdmin(opts makeOptions) extensionmanifest.ManifestAdmin {
	pages := []extensionmanifest.ManifestAdminPage{{
		Path:        "/settings",
		Label:       "Settings",
		Description: "Configure this extension.",
		Icon:        "i-lucide-settings",
		View:        "settings",
		Order:       100,
	}}
	entry := "/settings"
	if opts.VueAdminPage && opts.Kind == extensionmanifest.TypePlugin {
		entry = "/dashboard"
		pages = append([]extensionmanifest.ManifestAdminPage{{
			Path:        "/dashboard",
			Label:       "Dashboard",
			Description: "Manage this extension.",
			Icon:        "i-lucide-layout-dashboard",
			View:        "component",
			Menu:        true,
			Permission:  opts.ID + ".manage",
			Order:       20,
			Component: &extensionmanifest.SettingsComponent{
				ID:         "dashboard",
				APIVersion: extensionmanifest.AdminMicroFrontendAPIVersion,
				Entry:      "frontend/admin/dist/dashboard.mjs",
				CSS:        "frontend/admin/dist/dashboard.css",
			},
		}}, pages...)
	}
	return extensionmanifest.ManifestAdmin{Entry: entry, Pages: pages}
}

func writeVueAdminPageFiles(target string, opts makeOptions) error {
	files := map[string]string{
		"package.json": vueAdminPackageJSON(opts),
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "lib": ["ES2022", "DOM"],
    "types": ["vite/client"]
  },
  "include": ["src/**/*.ts", "src/**/*.vue", "vite.config.ts"]
}
`,
		"vite.config.ts": `import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue()],
  build: {
    emptyOutDir: false,
    cssCodeSplit: false,
    rollupOptions: {
      input: fileURLToPath(new URL('./src/admin.ts', import.meta.url)),
      preserveEntrySignatures: 'exports-only',
      output: {
        format: 'es',
        entryFileNames: 'dashboard.mjs',
        assetFileNames: 'dashboard[extname]'
      }
    }
  }
})
`,
		"src/admin.ts": `import { ADMIN_MICRO_FRONTEND_API_VERSION } from '@sforum/admin-sdk'
import { definePluginPage } from '@sforum/plugin-ui'
import '@sforum/plugin-ui/style.css'
import AdminDashboard from './AdminDashboard.vue'

// definePluginPage 挂载组件并注入宿主图标解析器；不需要手写 createApp，
// 也不会因为忘记注入而让图标静默消失。
export const { apiVersion, mount } = definePluginPage(
  AdminDashboard,
  ADMIN_MICRO_FRONTEND_API_VERSION
)
`,
		"src/AdminDashboard.vue": vueAdminDashboardSFC(opts),
		"dist/dashboard.mjs": `export const apiVersion = 1

export function mount(target, bridge) {
  const root = document.createElement('section')
  root.className = 'sforum-vue-admin-placeholder'
  const text = document.createElement('p')
  text.textContent = bridge.locale.startsWith('zh') ? 'Vue 后台页面已就绪' : 'Vue admin page is ready'
  const button = document.createElement('button')
  button.type = 'button'
  button.textContent = bridge.locale.startsWith('zh') ? '测试操作' : 'Test action'
  const onClick = () => bridge.toast({ title: text.textContent, kind: 'success' })
  button.addEventListener('click', onClick)
  root.append(text, button)
  target.append(root)
  return () => {
    button.removeEventListener('click', onClick)
    root.remove()
  }
}
`,
		"dist/dashboard.css": `.sforum-vue-admin-placeholder {
  display: grid;
  gap: 0.75rem;
  min-width: 0;
}

.sforum-vue-admin-placeholder button {
  width: fit-content;
  border: 1px solid var(--sf-accent, #0d9488);
  border-radius: 0.375rem;
  padding: 0.5rem 0.75rem;
  color: var(--sf-accent, #0d9488);
}
`,
	}
	for relative, body := range files {
		if err := writeFile(filepath.Join(target, "frontend", "admin", relative), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func vueAdminPackageJSON(opts makeOptions) string {
	return `{
  "name": "` + opts.ID + `-admin",
  "private": true,
  "type": "module",
  "scripts": {
    "build": "vite build"
  },
  "dependencies": {
    "@sforum/admin-sdk": "^1.3.0",
    "@sforum/plugin-ui": "^1.1.0",
    "vue": "^3.5.0"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": "^6.0.0",
    "typescript": "^6.0.0",
    "vite": "^8.0.0"
  }
}
`
}

func vueAdminDashboardSFC(opts makeOptions) string {
	return `<script setup lang="ts">
import { ref } from 'vue'
import type { AdminPageBridgeV1 } from '@sforum/admin-sdk'
import {
  SPluginAlert,
  SPluginButton,
  SPluginEmptyState,
  SPluginField,
  SPluginInput,
  SPluginPage,
  SPluginSection,
  SPluginTable,
  type SPluginTableColumn
} from '@sforum/plugin-ui'

const props = defineProps<{ bridge: AdminPageBridgeV1 }>()
const message = ref('Hello from ` + opts.Name + `')
const columns: SPluginTableColumn[] = [
  { key: 'name', label: 'Item', icon: 'i-lucide-box' },
  { key: 'status', label: 'Status', icon: 'i-lucide-activity' }
]
const rows = [{ id: 1, name: 'Vue page', status: 'Ready' }]

function completeAction() {
  props.bridge.toast({
    title: props.bridge.locale.startsWith('zh') ? '操作已完成' : 'Action completed',
    description: message.value,
    kind: 'success'
  })
}
</script>

<template>
  <SPluginPage>
    <SPluginAlert tone="info" icon="i-lucide-shield-check" title="Host shell inherited">
      This body uses Plugin UI SDK components; SForum still owns navigation and permissions.
    </SPluginAlert>

    <SPluginSection
      title="Quick action"
      description="Change the message, then run the action."
      icon="i-lucide-zap"
    >
      <SPluginField label="Message" for="dashboard-message">
        <SPluginInput id="dashboard-message" v-model="message" />
      </SPluginField>
      <template #actions>
        <SPluginButton icon="i-lucide-play" @click="completeAction">Run action</SPluginButton>
      </template>
    </SPluginSection>

    <SPluginSection title="Recent activity" icon="i-lucide-activity">
      <SPluginTable :columns="columns" :rows="rows" row-key="id">
        <template #empty>
          <SPluginEmptyState icon="i-lucide-inbox" title="No activity yet" />
        </template>
      </SPluginTable>
    </SPluginSection>
  </SPluginPage>
</template>
`
}
