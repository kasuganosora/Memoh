package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
		msgText.WriteString(fmt.Sprintf("[%s]: %s\n", role, content))
	}
	userPrompt := msgText.String()
	if userPrompt == "" {
		return nil
	}

	// Search for existing profile memories to merge with new signals.
	var existingTraits, existingFacts []string
	resp, err := s.memProvider.Search(ctx, memprovider.SearchRequest{
		Query: fmt.Sprintf("user profile traits facts %s", userID),
		BotID: botID,
		Limit: 5,
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
			} else {
				existingFacts = append(existingFacts, item.Memory)
			}
		}
	}

	// Merge existing profile context into the user prompt.
	if len(existingTraits) > 0 || len(existingFacts) > 0 {
		userPrompt += "\n\nExisting profile data for this user:\n"
		for _, t := range existingTraits {
			userPrompt += fmt.Sprintf("- %s\n", t)
		}
		for _, f := range existingFacts {
			userPrompt += fmt.Sprintf("- %s\n", f)
		}
	}

	// Call LLM to extract traits and facts.
	if s.llm == nil {
		return fmt.Errorf("profile: LLM not configured")
	}
	llmResp, err := s.llm.GenerateText(ctx, profileExtractSystemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("profile: llm extract: %w", err)
	}

	llmResp = strings.TrimSpace(llmResp)
	// Strip markdown fences if present.
	llmResp = stripFences(llmResp)

	var extract profileExtractResponse
	if err := json.Unmarshal([]byte(llmResp), &extract); err != nil {
		if s.logger != nil {
			s.logger.Warn("profile: failed to parse LLM response", slog.Any("error", err))
		}
		return nil // non-fatal: LLM response parsing is best-effort
	}

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
	profileText := formatProfileForStorage(profile)
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
			b.WriteString(fmt.Sprintf("- %s: %s (strength: %.1f, evidence: %s)\n", t.Name, t.Value, t.Strength, t.Evidence))
		}
	}
	if len(p.Facts) > 0 {
		b.WriteString("Facts:\n")
		for _, f := range p.Facts {
			b.WriteString(fmt.Sprintf("- [%s] %s (strength: %.1f, source: %s)\n", f.Category, f.Content, f.Strength, f.Source))
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
