// Package cli implements the CLI mode for the capability gateway.
// This mode provides an HTTP REST endpoint (/exec) that validates and executes
// commands, enforcing security policies (command prefix, deny patterns,
// rate limiting, audit logging).
//
// Commands are executed via exec.CommandContext (no shell), so shell metacharacters
// have no special meaning — security is enforced through command prefix requirements
// and deny-pattern matching on the full command string.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/google/shlex"
	"github.com/samyn92/agent-operator-core/pkg/cmdvalidator"
	"github.com/samyn92/agent-operator-core/pkg/gateway"
)

// ExecRequest is the request body for POST /exec
type ExecRequest struct {
	Command string `json:"command"`
	// Timeout in seconds (optional, default 60, max 300)
	Timeout int `json:"timeout,omitempty"`
	// AgentID for per-agent rate limiting (optional)
	AgentID string `json:"agent_id,omitempty"`
	// Workdir overrides the working directory (optional, must be within workspace)
	Workdir string `json:"workdir,omitempty"`
}

// ExecResponse is the response body for POST /exec
type ExecResponse struct {
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error,omitempty"`
}

// Handler implements the CLI gateway mode.
type Handler struct {
	config        gateway.Config
	logger        *slog.Logger
	limiter       *gateway.RateLimiter
	audit         *gateway.AuditLogger
	configWatcher *gateway.ConfigWatcher // nil if no watcher configured
}

// NewHandler creates a CLI mode handler.
func NewHandler(config gateway.Config, logger *slog.Logger) *Handler {
	return &Handler{
		config:  config,
		logger:  logger,
		limiter: gateway.NewRateLimiter(config.RateLimitRPM),
		audit:   gateway.NewAuditLogger(logger, config.AuditEnabled),
	}
}

// SetConfigWatcher attaches a ConfigWatcher for dynamic config reloading.
// When set, the handler reads command-prefix from the watcher instead of the
// static config, enabling hot-reload without pod restarts.
func (h *Handler) SetConfigWatcher(cw *gateway.ConfigWatcher) {
	h.configWatcher = cw
}

// commandPrefix returns the current command prefix. If a ConfigWatcher is
// attached, the value is read dynamically (reflecting ConfigMap updates).
// Otherwise, falls back to the static config loaded at startup.
func (h *Handler) commandPrefix() string {
	if h.configWatcher != nil {
		return h.configWatcher.CommandPrefix()
	}
	return h.config.CommandPrefix
}

// denyPatterns returns the current deny patterns from the ConfigWatcher.
// Returns nil if no watcher is configured or no deny-patterns file exists.
func (h *Handler) denyPatterns() []string {
	if h.configWatcher != nil {
		return h.configWatcher.DenyPatterns()
	}
	return nil
}

// Register adds CLI mode routes to the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /exec", h.handleExec)
}

func (h *Handler) handleExec(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Parse request
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Set default timeout
	timeout := 60
	if req.Timeout > 0 && req.Timeout <= 300 {
		timeout = req.Timeout
	}

	// Rate limiting
	if h.config.RateLimitRPM > 0 {
		agentKey := "global"
		if h.config.RateLimitPerAgent && req.AgentID != "" {
			agentKey = req.AgentID
		}

		if !h.limiter.Allow(agentKey) {
			h.logger.Warn("rate limit exceeded",
				"agent", agentKey,
				"command", req.Command,
			)
			h.sendError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}

	command := strings.TrimSpace(req.Command)

	// Step 1: Check against deny patterns (security backstop).
	// This catches commands that match deny rules even if OpenCode's permission
	// system was bypassed by a runtime "Always Allow" approval. The deny patterns
	// are loaded from the ConfigMap and hot-reloaded by ConfigWatcher.
	if patterns := h.denyPatterns(); len(patterns) > 0 {
		if matched := cmdvalidator.CheckDenyPatterns(command, patterns); matched != "" {
			h.logger.Warn("command blocked by deny pattern",
				"command", command,
				"matched_pattern", matched,
			)
			h.sendError(w, http.StatusForbidden, "command denied by security policy: matches deny pattern "+matched)
			return
		}
	}

	// Step 2: Handle command prefix enforcement
	// Read command prefix dynamically — may have been updated by ConfigWatcher
	cmdPrefix := h.commandPrefix()
	if cmdPrefix != "" {
		prefix := cmdPrefix
		if !strings.HasSuffix(prefix, " ") {
			prefix += " "
		}

		if strings.HasPrefix(command, prefix) {
			// Valid prefix — continue
		} else if command == strings.TrimSpace(cmdPrefix) {
			// Just the command itself (e.g., "kubectl" alone)
		} else {
			h.logger.Warn("command missing required prefix",
				"command", command,
				"required_prefix", cmdPrefix,
			)
			h.sendError(w, http.StatusForbidden, fmt.Sprintf("command must start with %q", cmdPrefix))
			return
		}
	}

	// Audit logging
	if h.config.AuditLogCommands {
		h.audit.LogRequest("command accepted",
			"command", command,
			"agent", req.AgentID,
		)
	}

	// Execute command
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	fullTokens, err := shlex.Split(command)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to parse command: "+err.Error())
		return
	}

	if len(fullTokens) == 0 {
		h.sendError(w, http.StatusBadRequest, "empty command")
		return
	}

	cmd := exec.CommandContext(ctx, fullTokens[0], fullTokens[1:]...)

	// Set working directory
	if h.config.WorkspacePath != "" {
		cmd.Dir = h.config.WorkspacePath
	}

	// Allow request to override workdir (must be within workspace for security)
	if req.Workdir != "" {
		if h.config.WorkspacePath != "" && strings.HasPrefix(req.Workdir, h.config.WorkspacePath) {
			cmd.Dir = req.Workdir
		} else if h.config.WorkspacePath == "" {
			cmd.Dir = req.Workdir
		}
	}

	// Capture stdout and stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to create stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to create stderr pipe: "+err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to start command: "+err.Error())
		return
	}

	stdoutBytes, _ := io.ReadAll(stdout)
	stderrBytes, _ := io.ReadAll(stderr)
	err = cmd.Wait()

	response := ExecResponse{
		Success:  err == nil,
		Stdout:   string(stdoutBytes),
		Stderr:   string(stderrBytes),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			response.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			response.Error = "command timed out"
			response.ExitCode = -1
		} else {
			response.Error = err.Error()
			response.ExitCode = -1
		}
	}

	duration := time.Since(startTime)

	if h.config.AuditLogOutput {
		h.audit.LogResponse("command output",
			"command", req.Command,
			"exit_code", response.ExitCode,
			"stdout_len", len(response.Stdout),
			"stderr_len", len(response.Stderr),
			"duration_ms", duration.Milliseconds(),
			"agent", req.AgentID,
		)
	}

	h.sendJSON(w, http.StatusOK, response)
}

func (h *Handler) sendError(w http.ResponseWriter, status int, message string) {
	h.sendJSON(w, status, ExecResponse{
		Success: false,
		Error:   message,
	})
}

func (h *Handler) sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
