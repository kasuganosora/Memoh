package agent

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateSystemPromptIncludesPlatformIdentitiesInChat(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType:               "chat",
		Now:                       time.Unix(1, 0).UTC(),
		Timezone:                  "UTC",
		PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"telegram\" username=\"@memoh\"/>",
	})

	if !strings.Contains(prompt, "## Platform Identities") {
		t.Fatalf("expected platform identities heading in prompt")
	}
	if !strings.Contains(prompt, `<identity channel="telegram" username="@memoh"/>`) {
		t.Fatalf("expected platform identity XML in prompt")
	}
}

func TestGenerateSystemPromptIncludesPlatformIdentitiesInDiscuss(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType:               "discuss",
		Now:                       time.Unix(1, 0).UTC(),
		Timezone:                  "UTC",
		PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"discord\" username=\"@memoh\"/>",
	})

	if !strings.Contains(prompt, "## Platform Identities") {
		t.Fatalf("expected platform identities heading in discuss prompt")
	}
	if !strings.Contains(prompt, `<identity channel="discord" username="@memoh"/>`) {
		t.Fatalf("expected platform identity XML in discuss prompt")
	}
}

func TestGenerateSystemPromptIncludesImageGenerationRulesInDiscuss(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType: "discuss",
		Now:         time.Unix(1, 0).UTC(),
		Timezone:    "UTC",
	})

	// Verify that the discuss mode prompt includes special rules for image generation
	if !strings.Contains(prompt, "Special Rules for Image Generation") {
		t.Fatalf("expected image generation rules section in discuss prompt")
	}
	if !strings.Contains(prompt, "generate_image") {
		t.Fatalf("expected generate_image tool mention in discuss prompt")
	}
	if !strings.Contains(prompt, "Important exception") {
		t.Fatalf("expected important exception note for image generation in discuss prompt")
	}
	if !strings.Contains(prompt, "`send` tool requirement does NOT apply to image generation") {
		t.Fatalf("expected clarification about send tool exception for image generation")
	}
}

func TestGenerateSystemPromptChatModeExcludesImageGenerationRules(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType: "chat",
		Now:         time.Unix(1, 0).UTC(),
		Timezone:    "UTC",
	})

	// Chat mode should NOT contain the discuss-specific image generation rules
	if strings.Contains(prompt, "Special Rules for Image Generation") {
		t.Fatalf("chat mode prompt should not contain discuss-specific image generation rules")
	}
}
