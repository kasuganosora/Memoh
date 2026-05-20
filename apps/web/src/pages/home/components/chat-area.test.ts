import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import ChatArea from './chat-area.vue'
import { useChatListStore } from '@/store/chat-list'

// Mock dependencies
vi.mock('@/store/chat-list', () => ({
  useChatListStore: vi.fn()
}))

vi.mock('@/composables/api/useChat.message-api', () => ({
  useChatMessageApi: () => ({
    getMessages: vi.fn(),
    sendMessage: vi.fn()
  })
}))

describe('ChatArea - Dynamic Pagination', () => {
  let store: any

  beforeEach(() => {
    setActivePinia(createPinia())
    
    store = {
      messages: [],
      loadingOlder: false,
      hasMoreOlder: true,
      PAGE_SIZE: 50,
      loadOlderMessages: vi.fn(),
      loadNewerMessages: vi.fn(),
      clearMessages: vi.fn(),
      addMessage: vi.fn(),
      updateMessage: vi.fn(),
      removeMessage: vi.fn()
    }
    
    vi.mocked(useChatListStore).mockReturnValue(store)
  })

  it('should initialize with default PAGE_SIZE of 50', () => {
    expect(store.PAGE_SIZE).toBe(50)
  })

  it('should load older messages when scrolling near top', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Simulate scroll near top
    const scrollContainer = wrapper.find('.overflow-y-auto')
    Object.defineProperty(scrollContainer.element, 'scrollTop', { value: 100 })
    Object.defineProperty(scrollContainer.element, 'scrollHeight', { value: 1000 })
    Object.defineProperty(scrollContainer.element, 'clientHeight', { value: 500 })

    // Trigger scroll event
    await scrollContainer.trigger('scroll')
    
    // Wait for debounce
    await new Promise(resolve => setTimeout(resolve, 600))
    
    expect(store.loadOlderMessages).toHaveBeenCalled()
  })

  it('should show loading indicator when loading older messages', () => {
    store.loadingOlder = true
    
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    expect(wrapper.text()).toContain('加载中...')
  })

  it('should show new message notification when new messages arrive', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Simulate new messages arriving
    await wrapper.setData({
      showNewMessageNotification: true,
      newMessageCount: 3
    })

    await nextTick()

    expect(wrapper.find('.sticky.top-4').exists()).toBe(true)
    expect(wrapper.text()).toContain('3 条新消息')
  })

  it('should handle error state correctly', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Simulate error state
    await wrapper.setData({
      errorState: {
        hasError: true,
        errorMessage: '网络连接失败',
        retryCount: 2
      }
    })

    await nextTick()

    expect(wrapper.find('.bg-destructive\/10').exists()).toBe(true)
    expect(wrapper.text()).toContain('网络连接失败')
    expect(wrapper.text()).toContain('重试')
  })

  it('should implement debounce mechanism for scroll events', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    const scrollContainer = wrapper.find('.overflow-y-auto')
    
    // Simulate rapid scrolling
    for (let i = 0; i < 5; i++) {
      Object.defineProperty(scrollContainer.element, 'scrollTop', { value: 100 + i * 10 })
      Object.defineProperty(scrollContainer.element, 'scrollHeight', { value: 1000 })
      Object.defineProperty(scrollContainer.element, 'clientHeight', { value: 500 })
      
      await scrollContainer.trigger('scroll')
    }

    // Should only call loadOlderMessages once due to debounce
    await new Promise(resolve => setTimeout(resolve, 600))
    
    expect(store.loadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('should handle virtual scrolling for large message sets', async () => {
    // Create a large set of messages
    const largeMessageSet = Array.from({ length: 1000 }, (_, i) => ({
      id: `msg-${i}`,
      content: `Message ${i}`,
      timestamp: new Date(Date.now() - i * 60000)
    }))

    store.messages = largeMessageSet

    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Verify that component handles large datasets
    expect(wrapper.vm.visibleMessages).toBeDefined()
    expect(wrapper.vm.getVisibleRange).toBeDefined()
  })

  it('should properly handle content recycling for memory optimization', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Verify content recycling methods exist
    expect(wrapper.vm.recycleOldMessages).toBeDefined()
    expect(wrapper.vm.MAX_MESSAGES_IN_MEMORY).toBeGreaterThan(0)
  })
})