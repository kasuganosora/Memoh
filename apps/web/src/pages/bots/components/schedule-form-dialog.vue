<template>
  <FormDialogShell
    v-model:open="open"
    :title="dialogTitle"
    :cancel-text="$t('common.cancel')"
    :submit-text="submitText"
    :submit-disabled="!canSubmit"
    :loading="isSaving"
    max-width-class="sm:max-w-[560px]"
    @submit="handleSubmit"
  >
    <template #body>
      <div class="mt-4 flex flex-col gap-4">
        <div class="flex items-end gap-3">
          <div class="space-y-1.5 flex-1 min-w-0">
            <Label for="schedule-name">
              {{ $t('bots.schedule.form.name') }}
            </Label>
            <Input
              id="schedule-name"
              v-model="form.name"
              :placeholder="$t('bots.schedule.form.namePlaceholder')"
            />
          </div>
          <div class="flex items-center gap-2 h-9 shrink-0">
            <Label
              class="cursor-pointer text-xs"
              @click="form.enabled = !form.enabled"
            >
              {{ $t('bots.schedule.form.enabled') }}
            </Label>
            <Switch
              :model-value="form.enabled"
              @update:model-value="(v: boolean) => form.enabled = !!v"
            />
          </div>
        </div>

        <div class="space-y-1.5">
          <Label
            for="schedule-description"
            class="flex items-center gap-1.5"
          >
            {{ $t('bots.schedule.form.description') }}
            <span class="text-[11px] text-muted-foreground font-normal">
              ({{ $t('common.optional') }})
            </span>
          </Label>
          <Input
            id="schedule-description"
            v-model="form.description"
            :placeholder="$t('bots.schedule.form.descriptionPlaceholder')"
          />
        </div>

        <div class="space-y-1.5">
          <Label for="schedule-command">
            {{ $t('bots.schedule.form.command') }}
          </Label>
          <Textarea
            id="schedule-command"
            v-model="form.command"
            class="text-xs"
            :placeholder="$t('bots.schedule.form.commandPlaceholder')"
            rows="3"
          />
          <p class="text-xs text-muted-foreground">
            {{ $t('bots.schedule.form.commandHint') }}
          </p>
        </div>

        <div class="space-y-1.5">
          <Label>{{ $t('bots.schedule.form.pattern') }}</Label>
          <SchedulePatternBuilder
            :state="patternState"
            :timezone="timezone"
            @update:state="(next) => patternState = next"
          />
        </div>

        <div class="space-y-1.5">
          <div class="flex items-center justify-between">
            <Label>{{ $t('bots.schedule.form.maxCalls') }}</Label>
            <div class="flex items-center gap-2">
              <Switch
                :model-value="maxCallsUnlimited"
                @update:model-value="(v: boolean) => handleMaxCallsUnlimited(!!v)"
              />
              <span class="text-xs text-muted-foreground">
                {{ $t('bots.schedule.form.maxCallsUnlimited') }}
              </span>
            </div>
          </div>
          <Input
            v-if="!maxCallsUnlimited"
            :model-value="form.maxCalls ?? 1"
            type="number"
            :min="1"
            :placeholder="'1'"
            @update:model-value="(v) => form.maxCalls = Math.max(1, Math.floor(Number(v) || 1))"
          />
        </div>

        <p
          v-if="submitError"
          class="text-xs text-destructive"
        >
          {{ submitError }}
        </p>
      </div>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Input, Label, Switch, Textarea } from '@memohai/ui'
import {
  postBotsByBotIdSchedule,
  putBotsByBotIdScheduleById,
  type ScheduleCreateRequest,
  type ScheduleSchedule,
  type ScheduleUpdateRequest,
} from '@memohai/sdk'
import FormDialogShell from '@/components/form-dialog-shell/index.vue'
import {
  defaultScheduleFormState,
  fromCron,
  isValidCron,
  toCron,
  type ScheduleFormState,
} from '@/utils/cron-pattern'
import SchedulePatternBuilder from './schedule-pattern-builder.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
  mode: 'create' | 'edit'
  schedule?: ScheduleSchedule | null
  timezone?: string
}>()

const open = defineModel<boolean>('open', { default: false })

const emit = defineEmits<{
  saved: [schedule: ScheduleSchedule]
}>()

const { t } = useI18n()

interface SchedulePlainForm {
  name: string
  description: string
  command: string
  maxCalls: number | null
  enabled: boolean
}

const form = reactive<SchedulePlainForm>({
  name: '',
  description: '',
  command: '',
  maxCalls: null,
  enabled: true,
})

const patternState = ref<ScheduleFormState>(defaultScheduleFormState())
const isSaving = ref(false)
const submitError = ref<string | null>(null)

const dialogTitle = computed(() => props.mode === 'create'
  ? t('bots.schedule.create')
  : t('bots.schedule.edit'))

const submitText = computed(() => props.mode === 'create'
  ? t('common.create')
  : t('common.save'))

const maxCallsUnlimited = computed(() => form.maxCalls === null)

function handleMaxCallsUnlimited(v: boolean) {
  form.maxCalls = v ? null : 1
}

const derivedPattern = computed(() => {
  try {
    return toCron(patternState.value).trim()
  } catch {
    return ''
  }
})

const canSubmit = computed(() => {
  if (isSaving.value) return false
  if (!form.name.trim()) return false
  if (!form.command.trim()) return false
  if (!derivedPattern.value) return false
  // Guard advanced mode: cron-parser must accept the raw text.
  if (patternState.value.mode === 'advanced' && !isValidCron(derivedPattern.value)) {
    return false
  }
  if (!maxCallsUnlimited.value && (form.maxCalls === null || form.maxCalls < 1)) return false
  return true
})

function resetForNew() {
  form.name = ''
  form.description = ''
  form.command = ''
  form.maxCalls = null
  form.enabled = true
  patternState.value = defaultScheduleFormState()
  submitError.value = null
}

function hydrateFromSchedule(s: ScheduleSchedule) {
  form.name = s.name ?? ''
  form.description = s.description ?? ''
  form.command = s.command ?? ''
  // See the note below handleSubmit: the SDK declares max_calls as
  // ScheduleNullableInt but the backend actually emits a plain number or
  // omits the field entirely. Read defensively.
  const maxCallsRaw = s.max_calls as unknown
  form.maxCalls = (typeof maxCallsRaw === 'number' && maxCallsRaw > 0) ? maxCallsRaw : null
  form.enabled = s.enabled ?? true
  patternState.value = fromCron(s.pattern ?? '')
  submitError.value = null
}

// Re-initialise whenever the dialog opens so that reopening on a different row
// picks up the new props. Edit mode always rehydrates from the current schedule
// prop (the round-trip invariant guarantees a clean state identical to what
// would be produced by building the same pattern in the UI).
watch(open, (next) => {
  if (!next) return
  if (props.mode === 'edit' && props.schedule) {
    hydrateFromSchedule(props.schedule)
  } else {
    resetForNew()
  }
})

async function handleSubmit() {
  if (!canSubmit.value) return
  submitError.value = null
  isSaving.value = true
  try {
    const pattern = derivedPattern.value
    // The SDK types max_calls as ScheduleNullableInt ({set, value}), but the
    // Go backend's custom (Un)MarshalJSON uses a plain nullable int on the
    // wire: either `null` (unlimited) or a raw integer. We cast through
    // unknown to bypass the mis-typed SDK declaration without lying about the
    // wire shape.
    const maxCallsWire = form.maxCalls ?? null
    if (props.mode === 'create') {
      const body = {
        name: form.name.trim(),
        description: form.description.trim(),
        command: form.command.trim(),
        pattern,
        enabled: form.enabled,
        max_calls: maxCallsWire,
      } as unknown as ScheduleCreateRequest
      const { data } = await postBotsByBotIdSchedule({
        path: { bot_id: props.botId },
        body,
        throwOnError: true,
      })
      if (data) emit('saved', data)
      toast.success(t('bots.schedule.saveSuccess'))
      open.value = false
    } else {
      const id = props.schedule?.id
      if (!id) throw new Error('schedule id missing')
      const body = {
        name: form.name.trim(),
        description: form.description.trim(),
        command: form.command.trim(),
        pattern,
        enabled: form.enabled,
        max_calls: maxCallsWire,
      } as unknown as ScheduleUpdateRequest
      const { data } = await putBotsByBotIdScheduleById({
        path: { bot_id: props.botId, id },
        body,
        throwOnError: true,
      })
      if (data) emit('saved', data)
      toast.success(t('bots.schedule.saveSuccess'))
      open.value = false
    }
  } catch (err) {
    submitError.value = resolveApiErrorMessage(err, t('bots.schedule.saveFailed'))
  } finally {
    isSaving.value = false
  }
}
</script>
