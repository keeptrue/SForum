import { describe, expect, test } from 'bun:test'
import { defineComponent, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import { useTopicReaction } from '~/composables/forum/useTopicReaction'
import { compileVueSfc, mount } from '../helpers/vueSfc'

const barPath = fileURLToPath(new URL('../../app/components/forum/SFTopicReactionBar.vue', import.meta.url))

// 控件被替换成会 emit change 的桩：本用例只验证「子组件 change → 父层 topic 回写」这条链。
const ReactionControlStub = defineComponent({
  name: 'SFReactionControl',
  emits: ['change'],
  template: '<button data-testid="like" @click="$emit(\'change\', true, 5)">like</button>'
})

const TopicReactionBar = await compileVueSfc(barPath, 'topic-reaction-bar', {
  SFReactionControl: ReactionControlStub,
  useI18n: () => ({ t: (key: string) => key })
})

const ParentHarness = defineComponent({
  components: { TopicReactionBar },
  setup() {
    const topic = ref({ likeCount: 2, likedByMe: false })
    const { applyReactionChange } = useTopicReaction(topic)
    return { topic, applyReactionChange }
  },
  template: `
    <div>
      <TopicReactionBar
        :topic-id="7"
        :like-count="topic.likeCount"
        :liked-by-me="topic.likedByMe"
        :show-reply="false"
        @change="applyReactionChange"
      />
      <span data-testid="count">{{ topic.likeCount }}</span>
      <span data-testid="liked">{{ topic.likedByMe }}</span>
    </div>
  `
})

describe('topic reaction change wiring', () => {
  test('the action row re-emits the control change and the parent topic state follows it', async () => {
    const wrapper = mount(ParentHarness, {
      global: { stubs: { UIcon: true } }
    })
    try {
      expect(wrapper.get('[data-testid="count"]').text()).toBe('2')
      expect(wrapper.get('[data-testid="liked"]').text()).toBe('false')

      await wrapper.get('[data-testid="like"]').trigger('click')

      expect(wrapper.get('[data-testid="count"]').text()).toBe('5')
      expect(wrapper.get('[data-testid="liked"]').text()).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })
})
