// mcp-exec-server — MCP stdio server that executes CLI commands.
//
// This is a generic, reusable MCP server that exposes a single "exec" tool
// for running CLI commands inside a container. It follows the same architecture
// as tool-bridge: the capability-gateway spawns it as a subprocess and bridges
// its stdio to SSE/HTTP for OpenCode agent connectivity.
//
// Architecture:
//
//	capability-gateway (Go, :8080)  --stdio-->  mcp-exec-server (this binary)
//	     |                                            |
//	     +-- SSE transport                            +-- exec.CommandContext
//	     +-- MCP deny rule enforcement                +-- stdin/stdout JSON-RPC
//	     +-- Rate limiting, audit logging             +-- Timeout, workdir support
//
// The gateway handles ALL security enforcement (deny rules, rate limiting).
// This server handles tool registration and command execution.
//
// Environment variables:
//
//	TOOL_NAME       - Name of the exec tool (default: "exec")
//	TOOL_DESCRIPTION - Description shown to the LLM (default: generic)
//	COMMAND_PREFIX  - Required prefix for all commands (e.g., "kubectl")
//	WORKSPACE_PATH  - Working directory for command execution (default: /data/workspace)
//	SERVER_NAME     - MCP server name (default: "mcp-exec-server")
//	SERVER_VERSION  - MCP server version (default: "0.1.0")
//	MAX_TIMEOUT     - Maximum command timeout in seconds (default: 300)
//	LOG_LEVEL       - debug, info, warn, error (default: "info")
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/shlex"
)

// =============================================================================
// JSON-RPC 2.0 types
// =============================================================================

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// =============================================================================
// MCP types
// =============================================================================

type mcpToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolsCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// =============================================================================
// Configuration
// =============================================================================

type config struct {
	toolName        string
	toolDescription string
	commandPrefix   string
	workspacePath   string
	serverName      string
	serverVersion   string
	maxTimeout      int
	logLevel        slog.Level
}

func loadConfig() config {
	c := config{
		toolName:      envOr("TOOL_NAME", "exec"),
		commandPrefix: os.Getenv("COMMAND_PREFIX"),
		workspacePath: envOr("WORKSPACE_PATH", "/data/workspace"),
		serverName:    envOr("SERVER_NAME", "mcp-exec-server"),
		serverVersion: envOr("SERVER_VERSION", "0.1.0"),
		maxTimeout:    envIntOr("MAX_TIMEOUT", 300),
	}

	// Build tool description from environment or generate a sensible default
	c.toolDescription = os.Getenv("TOOL_DESCRIPTION")
	if c.toolDescription == "" {
		if c.commandPrefix != "" {
			c.toolDescription = fmt.Sprintf(
				"Execute %s commands. All commands must start with %q.",
				c.commandPrefix, c.commandPrefix,
			)
		} else {
			c.toolDescription = "Execute shell commands and return stdout/stderr."
		}
	}

	switch strings.ToLower(envOr("LOG_LEVEL", "info")) {
	case "debug":
		c.logLevel = slog.LevelDebug
	case "warn":
		c.logLevel = slog.LevelWarn
	case "error":
		c.logLevel = slog.LevelError
	default:
		c.logLevel = slog.LevelInfo
	}

	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// =============================================================================
// MCP protocol handlers
// =============================================================================

func handleInitialize(req jsonRPCRequest, cfg config) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]interface{}{
				"name":    cfg.serverName,
				"version": cfg.serverVersion,
			},
		},
	}
}

func handleToolsList(req jsonRPCRequest, cfg config) jsonRPCResponse {
	// Build the input schema for the exec tool
	properties := map[string]interface{}{
		"command": map[string]interface{}{
			"type":        "string",
			"description": buildCommandDescription(cfg),
		},
		"timeout": map[string]interface{}{
			"type":        "integer",
			"description": fmt.Sprintf("Command timeout in seconds (default: 60, max: %d)", cfg.maxTimeout),
		},
		"workdir": map[string]interface{}{
			"type":        "string",
			"description": "Working directory for the command (must be within workspace)",
		},
	}

	tool := mcpToolDef{
		Name:        cfg.toolName,
		Description: cfg.toolDescription,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": properties,
			"required":   []string{"command"},
		},
	}

	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"tools": []mcpToolDef{tool}},
	}
}

func buildCommandDescription(cfg config) string {
	if cfg.commandPrefix != "" {
		return fmt.Sprintf(
			"The full command to execute. Must start with %q. Example: %q",
			cfg.commandPrefix, cfg.commandPrefix+" --help",
		)
	}
	return "The full command to execute."
}

func handleToolsCall(req jsonRPCRequest, cfg config, logger *slog.Logger) jsonRPCResponse {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, -32602, "Invalid params: "+err.Error())
	}

	if params.Name != cfg.toolName {
		return errorResponse(req.ID, -32602, fmt.Sprintf("Unknown tool: %s", params.Name))
	}

	// Extract arguments
	commandRaw, ok := params.Arguments["command"]
	if !ok {
		return errorResponse(req.ID, -32602, "Missing required argument: command")
	}
	command, ok := commandRaw.(string)
	if !ok {
		return errorResponse(req.ID, -32602, "Argument 'command' must be a string")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return errorResponse(req.ID, -32602, "Argument 'command' must not be empty")
	}

	// Enforce command prefix (defense-in-depth — gateway also checks deny rules)
	if cfg.commandPrefix != "" {
		prefix := cfg.commandPrefix
		if !strings.HasSuffix(prefix, " ") {
			prefix += " "
		}
		if !strings.HasPrefix(command, prefix) && command != strings.TrimSpace(cfg.commandPrefix) {
			return toolResult(req.ID, true, fmt.Sprintf(
				"Error: command must start with %q", cfg.commandPrefix,
			))
		}
	}

	// Parse timeout
	timeout := 60
	if t, ok := params.Arguments["timeout"]; ok {
		if tf, ok := t.(float64); ok && int(tf) > 0 && int(tf) <= cfg.maxTimeout {
			timeout = int(tf)
		}
	}

	// Parse workdir
	workdir := cfg.workspacePath
	if wd, ok := params.Arguments["workdir"]; ok {
		if wds, ok := wd.(string); ok && wds != "" {
			// Security: workdir must be within workspace
			clean := filepath.Clean(wds)
			if cfg.workspacePath != "" && strings.HasPrefix(clean, cfg.workspacePath) {
				workdir = clean
			}
		}
	}

	logger.Info("executing command",
		"command", command,
		"timeout", timeout,
		"workdir", workdir,
	)

	// Execute the command
	result := executeCommand(command, workdir, timeout)

	// Format output for the LLM
	var output strings.Builder
	if result.Stdout != "" {
		output.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("STDERR:\n")
		output.WriteString(result.Stderr)
	}
	if result.Error != "" {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("ERROR: ")
		output.WriteString(result.Error)
	}
	if !result.Success {
		exitInfo := fmt.Sprintf("\n[exit code: %d]", result.ExitCode)
		output.WriteString(exitInfo)
	}

	text := output.String()
	if text == "" {
		text = "(no output)"
	}

	logger.Info("command completed",
		"command", command,
		"exit_code", result.ExitCode,
		"success", result.Success,
	)

	return toolResult(req.ID, !result.Success, text)
}

// =============================================================================
// Command execution
// =============================================================================

type execResult struct {
	Success  bool
	ExitCode int
	Stdout   string
	Stderr   string
	Error    string
}

func executeCommand(command, workdir string, timeout int) execResult {
	tokens, err := shlex.Split(command)
	if err != nil {
		return execResult{Error: "failed to parse command: " + err.Error(), ExitCode: -1}
	}
	if len(tokens) == 0 {
		return execResult{Error: "empty command", ExitCode: -1}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, tokens[0], tokens[1:]...)
	if workdir != "" {
		cmd.Dir = workdir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return execResult{Error: "failed to create stdout pipe: " + err.Error(), ExitCode: -1}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return execResult{Error: "failed to create stderr pipe: " + err.Error(), ExitCode: -1}
	}

	if err := cmd.Start(); err != nil {
		return execResult{Error: "failed to start command: " + err.Error(), ExitCode: -1}
	}

	stdoutBytes, _ := io.ReadAll(stdout)
	stderrBytes, _ := io.ReadAll(stderr)
	err = cmd.Wait()

	result := execResult{
		Success:  err == nil,
		Stdout:   string(stdoutBytes),
		Stderr:   string(stderrBytes),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			result.Error = "command timed out"
			result.ExitCode = -1
		} else {
			result.Error = err.Error()
			result.ExitCode = -1
		}
	}

	return result
}

// =============================================================================
// Response helpers
// =============================================================================

func toolResult(id json.RawMessage, isError bool, text string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: map[string]interface{}{
			"content": []mcpContent{{Type: "text", Text: text}},
			"isError": isError,
		},
	}
}

func errorResponse(id json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
}

// =============================================================================
// MCP stdio transport
// =============================================================================

func processRequest(req jsonRPCRequest, cfg config, logger *slog.Logger) *jsonRPCResponse {
	logger.Debug("received request", "method", req.Method)

	// Notifications — no response expected
	if req.Method == "notifications/initialized" {
		logger.Info("client initialized")
		return nil
	}
	if strings.HasPrefix(req.Method, "notifications/") && req.ID == nil {
		return nil
	}

	// Requests
	switch req.Method {
	case "initialize":
		resp := handleInitialize(req, cfg)
		return &resp

	case "tools/list":
		resp := handleToolsList(req, cfg)
		return &resp

	case "tools/call":
		resp := handleToolsCall(req, cfg, logger)
		return &resp

	case "ping":
		resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
		return &resp

	default:
		resp := errorResponse(req.ID, -32601, "Method not found: "+req.Method)
		return &resp
	}
}

func sendResponse(w io.Writer, resp jsonRPCResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// =============================================================================
// Main
// =============================================================================

func main() {
	cfg := loadConfig()

	// Logger writes to stderr — stdout is reserved for MCP JSON-RPC
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: cfg.logLevel,
	}))

	logger.Info("starting mcp-exec-server",
		"server_name", cfg.serverName,
		"server_version", cfg.serverVersion,
		"tool_name", cfg.toolName,
		"command_prefix", cfg.commandPrefix,
		"workspace_path", cfg.workspacePath,
		"max_timeout", cfg.maxTimeout,
	)

	// Handle signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Read JSON-RPC messages from stdin, line by line
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024) // 4MB buffer

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var req jsonRPCRequest
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				resp := errorResponse(nil, -32700, "Parse error")
				if err := sendResponse(os.Stdout, resp); err != nil {
					logger.Error("failed to send response", "error", err)
				}
				continue
			}

			resp := processRequest(req, cfg, logger)
			if resp != nil {
				if err := sendResponse(os.Stdout, *resp); err != nil {
					logger.Error("failed to send response", "error", err)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			logger.Error("stdin read error", "error", err)
		}
		logger.Info("stdin closed — shutting down")
	}()

	// Wait for either stdin close or signal
	select {
	case <-done:
	case <-ctx.Done():
		logger.Info("received shutdown signal")
	}
}
