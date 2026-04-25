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

          <template
            v-for="field in orderedProviderFields"
            :key="field.key"
          >
            <FormField
              v-if="field.type === 'secret'"
              v-slot="{ componentField }"
              :name="field.key"
            >
              <FormItem>
                <Label
                  class="mb-2"
                  :for="`speech-provider-create-${field.key}`"
                >
                  {{ field.title || field.key }}
                </Label>
                <FormControl>
                  <Input
                    :id="`speech-provider-create-${field.key}`"
                    type="text"
                    :placeholder="field.description || ''"
                    v-bind="componentField"
                    :aria-label="field.title || field.key"
                  />
                </FormControl>
              </FormItem>
            </FormField>
            <FormField
              v-else-if="field.type === 'string' && !field.advanced"
              v-slot="{ componentField }"
              :name="field.key"
            >
              <FormItem>
                <Label
                  class="mb-2"
                  :for="`speech-provider-create-${field.key}`"
                >
                  {{ field.title || field.key }}
                </Label>
                <FormControl>
                  <Input
                    :id="`speech-provider-create-${field.key}`"
                    type="text"
                    :placeholder="field.description || ''"
                    v-bind="componentField"
                    :aria-label="field.title || field.key"
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
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { postProviders, getSpeechProvidersMeta } from '@memohai/sdk'
import type { ProvidersCreateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { Plus } from 'lucide-vue-next'
import FormDialogShell from '@/components/form-dialog-shell/index.vue'
import { useDialogMutation } from '@/composables/useDialogMutation'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import { computed, watch } from 'vue'

interface SpeechFieldSchema {
  key: string
  type: string
  title?: string
  description?: string
  required?: boolean
  advanced?: boolean
  enum?: string[]
  example?: unknown
  order?: number
}

interface SpeechConfigSchema {
  fields?: SpeechFieldSchema[]
}

interface SpeechProviderMeta {
  provider: string
  display_name: string
  description?: string
  config_schema?: SpeechConfigSchema
}

const open = defineModel<boolean>('open')
const { t } = useI18n()
const { run } = useDialogMutation()

const { data: metaList } = useQuery({
  key: () => ['speech-providers-meta'],
  query: async () => {
    const { data } = await getSpeechProvidersMeta({ throwOnError: true })
    return (data ?? []) as SpeechProviderMeta[]
  },
})

const providerTypeOptions = computed(() =>
  (metaList.value ?? []).map((m) => ({
    value: m.provider,
    label: m.display_name,
    description: m.description ?? '',
    keywords: [m.display_name, m.description ?? '', m.provider],
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
    if (typeof data.access_key === 'string' && data.access_key.trim() !== '') {
      config.access_key = data.access_key.trim()
    }
    if (typeof data.secret_key === 'string' && data.secret_key.trim() !== '') {
      config.secret_key = data.secret_key.trim()
    }
    if (typeof data.app_key === 'string' && data.app_key.trim() !== '') {
      config.app_key = data.app_key.trim()
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

const currentMeta = computed(() => {
  if (!metaList.value) return null
  return metaList.value.find((m) => m.provider === form.values.provider_type) ?? null
})

const orderedProviderFields = computed(() => {
  const fields = currentMeta.value?.config_schema?.fields ?? []
  return [...fields]
    .filter((f) => f.type === 'secret' || (f.type === 'string' && !f.advanced))
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1),
  provider_type: z.string().min(1),
  api_key: z.string().optional(),
  base_url: z.string().optional(),
  access_key: z.string().optional(),
  secret_key: z.string().optional(),
  app_key: z.string().optional(),
}))

const form = useForm({
  validationSchema: providerSchema,
  initialValues: {
    provider_type: 'edge-speech',
    api_key: '',
    base_url: '',
    access_key: '',
    secret_key: '',
    app_key: '',
  },
})

// Auto-fill base_url from config_schema example when provider type changes
watch(() => form.values.provider_type, (type) => {
  // Reset all credential fields
  form.setFieldValue('api_key', '')
  form.setFieldValue('base_url', '')
  form.setFieldValue('access_key', '')
  form.setFieldValue('secret_key', '')
  form.setFieldValue('app_key', '')

  if (!metaList.value) return
  const meta = metaList.value.find((m) => m.provider === type)
  if (!meta?.config_schema?.fields) return

  for (const field of meta.config_schema.fields) {
    if (field.key === 'base_url' && field.example && !isSecretField(field)) {
      form.setFieldValue('base_url', String(field.example))
    }
  }
})

function isSecretField(field: SpeechFieldSchema) {
  return field.type === 'secret'
}

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
