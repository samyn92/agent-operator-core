package gateway

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// RATE LIMITER TESTS
// =============================================================================

func TestRateLimiter_DisabledWhenZero(t *testing.T) {
	rl := NewRateLimiter(0)
	for i := 0; i < 100; i++ {
		if !rl.Allow("key") {
			t.Fatalf("request %d should be allowed when rate limiter is disabled", i)
		}
	}
}

func TestRateLimiter_DisabledWhenNegative(t *testing.T) {
	rl := NewRateLimiter(-1)
	for i := 0; i < 100; i++ {
		if !rl.Allow("key") {
			t.Fatalf("request %d should be allowed when rpm is negative", i)
		}
	}
}

func TestRateLimiter_AllowsUpToLimit(t *testing.T) {
	rl := NewRateLimiter(5)

	for i := 0; i < 5; i++ {
		if !rl.Allow("test") {
			t.Fatalf("request %d should be allowed (limit is 5)", i+1)
		}
	}

	// 6th request should be denied
	if rl.Allow("test") {
		t.Fatal("request 6 should be rate limited")
	}
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	rl := NewRateLimiter(2)

	// Use up key-a's budget
	rl.Allow("key-a")
	rl.Allow("key-a")
	if rl.Allow("key-a") {
		t.Fatal("key-a should be rate limited")
	}

	// key-b should still have full budget
	if !rl.Allow("key-b") {
		t.Fatal("key-b should not be rate limited")
	}
	if !rl.Allow("key-b") {
		t.Fatal("key-b second request should be allowed")
	}
	if rl.Allow("key-b") {
		t.Fatal("key-b third request should be rate limited")
	}
}

func TestRateLimiter_WindowResets(t *testing.T) {
	// Create a rate limiter with a very small window for testing
	rl := &RateLimiter{
		rpm:     1,
		buckets: make(map[string]*rateBucket),
	}

	// First request succeeds
	if !rl.Allow("test") {
		t.Fatal("first request should be allowed")
	}

	// Second request is rate limited
	if rl.Allow("test") {
		t.Fatal("second request should be rate limited")
	}

	// Manually expire the bucket
	rl.mu.Lock()
	rl.buckets["test"].resetTime = time.Now().Add(-1 * time.Second)
	rl.mu.Unlock()

	// Should be allowed again after reset
	if !rl.Allow("test") {
		t.Fatal("request should be allowed after window reset")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(100)

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				rl.Allow("concurrent")
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
	// If we get here without a race condition, the test passes
}

// =============================================================================
// HEALTH HANDLER TESTS
// =============================================================================

func TestHealthHandler_ReturnsOK(t *testing.T) {
	handler := HealthHandler()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Fatalf("expected 'ok', got %q", w.Body.String())
	}
}

// =============================================================================
// AUDIT LOGGER TESTS
// =============================================================================

func TestAuditLogger_DisabledIsNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	audit := NewAuditLogger(logger, false)

	// These should not panic even though logger is real
	audit.LogRequest("test message", "key", "value")
	audit.LogResponse("test response", "key", "value")
}

func TestAuditLogger_EnabledLogs(t *testing.T) {
	// Use a buffer to capture log output
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	audit := NewAuditLogger(logger, true)

	audit.LogRequest("test request", "command", "echo hello")
	audit.LogResponse("test response", "exit_code", 0)

	output := buf.String()
	if !strings.Contains(output, "test request") {
		t.Fatal("expected 'test request' in log output")
	}
	if !strings.Contains(output, "test response") {
		t.Fatal("expected 'test response' in log output")
	}
}

// =============================================================================
// LOAD CONFIG TESTS
// =============================================================================

func TestLoadConfig_Defaults(t *testing.T) {
	// Clear relevant env vars
	envs := []string{
		"GATEWAY_MODE", "GATEWAY_PORT", "TOOL_PORT", "TOOL_NAME",
		"GATEWAY_COMMAND", "WORKSPACE_PATH",
		"RATE_LIMIT_RPM", "RATE_LIMIT_PER_AGENT",
		"AUDIT_ENABLED", "AUDIT_LOG_COMMANDS", "AUDIT_LOG_OUTPUT",
	}
	saved := saveEnv(envs)
	defer restoreEnv(saved)
	clearEnv(envs)

	config := LoadConfig()

	if config.Mode != "cli" {
		t.Fatalf("expected default mode 'cli', got %q", config.Mode)
	}
	if config.Port != 8080 {
		t.Fatalf("expected default port 8080, got %d", config.Port)
	}
	if config.ToolName != "unknown" {
		t.Fatalf("expected default tool name 'unknown', got %q", config.ToolName)
	}
	if config.RateLimitRPM != 0 {
		t.Fatalf("expected default rate limit 0, got %d", config.RateLimitRPM)
	}
	if config.AuditEnabled {
		t.Fatal("expected audit disabled by default")
	}
}

func TestLoadConfig_GatewayMode(t *testing.T) {
	saved := saveEnv([]string{"GATEWAY_MODE"})
	defer restoreEnv(saved)

	os.Setenv("GATEWAY_MODE", "mcp")
	config := LoadConfig()

	if config.Mode != "mcp" {
		t.Fatalf("expected mode 'mcp', got %q", config.Mode)
	}
}

func TestLoadConfig_GatewayPort(t *testing.T) {
	saved := saveEnv([]string{"GATEWAY_PORT", "TOOL_PORT"})
	defer restoreEnv(saved)
	os.Unsetenv("TOOL_PORT")

	os.Setenv("GATEWAY_PORT", "9090")
	config := LoadConfig()

	if config.Port != 9090 {
		t.Fatalf("expected port 9090, got %d", config.Port)
	}
}

func TestLoadConfig_ToolPortOverridesGatewayPort(t *testing.T) {
	saved := saveEnv([]string{"GATEWAY_PORT", "TOOL_PORT"})
	defer restoreEnv(saved)

	os.Setenv("GATEWAY_PORT", "9090")
	os.Setenv("TOOL_PORT", "8081")
	config := LoadConfig()

	// TOOL_PORT is processed after GATEWAY_PORT, so it wins
	if config.Port != 8081 {
		t.Fatalf("expected TOOL_PORT 8081 to override, got %d", config.Port)
	}
}

func TestLoadConfig_ToolName(t *testing.T) {
	saved := saveEnv([]string{"TOOL_NAME"})
	defer restoreEnv(saved)

	os.Setenv("TOOL_NAME", "kubectl")
	config := LoadConfig()

	if config.ToolName != "kubectl" {
		t.Fatalf("expected tool name 'kubectl', got %q", config.ToolName)
	}
}

func TestLoadConfig_Command(t *testing.T) {
	saved := saveEnv([]string{"GATEWAY_COMMAND"})
	defer restoreEnv(saved)

	os.Setenv("GATEWAY_COMMAND", "npx @modelcontextprotocol/server-gitlab")
	config := LoadConfig()

	if config.Command != "npx @modelcontextprotocol/server-gitlab" {
		t.Fatalf("expected GATEWAY_COMMAND, got %q", config.Command)
	}
}

func TestLoadConfig_RateLimit(t *testing.T) {
	saved := saveEnv([]string{"RATE_LIMIT_RPM", "RATE_LIMIT_PER_AGENT"})
	defer restoreEnv(saved)

	os.Setenv("RATE_LIMIT_RPM", "120")
	os.Setenv("RATE_LIMIT_PER_AGENT", "true")
	config := LoadConfig()

	if config.RateLimitRPM != 120 {
		t.Fatalf("expected rate limit 120, got %d", config.RateLimitRPM)
	}
	if !config.RateLimitPerAgent {
		t.Fatal("expected rate limit per agent to be true")
	}
}

func TestLoadConfig_Audit(t *testing.T) {
	saved := saveEnv([]string{"AUDIT_ENABLED", "AUDIT_LOG_COMMANDS", "AUDIT_LOG_OUTPUT"})
	defer restoreEnv(saved)

	os.Setenv("AUDIT_ENABLED", "true")
	os.Setenv("AUDIT_LOG_COMMANDS", "true")
	os.Setenv("AUDIT_LOG_OUTPUT", "true")
	config := LoadConfig()

	if !config.AuditEnabled {
		t.Fatal("expected audit enabled")
	}
	if !config.AuditLogCommands {
		t.Fatal("expected audit log commands")
	}
	if !config.AuditLogOutput {
		t.Fatal("expected audit log output")
	}
}

func TestLoadConfig_InvalidPort(t *testing.T) {
	saved := saveEnv([]string{"GATEWAY_PORT", "TOOL_PORT"})
	defer restoreEnv(saved)
	os.Unsetenv("TOOL_PORT")

	os.Setenv("GATEWAY_PORT", "not-a-number")
	config := LoadConfig()

	// Should fall back to default
	if config.Port != 8080 {
		t.Fatalf("expected default port 8080 for invalid GATEWAY_PORT, got %d", config.Port)
	}
}

func TestLoadConfig_InvalidRateLimit(t *testing.T) {
	saved := saveEnv([]string{"RATE_LIMIT_RPM"})
	defer restoreEnv(saved)

	os.Setenv("RATE_LIMIT_RPM", "abc")
	config := LoadConfig()

	if config.RateLimitRPM != 0 {
		t.Fatalf("expected 0 for invalid rate limit, got %d", config.RateLimitRPM)
	}
}

// =============================================================================
// HELPERS
// =============================================================================

func saveEnv(keys []string) map[string]string {
	saved := make(map[string]string)
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			saved[k] = v
		} else {
			saved[k] = "\x00" // sentinel for "was not set"
		}
	}
	return saved
}

func restoreEnv(saved map[string]string) {
	for k, v := range saved {
		if v == "\x00" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}

func clearEnv(keys []string) {
	for _, k := range keys {
		os.Unsetenv(k)
	}
}
