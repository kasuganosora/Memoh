<template>
  <div class="space-y-6">
    <div class="space-y-4 mx-auto">
      <div class="rounded-md border p-4 space-y-3">
        <div>
          <Label>{{ $t('bots.settings.persona') }}</Label>
          <p class="text-xs text-muted-foreground mt-0.5">
            {{ $t('bots.settings.personaDescription') }}
          </p>
        </div>
        <div>
          <Textarea
            v-model="personaText"
            :placeholder="personaPlaceholder"
            :aria-label="$t('bots.settings.persona')"
            class="font-mono text-sm min-h-[200px]"
            rows="12"
          />
          <p
            v-if="jsonError"
            class="text-xs text-destructive mt-1"
          >
            {{ jsonError }}
          </p>
        </div>
      </div>

      <div class="flex justify-end">
        <Button
          size="sm"
          :disabled="!settingsChanged || isSaving || !!jsonError"
          @click="handleSaveSettings"
        >
          <Spinner
            v-if="isSaving"
            class="mr-2 size-4"
          />
          {{ $t('bots.settings.save') }}
        </Button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import { toast } from 'vue-sonner'
import { useI18n } from 'vue-i18n'
import { getBotsByBotIdSettings, putBotsByBotIdSettings } from '@memohai/sdk'
import type { SettingsUpsertRequest } from '@memohai/sdk'
import { Button, Spinner, Label, Textarea } from '@memohai/ui'

const { t } = useI18n()
const queryCache = useQueryCache()

const props = defineProps<{ botId: string }>()
const botIdRef = computed(() => props.botId)

const personaText = ref('')
const jsonError = ref('')

const personaPlaceholder = `{
  "name": "Name",
  "creature": "Creature",
  "vibe": "Vibe",
  "emoji": "Emoji",
  "description": "Description"
}`

const { data: settings } = useQuery({
  key: () => ['bot-settings', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: botIdRef.value },
      throwOnError: true,
    })
    return data
  },
})

// Initialize from loaded settings.
watch(
  () => settings.value,
  (val) => {
    if (!val) return
    const p = val.persona
    if (p && typeof p === 'object' && !Array.isArray(p) && Object.keys(p).length > 0) {
      personaText.value = JSON.stringify(p, null, 2)
    } else {
      personaText.value = ''
    }
  },
  { immediate: true },
)

// Validate JSON as user types.
const parsedPersona = computed<Record<string, unknown> | null>(() => {
  const raw = personaText.value.trim()
  if (!raw) return null
  try {
    const obj = JSON.parse(raw)
    if (typeof obj !== 'object' || Array.isArray(obj) || obj === null) {
      return null
    }
    return obj
  } catch {
    return null
  }
})

// Separate watcher for error display (computed must not have side effects).
watch(personaText, (raw) => {
  const trimmed = raw.trim()
  if (!trimmed) {
    jsonError.value = ''
    return
  }
  try {
    const obj = JSON.parse(trimmed)
    if (typeof obj !== 'object' || Array.isArray(obj) || obj === null) {
      jsonError.value = t('bots.settings.personaNotObject')
    } else {
      jsonError.value = ''
    }
  } catch {
    jsonError.value = t('bots.settings.personaInvalidJSON')
  }
})

const settingsChanged = computed(() => {
  if (!settings.value) return false
  const current = settings.value.persona
  const parsed = parsedPersona.value
  if (!parsed && (!current || Object.keys(current).length === 0)) return false
  if (!parsed) return true
  return JSON.stringify(parsed) !== JSON.stringify(current)
})

const { mutateAsync: updateSettings, isLoading: isSaving } = useMutation({
  mutation: async (body: SettingsUpsertRequest) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: botIdRef.value },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['bot-settings', botIdRef.value] }),
})

async function handleSaveSettings() {
  try {
    const parsed = parsedPersona.value
    await updateSettings({
      persona: parsed || {},
    })
    toast.success(t('bots.settings.saveSuccess'))
  } catch {
    return
  }
}
</script>
