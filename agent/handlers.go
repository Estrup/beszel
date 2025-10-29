package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/smart"

	"golang.org/x/exp/slog"
)

// HandlerContext provides context for request handlers
type HandlerContext struct {
	Client      *WebSocketClient
	Agent       *Agent
	Request     *common.HubRequest[cbor.RawMessage]
	RequestID   *uint32
	HubVerified bool
	// SendResponse abstracts how a handler sends responses (WS or SSH)
	SendResponse func(data any, requestID *uint32) error
}

// RequestHandler defines the interface for handling specific websocket request types
type RequestHandler interface {
	// Handle processes the request and returns an error if unsuccessful
	Handle(hctx *HandlerContext) error
}

// Responder sends handler responses back to the hub (over WS or SSH)
type Responder interface {
	SendResponse(data any, requestID *uint32) error
}

// HandlerRegistry manages the mapping between actions and their handlers
type HandlerRegistry struct {
	handlers map[common.WebSocketAction]RequestHandler
}

// NewHandlerRegistry creates a new handler registry with default handlers
func NewHandlerRegistry() *HandlerRegistry {
	registry := &HandlerRegistry{
		handlers: make(map[common.WebSocketAction]RequestHandler),
	}

	registry.Register(common.GetData, &GetDataHandler{})
	registry.Register(common.CheckFingerprint, &CheckFingerprintHandler{})
	registry.Register(common.GetContainerLogs, &GetContainerLogsHandler{})
	registry.Register(common.GetContainerInfo, &GetContainerInfoHandler{})
	registry.Register(common.GetSmartData, &GetSmartDataHandler{})
	registry.Register(common.StartContainer, &StartContainerHandler{})
	registry.Register(common.StopContainer, &StopContainerHandler{})
	registry.Register(common.RestartContainer, &RestartContainerHandler{})

	return registry
}

// Register registers a handler for a specific action type
func (hr *HandlerRegistry) Register(action common.WebSocketAction, handler RequestHandler) {
	hr.handlers[action] = handler
}

// Handle routes the request to the appropriate handler
func (hr *HandlerRegistry) Handle(hctx *HandlerContext) error {
	handler, exists := hr.handlers[hctx.Request.Action]
	if !exists {
		return fmt.Errorf("unknown action: %d", hctx.Request.Action)
	}

	// Check verification requirement - default to requiring verification
	if hctx.Request.Action != common.CheckFingerprint && !hctx.HubVerified {
		return errors.New("hub not verified")
	}

	// Log handler execution for debugging
	// slog.Debug("Executing handler", "action", hctx.Request.Action)

	return handler.Handle(hctx)
}

// GetHandler returns the handler for a specific action
func (hr *HandlerRegistry) GetHandler(action common.WebSocketAction) (RequestHandler, bool) {
	handler, exists := hr.handlers[action]
	return handler, exists
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// GetDataHandler handles system data requests
type GetDataHandler struct{}

func (h *GetDataHandler) Handle(hctx *HandlerContext) error {
	var options common.DataRequestOptions
	_ = cbor.Unmarshal(hctx.Request.Data, &options)

	sysStats := hctx.Agent.gatherStats(options.CacheTimeMs)
	return hctx.SendResponse(sysStats, hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// CheckFingerprintHandler handles authentication challenges
type CheckFingerprintHandler struct{}

func (h *CheckFingerprintHandler) Handle(hctx *HandlerContext) error {
	return hctx.Client.handleAuthChallenge(hctx.Request, hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// GetContainerLogsHandler handles container log requests
type GetContainerLogsHandler struct{}

func (h *GetContainerLogsHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return hctx.SendResponse("", hctx.RequestID)
	}

	var req common.ContainerLogsRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}

	ctx := context.Background()
	logContent, err := hctx.Agent.dockerManager.getLogs(ctx, req.ContainerID)
	if err != nil {
		return err
	}

	return hctx.SendResponse(logContent, hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// GetContainerInfoHandler handles container info requests
type GetContainerInfoHandler struct{}

func (h *GetContainerInfoHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return hctx.SendResponse("", hctx.RequestID)
	}

	var req common.ContainerInfoRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}

	ctx := context.Background()
	info, err := hctx.Agent.dockerManager.getContainerInfo(ctx, req.ContainerID)
	if err != nil {
		return err
	}

	return hctx.SendResponse(string(info), hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// GetSmartDataHandler handles SMART data requests
type GetSmartDataHandler struct{}

func (h *GetSmartDataHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.smartManager == nil {
		// return empty map to indicate no data
		return hctx.SendResponse(map[string]smart.SmartData{}, hctx.RequestID)
	}
	if err := hctx.Agent.smartManager.Refresh(false); err != nil {
		slog.Debug("smart refresh failed", "err", err)
	}
	data := hctx.Agent.smartManager.GetCurrentData()
	return hctx.SendResponse(data, hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// StartContainerHandler handles container start requests
type StartContainerHandler struct{}

func (h *StartContainerHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return errors.New("docker not available")
	}

	var req common.ContainerControlRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}

	ctx := context.Background()
	if err := hctx.Agent.dockerManager.startContainer(ctx, req.ContainerID); err != nil {
		return err
	}

	return hctx.SendResponse("ok", hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// StopContainerHandler handles container stop requests
type StopContainerHandler struct{}

func (h *StopContainerHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return errors.New("docker not available")
	}

	var req common.ContainerControlRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}

	// Default to 10 seconds if timeout not specified
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}

	ctx := context.Background()
	if err := hctx.Agent.dockerManager.stopContainer(ctx, req.ContainerID, timeout); err != nil {
		return err
	}

	return hctx.SendResponse("ok", hctx.RequestID)
}

////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////

// RestartContainerHandler handles container restart requests
type RestartContainerHandler struct{}

func (h *RestartContainerHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return errors.New("docker not available")
	}

	var req common.ContainerControlRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}

	// Default to 10 seconds if timeout not specified
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}

	ctx := context.Background()
	if err := hctx.Agent.dockerManager.restartContainer(ctx, req.ContainerID, timeout); err != nil {
		return err
	}

	return hctx.SendResponse("ok", hctx.RequestID)
}
