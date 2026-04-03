// Package gateway provides shared middleware for the capability-gateway binary.
package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/samyn92/agent-operator-core/pkg/cmdvalidator"
)

// ConfigWatcher watches a directory (typically a mounted ConfigMap) for changes
// and reloads configuration values in-memory. This allows permission and config
// changes to take effect without restarting the pod.
//
// Kubernetes ConfigMap volume mounts use a symlink-swap mechanism:
// the kubelet atomically updates `..data` -> `..2024_01_01_00_00_00.123456` symlinks.
// fsnotify sees CREATE events on the `..data` symlink when this happens.
type ConfigWatcher struct {
	watcher *fsnotify.Watcher
	dir     string
	logger  *slog.Logger
	done    chan struct{}
	started bool

	mu            sync.RWMutex
	commandPrefix string
	denyPatterns  []string                   // wildcard patterns from the deny-patterns ConfigMap key
	mcpDenyRules  []cmdvalidator.MCPDenyRule // parsed MCP deny rules from the mcp-deny-rules ConfigMap key
	onReload      func(key, value string)    // optional callback for testing/extensibility
}

// NewConfigWatcher creates a watcher for the given directory.
// It performs an initial load of all known config files.
// Call Start() to begin watching for changes, and Stop() to shut down.
func NewConfigWatcher(dir string, logger *slog.Logger) (*ConfigWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	cw := &ConfigWatcher{
		watcher: watcher,
		dir:     dir,
		logger:  logger,
		done:    make(chan struct{}),
	}

	// Initial load
	cw.loadAll()

	// Watch the directory for changes
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}

	return cw, nil
}

// Start begins watching for file changes in a background goroutine.
// Call Stop() to shut down the watcher.
func (cw *ConfigWatcher) Start() {
	cw.started = true
	go cw.run()
}

// Stop shuts down the watcher. If Start() was called, it waits for the
// background goroutine to exit.
func (cw *ConfigWatcher) Stop() {
	cw.watcher.Close()
	if cw.started {
		<-cw.done
	}
}

// CommandPrefix returns the current command prefix (thread-safe).
func (cw *ConfigWatcher) CommandPrefix() string {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.commandPrefix
}

// DenyPatterns returns the current deny patterns (thread-safe).
// Returns nil if no deny-patterns file exists.
func (cw *ConfigWatcher) DenyPatterns() []string {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.denyPatterns
}

// MCPDenyRules returns the current MCP deny rules (thread-safe).
// Returns nil if no mcp-deny-rules file exists.
func (cw *ConfigWatcher) MCPDenyRules() []cmdvalidator.MCPDenyRule {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.mcpDenyRules
}

// SetOnReload sets a callback invoked after each config reload (for testing).
func (cw *ConfigWatcher) SetOnReload(fn func(key, value string)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = fn
}

func (cw *ConfigWatcher) run() {
	defer close(cw.done)

	// Debounce timer to coalesce rapid events. On Linux, os.WriteFile can
	// produce a CREATE (truncate to 0 bytes) followed by a WRITE with the
	// actual data. Without debouncing we may read the file between truncation
	// and write, getting an empty value.
	const debounceDelay = 50 * time.Millisecond
	var debounceC <-chan time.Time
	pending := false

	for {
		select {
		case event, ok := <-cw.watcher.Events:
			if !ok {
				// Channel closed; do a final reload if events were pending
				if pending {
					cw.loadAll()
				}
				return
			}
			// Kubernetes ConfigMap volumes use a symlink swap:
			// ..data -> ..2024_01_01_00_00_00.123456
			// When the kubelet updates the ConfigMap, it creates a new timestamped
			// directory, atomically swaps the ..data symlink, then removes the old one.
			// We detect this by watching for CREATE events on ..data.
			// We also detect direct file writes (CREATE/WRITE) for non-dotfiles,
			// which covers both the K8s symlink swap and simple file overwrites in tests.
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				base := filepath.Base(event.Name)
				if base == "..data" || !strings.HasPrefix(base, "..") {
					cw.logger.Info("config change detected, scheduling reload",
						"event", event.Name,
						"op", event.Op.String(),
					)
					pending = true
					debounceC = time.After(debounceDelay)
				}
			}

		case <-debounceC:
			debounceC = nil
			pending = false
			cw.logger.Info("debounce expired, reloading config")
			cw.loadAll()

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			cw.logger.Error("config watcher error", "error", err)
		}
	}
}

func (cw *ConfigWatcher) loadAll() {
	cw.loadFile("command-prefix", func(value string) {
		cw.mu.Lock()
		old := cw.commandPrefix
		cw.commandPrefix = value
		fn := cw.onReload
		cw.mu.Unlock()

		if old != value {
			cw.logger.Info("command-prefix reloaded",
				"old", old,
				"new", value,
			)
		}
		if fn != nil {
			fn("command-prefix", value)
		}
	})

	cw.loadFile("deny-patterns", func(value string) {
		var patterns []string
		if value != "" {
			for _, line := range strings.Split(value, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					patterns = append(patterns, line)
				}
			}
		}

		cw.mu.Lock()
		old := cw.denyPatterns
		cw.denyPatterns = patterns
		fn := cw.onReload
		cw.mu.Unlock()

		if len(old) != len(patterns) {
			cw.logger.Info("deny-patterns reloaded",
				"old_count", len(old),
				"new_count", len(patterns),
			)
		}
		if fn != nil {
			fn("deny-patterns", value)
		}
	})

	cw.loadFile("mcp-deny-rules", func(value string) {
		rules, errs := cmdvalidator.ParseMCPDenyRules(value)
		for _, err := range errs {
			cw.logger.Warn("invalid MCP deny rule", "error", err)
		}

		cw.mu.Lock()
		old := cw.mcpDenyRules
		cw.mcpDenyRules = rules
		fn := cw.onReload
		cw.mu.Unlock()

		if len(old) != len(rules) {
			cw.logger.Info("mcp-deny-rules reloaded",
				"old_count", len(old),
				"new_count", len(rules),
			)
		}
		if fn != nil {
			fn("mcp-deny-rules", value)
		}
	})
}

func (cw *ConfigWatcher) loadFile(name string, setter func(string)) {
	path := filepath.Join(cw.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			cw.logger.Warn("failed to read config file",
				"path", path,
				"error", err,
			)
		}
		return
	}
	setter(strings.TrimSpace(string(data)))
}
