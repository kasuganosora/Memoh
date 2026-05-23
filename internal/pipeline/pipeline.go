package pipeline

import (
	"log/slog"
	"sync"
)

// DefaultMaxICNodes is the default maximum number of IC nodes per session.
// When exceeded, the oldest nodes are trimmed to keep memory bounded.
const DefaultMaxICNodes = 1000

// Pipeline manages per-session IC/RC state. It is goroutine-safe.
type Pipeline struct {
	mu           sync.RWMutex
	renderParams RenderParams
	sessions     map[string]IntermediateContext
	rendered     map[string]RenderedContext
	maxNodes     int
	logger       *slog.Logger
}

// PipelineOption configures a Pipeline.
type PipelineOption func(*Pipeline)

// WithMaxNodes sets the maximum number of IC nodes per session.
func WithMaxNodes(n int) PipelineOption {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxNodes = n
		}
	}
}

// WithLogger sets the logger for the Pipeline.
func WithPipelineLogger(log *slog.Logger) PipelineOption {
	return func(p *Pipeline) {
		if log != nil {
			p.logger = log
		}
	}
}

// NewPipeline creates a Pipeline with the given default render params.
func NewPipeline(params RenderParams, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		renderParams: params,
		sessions:     make(map[string]IntermediateContext),
		rendered:     make(map[string]RenderedContext),
		maxNodes:     DefaultMaxICNodes,
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// trimICNodes enforces the sliding window on IC nodes.
// When len(nodes) > maxNodes, it removes the oldest nodes to bring the count
// back to maxNodes. This prevents unbounded memory growth in long-lived sessions.
func (p *Pipeline) trimICNodes(ic *IntermediateContext) {
	if p.maxNodes <= 0 || len(ic.Nodes) <= p.maxNodes {
		return
	}
	trimCount := len(ic.Nodes) - p.maxNodes
	p.logger.Info("pipeline: trimming IC nodes (sliding window)",
		slog.String("session_id", ic.SessionID),
		slog.Int("total_nodes", len(ic.Nodes)),
		slog.Int("max_nodes", p.maxNodes),
		slog.Int("trimmed", trimCount),
	)
	ic.Nodes = ic.Nodes[trimCount:]
}

// PushEvent processes a single canonical event through the pipeline:
// reduce IC → trim → render RC. Returns the new RenderedContext.
func (p *Pipeline) PushEvent(sessionID string, event CanonicalEvent) RenderedContext {
	p.mu.Lock()
	defer p.mu.Unlock()

	ic, ok := p.sessions[sessionID]
	if !ok {
		ic = NewEmptyIC(sessionID)
	}

	newIC := Reduce(ic, event)
	p.trimICNodes(&newIC)
	p.sessions[sessionID] = newIC

	rc := Render(newIC, p.renderParams)
	p.rendered[sessionID] = rc
	return rc
}

// ReplaySession rebuilds IC from persisted events, then renders RC.
// Used for cold-start recovery.
func (p *Pipeline) ReplaySession(sessionID string, events []CanonicalEvent) RenderedContext {
	p.mu.Lock()
	defer p.mu.Unlock()

	ic := NewEmptyIC(sessionID)
	for _, event := range events {
		ic = Reduce(ic, event)
	}
	p.trimICNodes(&ic)
	p.sessions[sessionID] = ic

	rc := Render(ic, p.renderParams)
	p.rendered[sessionID] = rc
	return rc
}

// GetRC returns the current RenderedContext for a session, or nil if not loaded.
func (p *Pipeline) GetRC(sessionID string) RenderedContext {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rendered[sessionID]
}

// GetIC returns the current IntermediateContext for a session.
func (p *Pipeline) GetIC(sessionID string) (IntermediateContext, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ic, ok := p.sessions[sessionID]
	return ic, ok
}

// SessionIDs returns all loaded session IDs.
func (p *Pipeline) SessionIDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.rendered))
	for id := range p.rendered {
		ids = append(ids, id)
	}
	return ids
}

// DropSession removes a session's state from the pipeline.
func (p *Pipeline) DropSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
	delete(p.rendered, sessionID)
}

// SessionCount returns the number of currently loaded sessions.
// Useful for monitoring memory usage and detecting session accumulation.
func (p *Pipeline) SessionCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}

// UpdateRenderParams replaces the default render params and re-renders all
// loaded sessions.
func (p *Pipeline) UpdateRenderParams(params RenderParams) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.renderParams = params
	for sessionID, ic := range p.sessions {
		rc := Render(ic, p.renderParams)
		p.rendered[sessionID] = rc
	}
}
