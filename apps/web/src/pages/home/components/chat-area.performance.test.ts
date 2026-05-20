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

describe('ChatArea - Performance Tests', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let store: Record<string, any>

  beforeEach(() => {
    setActivePinia(createPinia())
    
    store = {
      messages: [],
      loadingOlder: false,
      hasMoreOlder: false,
      PAGE_SIZE: 50,
      loadOlderMessages: vi.fn(),
      loadNewerMessages: vi.fn()
    }
    
    vi.mocked(useChatListStore).mockReturnValue(store)
  })

  it('should handle 1000+ messages efficiently', async () => {
    // Create a large dataset
    const largeDataset = Array.from({ length: 1500 }, (_, i) => ({
      id: `msg-${i}`,
      content: `This is a test message ${i} with some content to simulate real message data`,
      timestamp: new Date(Date.now() - i * 60000),
      sender: i % 2 === 0 ? 'user' : 'assistant',
      attachments: i % 10 === 0 ? [{ type: 'image', url: 'test.jpg' }] : []
    }))

    store.messages = largeDataset

    const startTime = performance.now()
    
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    const mountTime = performance.now() - startTime
    
    // Mounting should be reasonably fast even with large datasets
    expect(mountTime).toBeLessThan(1000) // Should mount in under 1 second

    // Verify virtual scrolling is working
    expect(wrapper.vm.visibleMessages.length).toBeLessThan(100) // Only visible messages should be rendered
  })

  it('should maintain smooth scrolling with debounce', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    const scrollContainer = wrapper.find('.overflow-y-auto')
    
    // Test rapid scrolling performance
    const scrollStartTime = performance.now()
    
    for (let i = 0; i < 20; i++) {
      Object.defineProperty(scrollContainer.element, 'scrollTop', { value: i * 50 })
      Object.defineProperty(scrollContainer.element, 'scrollHeight', { value: 5000 })
      Object.defineProperty(scrollContainer.element, 'clientHeight', { value: 500 })
      
      await scrollContainer.trigger('scroll')
      await nextTick()
    }

    const scrollTime = performance.now() - scrollStartTime
    
    // Scrolling should be smooth and responsive
    expect(scrollTime).toBeLessThan(500) // Should handle 20 scroll events in under 500ms
    
    // Debounce should prevent excessive API calls
    expect(store.loadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('should optimize memory usage with content recycling', async () => {
    // Create a very large dataset to test memory limits
    const veryLargeDataset = Array.from({ length: 5000 }, (_, i) => ({
      id: `msg-${i}`,
      content: `Message ${i}`.repeat(10), // Make messages larger
      timestamp: new Date(Date.now() - i * 60000),
      sender: 'user'
    }))

    store.messages = veryLargeDataset

    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Verify that content recycling is active
    expect(wrapper.vm.MAX_MESSAGES_IN_MEMORY).toBeLessThan(5000)
    
    // Check that only a subset of messages is kept in memory
    expect(wrapper.vm.optimizedMessages.length).toBeLessThan(1000)
  })

  it('should handle concurrent operations without blocking', async () => {
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    // Simulate concurrent operations: scrolling + new messages + error handling
    const operations = [
      () => wrapper.vm.handleScroll({ target: { scrollTop: 100, scrollHeight: 1000, clientHeight: 500 } }),
      () => wrapper.vm.handleNewMessage({ id: 'new-msg', content: 'Test', timestamp: new Date() }),
      () => wrapper.vm.handleLoadError('Network error')
    ]

    const startTime = performance.now()
    
    // Execute operations concurrently
    await Promise.all(operations.map(op => op()))
    
    const concurrentTime = performance.now() - startTime
    
    // Concurrent operations should complete quickly
    expect(concurrentTime).toBeLessThan(200)
  })

  it('should measure rendering performance with different message types', async () => {
    const mixedMessages = Array.from({ length: 200 }, (_, i) => {
      const types = ['text', 'image', 'file', 'tool_call']
      const type = types[i % types.length]
      
      return {
        id: `msg-${i}`,
        content: type === 'text' ? `Text message ${i}` : '',
        timestamp: new Date(Date.now() - i * 60000),
        attachments: type === 'image' ? [{ type: 'image', url: 'test.jpg' }] : [],
        toolCalls: type === 'tool_call' ? [{ name: 'search', input: { q: 'test' } }] : []
      }
    })

    store.messages = mixedMessages

    const renderStartTime = performance.now()
    
    const wrapper = mount(ChatArea, {
      global: {
        plugins: [createPinia()]
      }
    })

    const renderTime = performance.now() - renderStartTime
    
    // Should render mixed content types efficiently
    expect(renderTime).toBeLessThan(300)
    
    // Verify all message types are handled correctly
    expect(wrapper.findAll('.message-item').length).toBeGreaterThan(0)
  })
})