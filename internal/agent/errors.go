package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Error codes for agent package.
const (
	// Tool execution errors.
	ErrToolExecutionFailed = "TOOL_EXECUTION_FAILED"
	ErrToolTimeout         = "TOOL_TIMEOUT"
	ErrToolInvalidInput    = "TOOL_INVALID_INPUT"

	// Container errors.
	ErrContainerUnreachable = "CONTAINER_UNREACHABLE"
	ErrContainerTimeout     = "CONTAINER_TIMEOUT"
	ErrFileTooLarge         = "FILE_TOO_LARGE"

	// Subagent errors.
	ErrSubagentTimeout    = "SUBAGENT_TIMEOUT"
	ErrSubagentRateLimit  = "SUBAGENT_RATE_LIMIT"
	ErrSubagentMaxRetries = "SUBAGENT_MAX_RETRIES"

	// Background task errors.
	ErrBackgroundTaskFailed = "BACKGROUND_TASK_FAILED"
	ErrTaskNotFound         = "TASK_NOT_FOUND"
	ErrTaskAlreadyRunning   = "TASK_ALREADY_RUNNING"
)

// AgentError represents a structured error with code and context.
type AgentError struct {
	Code    string
	Message string
	Cause   error
	Context map[string]interface{}
}

func (e *AgentError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AgentError) Unwrap() error {
	return e.Cause
}

// NewAgentError creates a new AgentError.
func NewAgentError(code, message string, cause error) *AgentError {
	return &AgentError{
		Code:    code,
		Message: message,
		Cause:   cause,
		Context: make(map[string]interface{}),
	}
}

// WithContext adds context to the error.
func (e *AgentError) WithContext(key string, value interface{}) *AgentError {
	e.Context[key] = value
	return e
}

// IsRetryableError checks if an error is retryable.
func IsRetryableError(err error) bool {
	var agentErr *AgentError
	if errors.As(err, &agentErr) {
		switch agentErr.Code {
		case ErrToolTimeout, ErrContainerTimeout, ErrSubagentTimeout, ErrSubagentRateLimit:
			return true
		default:
			return false
		}
	}

	// Check for network-related errors
	return isNetworkError(err)
}

// IsFatalError checks if an error is fatal (should not be retried).
func IsFatalError(err error) bool {
	var agentErr *AgentError
	if errors.As(err, &agentErr) {
		switch agentErr.Code {
		case ErrToolInvalidInput, ErrFileTooLarge, ErrSubagentMaxRetries:
			return true
		default:
			return false
		}
	}
	return false
}

// NewToolError creates a tool-related error with the given code.
func NewToolError(code, message string, cause error) *AgentError {
	e := NewAgentError(code, message, cause)
	e.Context["category"] = "tool"
	return e
}

// NewContainerError creates a container-related error with the given code.
func NewContainerError(code, message string, cause error) *AgentError {
	e := NewAgentError(code, message, cause)
	e.Context["category"] = "container"
	return e
}

// NewSubagentError creates a subagent-related error with the given code.
func NewSubagentError(code, message string, cause error) *AgentError {
	e := NewAgentError(code, message, cause)
	e.Context["category"] = "subagent"
	return e
}

// isNetworkError checks if the error is network-related.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	networkKeywords := []string{
		"connection reset",
		"connection refused",
		"timeout",
		"deadline exceeded",
		"network error",
		"unexpected EOF", // Only unexpected EOF indicates network issues; normal io.EOF is not a network error
	}

	for _, keyword := range networkKeywords {
		if containsIgnoreCase(errStr, keyword) {
			return true
		}
	}

	return false
}

func containsIgnoreCase(s, substr string) bool {
	sLower := strings.ToLower(s)
	substrLower := strings.ToLower(substr)
	return strings.Contains(sLower, substrLower)
}
