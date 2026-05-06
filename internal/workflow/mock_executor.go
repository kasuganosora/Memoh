package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// MockNodeExecutor implements NodeExecutor for testing.
type MockNodeExecutor struct {
	mu          sync.Mutex
	nodeOutputs map[string]json.RawMessage // node name -> output
	nodeErrors  map[string]error           // node name -> error
	execCalls   map[string]int             // track call count per node
}

// NewMockNodeExecutor creates a mock executor.
func NewMockNodeExecutor() *MockNodeExecutor {
	return &MockNodeExecutor{
		nodeOutputs: make(map[string]json.RawMessage),
		nodeErrors:  make(map[string]error),
		execCalls:   make(map[string]int),
	}
}

// SetOutput sets a predefined output for a node by name.
func (m *MockNodeExecutor) SetOutput(name string, output json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodeOutputs[name] = output
}

// SetError sets a predefined error for a node by name.
func (m *MockNodeExecutor) SetError(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodeErrors[name] = err
}

// GetCallCount returns how many times a node was executed.
func (m *MockNodeExecutor) GetCallCount(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execCalls[name]
}

// Execute runs the node and returns the predefined output or error.
func (m *MockNodeExecutor) Execute(_ context.Context, node Node) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.execCalls[node.Name]++
	if err, ok := m.nodeErrors[node.Name]; ok {
		// Clear error after first failure to simulate retry success
		delete(m.nodeErrors, node.Name)
		return nil, err
	}
	if output, ok := m.nodeOutputs[node.Name]; ok {
		return output, nil
	}
	// Default success response
	msg := fmt.Sprintf(`{"result": "ok", "node": "%s"}`, node.Name)
	return json.RawMessage(msg), nil
}

// Ensure mock satisfies interface.
var _ NodeExecutor = (*MockNodeExecutor)(nil)

// errNodeExecutor is a simple error-returning executor for failure tests.
type errNodeExecutor struct {
	err error
}

func (e *errNodeExecutor) Execute(_ context.Context, _ Node) (json.RawMessage, error) {
	return nil, e.err
}

func newErrNodeExecutor(msg string) *errNodeExecutor {
	return &errNodeExecutor{err: errors.New(msg)}
}
