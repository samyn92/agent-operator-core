package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samyn92/agent-operator-core/pkg/gateway"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestNewHandler(t *testing.T) {
	config := gateway.Config{
		Mode:         "mcp",
		Port:         8080,
		ToolName:     "test-mcp",
		Command:      "cat",
		RateLimitRPM: 60,
		AuditEnabled: true,
	}

	handler := NewHandler(config, testLogger())

	if handler == nil {
		t.Fatal("expected non-nil handler")
	}
	if handler.config.Mode != "mcp" {
		t.Fatalf("expected mode 'mcp', got %q", handler.config.Mode)
	}
}

func TestRegister(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)

	// Verify POST /message route is registered (doesn't block like SSE)
	// Missing sessionId should give 400, not 404
	req := httptest.NewRequest("POST", "/message", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatal("POST /message route not registered")
	}
	// Should be 400 (missing sessionId), not 404 (route not found)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 from POST /message without sessionId, got %d", w.Code)
	}

	// Verify GET /sse route exists by checking that POST /sse returns 405 (method not allowed)
	// This confirms the route pattern is registered without triggering the blocking SSE handler
	req = httptest.NewRequest("POST", "/sse", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatal("GET /sse route not registered")
	}
}

func TestHandleMessage_MissingSessionId(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"test","id":1}`)
	req := httptest.NewRequest("POST", "/message", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing sessionId, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing sessionId") {
		t.Fatalf("expected 'missing sessionId' error, got %q", w.Body.String())
	}
}

func TestHandleMessage_SessionNotFound(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"test","id":1}`)
	req := httptest.NewRequest("POST", "/message?sessionId=nonexistent", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown session, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "session not found") {
		t.Fatalf("expected 'session not found' error, got %q", w.Body.String())
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	// Manually store a fake session so the session lookup succeeds
	sess := &session{
		id:       "test-session",
		messages: make(chan []byte, 64),
		done:     make(chan struct{}),
	}
	handler.sessions.Store("test-session", sess)

	mux := http.NewServeMux()
	handler.Register(mux)

	body := bytes.NewBufferString(`not valid json`)
	req := httptest.NewRequest("POST", "/message?sessionId=test-session", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Fatalf("expected 'invalid JSON' error, got %q", w.Body.String())
	}
}

func TestHandleMessage_RateLimited(t *testing.T) {
	config := gateway.Config{
		Mode:         "mcp",
		Command:      "cat",
		RateLimitRPM: 2,
	}
	handler := NewHandler(config, testLogger())

	// Store a fake session with a real stdin pipe so writes don't panic
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	sess := &session{
		id:       "test-session",
		stdin:    pw,
		messages: make(chan []byte, 64),
		done:     make(chan struct{}),
	}
	handler.sessions.Store("test-session", sess)

	mux := http.NewServeMux()
	handler.Register(mux)

	// Drain the pipe reader so writes don't block
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := pr.Read(buf); err != nil {
				return
			}
		}
	}()

	// First 2 requests should succeed (accepted)
	for i := 0; i < 2; i++ {
		body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"test","id":1}`)
		req := httptest.NewRequest("POST", "/message?sessionId=test-session", body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d should not be rate limited", i+1)
		}
		if w.Code != http.StatusAccepted {
			t.Fatalf("request %d expected 202, got %d", i+1, w.Code)
		}
	}

	// Third request should be rate limited
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"test","id":1}`)
	req := httptest.NewRequest("POST", "/message?sessionId=test-session", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for rate limited request, got %d", w.Code)
	}
}

func TestHandleSSE_MissingCommand(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "", Port: 8080}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)

	req := httptest.NewRequest("GET", "/sse", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for missing command, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "GATEWAY_COMMAND") {
		t.Fatalf("expected GATEWAY_COMMAND error, got %q", w.Body.String())
	}
}

func TestHandleSSE_InvalidCommand(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "/nonexistent/binary/path", Port: 8080}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)

	req := httptest.NewRequest("GET", "/sse", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for invalid command, got %d", w.Code)
	}
}

// TestSSEEndToEnd tests the full SSE flow: connect, receive endpoint event,
// send a message, and receive the echoed response.
// Uses "cat" as the MCP server subprocess since it echoes stdin to stdout.
func TestSSEEndToEnd(t *testing.T) {
	config := gateway.Config{
		Mode:    "mcp",
		Command: "cat",
		Port:    9999, // Use a unique port for the test
	}
	handler := NewHandler(config, testLogger())

	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /healthz", gateway.HealthHandler())

	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect to SSE endpoint
	resp, err := http.Get(server.URL + "/sse")
	if err != nil {
		t.Fatalf("failed to connect to /sse: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /sse, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Read the first event — should be "endpoint" with the POST URL
	scanner := bufio.NewScanner(resp.Body)
	var endpointURL string

	// Read lines until we get the endpoint event
	deadline := time.After(5 * time.Second)
	eventCh := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				eventCh <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()

	select {
	case data := <-eventCh:
		endpointURL = data
	case <-deadline:
		t.Fatal("timed out waiting for endpoint event")
	}

	if !strings.Contains(endpointURL, "/message?sessionId=") {
		t.Fatalf("expected endpoint URL with sessionId, got %q", endpointURL)
	}

	// The endpoint URL has localhost:9999 (the config port), but we need to
	// use the test server's actual URL. Extract the sessionId and rebuild.
	parts := strings.SplitN(endpointURL, "/message?", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected endpoint URL format: %q", endpointURL)
	}
	messageURL := server.URL + "/message?" + parts[1]

	// Send a JSON-RPC message
	jsonRPC := `{"jsonrpc":"2.0","method":"initialize","id":1}`
	postResp, err := http.Post(messageURL, "application/json", strings.NewReader(jsonRPC))
	if err != nil {
		t.Fatalf("failed to POST message: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(postResp.Body)
		t.Fatalf("expected 202 from /message, got %d: %s", postResp.StatusCode, body)
	}

	// Read the echoed response from the SSE stream.
	// "cat" echoes our input, so we should get back the same JSON.
	responseCh := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				responseCh <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()

	select {
	case data := <-responseCh:
		// Verify the echoed data is valid JSON and matches our input
		if !json.Valid([]byte(data)) {
			t.Fatalf("expected valid JSON response, got %q", data)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if msg["method"] != "initialize" {
			t.Fatalf("expected method 'initialize', got %v", msg["method"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for echoed response from SSE stream")
	}
}

func TestSpawnMCPServer_EmptyCommand(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: ""}
	handler := NewHandler(config, testLogger())

	ctx := context.Background()
	_, err := handler.spawnMCPServer(ctx, "test")
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "GATEWAY_COMMAND") {
		t.Fatalf("expected GATEWAY_COMMAND error, got %q", err.Error())
	}
}

func TestSpawnMCPServer_InvalidCommand(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "/nonexistent/binary"}
	handler := NewHandler(config, testLogger())

	ctx := context.Background()
	_, err := handler.spawnMCPServer(ctx, "test")
	if err == nil {
		t.Fatal("expected error for invalid command")
	}
}

func TestSpawnMCPServer_Success(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	ctx := context.Background()
	sess, err := handler.spawnMCPServer(ctx, "test-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer sess.close()

	if sess.id != "test-session" {
		t.Fatalf("expected session id 'test-session', got %q", sess.id)
	}
	if sess.cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	if sess.cmd.Process == nil {
		t.Fatal("expected non-nil process")
	}
	if sess.stdin == nil {
		t.Fatal("expected non-nil stdin")
	}
}

func TestSessionClose(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	ctx := context.Background()
	sess, err := handler.spawnMCPServer(ctx, "close-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Process should be running
	pid := sess.cmd.Process.Pid
	if pid == 0 {
		t.Fatal("expected non-zero PID")
	}

	// Close should kill the process
	sess.close()

	// Wait a bit for the process to be cleaned up
	time.Sleep(100 * time.Millisecond)

	// Verify the done channel is eventually closed (process exited)
	select {
	case <-sess.done:
		// Good — process exited
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for process to exit after close")
	}
}

func TestReadFromServer_ValidJSON(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	sess, err := handler.spawnMCPServer(context.Background(), "read-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer sess.close()

	// Start reading from server
	go sess.readFromServer(testLogger())

	// Write a valid JSON line to stdin — cat will echo it to stdout
	msg := `{"jsonrpc":"2.0","result":{"capabilities":{}},"id":1}`
	sess.mu.Lock()
	_, err = sess.stdin.Write([]byte(msg + "\n"))
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("failed to write to stdin: %v", err)
	}

	// Read the echoed message from the messages channel
	select {
	case data := <-sess.messages:
		if string(data) != msg {
			t.Fatalf("expected %q, got %q", msg, string(data))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message from server")
	}
}

func TestReadFromServer_SkipsNonJSON(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	sess, err := handler.spawnMCPServer(context.Background(), "skip-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer sess.close()

	go sess.readFromServer(testLogger())

	// Write a non-JSON line followed by a valid JSON line
	sess.mu.Lock()
	sess.stdin.Write([]byte("this is not json\n"))
	sess.stdin.Write([]byte(`{"jsonrpc":"2.0","id":2}` + "\n"))
	sess.mu.Unlock()

	// Should only receive the valid JSON message
	select {
	case data := <-sess.messages:
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("expected valid JSON, got error: %v", err)
		}
		if msg["id"].(float64) != 2 {
			t.Fatalf("expected id 2, got %v", msg["id"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for valid JSON message")
	}
}

func TestReadFromServer_EmptyLines(t *testing.T) {
	config := gateway.Config{Mode: "mcp", Command: "cat"}
	handler := NewHandler(config, testLogger())

	sess, err := handler.spawnMCPServer(context.Background(), "empty-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer sess.close()

	go sess.readFromServer(testLogger())

	// Write empty lines and then a valid message
	sess.mu.Lock()
	sess.stdin.Write([]byte("\n\n\n"))
	sess.stdin.Write([]byte(`{"ok":true}` + "\n"))
	sess.mu.Unlock()

	select {
	case data := <-sess.messages:
		if string(data) != `{"ok":true}` {
			t.Fatalf("expected {\"ok\":true}, got %q", string(data))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}
