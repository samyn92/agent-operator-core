package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/samyn92/agent-operator-core/pkg/gateway"
)

// newTestHandler creates a CLI handler for testing with the given config overrides.
func newTestHandler(config gateway.Config) *Handler {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return NewHandler(config, logger)
}

func TestHandleExec(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	tests := []struct {
		name           string
		request        ExecRequest
		expectedStatus int
		expectSuccess  bool
		expectError    string
	}{
		{
			name: "allowed command",
			request: ExecRequest{
				Command: "echo hello",
			},
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name: "command with semicolons allowed (no shell injection via exec)",
			request: ExecRequest{
				Command: "echo hello; world",
			},
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name: "empty command",
			request: ExecRequest{
				Command: "",
			},
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
			expectError:    "empty command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
			w := httptest.NewRecorder()

			handler.handleExec(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var resp ExecResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			if resp.Success != tt.expectSuccess {
				t.Errorf("expected success=%v, got %v", tt.expectSuccess, resp.Success)
			}

			if tt.expectError != "" && resp.Error == "" {
				t.Errorf("expected error containing %q, got empty", tt.expectError)
			}
		})
	}
}

func TestHandleExecWithCommandPrefix(t *testing.T) {
	// Use "echo" as prefix since it's available on all platforms
	handler := newTestHandler(gateway.Config{
		ToolName:      "echo-tool",
		CommandPrefix: "echo",
	})

	tests := []struct {
		name           string
		request        ExecRequest
		expectedStatus int
		expectSuccess  bool
		expectError    string
	}{
		{
			name: "command with prefix - allowed",
			request: ExecRequest{
				Command: "echo hello world",
			},
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name: "command with prefix and flags - allowed",
			request: ExecRequest{
				Command: "echo -n test",
			},
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name: "command without prefix - rejected",
			request: ExecRequest{
				Command: "get pods",
			},
			expectedStatus: http.StatusForbidden,
			expectSuccess:  false,
			expectError:    "must start with",
		},
		{
			name: "wrong prefix - rejected",
			request: ExecRequest{
				Command: "helm get pods",
			},
			expectedStatus: http.StatusForbidden,
			expectSuccess:  false,
			expectError:    "must start with",
		},
		{
			name: "just the prefix command itself - allowed",
			request: ExecRequest{
				Command: "echo",
			},
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
			w := httptest.NewRecorder()

			handler.handleExec(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var resp ExecResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			if resp.Success != tt.expectSuccess {
				t.Errorf("expected success=%v, got %v (error: %s)", tt.expectSuccess, resp.Success, resp.Error)
			}

			if tt.expectError != "" && resp.Error == "" {
				t.Errorf("expected error containing %q, got empty", tt.expectError)
			}
		})
	}
}

func TestHandleExecRateLimiting(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName:     "test-tool",
		RateLimitRPM: 3,
	})

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		body, _ := json.Marshal(ExecRequest{Command: "echo hello"})
		req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
		w := httptest.NewRecorder()

		handler.handleExec(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected status 200, got %d", i+1, w.Code)
		}
	}

	// 4th request should be rate limited
	body, _ := json.Marshal(ExecRequest{Command: "echo hello"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: expected status 429, got %d", w.Code)
	}
}

func TestHandleExecPerAgentRateLimiting(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName:          "test-tool",
		RateLimitRPM:      2,
		RateLimitPerAgent: true,
	})

	// Use up agent-a's budget
	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(ExecRequest{Command: "echo hello", AgentID: "agent-a"})
		req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleExec(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("agent-a request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// agent-a should be rate limited
	body, _ := json.Marshal(ExecRequest{Command: "echo hello", AgentID: "agent-a"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.handleExec(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("agent-a 3rd request: expected 429, got %d", w.Code)
	}

	// agent-b should still work
	body, _ = json.Marshal(ExecRequest{Command: "echo hello", AgentID: "agent-b"})
	req = httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w = httptest.NewRecorder()
	handler.handleExec(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("agent-b request: expected 200, got %d", w.Code)
	}
}

func TestHandleExecInvalidBody(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	req := httptest.NewRequest("POST", "/exec", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid body, got %d", w.Code)
	}

	var resp ExecResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Success {
		t.Error("expected success=false for invalid body")
	}
}

func TestHandleExecTimeout(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	// Custom timeout within bounds
	body, _ := json.Marshal(ExecRequest{Command: "echo hello", Timeout: 10})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestHandleExecCapturesStdout(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	body, _ := json.Marshal(ExecRequest{Command: "echo hello"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	var resp ExecResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Stdout != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got %q", resp.Stdout)
	}
	if !resp.Success {
		t.Errorf("expected success=true, got false (error: %s)", resp.Error)
	}
	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}
}

func TestHandleExecCapturesExitCode(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	body, _ := json.Marshal(ExecRequest{Command: "false"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	var resp ExecResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Success {
		t.Error("expected success=false for 'false' command")
	}
	if resp.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", resp.ExitCode)
	}
}

func TestRegister(t *testing.T) {
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	mux := http.NewServeMux()
	handler.Register(mux)

	// Test that POST /exec is registered by sending a valid request
	body, _ := json.Marshal(ExecRequest{Command: "echo registered"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 via mux, got %d", w.Code)
	}

	var resp ExecResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success=true via mux (error: %s)", resp.Error)
	}
}

// =============================================================================
// DENY PATTERN ENFORCEMENT TESTS
// =============================================================================

func TestHandleExecDenyPatterns(t *testing.T) {
	// Set up a config watcher with deny patterns
	dir := t.TempDir()
	denyPatterns := "git -C * push * main\ngit -C * push * master\ngit -C * push --force *"
	if err := os.WriteFile(dir+"/deny-patterns", []byte(denyPatterns), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/command-prefix", []byte("git -C /workspace "), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := gateway.NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	handler := newTestHandler(gateway.Config{
		ToolName:      "git-tool",
		CommandPrefix: "git -C /workspace",
	})
	handler.SetConfigWatcher(cw)

	tests := []struct {
		name           string
		command        string
		expectedStatus int
		expectError    string // expected error substring (for denied commands)
	}{
		{
			name:           "push to main blocked",
			command:        "git -C /workspace push origin main",
			expectedStatus: http.StatusForbidden,
			expectError:    "command denied by security policy",
		},
		{
			name:           "push to master blocked",
			command:        "git -C /workspace push origin master",
			expectedStatus: http.StatusForbidden,
			expectError:    "command denied by security policy",
		},
		{
			name:           "force push blocked",
			command:        "git -C /workspace push --force origin feature",
			expectedStatus: http.StatusForbidden,
			expectError:    "command denied by security policy",
		},
		{
			name:           "push to feature branch allowed through gateway",
			command:        "git -C /workspace push origin feature-branch",
			expectedStatus: http.StatusOK, // Gateway allows it (command may fail at exec, that's OK)
		},
		{
			name:           "non-push command allowed through gateway",
			command:        "git -C /workspace add test.txt",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "status command allowed through gateway",
			command:        "git -C /workspace status",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "commit command allowed through gateway",
			command:        "git -C /workspace commit -m test",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(ExecRequest{Command: tt.command})
			req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
			w := httptest.NewRecorder()

			handler.handleExec(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var resp ExecResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}

			if tt.expectError != "" {
				if resp.Error == "" || !strings.Contains(resp.Error, tt.expectError) {
					t.Errorf("expected error containing %q, got %q", tt.expectError, resp.Error)
				}
			}
		})
	}
}

func TestHandleExecNoDenyPatterns(t *testing.T) {
	// Without deny patterns, all commands should pass (if other checks pass)
	handler := newTestHandler(gateway.Config{
		ToolName: "test-tool",
	})

	body, _ := json.Marshal(ExecRequest{Command: "echo hello"})
	req := httptest.NewRequest("POST", "/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.handleExec(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 without deny patterns, got %d", w.Code)
	}
}
