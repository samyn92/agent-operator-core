// Package gateway provides shared middleware for the capability-gateway binary.
package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
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
	onReload      func(key, value string) // optional callback for testing/extensibility
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

// SetOnReload sets a callback invoked after each config reload (for testing).
func (cw *ConfigWatcher) SetOnReload(fn func(key, value string)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = fn
}

func (cw *ConfigWatcher) run() {
	defer close(cw.done)

	for {
		select {
		case event, ok := <-cw.watcher.Events:
			if !ok {
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
					cw.logger.Info("config change detected, reloading",
						"event", event.Name,
						"op", event.Op.String(),
					)
					cw.loadAll()
				}
			}

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
