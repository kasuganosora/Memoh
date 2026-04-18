<template>
  <div class="p-4">
    <section class="flex items-center gap-3">
      <Volume2
        class="size-5"
      />
      <div class="min-w-0">
        <h2 class="text-sm font-semibold truncate">
          {{ curProvider?.name }}
        </h2>
        <p class="text-xs text-muted-foreground">
          {{ currentMeta?.display_name ?? curProvider?.client_type }}
        </p>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <ConfirmPopover
          :message="$t('speech.deleteConfirm')"
          :loading="deleteLoading"
          @confirm="handleDeleteProvider"
        >
          <template #trigger>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              class="size-8"
              :aria-label="$t('common.delete')"
            >
              <Trash2 class="size-4" />
            </Button>
          </template>
        </ConfirmPopover>
        <span class="text-xs text-muted-foreground">
          {{ $t('common.enable') }}
        </span>
        <Switch
          :model-value="curProvider?.enable ?? false"
          :disabled="!curProvider?.id || enableLoading"
          @update:model-value="handleToggleEnable"
        />
      </div>
    </section>
    <Separator class="mt-4 mb-6" />

    <!-- Provider Config (API Key / Base URL) -->
    <section
      v-if="needsCredentials"
      class="mb-6 space-y-3"
    >
      <h3 class="text-xs font-medium">
        {{ $t('provider.apiKey') }}
      </h3>
      <div class="space-y-2">
        <div class="space-y-1">
          <Label
            class="text-xs"
            for="speech-provider-api-key"
          >
            {{ $t('provider.apiKey') }}
          </Label>
          <Input
            id="speech-provider-api-key"
            v-model="configForm.api_key"
            type="password"
            :placeholder="maskedApiKey || $t('provider.apiKeyPlaceholder')"
            :aria-label="$t('provider.apiKey')"
          />
        </div>
        <div class="space-y-1">
          <Label
            class="text-xs"
            for="speech-provider-base-url"
          >
            {{ $t('provider.url') }}
          </Label>
          <Input
            id="speech-provider-base-url"
            v-model="configForm.base_url"
            type="text"
            :placeholder="$t('provider.urlPlaceholder')"
            :aria-label="$t('provider.url')"
          />
        </div>
        <div class="flex justify-end">
          <LoadingButton
            type="button"
            size="sm"
            :disabled="!configHasChanges"
            :loading="configSaving"
            @click="handleSaveConfig"
          >
            {{ $t('provider.saveChanges') }}
          </LoadingButton>
        </div>
      </div>
    </section>

    <!-- Models -->
    <section>
      <div class="flex justify-between items-center mb-4">
        <h3 class="text-xs font-medium">
          {{ $t('speech.models') }}
        </h3>
      </div>

      <div
        v-if="providerModels.length === 0"
        class="text-xs text-muted-foreground py-4 text-center space-y-2"
      >
        <p>{{ $t('speech.noModels') }}</p>
        <Button
          v-if="currentMeta?.models?.length"
          variant="outline"
          size="sm"
          :disabled="importLoading"
          @click="handleImportModels"
        >
          {{ $t('speech.importModels') }}
        </Button>
      </div>

      <div
        v-for="model in providerModels"
        :key="model.id"
        class="border border-border rounded-lg mb-4"
      >
        <button
          type="button"
          class="w-full flex items-center justify-between p-3 text-left hover:bg-accent/50 rounded-t-lg transition-colors"
          @click="toggleModel(model.id ?? '')"
        >
          <div>
            <span class="text-xs font-medium">{{ model.name || model.model_id }}</span>
            <span
              v-if="model.name"
              class="text-xs text-muted-foreground ml-2"
            >{{ model.model_id }}</span>
          </div>
          <component
            :is="expandedModelId === model.id ? ChevronUp : ChevronDown"
            class="size-3 text-muted-foreground"
          />
        </button>

        <div
          v-if="expandedModelId === model.id"
          class="px-3 pb-3 space-y-4 border-t border-border pt-3"
        >
          <ModelConfigEditor
            :model-id="model.id ?? ''"
            :model-name="model.model_id ?? ''"
            :config="model.config || {}"
            :capabilities="getModelCapabilities(model.model_id ?? '')"
            @save="(cfg) => handleSaveModelConfig(model.id ?? '', cfg)"
            @test="(text, cfg) => handleTestModel(model.id ?? '', text, cfg)"
          />
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import {
  Separator,
  Switch,
  Button,
  Input,
  Label,
} from '@memohai/ui'
import ModelConfigEditor from './model-config-editor.vue'
import ConfirmPopover from '@/components/confirm-popover/index.vue'
import LoadingButton from '@/components/loading-button/index.vue'
import { Volume2, ChevronUp, ChevronDown, Trash2 } from 'lucide-vue-next'
import { computed, inject, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getSpeechProvidersMeta, getSpeechModels, getProvidersById, putProvidersById, putModelsById, postModels, deleteProvidersById, deleteModelsById } from '@memohai/sdk'
import type { TtsSpeechProviderResponse, TtsProviderMetaResponse, TtsModelInfo } from '@memohai/sdk'

const CREDENTIAL_REQUIRED_TYPES = new Set(['grok-speech', 'gemini-speech'])

const { t } = useI18n()
const curProvider = inject('curTtsProvider', ref<TtsSpeechProviderResponse>())
const curProviderId = computed(() => curProvider.value?.id)
const enableLoading = ref(false)

const needsCredentials = computed(() => CREDENTIAL_REQUIRED_TYPES.has(curProvider.value?.client_type ?? ''))

// Provider config state
const configForm = ref({ api_key: '', base_url: '' })
const storedBaseUrl = ref('')
const maskedApiKey = ref('')
const configSaving = ref(false)

const configHasChanges = computed(() => {
  const apiKeyChanged = configForm.value.api_key.trim() !== ''
  const baseUrlChanged = configForm.value.base_url.trim() !== '' && configForm.value.base_url.trim() !== storedBaseUrl.value
  return apiKeyChanged || baseUrlChanged
})

async function loadProviderConfig() {
  if (!curProviderId.value || !needsCredentials.value) return
  try {
    const { data } = await getProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
    const cfg = (data as Record<string, unknown>)?.config as Record<string, unknown> | undefined
    if (cfg) {
      maskedApiKey.value = (cfg.api_key as string) || ''
      storedBaseUrl.value = (cfg.base_url as string) || ''
      configForm.value.base_url = storedBaseUrl.value
    }
  } catch {
    // ignore
  }
}

watch(curProviderId, (id) => {
  configForm.value = { api_key: '', base_url: '' }
  maskedApiKey.value = ''
  storedBaseUrl.value = ''
  if (id) loadProviderConfig()
}, { immediate: true })

async function handleSaveConfig() {
  if (!curProviderId.value) return
  configSaving.value = true
  try {
    const config: Record<string, unknown> = {}
    if (configForm.value.base_url.trim()) {
      config.base_url = configForm.value.base_url.trim()
    }
    if (configForm.value.api_key.trim()) {
      config.api_key = configForm.value.api_key.trim()
    }
    await putProvidersById({
      path: { id: curProviderId.value },
      body: { config },
      throwOnError: true,
    })
    // Reset api_key field after save (it's been sent)
    configForm.value.api_key = ''
    await loadProviderConfig()
    queryCache.invalidateQueries({ key: ['speech-providers'] })
  } catch {
    toast.error(t('common.saveFailed'))
  } finally {
    configSaving.value = false
  }
}

const { data: metaList } = useQuery({
  key: () => ['speech-providers-meta'],
  query: async () => {
    const { data } = await getSpeechProvidersMeta({ throwOnError: true })
    return data
  },
})

// client_type -> adapter type mapping (e.g. "grok-speech" -> "grok")
function clientTypeToAdapterType(clientType: string): string {
  return clientType.replace(/-speech$/, '')
}

const currentMeta = computed<TtsProviderMetaResponse | null>(() => {
  if (!metaList.value || !curProvider.value?.client_type) return null
  const adapterType = clientTypeToAdapterType(curProvider.value.client_type)
  return (metaList.value as TtsProviderMetaResponse[]).find((m) => m.provider === adapterType) ?? null
})

function getModelCapabilities(modelId: string) {
  const meta = currentMeta.value
  if (!meta?.models) return null
  return meta.models.find((m: TtsModelInfo) => m.id === modelId)?.capabilities ?? null
}

const { data: allSpeechModels } = useQuery({
  key: () => ['speech-models'],
  query: async () => {
    const { data } = await getSpeechModels({ throwOnError: true })
    return data
  },
})

const providerModels = computed(() => {
  if (!allSpeechModels.value || !curProviderId.value) return []
  return allSpeechModels.value.filter((m) => m.provider_id === curProviderId.value)
})

const expandedModelId = ref('')
function toggleModel(id: string) {
  expandedModelId.value = expandedModelId.value === id ? '' : id
}

const importLoading = ref(false)
async function handleImportModels() {
  if (!curProviderId.value || !currentMeta.value?.models) return
  importLoading.value = true
  try {
    let created = 0
    for (const m of currentMeta.value.models) {
      try {
        await postModels({
          body: {
            model_id: m.id,
            name: m.name,
            provider_id: curProviderId.value,
            type: 'speech',
          },
          throwOnError: true,
        })
        created++
      } catch {
        // Model may already exist, skip
      }
    }
    if (created > 0) {
      toast.success(t('speech.importSuccess'))
    } else {
      toast.info(t('speech.noModels'))
    }
    queryCache.invalidateQueries({ key: ['speech-models'] })
  } catch {
    toast.error(t('common.saveFailed'))
  } finally {
    importLoading.value = false
  }
}

const queryCache = useQueryCache()

async function handleToggleEnable(value: boolean) {
  if (!curProviderId.value || !curProvider.value) return

  const prev = curProvider.value.enable ?? false
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    await putProvidersById({
      path: { id: curProviderId.value },
      body: { enable: value },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['speech-providers'] })
  } catch {
    curProvider.value = { ...curProvider.value, enable: prev }
    toast.error(t('common.saveFailed'))
  } finally {
    enableLoading.value = false
  }
}

const deleteLoading = ref(false)
async function handleDeleteProvider() {
  if (!curProviderId.value) return
  deleteLoading.value = true
  try {
    // Delete associated speech models first
    for (const model of providerModels.value) {
      if (model.id) {
        try {
          await deleteModelsById({ path: { id: model.id }, throwOnError: true })
        } catch {
          // Model may already be gone, continue
        }
      }
    }
    await deleteProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
    queryCache.invalidateQueries({ key: ['speech-providers'] })
    queryCache.invalidateQueries({ key: ['speech-models'] })
    curProvider.value = undefined
  } catch {
    toast.error(t('common.saveFailed'))
  } finally {
    deleteLoading.value = false
  }
}

async function handleSaveModelConfig(modelId: string, config: Record<string, unknown>) {
  if (!modelId) return
  const model = providerModels.value.find((m) => m.id === modelId)
  if (!model) return
  try {
    await putModelsById({
      path: { id: modelId },
      body: {
        model_id: model.model_id ?? '',
        name: model.name ?? '',
        provider_id: model.provider_id ?? '',
        type: model.type ?? 'speech',
        config,
      },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['speech-models'] })
    toast.success(t('speech.importSuccess'))
  } catch {
    toast.error(t('common.saveFailed'))
  }
}

async function handleTestModel(modelId: string, text: string, config: Record<string, unknown>) {
  const apiBase = import.meta.env.VITE_API_URL?.trim() || '/api'
  const token = localStorage.getItem('token')
  const resp = await fetch(`${apiBase}/speech-models/${modelId}/test`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({ text, config }),
  })
  if (!resp.ok) {
    const errBody = await resp.text()
    let msg: string
    try {
      msg = JSON.parse(errBody)?.message ?? errBody
    } catch {
      msg = errBody
    }
    throw new Error(msg)
  }
  return resp.blob()
}
</script>
