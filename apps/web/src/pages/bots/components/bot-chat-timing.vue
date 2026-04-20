<template>
  <div class="space-y-6">
    <!-- Settings -->
    <div class="space-y-4 mx-auto">
      <div class="flex items-center justify-between">
        <div>
          <Label>{{ $t('bots.settings.chatTimingEnabled') }}</Label>
          <p class="text-xs text-muted-foreground mt-0.5">
            {{ $t('bots.settings.chatTimingDescription') }}
          </p>
        </div>
        <Switch
          :model-value="settingsForm.enabled"
          @update:model-value="(val) => settingsForm.enabled = !!val"
        />
      </div>

      <div
        v-if="settingsForm.enabled"
        class="space-y-4"
      >
        <!-- Debounce -->
        <div class="space-y-3 rounded-md border p-4">
          <Label class="text-sm font-medium">{{ $t('bots.settings.chatTimingDebounce') }}</Label>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingQuietPeriod') }}</Label>
              <Input
                v-model.number="settingsForm.debounce_quiet_period_sec"
                type="number"
                :min="0"
                :step="0.5"
                :placeholder="'2'"
                :aria-label="$t('bots.settings.chatTimingQuietPeriod')"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingMaxWait') }}</Label>
              <Input
                v-model.number="settingsForm.debounce_max_wait_sec"
                type="number"
                :min="0"
                :step="1"
                :placeholder="'15'"
                :aria-label="$t('bots.settings.chatTimingMaxWait')"
              />
            </div>
          </div>
        </div>

        <!-- Timing Gate -->
        <div class="flex items-center justify-between rounded-md border p-4">
          <div>
            <Label>{{ $t('bots.settings.chatTimingTimingGate') }}</Label>
            <p class="text-xs text-muted-foreground mt-0.5">
              {{ $t('bots.settings.chatTimingTimingGateDescription') }}
            </p>
          </div>
          <Switch
            :model-value="settingsForm.timing_gate"
            @update:model-value="(val) => settingsForm.timing_gate = !!val"
          />
        </div>

        <!-- Talk Value -->
        <div class="space-y-3 rounded-md border p-4">
          <Label class="text-sm font-medium">{{ $t('bots.settings.chatTimingTalkValue') }}</Label>
          <p class="text-xs text-muted-foreground mt-0.5">
            {{ $t('bots.settings.chatTimingTalkValueDescription') }}
          </p>
          <div class="flex items-center gap-3">
            <Slider
              :model-value="[settingsForm.talk_value]"
              :min="0.01"
              :max="1"
              :step="0.01"
              class="flex-1"
              @update:model-value="(val) => settingsForm.talk_value = val[0]"
            />
            <span class="text-sm text-muted-foreground w-12 text-right tabular-nums">{{ settingsForm.talk_value.toFixed(2) }}</span>
          </div>
        </div>

        <!-- Interrupt -->
        <div class="space-y-3 rounded-md border p-4">
          <div class="flex items-center justify-between">
            <div>
              <Label>{{ $t('bots.settings.chatTimingInterrupt') }}</Label>
              <p class="text-xs text-muted-foreground mt-0.5">
                {{ $t('bots.settings.chatTimingInterruptDescription') }}
              </p>
            </div>
            <Switch
              :model-value="settingsForm.interrupt_enabled"
              @update:model-value="(val) => settingsForm.interrupt_enabled = !!val"
            />
          </div>
          <div
            v-if="settingsForm.interrupt_enabled"
            class="grid grid-cols-2 gap-4"
          >
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingMaxConsecutive') }}</Label>
              <Input
                v-model.number="settingsForm.interrupt_max_consecutive"
                type="number"
                :min="1"
                :max="10"
                :placeholder="'3'"
                :aria-label="$t('bots.settings.chatTimingMaxConsecutive')"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingMaxRounds') }}</Label>
              <Input
                v-model.number="settingsForm.interrupt_max_rounds"
                type="number"
                :min="1"
                :max="20"
                :placeholder="'6'"
                :aria-label="$t('bots.settings.chatTimingMaxRounds')"
              />
            </div>
          </div>
        </div>

        <!-- Idle Compensation -->
        <div class="space-y-3 rounded-md border p-4">
          <div class="flex items-center justify-between">
            <div>
              <Label>{{ $t('bots.settings.chatTimingIdleComp') }}</Label>
              <p class="text-xs text-muted-foreground mt-0.5">
                {{ $t('bots.settings.chatTimingIdleCompDescription') }}
              </p>
            </div>
            <Switch
              :model-value="settingsForm.idle_comp_enabled"
              @update:model-value="(val) => settingsForm.idle_comp_enabled = !!val"
            />
          </div>
          <div
            v-if="settingsForm.idle_comp_enabled"
            class="grid grid-cols-2 gap-4"
          >
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingIdleWindow') }}</Label>
              <Input
                v-model.number="settingsForm.idle_comp_window_min"
                type="number"
                :min="1"
                :step="5"
                :placeholder="'60'"
                :aria-label="$t('bots.settings.chatTimingIdleWindow')"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">{{ $t('bots.settings.chatTimingMinIdle') }}</Label>
              <Input
                v-model.number="settingsForm.idle_comp_min_idle_min"
                type="number"
                :min="1"
                :step="1"
                :placeholder="'5'"
                :aria-label="$t('bots.settings.chatTimingMinIdle')"
              />
            </div>
          </div>
        </div>
      </div>

      <div class="flex justify-end">
        <Button
          size="sm"
          :disabled="!settingsChanged || isSaving"
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
import { reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import {
  Button, Spinner, Label, Switch, Input, Slider,
} from '@memohai/ui'
import {
  getBotsByBotIdSettings, putBotsByBotIdSettings,
} from '@memohai/sdk'
import type { SettingsSettings, SettingsUpsertRequest } from '@memohai/sdk'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import type { Ref } from 'vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const botIdRef = computed(() => props.botId) as Ref<string>

// ---- Settings ----
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', botIdRef.value],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({ path: { bot_id: botIdRef.value }, throwOnError: true })
    return data
  },
  enabled: () => !!botIdRef.value,
})

// Form uses user-friendly units (seconds, minutes) and converts to nanoseconds on save.
const NS_PER_SEC = 1_000_000_000
const NS_PER_MIN = 60 * NS_PER_SEC

const settingsForm = reactive({
  enabled: false,
  debounce_quiet_period_sec: 2,
  debounce_max_wait_sec: 15,
  timing_gate: true,
  talk_value: 0.5,
  interrupt_enabled: true,
  interrupt_max_consecutive: 3,
  interrupt_max_rounds: 6,
  idle_comp_enabled: true,
  idle_comp_window_min: 60,
  idle_comp_min_idle_min: 5,
})

function fromNs(ns: number | undefined, divisor: number, fallback: number): number {
  if (ns === undefined || ns === 0) return fallback
  return Number((ns / divisor).toFixed(2))
}

watch(settings, (val: SettingsSettings | undefined) => {
  if (!val) return
  const ct = val.chat_timing
  settingsForm.enabled = ct?.enabled ?? false
  settingsForm.debounce_quiet_period_sec = fromNs(ct?.debounce_quiet_period, NS_PER_SEC, 2)
  settingsForm.debounce_max_wait_sec = fromNs(ct?.debounce_max_wait, NS_PER_SEC, 15)
  settingsForm.timing_gate = ct?.timing_gate ?? true
  settingsForm.talk_value = ct?.talk_value ?? 0.5
  settingsForm.interrupt_enabled = ct?.interrupt_enabled ?? true
  settingsForm.interrupt_max_consecutive = ct?.interrupt_max_consecutive ?? 3
  settingsForm.interrupt_max_rounds = ct?.interrupt_max_rounds ?? 6
  settingsForm.idle_comp_enabled = ct?.idle_comp_enabled ?? true
  settingsForm.idle_comp_window_min = fromNs(ct?.idle_comp_window_size, NS_PER_MIN, 60)
  settingsForm.idle_comp_min_idle_min = fromNs(ct?.idle_comp_min_idle_before_credit, NS_PER_MIN, 5)
}, { immediate: true })

const settingsChanged = computed(() => {
  if (!settings.value) return false
  const ct = settings.value.chat_timing
  return settingsForm.enabled !== (ct?.enabled ?? false)
    || settingsForm.debounce_quiet_period_sec !== fromNs(ct?.debounce_quiet_period, NS_PER_SEC, 2)
    || settingsForm.debounce_max_wait_sec !== fromNs(ct?.debounce_max_wait, NS_PER_SEC, 15)
    || settingsForm.timing_gate !== (ct?.timing_gate ?? true)
    || settingsForm.talk_value !== (ct?.talk_value ?? 0.5)
    || settingsForm.interrupt_enabled !== (ct?.interrupt_enabled ?? true)
    || settingsForm.interrupt_max_consecutive !== (ct?.interrupt_max_consecutive ?? 3)
    || settingsForm.interrupt_max_rounds !== (ct?.interrupt_max_rounds ?? 6)
    || settingsForm.idle_comp_enabled !== (ct?.idle_comp_enabled ?? true)
    || settingsForm.idle_comp_window_min !== fromNs(ct?.idle_comp_window_size, NS_PER_MIN, 60)
    || settingsForm.idle_comp_min_idle_min !== fromNs(ct?.idle_comp_min_idle_before_credit, NS_PER_MIN, 5)
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
    await updateSettings({
      chat_timing: {
        enabled: settingsForm.enabled,
        debounce_quiet_period: Math.round(settingsForm.debounce_quiet_period_sec * NS_PER_SEC),
        debounce_max_wait: Math.round(settingsForm.debounce_max_wait_sec * NS_PER_SEC),
        timing_gate: settingsForm.timing_gate,
        talk_value: settingsForm.talk_value,
        interrupt_enabled: settingsForm.interrupt_enabled,
        interrupt_max_consecutive: settingsForm.interrupt_max_consecutive,
        interrupt_max_rounds: settingsForm.interrupt_max_rounds,
        idle_comp_enabled: settingsForm.idle_comp_enabled,
        idle_comp_window_size: Math.round(settingsForm.idle_comp_window_min * NS_PER_MIN),
        idle_comp_min_idle_before_credit: Math.round(settingsForm.idle_comp_min_idle_min * NS_PER_MIN),
      },
    })
    toast.success(t('bots.settings.saveSuccess'))
  } catch {
    return
  }
}
</script>
