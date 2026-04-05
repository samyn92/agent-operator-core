// Package gateway provides shared middleware for the capability-gateway binary.
// This includes rate limiting, audit logging, health checks, and configuration.
package gateway

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Config holds shared gateway configuration loaded from environment variables.
type Config struct {
	// Port is the HTTP port to listen on
	Port int
	// ToolName is the name of the capability (for logging/metrics)
	ToolName string

	// Command is the MCP server command to spawn
	Command string

	// ConfigPath is the path to config files (mcp-deny-rules, etc.)
	ConfigPath string

	// Shared security/observability
	RateLimitRPM      int
	RateLimitPerAgent bool
	AuditEnabled      bool
	AuditLogCommands  bool
	AuditLogOutput    bool
}

// LoadConfig reads gateway configuration from environment variables.
func LoadConfig() Config {
	config := Config{
		Port:       8080,
		ConfigPath: "/etc/tool",
	}

	if port := os.Getenv("GATEWAY_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			config.Port = p
		}
	}
	// Also support TOOL_PORT env var as an alias for GATEWAY_PORT
	if port := os.Getenv("TOOL_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			config.Port = p
		}
	}

	config.ToolName = os.Getenv("TOOL_NAME")
	if config.ToolName == "" {
		config.ToolName = "unknown"
	}

	// MCP server command to spawn
	config.Command = os.Getenv("GATEWAY_COMMAND")

	// Shared security
	if rpm := os.Getenv("RATE_LIMIT_RPM"); rpm != "" {
		if r, err := strconv.Atoi(rpm); err == nil {
			config.RateLimitRPM = r
		}
	}
	config.RateLimitPerAgent = os.Getenv("RATE_LIMIT_PER_AGENT") == "true"
	config.AuditEnabled = os.Getenv("AUDIT_ENABLED") == "true"
	config.AuditLogCommands = os.Getenv("AUDIT_LOG_COMMANDS") == "true"
	config.AuditLogOutput = os.Getenv("AUDIT_LOG_OUTPUT") == "true"

	return config
}

// =============================================================================
// RATE LIMITER
// =============================================================================

// RateLimiter provides per-key rate limiting with a sliding window.
type RateLimiter struct {
	rpm     int
	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count     int
	resetTime time.Time
}

// NewRateLimiter creates a new rate limiter with the given requests-per-minute limit.
// If rpm is 0, the limiter is disabled (Allow always returns true).
func NewRateLimiter(rpm int) *RateLimiter {
	return &RateLimiter{
		rpm:     rpm,
		buckets: make(map[string]*rateBucket),
	}
}

// Allow checks if the given key is within the rate limit.
// Returns true if the request is allowed, false if rate limited.
func (rl *RateLimiter) Allow(key string) bool {
	if rl.rpm <= 0 {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	bucket, exists := rl.buckets[key]

	if !exists || now.After(bucket.resetTime) {
		rl.buckets[key] = &rateBucket{
			count:     1,
			resetTime: now.Add(time.Minute),
		}
		return true
	}

	if bucket.count >= rl.rpm {
		return false
	}

	bucket.count++
	return true
}

// =============================================================================
// HEALTH CHECK HANDLER
// =============================================================================

// HealthHandler returns an HTTP handler for health/readiness checks.
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

// =============================================================================
// AUDIT LOGGER
// =============================================================================

// AuditLogger provides structured audit logging for capability usage.
type AuditLogger struct {
	logger  *slog.Logger
	enabled bool
}

// NewAuditLogger creates an audit logger. If enabled is false, all methods are no-ops.
func NewAuditLogger(logger *slog.Logger, enabled bool) *AuditLogger {
	return &AuditLogger{
		logger:  logger,
		enabled: enabled,
	}
}

// LogRequest logs an incoming request (command or MCP message).
func (a *AuditLogger) LogRequest(msg string, attrs ...any) {
	if !a.enabled {
		return
	}
	a.logger.Info(msg, attrs...)
}

// LogResponse logs the response/result of a request.
func (a *AuditLogger) LogResponse(msg string, attrs ...any) {
	if !a.enabled {
		return
	}
	a.logger.Info(msg, attrs...)
}
