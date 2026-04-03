package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConfigWatcher_InitialLoad(t *testing.T) {
	dir := t.TempDir()

	// Write command-prefix file before creating watcher
	if err := os.WriteFile(filepath.Join(dir, "command-prefix"), []byte("kubectl "), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	if got := cw.CommandPrefix(); got != "kubectl" {
		t.Fatalf("expected initial command-prefix 'kubectl', got %q", got)
	}
}

func TestConfigWatcher_InitialLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	// No command-prefix file

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	if got := cw.CommandPrefix(); got != "" {
		t.Fatalf("expected empty command-prefix when file doesn't exist, got %q", got)
	}
}

func TestConfigWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()

	// Initial content
	if err := os.WriteFile(filepath.Join(dir, "command-prefix"), []byte("kubectl"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	if got := cw.CommandPrefix(); got != "kubectl" {
		t.Fatalf("expected initial 'kubectl', got %q", got)
	}

	// Set up a channel to detect reload
	reloaded := make(chan string, 1)
	cw.SetOnReload(func(key, value string) {
		if key == "command-prefix" {
			reloaded <- value
		}
	})

	// Update the file (simulates Kubernetes ConfigMap update)
	if err := os.WriteFile(filepath.Join(dir, "command-prefix"), []byte("helm"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for reload with timeout
	select {
	case val := <-reloaded:
		if val != "helm" {
			t.Fatalf("expected reloaded value 'helm', got %q", val)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}

	// Verify the getter returns the new value
	if got := cw.CommandPrefix(); got != "helm" {
		t.Fatalf("expected updated 'helm', got %q", got)
	}
}

func TestConfigWatcher_SymlinkSwap(t *testing.T) {
	// Simulate the Kubernetes ConfigMap volume symlink swap pattern:
	// ..data -> ..2024_01_01/
	// command-prefix -> ..data/command-prefix

	dir := t.TempDir()

	// Create initial target directory
	target1 := filepath.Join(dir, "..2024_01_01")
	if err := os.Mkdir(target1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target1, "command-prefix"), []byte("kubectl"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create ..data symlink -> ..2024_01_01
	dataLink := filepath.Join(dir, "..data")
	if err := os.Symlink(target1, dataLink); err != nil {
		t.Fatal(err)
	}

	// Create command-prefix symlink -> ..data/command-prefix
	if err := os.Symlink(filepath.Join("..data", "command-prefix"), filepath.Join(dir, "command-prefix")); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	if got := cw.CommandPrefix(); got != "kubectl" {
		t.Fatalf("expected initial 'kubectl', got %q", got)
	}

	// Set up reload detection
	reloaded := make(chan string, 1)
	cw.SetOnReload(func(key, value string) {
		if key == "command-prefix" {
			reloaded <- value
		}
	})

	// Simulate Kubernetes symlink swap:
	// 1. Create new target directory
	target2 := filepath.Join(dir, "..2024_01_02")
	if err := os.Mkdir(target2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target2, "command-prefix"), []byte("helm"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Atomically swap ..data symlink
	tmpLink := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink(target2, tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpLink, dataLink); err != nil {
		t.Fatal(err)
	}

	// Wait for reload
	select {
	case val := <-reloaded:
		if val != "helm" {
			t.Fatalf("expected reloaded value 'helm', got %q", val)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for config reload after symlink swap")
	}

	if got := cw.CommandPrefix(); got != "helm" {
		t.Fatalf("expected updated 'helm', got %q", got)
	}
}

func TestConfigWatcher_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "command-prefix"), []byte("kubectl"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	// Concurrent reads should not race
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = cw.CommandPrefix()
			}
		}()
	}
	wg.Wait()
}

func TestConfigWatcher_InvalidDirectory(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	_, err := NewConfigWatcher("/nonexistent/path", logger)
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestConfigWatcher_TrimWhitespace(t *testing.T) {
	dir := t.TempDir()
	// ConfigMap volumes sometimes have trailing newlines
	if err := os.WriteFile(filepath.Join(dir, "command-prefix"), []byte("kubectl \n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	if got := cw.CommandPrefix(); got != "kubectl" {
		t.Fatalf("expected trimmed 'kubectl', got %q", got)
	}
}
