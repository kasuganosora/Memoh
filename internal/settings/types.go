package settings

const (
	DefaultLanguage          = "auto"
	DefaultReasoningEffort   = "medium"
	DefaultHeartbeatInterval = 30
)

type Settings struct {
	ChatModelID            string           `json:"chat_model_id"`
	ImageModelID           string           `json:"image_model_id"`
	SearchProviderID       string           `json:"search_provider_id"`
	MemoryProviderID       string           `json:"memory_provider_id"`
	TtsModelID             string           `json:"tts_model_id"`
	TranscriptionModelID   string           `json:"transcription_model_id"`
	BrowserContextID       string           `json:"browser_context_id"`
	Language               string           `json:"language"`
	AclDefaultEffect       string           `json:"acl_default_effect"`
	Timezone               string           `json:"timezone"`
	ReasoningEnabled       bool             `json:"reasoning_enabled"`
	ReasoningEffort        string           `json:"reasoning_effort"`
	HeartbeatEnabled       bool             `json:"heartbeat_enabled"`
	HeartbeatInterval      int              `json:"heartbeat_interval"`
	HeartbeatModelID       string           `json:"heartbeat_model_id"`
	TitleModelID           string           `json:"title_model_id"`
	CompactionEnabled      bool             `json:"compaction_enabled"`
	CompactionThreshold    int              `json:"compaction_threshold"`
	CompactionRatio        int              `json:"compaction_ratio"`
	CompactionModelID      string           `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID    string           `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults bool             `json:"persist_full_tool_results"`
	ShowToolCallsInIM      bool             `json:"show_tool_calls_in_im"`
	ChatTiming             ChatTimingConfig `json:"chat_timing"`
}

type UpsertRequest struct {
	ChatModelID            string            `json:"chat_model_id,omitempty"`
	ImageModelID           string            `json:"image_model_id,omitempty"`
	SearchProviderID       string            `json:"search_provider_id,omitempty"`
	MemoryProviderID       string            `json:"memory_provider_id,omitempty"`
	TtsModelID             string            `json:"tts_model_id,omitempty"`
	TranscriptionModelID   string            `json:"transcription_model_id,omitempty"`
	BrowserContextID       string            `json:"browser_context_id,omitempty"`
	Language               string            `json:"language,omitempty"`
	AclDefaultEffect       string            `json:"acl_default_effect,omitempty"`
	Timezone               *string           `json:"timezone,omitempty"`
	ReasoningEnabled       *bool             `json:"reasoning_enabled,omitempty"`
	ReasoningEffort        *string           `json:"reasoning_effort,omitempty"`
	HeartbeatEnabled       *bool             `json:"heartbeat_enabled,omitempty"`
	HeartbeatInterval      *int              `json:"heartbeat_interval,omitempty"`
	HeartbeatModelID       string            `json:"heartbeat_model_id,omitempty"`
	TitleModelID           string            `json:"title_model_id,omitempty"`
	CompactionEnabled      *bool             `json:"compaction_enabled,omitempty"`
	CompactionThreshold    *int              `json:"compaction_threshold,omitempty"`
	CompactionRatio        *int              `json:"compaction_ratio,omitempty"`
	CompactionModelID      *string           `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID    string            `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults *bool             `json:"persist_full_tool_results,omitempty"`
	ShowToolCallsInIM      *bool             `json:"show_tool_calls_in_im,omitempty"`
	ChatTiming             *ChatTimingConfig `json:"chat_timing"`
	EnableReplyer          *bool             `json:"enable_replyer,omitempty"`
	ReplyerModelID         string            `json:"replyer_model_id,omitempty"`
	EnableExpressionLearn  *bool             `json:"enable_expression_learning,omitempty"`
	EnableProfileTracking  *bool             `json:"enable_profile_tracking,omitempty"`
	MemorySearchMode       *string           `json:"memory_search_mode,omitempty"`
}

type ChatTimingConfig struct {
	Enabled                     bool    `json:"enabled,omitempty"`
	DebounceQuietPeriod         int64   `json:"debounce_quiet_period,omitempty"`
	DebounceMaxWait             int64   `json:"debounce_max_wait,omitempty"`
	TimingGate                  bool    `json:"timing_gate,omitempty"`
	TalkValue                   float64 `json:"talk_value,omitempty"`
	InterruptEnabled            bool    `json:"interrupt_enabled,omitempty"`
	InterruptMaxConsecutive     int     `json:"interrupt_max_consecutive,omitempty"`
	InterruptMaxRounds          int     `json:"interrupt_max_rounds,omitempty"`
	IdleCompEnabled             bool    `json:"idle_comp_enabled,omitempty"`
	IdleCompWindowSize          int64   `json:"idle_comp_window_size,omitempty"`
	IdleCompMinIdleBeforeCredit int64   `json:"idle_comp_min_idle_before_credit,omitempty"`
	EnableReplyer               bool    `json:"enable_replyer,omitempty"`
	ReplyerModelID              string  `json:"replyer_model_id,omitempty"`
	EnableExpressionLearn       bool    `json:"enable_expression_learning,omitempty"`
	EnableProfileTracking       bool    `json:"enable_profile_tracking,omitempty"`
	MemorySearchMode            string  `json:"memory_search_mode,omitempty"`
}
