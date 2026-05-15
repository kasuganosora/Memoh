package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// PromptLLM is the minimal interface the adapter needs from a compact model.
// Satisfied by agent.Generate or any simple text-generation function.
type PromptLLM interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// PromptBasedDreamLLM implements DreamLLM using simple prompt patterns
// designed for a compact (cheap) model.
type PromptBasedDreamLLM struct {
	llm    PromptLLM
	logger *slog.Logger
}

// NewPromptBasedDreamLLM creates a dream LLM adapter backed by a compact model.
func NewPromptBasedDreamLLM(llm PromptLLM, logger *slog.Logger) *PromptBasedDreamLLM {
	return &PromptBasedDreamLLM{
		llm:    llm,
		logger: logger.With(slog.String("component", "dream-llm")),
	}
}

const dreamSystemPrompt = `You are a memory maintenance assistant. Keep responses concise and structured.
Output ONLY valid JSON — no markdown fences, no explanations.`

// ShouldMerge asks the compact model whether two memories should be merged.
func (a *PromptBasedDreamLLM) ShouldMerge(ctx context.Context, m1, m2 string) (bool, string, error) {
	prompt := fmt.Sprintf(`Determine if these two memories are saying the same thing and should be merged:

Memory 1: %s
Memory 2: %s

Output JSON: {"merge": true/false, "merged_text": "combined text if merging"}
Only merge if they are essentially the same fact expressed differently.`, m1, m2)

	text, err := a.llm.GenerateText(ctx, dreamSystemPrompt, prompt)
	if err != nil {
		return false, "", err
	}

	var resp struct {
		Merge      bool   `json:"merge"`
		MergedText string `json:"merged_text"`
	}
	if err := json.Unmarshal([]byte(cleanJSON(text)), &resp); err != nil {
		a.logger.Debug("ShouldMerge parse failed", slog.String("text", text), slog.Any("error", err))
		return false, "", nil
	}
	return resp.Merge, resp.MergedText, nil
}

// IsHarmful asks the compact model if a memory contains sensitive content.
func (a *PromptBasedDreamLLM) IsHarmful(ctx context.Context, memory string) (bool, error) {
	prompt := fmt.Sprintf(`Check if this memory contains private, sensitive, or harmful information:

Memory: %s

Output JSON: {"harmful": true/false}`, memory)

	text, err := a.llm.GenerateText(ctx, dreamSystemPrompt, prompt)
	if err != nil {
		return false, err
	}

	var resp struct {
		Harmful bool `json:"harmful"`
	}
	if err := json.Unmarshal([]byte(cleanJSON(text)), &resp); err != nil {
		a.logger.Debug("IsHarmful parse failed", slog.String("text", text))
		return false, nil
	}
	return resp.Harmful, nil
}

// FindAssociations analyzes a batch of memories and returns semantic links.
// Designed for the compact model — small batch, simple JSON output.
func (a *PromptBasedDreamLLM) FindAssociations(ctx context.Context, memories []string) ([]MemoryAssociation, error) {
	if len(memories) < 2 {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Analyze these memories and find pairs that are semantically related.\n")
	sb.WriteString("For each related pair, output the 0-based indices and a short label.\n\n")
	sb.WriteString("Memories:\n")
	for i, m := range memories {
		fmt.Fprintf(&sb, "%d: %s\n", i, strings.TrimSpace(m))
	}
	sb.WriteString("\nOutput JSON array: [{\"a\": 0, \"b\": 1, \"label\": \"same_topic\"}, ...]\n")
	sb.WriteString("Labels: same_topic, contradicts, supports, prerequisite, example_of, part_of\n")
	sb.WriteString("Only include clear, strong associations. Do NOT force connections where none exist.\n")
	sb.WriteString("Maximum 5 pairs total.")

	text, err := a.llm.GenerateText(ctx, dreamSystemPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("FindAssociations LLM call failed: %w", err)
	}

	cleanText := cleanJSON(text)

	// Try as array first
	var pairs []MemoryAssociation
	if err := json.Unmarshal([]byte(cleanText), &pairs); err != nil {
		// Try wrapped: {"pairs": [...]}
		var wrapped struct {
			Pairs []MemoryAssociation `json:"pairs"`
		}
		if err2 := json.Unmarshal([]byte(cleanText), &wrapped); err2 != nil {
			a.logger.Debug("FindAssociations parse failed",
				slog.String("text", text[:min(len(text), 200)]),
			)
			return nil, nil // graceful degradation: no associations is not an error
		}
		pairs = wrapped.Pairs
	}

	// Validate indices
	var result []MemoryAssociation
	for _, p := range pairs {
		if p.IndexA >= 0 && p.IndexA < len(memories) &&
			p.IndexB >= 0 && p.IndexB < len(memories) &&
			p.IndexA != p.IndexB {
			result = append(result, p)
		}
	}

	return result, nil
}

// cleanJSON strips markdown fences and trims whitespace from LLM output.
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// Ensure PromptBasedDreamLLM satisfies DreamLLM.
var _ DreamLLM = (*PromptBasedDreamLLM)(nil)

// AggregateScenes analyzes a batch of memories and proposes scene clusters.
func (a *PromptBasedDreamLLM) AggregateScenes(ctx context.Context, memories []string) ([]SceneCandidate, error) {
	if len(memories) < 3 {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Analyze these memories and group them into coherent scenes/topics.\n")
	sb.WriteString("A scene is a cluster of memories about the same topic, event, or interaction pattern.\n\n")
	sb.WriteString("Memories (0-based index):\n")
	for i, m := range memories {
		fmt.Fprintf(&sb, "%d: %s\n", i, strings.TrimSpace(m))
	}
	sb.WriteString("\nOutput JSON array of scenes:\n")
	sb.WriteString(`[{"title": "short title", "summary": "1-2 sentence description", "memory_ids": ["0", "2", "5"]}]`)
	sb.WriteString("\n\nRules:\n")
	sb.WriteString("- Each scene must have at least 2 memories\n")
	sb.WriteString("- A memory can only belong to one scene\n")
	sb.WriteString("- Only create scenes for clearly related memories\n")
	sb.WriteString("- Maximum 5 scenes per batch\n")
	sb.WriteString("- memory_ids are the 0-based indices as strings\n")

	text, err := a.llm.GenerateText(ctx, dreamSystemPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("AggregateScenes LLM call failed: %w", err)
	}

	cleanText := cleanJSON(text)

	// Try as array first.
	var candidates []SceneCandidate
	if err := json.Unmarshal([]byte(cleanText), &candidates); err != nil {
		// Try wrapped: {"scenes": [...]}
		var wrapped struct {
			Scenes []SceneCandidate `json:"scenes"`
		}
		if err2 := json.Unmarshal([]byte(cleanText), &wrapped); err2 != nil {
			a.logger.Debug("AggregateScenes parse failed",
				slog.String("text", text[:min(len(text), 200)]),
			)
			return nil, nil // graceful degradation
		}
		candidates = wrapped.Scenes
	}

	// Validate: filter out invalid candidates.
	var valid []SceneCandidate
	for _, c := range candidates {
		if c.Title != "" && len(c.MemoryIDs) >= 2 {
			valid = append(valid, c)
		}
	}

	return valid, nil
}

// NoOpDreamLLM is a stub DreamLLM that does nothing. Useful when no compact
// model is configured — dream runs are silent no-ops.
type NoOpDreamLLM struct{}

func (NoOpDreamLLM) ShouldMerge(context.Context, string, string) (bool, string, error) {
	return false, "", nil
}

func (NoOpDreamLLM) IsHarmful(context.Context, string) (bool, error) {
	return false, nil
}

func (NoOpDreamLLM) FindAssociations(context.Context, []string) ([]MemoryAssociation, error) {
	return nil, errors.New("dream LLM not configured")
}

func (NoOpDreamLLM) AggregateScenes(context.Context, []string) ([]SceneCandidate, error) {
	return nil, errors.New("dream LLM not configured")
}

var _ DreamLLM = NoOpDreamLLM{}
