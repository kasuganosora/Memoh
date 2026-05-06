package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/workflow"
)

// PipelineHandler provides HTTP endpoints for pipeline observability.
type PipelineHandler struct {
	scheduler      *workflow.Scheduler
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

// NewPipelineHandler creates a new pipeline HTTP handler.
func NewPipelineHandler(log *slog.Logger, scheduler *workflow.Scheduler, botService *bots.Service, accountService *accounts.Service) *PipelineHandler {
	return &PipelineHandler{
		scheduler:      scheduler,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "pipeline")),
	}
}

// Register registers pipeline routes on the Echo instance.
func (h *PipelineHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/pipelines")
	group.GET("", h.List)
	group.GET("/:id/status", h.Status)
	group.GET("/:id/graph", h.Graph)
	group.POST("/:id/retry", h.Retry)
}

// List lists all pipelines for a bot.
func (h *PipelineHandler) List(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	limit, offset := parseOffsetLimit(c)
	botUUID, err := uuid.Parse(botID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid bot id")
	}
	pipelines, err := h.scheduler.ListPipelines(c.Request().Context(), botUUID, int32(limit), int32(offset))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"items": pipelines})
}

// Status returns the full DAG status for a pipeline.
// GET /bots/:bot_id/pipelines/:id/status
func (h *PipelineHandler) Status(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	pipelineID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid pipeline id")
	}
	status, err := h.scheduler.GetPipelineStatus(c.Request().Context(), pipelineID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, status)
}

// Graph returns the topological structure for frontend visualization.
// GET /bots/:bot_id/pipelines/:id/graph
func (h *PipelineHandler) Graph(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	pipelineID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid pipeline id")
	}
	status, err := h.scheduler.GetPipelineStatus(c.Request().Context(), pipelineID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, status.Graph)
}

// Retry retries failed nodes in a pipeline.
// POST /bots/:bot_id/pipelines/:id/retry
func (h *PipelineHandler) Retry(c echo.Context) error {
	userID, err := h.requireUserID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}
	pipelineID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid pipeline id")
	}
	status, err := h.scheduler.GetPipelineStatus(c.Request().Context(), pipelineID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	pwn := &workflow.PipelineWithNodes{
		Pipeline: status.Pipeline,
		Nodes:    status.Nodes,
	}
	if err := h.scheduler.Resume(c.Request().Context(), pwn); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "retry initiated"})
}

func (h *PipelineHandler) requireUserID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *PipelineHandler) authorizeBotAccess(ctx context.Context, userID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, userID, botID)
}
