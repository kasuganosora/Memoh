package chattiming

import (
	"log/slog"

	agentpkg "github.com/memohai/memoh/internal/agent"
)

// Service provides chat timing components configured for a specific bot.
type Service struct {
	agent  *agentpkg.Agent
	logger *slog.Logger
}

// NewService creates a new Service with the given agent.
func NewService(agent *agentpkg.Agent, logger *slog.Logger) *Service {
	return &Service{
		agent:  agent,
		logger: logger,
	}
}

// NewDebouncer creates a debouncer from config.
func (s *Service) NewDebouncer(cfg Config) *Debouncer {
	s.logger.Debug("creating debouncer", slog.Float64("quiet_period", cfg.Debounce.QuietPeriod.Seconds()))
	return NewDebouncer(cfg.Debounce)
}

// NewTimingGate creates a timing gate.
func (s *Service) NewTimingGate() *TimingGate {
	return NewTimingGate(s.agent, s.logger)
}

// NewInterruptController creates an interrupt controller from config.
func (s *Service) NewInterruptController(cfg Config) *InterruptController {
	s.logger.Debug("creating interrupt controller", slog.Int("max_consecutive", cfg.Interrupt.MaxConsecutive))
	return NewInterruptController(cfg.Interrupt)
}

// NewIdleCompensator creates an idle compensator from config.
func (s *Service) NewIdleCompensator(cfg Config) *IdleCompensator {
	s.logger.Debug("creating idle compensator", slog.Bool("enabled", cfg.IdleCompensation.Enabled))
	return NewIdleCompensator(cfg.IdleCompensation)
}
