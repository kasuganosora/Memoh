package agent

import (
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	skillset "github.com/memohai/memoh/internal/skills"
)

//go:embed prompts/*.md
var promptsFS embed.FS

var (
	systemChatTmpl           string
	systemDiscussTmpl        string
	systemHeartbeatTmpl      string
	systemHeartbeatAlertTmpl string
	systemScheduleTmpl       string
	systemSubagentTmpl       string
	scheduleTmpl             string
	heartbeatTmpl            string

	ReplyerPrompt       string
	MemoryExtractPrompt string
	MemoryUpdatePrompt  string

	includes map[string]string
)

var includeRe = regexp.MustCompile(`\{\{include:(\w+)\}\}`)

func init() {
	systemChatTmpl = mustReadPrompt("prompts/system_chat.md")
	systemDiscussTmpl = mustReadPrompt("prompts/system_discuss.md")
	systemHeartbeatTmpl = mustReadPrompt("prompts/system_heartbeat.md")
	systemHeartbeatAlertTmpl = mustReadPrompt("prompts/system_heartbeat_alert.md")
	systemScheduleTmpl = mustReadPrompt("prompts/system_schedule.md")
	systemSubagentTmpl = mustReadPrompt("prompts/system_subagent.md")
	scheduleTmpl = mustReadPrompt("prompts/schedule.md")
	heartbeatTmpl = mustReadPrompt("prompts/heartbeat.md")
	ReplyerPrompt = readOptionalPrompt("prompts/system_replyer.md")
	MemoryExtractPrompt = mustReadPrompt("prompts/memory_extract.md")
	MemoryUpdatePrompt = mustReadPrompt("prompts/memory_update.md")

	includes = map[string]string{
		"_memory":        mustReadPrompt("prompts/_memory.md"),
		"_tools":         mustReadPrompt("prompts/_tools.md"),
		"_contacts":      mustReadPrompt("prompts/_contacts.md"),
		"_identities":    mustReadPrompt("prompts/_identities.md"),
		"_schedule_task": mustReadPrompt("prompts/_schedule_task.md"),
		"_subagent":      mustReadPrompt("prompts/_subagent.md"),
		"_cross_session": mustReadPrompt("prompts/_cross_session.md"),
	}

	systemChatTmpl = resolveIncludes(systemChatTmpl)
	systemDiscussTmpl = resolveIncludes(systemDiscussTmpl)
	systemHeartbeatTmpl = resolveIncludes(systemHeartbeatTmpl)
	systemHeartbeatAlertTmpl = resolveIncludes(systemHeartbeatAlertTmpl)
	systemScheduleTmpl = resolveIncludes(systemScheduleTmpl)
	systemSubagentTmpl = resolveIncludes(systemSubagentTmpl)
}

func mustReadPrompt(name string) string {
	data, err := promptsFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded prompt %s: %v", name, err))
	}
	return string(data)
}

// readOptionalPrompt is like mustReadPrompt but returns a built-in
// fallback instead of panicking when the file is missing. Used for
// prompts that can be added incrementally (e.g. replyer).
func readOptionalPrompt(name string) string {
	data, err := promptsFS.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(data)
}

// resolveIncludes replaces {{include:_name}} placeholders with the content of the named fragment.
func resolveIncludes(tmpl string) string {
	return includeRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := includeRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		content, ok := includes[sub[1]]
		if !ok {
			return match
		}
		return strings.TrimSpace(content)
	})
}

// render replaces all {{key}} placeholders in tmpl with values from vars.
func render(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return strings.TrimSpace(result)
}

func selectSystemTemplate(sessionType string) string {
	switch sessionType {
	case "discuss":
		return systemDiscussTmpl
	case "heartbeat":
		return systemHeartbeatTmpl
	case "heartbeat_alert":
		return systemHeartbeatAlertTmpl
	case "schedule":
		return systemScheduleTmpl
	case "subagent":
		return systemSubagentTmpl
	default:
		return systemChatTmpl
	}
}

// GenerateSystemPrompt builds the complete system prompt from files, skills, and context.
func GenerateSystemPrompt(params SystemPromptParams) string {
	home := "/data"
	now := params.Now
	if now.IsZero() {
		now = TimeNow()
	}
	timezoneName := strings.TrimSpace(params.Timezone)
	if timezoneName == "" {
		timezoneName = "UTC"
	}

	readToolDesc := "- `read`: read file content"
	if params.SupportsImageInput {
		readToolDesc += " (also supports images: PNG, JPEG, GIF, WebP)"
	}
	basicTools := []string{readToolDesc}
	basicTools = append(basicTools,
		"- `write`: write file content",
		"- `list`: list directory entries",
		"- `edit`: replace exact text in a file",
		"- `exec`: execute command",
	)

	skillsSection := buildSkillsSection(params.Skills)

	fileSections := ""
	var fileSectionsSb strings.Builder
	for _, f := range params.Files {
		if f.Content == "" {
			continue
		}
		fileSectionsSb.WriteString("\n\n" + formatSystemFile(f))
	}
	fileSections += fileSectionsSb.String()

	tmpl := selectSystemTemplate(params.SessionType)

	result := render(tmpl, map[string]string{
		"home":                      home,
		"currentTime":               now.Format(time.RFC3339),
		"timezone":                  timezoneName,
		"basicTools":                strings.Join(basicTools, "\n"),
		"skillsSection":             skillsSection,
		"platformIdentitiesSection": strings.TrimSpace(params.PlatformIdentitiesSection),
		"fileSections":              fileSections,
	})

	if recap := buildIdentityRecap(params.Files); recap != "" {
		result += recap
	}

	return result
}

// SystemPromptParams holds all inputs for system prompt generation.
type SystemPromptParams struct {
	SessionType               string
	Skills                    []SkillEntry
	Files                     []SystemFile
	Now                       time.Time
	Timezone                  string
	SupportsImageInput        bool
	PlatformIdentitiesSection string
}

// GenerateSchedulePrompt builds the user message for a scheduled task trigger.
func GenerateSchedulePrompt(s Schedule) string {
	maxCallsStr := "Unlimited"
	if s.MaxCalls != nil {
		maxCallsStr = strconv.Itoa(*s.MaxCalls)
	}
	return render(scheduleTmpl, map[string]string{
		"name":        s.Name,
		"description": s.Description,
		"maxCalls":    maxCallsStr,
		"pattern":     s.Pattern,
		"command":     s.Command,
	})
}

// GenerateHeartbeatPrompt builds the user message for a heartbeat trigger.
func GenerateHeartbeatPrompt(interval int, checklist string, now time.Time, lastHeartbeatAt string) string {
	checklistSection := ""
	if strings.TrimSpace(checklist) != "" {
		checklistSection = "\n## HEARTBEAT.md (checklist)\n\n" + strings.TrimSpace(checklist) + "\n"
	}
	lastHB := strings.TrimSpace(lastHeartbeatAt)
	if lastHB == "" {
		lastHB = "never (first heartbeat)"
	}
	return render(heartbeatTmpl, map[string]string{
		"interval":         strconv.Itoa(interval),
		"timeNow":          now.Format(time.RFC3339),
		"lastHeartbeat":    lastHB,
		"checklistSection": checklistSection,
	})
}

func buildSkillsSection(skills []SkillEntry) string {
	if len(skills) == 0 {
		return ""
	}
	sorted := make([]SkillEntry, len(skills))
	copy(sorted, skills)
	slices.SortFunc(sorted, func(a, b SkillEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	var sb strings.Builder
	sb.WriteString("## Skills\n\n")
	sb.WriteString("Memoh-managed skills are stored in `" + skillset.ManagedDir() + "/`. ")
	sb.WriteString("Compatible external skill directories inside the bot container may also be discovered automatically. ")
	sb.WriteString("Each skill is a `SKILL.md` file inside a named subdirectory.\n\n")
	sb.WriteString("Call `use_skill` with the skill name to load its full instructions before following them. ")
	sb.WriteString("Only activate a skill when it is relevant to the current task.\n\n")
	sb.WriteString(strconv.Itoa(len(sorted)))
	sb.WriteString(" skill(s) available:\n")
	for _, s := range sorted {
		sb.WriteString("- **" + s.Name + "**: " + s.Description + "\n")
	}
	return sb.String()
}

func formatSystemFile(file SystemFile) string {
	return fmt.Sprintf("## %s\n\n%s", file.Filename, file.Content)
}

// buildIdentityRecap creates a concise identity recap from IDENTITY.md and
// SOUL.md file content. Appended at the end of the system prompt as a "bookend"
// to help the model maintain consistent personality in long conversations.
func buildIdentityRecap(files []SystemFile) string {
	var identity, soul string
	for _, f := range files {
		switch f.Filename {
		case "IDENTITY.md":
			identity = f.Content
		case "SOUL.md":
			soul = f.Content
		}
	}
	if identity == "" && soul == "" {
		return ""
	}

	name := ExtractIdentityField(identity, "Name")
	vibe := ExtractIdentityField(identity, "Vibe")
	creature := ExtractIdentityField(identity, "Creature")
	emoji := ExtractIdentityField(identity, "Emoji")

	var sb strings.Builder
	sb.WriteString("\n\n## Identity Recap\n\n")

	var parts []string
	if name != "" {
		parts = append(parts, "your name is **"+name+"**")
	}
	if creature != "" {
		parts = append(parts, "you are a "+creature)
	}
	if vibe != "" {
		parts = append(parts, "your vibe is "+vibe)
	}
	if emoji != "" {
		parts = append(parts, "your emoji is "+emoji)
	}

	if len(parts) > 0 {
		sb.WriteString("Remember your core identity: ")
		sb.WriteString(strings.Join(parts, ", "))
		sb.WriteString(". ")
	}

	if soul != "" {
		sb.WriteString("Your core beliefs from SOUL.md are defined above — stay true to them.\n")
	}

	sb.WriteString("Maintain consistency with your defined personality and communication style throughout this conversation.")

	return sb.String()
}

// ExtractIdentityField extracts a bold-marked field value from IDENTITY.md content.
// Supports two formats:
//   - Same line:  **Name:** Alice
//   - Next line:  **Name:**\n  Alice
//
// Returns empty string if the field is not found or contains only template placeholders.
func ExtractIdentityField(content, field string) string {
	// Same-line format: **Name:** value
	if v := extractSameLine(content, field); v != "" && !templatePlaceholder(v) {
		return v
	}
	// Multi-line format: **Name:**\n  value (value on next indented line)
	if v := extractNextLine(content, field); v != "" && !templatePlaceholder(v) {
		return v
	}
	return ""
}

func extractSameLine(content, field string) string {
	re := regexp.MustCompile(`\*\*` + regexp.QuoteMeta(field) + `:\*\*[ \t]*(.+)`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func extractNextLine(content, field string) string {
	// Match **Field:** at end of line, capture the first non-blank line that follows.
	re := regexp.MustCompile(`\*\*` + regexp.QuoteMeta(field) + `:\*\*[ \t]*\n[ \t]*(\S[^\n]*)`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// templatePlaceholder returns true if the value looks like an unfilled template hint.
// Template placeholders are italicized hints like _(pick something you like)_.
func templatePlaceholder(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "_(") && strings.HasSuffix(s, ")_")
}
