// Package mcp implements the MCP mode for the capability gateway.
// This mode bridges an MCP server's stdio transport to HTTP/SSE, allowing
// agents to connect to stdio-based MCP servers over the network.
//
// Protocol flow:
//  1. Client connects to GET /sse — receives an SSE stream
//  2. First SSE event is "endpoint" containing the POST URL for messages
//  3. Client sends JSON-RPC messages via POST /message?sessionId=xxx
//  4. Gateway writes the message to the MCP server's stdin
//  5. Gateway reads JSON-RPC responses from MCP server's stdout
//  6. Responses are sent back over the SSE stream
//
// This implements the MCP SSE transport spec:
// https://modelcontextprotocol.io/docs/concepts/transports#http-with-sse
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/shlex"
	"github.com/samyn92/agent-operator-core/pkg/cmdvalidator"
	"github.com/samyn92/agent-operator-core/pkg/gateway"
)

// Handler implements the MCP gateway mode.
type Handler struct {
	config        gateway.Config
	logger        *slog.Logger
	limiter       *gateway.RateLimiter
	audit         *gateway.AuditLogger
	configWatcher *gateway.ConfigWatcher // nil if no watcher configured

	// sessions tracks active SSE sessions
	sessions sync.Map // sessionID -> *session
	nextID   atomic.Uint64
}

// session represents an active MCP SSE connection.
type session struct {
	id       string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	messages chan []byte // outbound SSE messages to client
	done     chan struct{}
	mu       sync.Mutex // protects stdin writes
}

// NewHandler creates an MCP mode handler.
func NewHandler(config gateway.Config, logger *slog.Logger) *Handler {
	return &Handler{
		config:  config,
		logger:  logger,
		limiter: gateway.NewRateLimiter(config.RateLimitRPM),
		audit:   gateway.NewAuditLogger(logger, config.AuditEnabled),
	}
}

// SetConfigWatcher attaches a ConfigWatcher for dynamic config reloading.
// When set, the handler reads MCP deny rules from the watcher, enabling
// hot-reload without pod restarts.
func (h *Handler) SetConfigWatcher(cw *gateway.ConfigWatcher) {
	h.configWatcher = cw
}

// mcpDenyRules returns the current MCP deny rules from the ConfigWatcher.
// Returns nil if no watcher is configured or no mcp-deny-rules file exists.
func (h *Handler) mcpDenyRules() []cmdvalidator.MCPDenyRule {
	if h.configWatcher != nil {
		return h.configWatcher.MCPDenyRules()
	}
	return nil
}

// Register adds MCP mode routes to the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /sse", h.handleSSE)
	mux.HandleFunc("POST /message", h.handleMessage)
}

// handleSSE establishes an SSE connection, spawns the MCP server subprocess,
// and streams responses back to the client.
func (h *Handler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Generate session ID
	id := fmt.Sprintf("session-%d", h.nextID.Add(1))

	h.logger.Info("new MCP SSE session", "session", id)

	// Spawn the MCP server subprocess
	sess, err := h.spawnMCPServer(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to spawn MCP server", "error", err)
		http.Error(w, "failed to start MCP server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.sessions.Store(id, sess)
	defer func() {
		h.sessions.Delete(id)
		sess.close()
	}()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send the endpoint event — tells the client where to POST messages
	// The endpoint URL includes the session ID for routing
	port := h.config.Port
	endpointURL := fmt.Sprintf("http://localhost:%d/message?sessionId=%s", port, id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
	flusher.Flush()

	h.logger.Info("sent endpoint event", "session", id, "url", endpointURL)

	// Start reading from MCP server stdout in background
	go sess.readFromServer(h.logger)

	// Stream messages from the MCP server to the SSE client
	for {
		select {
		case msg, ok := <-sess.messages:
			if !ok {
				return // Channel closed, server exited
			}

			if h.config.AuditEnabled {
				h.audit.LogResponse("mcp response",
					"session", id,
					"size", len(msg),
				)
			}

			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()

		case <-sess.done:
			return // Server process exited

		case <-r.Context().Done():
			return // Client disconnected
		}
	}
}

// jsonRPCRequest represents a JSON-RPC 2.0 request (partial parse).
// We only parse the fields we need for deny-rule enforcement.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      json.RawMessage `json:"id"` // can be string, number, or null
	Params  json.RawMessage `json:"params,omitempty"`
}

// toolsCallParams represents the params of a tools/call JSON-RPC request.
type toolsCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// jsonRPCError represents a JSON-RPC 2.0 error response.
type jsonRPCError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// handleMessage receives a JSON-RPC message from the client and writes it
// to the MCP server's stdin. For tools/call messages, it first checks the
// request against MCP deny rules and blocks denied tool invocations.
func (h *Handler) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId parameter", http.StatusBadRequest)
		return
	}

	// Rate limiting
	if h.config.RateLimitRPM > 0 {
		if !h.limiter.Allow(sessionID) {
			h.logger.Warn("rate limit exceeded", "session", sessionID)
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}

	// Look up session
	val, ok := h.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	sess := val.(*session)

	// Read the JSON-RPC message body
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate it's valid JSON
	if !json.Valid(body) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Check MCP deny rules for tools/call requests.
	// This is the MCP equivalent of CLI deny-pattern enforcement — it ensures
	// deny rules cannot be bypassed by OpenCode's runtime permission system.
	if rules := h.mcpDenyRules(); len(rules) > 0 {
		if denied := h.checkToolsCallDeny(body, rules, sess, sessionID); denied {
			// JSON-RPC error was injected onto the SSE stream; don't forward to server.
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	if h.config.AuditEnabled {
		h.audit.LogRequest("mcp request",
			"session", sessionID,
			"size", len(body),
		)
	}

	h.logger.Debug("forwarding message to MCP server",
		"session", sessionID,
		"size", len(body),
	)

	// Write the message to the MCP server's stdin.
	// MCP stdio transport uses newline-delimited JSON.
	sess.mu.Lock()
	_, err = fmt.Fprintf(sess.stdin, "%s\n", body)
	sess.mu.Unlock()

	if err != nil {
		h.logger.Error("failed to write to MCP server stdin", "error", err, "session", sessionID)
		http.Error(w, "failed to send message to MCP server", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// checkToolsCallDeny checks if a JSON-RPC message is a tools/call request that
// matches any MCP deny rule. If denied, it injects a JSON-RPC error response
// directly onto the session's SSE message channel (preserving the MCP protocol
// contract) and returns true. Returns false if the message should be forwarded.
//
// This is the MCP equivalent of the CLI handler's deny-pattern enforcement step.
// It ensures deny rules are enforced at the gateway level as a security backstop,
// regardless of what OpenCode's permission system decided.
func (h *Handler) checkToolsCallDeny(body []byte, rules []cmdvalidator.MCPDenyRule, sess *session, sessionID string) bool {
	// Parse only the method field first — most messages are not tools/call
	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return false // can't parse — let it through (already validated as JSON)
	}

	if req.Method != "tools/call" {
		return false
	}

	// Parse the params to get tool name and arguments
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		h.logger.Warn("failed to parse tools/call params",
			"session", sessionID,
			"error", err,
		)
		return false // can't parse params — let it through
	}

	// Check against deny rules
	matched := cmdvalidator.CheckMCPDenyRules(params.Name, params.Arguments, rules)
	if matched == "" {
		return false // allowed
	}

	h.logger.Warn("MCP tool call blocked by deny rule",
		"session", sessionID,
		"tool", params.Name,
		"matched_rule", matched,
	)

	if h.config.AuditEnabled {
		h.audit.LogRequest("mcp tool call denied",
			"session", sessionID,
			"tool", params.Name,
			"matched_rule", matched,
		)
	}

	// Inject a JSON-RPC error response onto the SSE stream.
	// This preserves the MCP protocol contract: the client sent a request with
	// an ID and expects a response with that same ID.
	errResp := jsonRPCError{
		JSONRPC: "2.0",
		ID:      req.ID,
	}
	errResp.Error.Code = -32001 // Server error (MCP spec uses -32xxx for server errors)
	errResp.Error.Message = fmt.Sprintf("tool call denied by security policy: %s", matched)

	errBody, err := json.Marshal(errResp)
	if err != nil {
		h.logger.Error("failed to marshal deny error response", "error", err)
		return true // still denied, just can't send the error response
	}

	// Push the error response onto the SSE channel
	select {
	case sess.messages <- errBody:
		// Successfully injected
	default:
		h.logger.Warn("SSE message channel full, dropping deny error response",
			"session", sessionID,
		)
	}

	return true
}

// spawnMCPServer starts the MCP server subprocess.
func (h *Handler) spawnMCPServer(ctx context.Context, id string) (*session, error) {
	command := h.config.Command
	if command == "" {
		return nil, fmt.Errorf("GATEWAY_COMMAND not set")
	}

	tokens, err := shlex.Split(command)
	if err != nil {
		return nil, fmt.Errorf("failed to parse command %q: %w", command, err)
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	cmd := exec.CommandContext(ctx, tokens[0], tokens[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for logging (don't bridge it to SSE)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to start MCP server: %w", err)
	}

	sess := &session{
		id:       id,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewScanner(stdout),
		messages: make(chan []byte, 64),
		done:     make(chan struct{}),
	}

	// Log stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				h.logger.Info("mcp server stderr", "session", id, "line", line)
			}
		}
	}()

	// Wait for process exit in background
	go func() {
		err := cmd.Wait()
		if err != nil {
			h.logger.Info("MCP server process exited", "session", id, "error", err)
		} else {
			h.logger.Info("MCP server process exited cleanly", "session", id)
		}
		close(sess.done)
	}()

	h.logger.Info("spawned MCP server", "session", id, "command", command, "pid", cmd.Process.Pid)

	return sess, nil
}

// readFromServer reads newline-delimited JSON from the MCP server's stdout
// and pushes each message onto the session's message channel.
func (s *session) readFromServer(logger *slog.Logger) {
	// MCP stdio uses newline-delimited JSON-RPC
	// Each line from stdout is a complete JSON-RPC message
	for s.stdout.Scan() {
		line := strings.TrimSpace(s.stdout.Text())
		if line == "" {
			continue
		}

		// Validate JSON before sending
		if !json.Valid([]byte(line)) {
			logger.Warn("non-JSON line from MCP server, skipping",
				"session", s.id,
				"line", line,
			)
			continue
		}

		select {
		case s.messages <- []byte(line):
		case <-s.done:
			return
		}
	}

	if err := s.stdout.Err(); err != nil {
		logger.Error("error reading MCP server stdout", "session", s.id, "error", err)
	}

	// Close the messages channel to signal the SSE handler
	close(s.messages)
}

// close shuts down the session, killing the MCP server process.
func (s *session) close() {
	s.stdin.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
}
