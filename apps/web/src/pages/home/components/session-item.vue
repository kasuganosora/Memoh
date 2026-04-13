<template>
  <button
    v-if="!isEditing"
    class="group flex items-center h-12 w-full rounded-lg px-2.5 text-left transition-colors cursor-pointer"
    :class="isActive ? 'bg-sidebar-accent' : 'hover:bg-sidebar-accent/60'"
    @click="$emit('select', session)"
    @dblclick="startEditing"
  >
    <div class="relative shrink-0 mr-2.5">
      <Avatar
        v-if="isIMSession"
        class="size-[26px] border border-border bg-accent"
      >
        <AvatarImage
          v-if="avatarUrl"
          :src="avatarUrl"
          :alt="displayLabel"
        />
        <AvatarFallback class="text-[9px] bg-accent text-muted-foreground">
          {{ avatarFallback }}
        </AvatarFallback>
      </Avatar>
      <div
        v-else
        class="flex items-center justify-center size-[26px] rounded-full bg-accent border border-border"
      >
        <component
          :is="iconComponent"
          class="size-2.5"
          :class="iconClass"
        />
      </div>
      <div
        v-if="isIMSession && session.channel_type"
        class="absolute -bottom-px -right-px flex items-center justify-center size-3 rounded-full bg-background border border-border"
      >
        <ChannelBadge
          :platform="session.channel_type"
          class="size-2"
        />
      </div>
    </div>

    <div class="flex flex-col flex-1 min-w-0">
      <div class="flex items-center">
        <span class="truncate text-xs font-medium text-foreground leading-[18px] flex-1">
          {{ session.title || t('chat.untitledSession') }}
        </span>
        <span
          v-if="session.updated_at"
          class="text-[8px] text-muted-foreground ml-1 shrink-0"
        >
          {{ formatTime(session.updated_at) }}
        </span>
      </div>
      <div
        v-if="subLabel"
        class="text-[11px] font-medium text-muted-foreground truncate leading-[16.5px]"
      >
        {{ subLabel }}
      </div>
    </div>
  </button>

  <!-- Inline edit mode (only for web sessions) -->
  <div
    v-else
    class="flex items-center h-12 w-full rounded-lg px-2.5 bg-background"
  >
    <input
      ref="editInput"
      v-model="editTitle"
      class="flex-1 min-w-0 text-xs font-medium text-foreground bg-transparent border border-border rounded px-1.5 py-1 outline-none focus:border-primary"
      maxlength="100"
      @keydown.enter="saveTitle"
      @keydown.escape="cancelEditing"
      @blur="saveTitle"
    >
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, type Component } from 'vue'
import { HeartPulse, Clock, GitBranch, MessageSquare } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { SessionSummary } from '@/composables/api/useChat'
import { updateSessionTitle } from '@/composables/api/useChat.chat-api'
import { useChatStore } from '@/store/chat-list'
import { storeToRefs } from 'pinia'
import { Avatar, AvatarImage, AvatarFallback } from '@memohai/ui'
import ChannelBadge from '@/components/chat-list/channel-badge/index.vue'

const props = defineProps<{
  session: SessionSummary
  isActive: boolean
}>()

defineEmits<{
  select: [session: SessionSummary]
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

const WEB_CHANNELS = new Set(['local', ''])

const isIMSession = computed(() => {
  const ct = (props.session.channel_type ?? '').trim().toLowerCase()
  return ct !== '' && !WEB_CHANNELS.has(ct)
})

const isWebSession = computed(() => {
  const ct = (props.session.channel_type ?? '').trim().toLowerCase()
  return WEB_CHANNELS.has(ct)
})

// --- Rename logic ---
const isEditing = ref(false)
const editTitle = ref('')
const editInput = ref<HTMLInputElement | null>(null)

function startEditing() {
  if (!isWebSession.value) return
  editTitle.value = props.session.title || ''
  isEditing.value = true
  nextTick(() => {
    editInput.value?.focus()
    editInput.value?.select()
  })
}

async function saveTitle() {
  if (!isEditing.value) return
  const newTitle = editTitle.value.trim()
  isEditing.value = false

  if (newTitle === props.session.title) return

  const botId = currentBotId.value
  if (!botId) return

  try {
    await updateSessionTitle(botId, props.session.id, newTitle)
    // Update local store immediately so UI reflects the change
    const target = chatStore.sessions.find(s => s.id === props.session.id)
    if (target) target.title = newTitle
  } catch {
    // Revert on error
  }
}

function cancelEditing() {
  isEditing.value = false
}

const iconComponent = computed<Component>(() => {
  switch (props.session.type) {
    case 'heartbeat': return HeartPulse
    case 'schedule': return Clock
    case 'subagent': return GitBranch
    default: return MessageSquare
  }
})

const iconClass = computed(() => {
  switch (props.session.type) {
    case 'heartbeat': return 'text-rose-400'
    case 'schedule': return 'text-amber-400'
    case 'subagent': return 'text-violet-400'
    default: return 'text-muted-foreground'
  }
})

function routeMeta(): Record<string, unknown> {
  return props.session.route_metadata ?? {}
}

function isGroupConversation(): boolean {
  const ct = (props.session.route_conversation_type ?? '').trim().toLowerCase()
  return ct === 'group' || ct === 'supergroup' || ct === 'channel'
}

const avatarUrl = computed<string | null>(() => {
  const meta = routeMeta()
  if (isGroupConversation()) {
    const convAvatar = (meta.conversation_avatar_url as string ?? '').trim()
    if (convAvatar) return convAvatar
  }
  const url = (meta.sender_avatar_url as string ?? '').trim()
  return url || null
})

const displayLabel = computed(() => {
  const meta = routeMeta()
  return (meta.conversation_name as string ?? '').trim()
    || (meta.sender_display_name as string ?? '').trim()
    || (meta.sender_username as string ?? '').trim()
    || ''
})

const avatarFallback = computed(() => {
  return displayLabel.value ? displayLabel.value.charAt(0).toUpperCase() : '?'
})

const subLabel = computed(() => {
  if (props.session.type === 'discuss') return t('chat.sessionTypeDiscuss')
  if (props.session.type === 'heartbeat') return t('chat.sessionTypeHeartbeat')
  if (props.session.type === 'schedule') return t('chat.sessionTypeSchedule')
  if (props.session.type === 'subagent') return t('chat.sessionTypeSubagent')
  if (!isIMSession.value) return ''
  const meta = routeMeta()
  if (isGroupConversation()) {
    const handle = (meta.conversation_handle as string ?? '').trim()
    if (handle) return handle.startsWith('@') ? handle : `@${handle}`
    const name = (meta.conversation_name as string ?? '').trim()
    if (name) return `@${name}`
  }
  const username = (meta.sender_username as string ?? '').trim()
  if (username) return `@${username}`
  const name = (meta.sender_display_name as string ?? '').trim()
  if (name) return name
  return ''
})

function formatTime(dateStr: string): string {
  try {
    const d = new Date(dateStr)
    if (Number.isNaN(d.getTime())) return ''
    const now = new Date()
    const diff = now.getTime() - d.getTime()
    const day = 86400000
    if (diff < day) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    if (diff < 7 * day) return d.toLocaleDateString(undefined, { weekday: 'short' })
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  } catch {
    return ''
  }
}
</script>
