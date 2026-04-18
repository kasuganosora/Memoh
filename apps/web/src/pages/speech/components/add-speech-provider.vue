<template>
  <section>
    <FormDialogShell
      v-model:open="open"
      :title="$t('speech.add')"
      :cancel-text="$t('common.cancel')"
      :submit-text="$t('speech.add')"
      :submit-disabled="(form.meta.value.valid === false) || isLoading"
      :loading="isLoading"
      @submit="createProvider"
    >
      <template #trigger>
        <Button
          class="w-full shadow-none! text-muted-foreground mb-4"
          variant="outline"
        >
          <Plus
            class="mr-1"
          /> {{ $t('speech.add') }}
        </Button>
      </template>
      <template #body>
        <div
          class="flex-col gap-3 flex mt-4"
        >
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FormItem>
              <Label
                class="mb-2"
                for="speech-provider-create-name"
              >
                {{ $t('common.name') }}
              </Label>
              <FormControl>
                <Input
                  id="speech-provider-create-name"
                  type="text"
                  :placeholder="$t('common.namePlaceholder')"
                  v-bind="componentField"
                  :aria-label="$t('common.name')"
                />
              </FormControl>
            </FormItem>
          </FormField>

          <FormField
            v-slot="{ value, handleChange }"
            name="provider_type"
          >
            <FormItem>
              <Label class="mb-2">
                {{ $t('speech.providerType') }}
              </Label>
              <FormControl>
                <SearchableSelectPopover
                  :model-value="value"
                  :options="providerTypeOptions"
                  :placeholder="$t('common.typePlaceholder')"
                  @update:model-value="handleChange"
                />
              </FormControl>
            </FormItem>
          </FormField>

          <template v-if="needsCredentials">
            <FormField
              v-slot="{ componentField }"
              name="api_key"
            >
              <FormItem>
                <Label
                  class="mb-2"
                  for="speech-provider-create-api-key"
                >
                  {{ $t('provider.apiKey') }}
                </Label>
                <FormControl>
                  <Input
                    id="speech-provider-create-api-key"
                    type="text"
                    :placeholder="$t('provider.apiKeyPlaceholder')"
                    v-bind="componentField"
                    :aria-label="$t('provider.apiKey')"
                  />
                </FormControl>
              </FormItem>
            </FormField>

            <FormField
              v-slot="{ componentField }"
              name="base_url"
            >
              <FormItem>
                <Label
                  class="mb-2"
                  for="speech-provider-create-base-url"
                >
                  {{ $t('provider.url') }}
                </Label>
                <FormControl>
                  <Input
                    id="speech-provider-create-base-url"
                    type="text"
                    :placeholder="$t('provider.urlPlaceholder')"
                    v-bind="componentField"
                    :aria-label="$t('provider.url')"
                  />
                </FormControl>
              </FormItem>
            </FormField>
          </template>
        </div>
      </template>
    </FormDialogShell>
  </section>
</template>
<script setup lang="ts">
import {
  Button,
  Input,
  FormField,
  FormControl,
  FormItem,
  Label,
} from '@memohai/ui'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useForm } from 'vee-validate'
import { useMutation, useQueryCache } from '@pinia/colada'
import { postProviders } from '@memohai/sdk'
import type { ProvidersCreateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { Plus } from 'lucide-vue-next'
import FormDialogShell from '@/components/form-dialog-shell/index.vue'
import { useDialogMutation } from '@/composables/useDialogMutation'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import { computed, watch } from 'vue'

const open = defineModel<boolean>('open')
const { t } = useI18n()
const { run } = useDialogMutation()

const SPEECH_CLIENT_TYPES = [
  { value: 'edge-speech', label: 'Edge Speech', hint: 'Microsoft Edge Read Aloud TTS' },
  { value: 'grok-speech', label: 'xAI Grok (Speech)', hint: 'xAI Grok text-to-speech API' },
  { value: 'gemini-speech', label: 'Google Gemini (Speech)', hint: 'Gemini 3.1 Flash TTS' },
] as const

const CREDENTIAL_REQUIRED_TYPES = new Set(['grok-speech', 'gemini-speech'])

const needsCredentials = computed(() => CREDENTIAL_REQUIRED_TYPES.has(form.values.provider_type))

const providerTypeOptions = computed(() =>
  SPEECH_CLIENT_TYPES.map((ct) => ({
    value: ct.value,
    label: ct.label,
    description: ct.hint,
    keywords: [ct.label, ct.hint],
  })),
)

const queryCache = useQueryCache()
const { mutateAsync: createProviderMutation, isLoading } = useMutation({
  mutation: async (data: Record<string, unknown>) => {
    const config: Record<string, unknown> = {}
    if (data.base_url && (data.base_url as string).trim() !== '') {
      config.base_url = (data.base_url as string).trim()
    }
    if (typeof data.api_key === 'string' && data.api_key.trim() !== '') {
      config.api_key = data.api_key.trim()
    }
    const payload = {
      name: data.name,
      client_type: data.provider_type,
      config,
    }
    const { data: result } = await postProviders({ body: payload as ProvidersCreateRequest, throwOnError: true })
    return result
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['speech-providers'] })
    queryCache.invalidateQueries({ key: ['providers'] })
  },
})

const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1),
  provider_type: z.string().min(1),
  api_key: z.string().optional(),
  base_url: z.string().optional(),
}).superRefine((value, ctx) => {
  if (CREDENTIAL_REQUIRED_TYPES.has(value.provider_type) && !value.api_key?.trim()) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['api_key'],
      message: 'API key is required',
    })
  }
}))

const form = useForm({
  validationSchema: providerSchema,
  initialValues: {
    provider_type: 'edge-speech',
    api_key: '',
    base_url: '',
  },
})

watch(() => form.values.provider_type, (type) => {
  if (type === 'grok-speech' && !form.values.base_url) {
    form.setFieldValue('base_url', 'https://api.x.ai/v1')
  }
  if (type === 'gemini-speech' && !form.values.base_url) {
    form.setFieldValue('base_url', 'https://generativelanguage.googleapis.com/v1beta')
  }
  if (type === 'edge-speech') {
    form.setFieldValue('api_key', '')
    form.setFieldValue('base_url', '')
  }
})

const createProvider = form.handleSubmit(async (value) => {
  await run(
    () => createProviderMutation(value),
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        open.value = false
        form.resetForm()
      },
    },
  )
})
</script>
