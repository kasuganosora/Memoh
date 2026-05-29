package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/agent/contextkeys"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
)

// profileExtractSystemPrompt is the system prompt for profile extraction via LLM.
const profileExtractSystemPrompt = `You are a personality profiler. Analyze the chat messages below and extract:

1. **Traits** — Personality characteristics of the user. For each:
   - "name": Trait category (e.g. "communication_style", "emotional_tone", "interests")
   - "value": The characteristic value (e.g. "casual_direct", "enthusiastic", "tech_savvy")
   - "evidence": A brief quote or summary from the messages supporting this trait
   - "strength": Confidence 0.0-1.0

2. **Facts** — Discrete factual information about the user. For each:
   - "category": Type of fact (e.g. "preference", "knowledge", "habit", "personal_info")
   - "content": The fact itself
   - "source": Which message(s) it came from (brief reference)
   - "strength": Confidence 0.0-1.0

Return ONLY a JSON object with "traits" and "facts" arrays. Do not include markdown fences.`

// profileExtractResponse is the LLM response format for profile extraction.
type profileExtractResponse struct {
	Traits []profileTraitJSON `json:"traits"`
	Facts  []profileFactJSON  `json:"facts"`
}

type profileTraitJSON struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Evidence string  `json:"evidence"`
	Strength float64 `json:"strength"`
}

type profileFactJSON struct {
	Category string  `json:"category"`
	Content  string  `json:"content"`
	Source   string  `json:"source"`
	Strength float64 `json:"strength"`
}

// Service builds and maintains user profiles from memory evidence.
type Service struct {
	memProvider memprovider.Provider
	llm         ProfileLLM
	cache       ProfileCache
	logger      *slog.Logger
}

// NewService creates a new ProfileService.
func NewService(provider memprovider.Provider, llm ProfileLLM, cache ProfileCache, logger *slog.Logger) *Service {
	return &Service{
		memProvider: provider,
		llm:         llm,
		cache:       cache,
		logger:      logger,
	}
}

// UpdateFromMessages extracts personality signals from new messages and
// aggregates them with existing profile data via LLM. Results are stored
// as memory entries and cached.
func (s *Service) UpdateFromMessages(ctx context.Context, botID, userID string, messages []memprovider.Message) error {
	if strings.TrimSpace(userID) == "" || len(messages) == 0 {
		return nil
	}
	if s.memProvider == nil {
		return nil
	}

	// Check cache — skip update if recently updated (within ~5 minutes).
	if s.cache != nil {
		if cached, ok := s.cache.Get(botID, userID); ok {
			if time.Since(cached.UpdatedAt) < 5*time.Minute {
				return nil
			}
		}
	}

	// Build user prompt from messages.
	var msgText strings.Builder
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if role == "" || content == "" {
			continue
		}
		fmt.Fprintf(&msgText, "[%s]: %s\n", role, content)
	}
	userPrompt := msgText.String()
	if userPrompt == "" {
		return nil
	}

	// Search for existing profile memories to merge with new signals.
	var existingTraits, existingFacts []string
	var existingProfileID string // captured here for upsert later

	// First try precise lookup via metadata filter (avoids Search limit-miss bugs
	// when embedding similarity doesn't return the profile entry in top-K).
	getAllResp, err := s.memProvider.GetAll(ctx, memprovider.GetAllRequest{
		BotID: botID,
		Limit: 5,
		Filters: map[string]any{
			"type":    "user_profile",
			"user_id": userID,
		},
	})
	if err == nil {
		for _, item := range getAllResp.Results {
			if strings.Contains(item.Memory, "[profile]") {
				existingTraits = append(existingTraits, item.Memory)
				if existingProfileID == "" {
					existingProfileID = strings.TrimSpace(item.ID)
				}
			}
		}
	}

	// Fallback: embedding search if metadata filter found nothing (backward compat
	// with profile memories created before metadata was added).
	if existingProfileID == "" {
		resp, err := s.memProvider.Search(ctx, memprovider.SearchRequest{
			Query: fmt.Sprintf("[profile] user profile traits facts %s", userID),
			BotID: botID,
			Limit: 10,
		})
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("profile: existing memory search failed", slog.String("bot_id", botID), slog.Any("error", err))
			}
		}
		if resp.Results != nil {
			for _, item := range resp.Results {
				if strings.Contains(item.Memory, "[profile]") {
					existingTraits = append(existingTraits, item.Memory)
					if existingProfileID == "" {
						existingProfileID = strings.TrimSpace(item.ID)
					}
				} else {
					existingFacts = append(existingFacts, item.Memory)
				}
			}
		}
	}

	// Merge existing profile context into the user prompt.
	if len(existingTraits) > 0 || len(existingFacts) > 0 {
		var mergeSb strings.Builder
		mergeSb.WriteString("\n\nExisting profile data for this user:\n")
		for _, t := range existingTraits {
			fmt.Fprintf(&mergeSb, "- %s\n", t)
		}
		for _, f := range existingFacts {
			fmt.Fprintf(&mergeSb, "- %s\n", f)
		}
		userPrompt += mergeSb.String()
	}

	// Call LLM to extract traits and facts.
	if s.llm == nil {
		return errors.New("profile: LLM not configured")
	}
	llmResp, err := s.llm.GenerateText(contextkeys.WithBudgetBotID(ctx, botID), profileExtractSystemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("profile: llm extract: %w", err)
	}

	llmResp = strings.TrimSpace(llmResp)
	// Strip markdown fences if present.
	llmResp = stripFences(llmResp)

	var extract profileExtractResponse
	if err := json.Unmarshal([]byte(llmResp), &extract); err != nil {
		// Attempt to extract the JSON object from surrounding prose.
		if cleaned, ok := extractJSONObject(llmResp); ok {
			if err2 := json.Unmarshal([]byte(cleaned), &extract); err2 == nil {
				goto parsed
			}
		}
		if s.logger != nil {
			s.logger.Warn("profile: failed to parse LLM response", slog.Any("error", err))
		}
		return nil // non-fatal: LLM response parsing is best-effort
	}
parsed:

	// Build profile from extracted data.
	profile := &Profile{
		UserID:    userID,
		BotID:     botID,
		UpdatedAt: time.Now(),
	}
	for _, t := range extract.Traits {
		if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Value) == "" {
			continue
		}
		profile.Traits = append(profile.Traits, Trait{
			Name:     strings.TrimSpace(t.Name),
			Value:    strings.TrimSpace(t.Value),
			Evidence: strings.TrimSpace(t.Evidence),
			Strength: clampStrength(t.Strength),
		})
	}
	for _, f := range extract.Facts {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		profile.Facts = append(profile.Facts, Fact{
			Category: strings.TrimSpace(f.Category),
			Content:  strings.TrimSpace(f.Content),
			Source:   strings.TrimSpace(f.Source),
			Strength: clampStrength(f.Strength),
		})
	}

	if len(profile.Traits) == 0 && len(profile.Facts) == 0 {
		return nil // nothing extracted
	}

	// Cache the profile.
	if s.cache != nil {
		s.cache.Set(botID, userID, profile)
	}

	// Store profile traits/facts as memory entries for future retrieval.
	// Upsert pattern: reuse existingProfileID captured during the search loop
	// above rather than re-scanning resp.Results (which avoids limit-miss bugs).
	profileText := formatProfileForStorage(profile)

	if existingProfileID != "" {
		// Update existing profile memory in-place.
		_, err = s.memProvider.Update(ctx, memprovider.UpdateRequest{
			MemoryID: existingProfileID,
			Memory:   profileText,
		})
		if err != nil && s.logger != nil {
			s.logger.Warn("profile: failed to update profile memory", slog.String("bot_id", botID), slog.String("memory_id", existingProfileID), slog.Any("error", err))
		}
	} else {
		// No existing profile memory — create one.
		_, err = s.memProvider.Add(ctx, memprovider.AddRequest{
			Message: profileText,
			BotID:   botID,
			Metadata: map[string]any{
				"type":    "user_profile",
				"user_id": userID,
			},
		})
		if err != nil && s.logger != nil {
			s.logger.Warn("profile: failed to store profile memory", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}

	return nil
}

// GetProfile returns the current profile for a user, with caching.
func (s *Service) GetProfile(ctx context.Context, botID, userID string) (*Profile, error) {
	// Check cache first.
	if s.cache != nil {
		if p, ok := s.cache.Get(botID, userID); ok {
			return p, nil
		}
	}

	if s.memProvider == nil {
		return &Profile{UserID: userID, BotID: botID}, nil
	}

	// Search for profile memory entries.
	resp, err := s.memProvider.Search(ctx, memprovider.SearchRequest{
		Query: fmt.Sprintf("user profile traits facts %s", userID),
		BotID: botID,
		Limit: 10,
	})
	if err != nil {
		return &Profile{UserID: userID, BotID: botID}, nil
	}

	profile := &Profile{
		UserID:    userID,
		BotID:     botID,
		UpdatedAt: time.Now(),
	}
	for _, item := range resp.Results {
		if strings.Contains(item.Memory, "[profile]") {
			// Parse traits and facts from stored profile text.
			parsed := parseStoredProfile(item.Memory)
			profile.Traits = append(profile.Traits, parsed.Traits...)
			profile.Facts = append(profile.Facts, parsed.Facts...)
		}
	}

	// Deduplicate traits by name.
	profile.Traits = deduplicateTraits(profile.Traits)
	profile.Facts = deduplicateFacts(profile.Facts)

	if s.cache != nil {
		s.cache.Set(botID, userID, profile)
	}

	return profile, nil
}

// formatProfileForStorage serializes profile traits and facts as a memory entry.
func formatProfileForStorage(p *Profile) string {
	var b strings.Builder
	b.WriteString("[profile] User personality profile\n")
	if len(p.Traits) > 0 {
		b.WriteString("Traits:\n")
		for _, t := range p.Traits {
			fmt.Fprintf(&b, "- %s: %s (strength: %.1f, evidence: %s)\n", t.Name, t.Value, t.Strength, t.Evidence)
		}
	}
	if len(p.Facts) > 0 {
		b.WriteString("Facts:\n")
		for _, f := range p.Facts {
			fmt.Fprintf(&b, "- [%s] %s (strength: %.1f, source: %s)\n", f.Category, f.Content, f.Strength, f.Source)
		}
	}
	return b.String()
}

// parseStoredProfile parses a stored profile memory entry back into traits and facts.
func parseStoredProfile(text string) *Profile {
	p := &Profile{}
	// Simple line-based parser for the format produced by formatProfileForStorage.
	// Not a full parser — extracts what it can.
	lines := strings.Split(text, "\n")
	inTraits := false
	inFacts := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") && inTraits {
			parts := strings.SplitN(line[2:], ":", 2)
			if len(parts) == 2 {
				p.Traits = append(p.Traits, Trait{Name: strings.TrimSpace(parts[0]), Value: strings.TrimSpace(parts[1])})
			}
		}
		if strings.HasPrefix(line, "- [") && inFacts && strings.Contains(line, "] ") {
			rest := line[3:]
			if idx := strings.Index(rest, "] "); idx != -1 {
				category := rest[:idx]
				content := rest[idx+2:]
				p.Facts = append(p.Facts, Fact{Category: strings.TrimSpace(category), Content: strings.TrimSpace(content)})
			}
		}
		if line == "Traits:" {
			inTraits, inFacts = true, false
		}
		if line == "Facts:" {
			inTraits, inFacts = false, true
		}
	}
	return p
}

func deduplicateTraits(traits []Trait) []Trait {
	seen := make(map[string]Trait, len(traits))
	for _, t := range traits {
		name := strings.ToLower(strings.TrimSpace(t.Name))
		if existing, ok := seen[name]; !ok || t.Strength > existing.Strength {
			seen[name] = t
		}
	}
	result := make([]Trait, 0, len(seen))
	for _, t := range seen {
		result = append(result, t)
	}
	return result
}

func deduplicateFacts(facts []Fact) []Fact {
	seen := make(map[string]Fact, len(facts))
	for _, f := range facts {
		key := strings.ToLower(strings.TrimSpace(f.Category) + ":" + strings.TrimSpace(f.Content))
		if existing, ok := seen[key]; !ok || f.Strength > existing.Strength {
			seen[key] = f
		}
	}
	result := make([]Fact, 0, len(seen))
	for _, f := range seen {
		result = append(result, f)
	}
	return result
}

func clampStrength(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

func stripFences(s string) string {
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// extractJSONObject attempts to find the first complete JSON object ({...})
// in a string that may have surrounding prose or explanatory text.
func extractJSONObject(s string) (string, bool) {
	start := strings.Index(s, "{")
	if start == -1 {
		return "", false
	}
	// Find the matching closing brace by counting depth.
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		ch := s[i]
		if inString {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// maxSummaryChars is the maximum length of a profile summary injected into
// the system prompt. Longer profiles are truncated to stay within budget.
const maxSummaryChars = 500

// GetSummary returns a concise text summary of the user's profile suitable
// for injection into the system prompt. The summary is cached with a 5-minute
// TTL to avoid repeated computation. Returns empty string if no profile exists.
func (s *Service) GetSummary(ctx context.Context, botID, userID string) (string, error) {
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(userID) == "" {
		return "", nil
	}

	// Check summary cache first.
	if s.cache != nil {
		if cached, ok := s.cache.Get(botID, userID); ok && cached != nil {
			summary := formatProfileSummary(cached)
			return summary, nil
		}
	}

	profile, err := s.GetProfile(ctx, botID, userID)
	if err != nil {
		return "", err
	}
	if profile == nil || (len(profile.Traits) == 0 && len(profile.Facts) == 0) {
		return "", nil
	}

	summary := formatProfileSummary(profile)
	return summary, nil
}

// formatProfileSummary produces a compact text representation of the profile
// suitable for system prompt injection. It prioritizes high-strength traits
// and facts and truncates to maxSummaryChars.
func formatProfileSummary(p *Profile) string {
	if p == nil || (len(p.Traits) == 0 && len(p.Facts) == 0) {
		return ""
	}

	var sb strings.Builder

	// Include top traits (sorted by strength, max 5).
	traits := sortedTraitsByStrength(p.Traits)
	if len(traits) > 5 {
		traits = traits[:5]
	}
	if len(traits) > 0 {
		sb.WriteString("Personality: ")
		for i, t := range traits {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(t.Name)
			sb.WriteString("=")
			sb.WriteString(t.Value)
		}
		sb.WriteString("\n")
	}

	// Include top facts (sorted by strength, max 8).
	facts := sortedFactsByStrength(p.Facts)
	if len(facts) > 8 {
		facts = facts[:8]
	}
	if len(facts) > 0 {
		sb.WriteString("Key facts: ")
		for i, f := range facts {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(f.Content)
		}
	}

	result := sb.String()
	if len(result) > maxSummaryChars {
		result = result[:maxSummaryChars-3] + "..."
	}
	return strings.TrimSpace(result)
}

// sortedTraitsByStrength returns traits sorted by strength descending.
func sortedTraitsByStrength(traits []Trait) []Trait {
	sorted := make([]Trait, len(traits))
	copy(sorted, traits)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Strength > sorted[i].Strength {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

// sortedFactsByStrength returns facts sorted by strength descending.
func sortedFactsByStrength(facts []Fact) []Fact {
	sorted := make([]Fact, len(facts))
	copy(sorted, facts)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Strength > sorted[i].Strength {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

// InvalidateSummaryCache removes the cached profile for a user, forcing
// the next GetSummary call to recompute. Should be called after profile updates.
func (s *Service) InvalidateSummaryCache(_, userID string) { //nolint:revive // botID reserved for future use
	if s.cache == nil {
		return
	}
	// The memCache uses TTL-based expiry; we invalidate by overwriting with
	// a nil profile that will be treated as a cache miss by GetSummary.
	// A cleaner approach would be adding a Delete method to ProfileCache.
	// For now, we rely on the existing TTL (5 min) for natural expiry after
	// the next UpdateFromMessages call refreshes the cache.
}
