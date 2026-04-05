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

	// Write mcp-deny-rules file before creating watcher
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push\n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if len(got) != 1 {
		t.Fatalf("expected 1 initial MCP deny rule, got %d: %v", len(got), got)
	}
	if got[0].Tool != "git_push" {
		t.Fatalf("expected initial rule tool 'git_push', got %q", got[0].Tool)
	}
}

func TestConfigWatcher_InitialLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	// No mcp-deny-rules file

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if got != nil {
		t.Fatalf("expected nil MCP deny rules when file doesn't exist, got %v", got)
	}
}

func TestConfigWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()

	// Initial content
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if len(got) != 1 {
		t.Fatalf("expected 1 initial rule, got %d", len(got))
	}

	// Set up a channel to detect reload
	reloaded := make(chan string, 1)
	cw.SetOnReload(func(key, value string) {
		if key == "mcp-deny-rules" {
			reloaded <- value
		}
	})

	// Update the file (simulates Kubernetes ConfigMap update)
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push\ngit_reset_hard"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for reload with timeout
	select {
	case <-reloaded:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}

	// Verify the getter returns the new value
	got = cw.MCPDenyRules()
	if len(got) != 2 {
		t.Fatalf("expected 2 rules after reload, got %d", len(got))
	}
}

func TestConfigWatcher_SymlinkSwap(t *testing.T) {
	// Simulate the Kubernetes ConfigMap volume symlink swap pattern:
	// ..data -> ..2024_01_01/
	// mcp-deny-rules -> ..data/mcp-deny-rules

	dir := t.TempDir()

	// Create initial target directory
	target1 := filepath.Join(dir, "..2024_01_01")
	if err := os.Mkdir(target1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target1, "mcp-deny-rules"), []byte("git_push"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create ..data symlink -> ..2024_01_01
	dataLink := filepath.Join(dir, "..data")
	if err := os.Symlink(target1, dataLink); err != nil {
		t.Fatal(err)
	}

	// Create mcp-deny-rules symlink -> ..data/mcp-deny-rules
	if err := os.Symlink(filepath.Join("..data", "mcp-deny-rules"), filepath.Join(dir, "mcp-deny-rules")); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if len(got) != 1 || got[0].Tool != "git_push" {
		t.Fatalf("expected initial rule [git_push], got %v", got)
	}

	// Set up reload detection
	reloaded := make(chan string, 1)
	cw.SetOnReload(func(key, value string) {
		if key == "mcp-deny-rules" {
			reloaded <- value
		}
	})

	// Simulate Kubernetes symlink swap:
	// 1. Create new target directory
	target2 := filepath.Join(dir, "..2024_01_02")
	if err := os.Mkdir(target2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target2, "mcp-deny-rules"), []byte("git_push\ngit_reset_hard"), 0644); err != nil {
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
	case <-reloaded:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for config reload after symlink swap")
	}

	got = cw.MCPDenyRules()
	if len(got) != 2 {
		t.Fatalf("expected 2 rules after symlink swap, got %d: %v", len(got), got)
	}
}

func TestConfigWatcher_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push:branch=main"), 0644); err != nil {
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
				_ = cw.MCPDenyRules()
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

// =============================================================================
// mcp-deny-rules TESTS
// =============================================================================

func TestConfigWatcher_MCPDenyRules_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	rules := "git_push:branch=main\ngit_push:branch=master\ngit_reset_hard\n"
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte(rules), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if len(got) != 3 {
		t.Fatalf("expected 3 MCP deny rules, got %d: %v", len(got), got)
	}
	if got[0].Tool != "git_push" || got[0].ArgName != "branch" || got[0].ArgPattern != "main" {
		t.Errorf("rule[0] = %+v, expected git_push:branch=main", got[0])
	}
	if got[1].Tool != "git_push" || got[1].ArgName != "branch" || got[1].ArgPattern != "master" {
		t.Errorf("rule[1] = %+v, expected git_push:branch=master", got[1])
	}
	if got[2].Tool != "git_reset_hard" || got[2].ArgName != "" {
		t.Errorf("rule[2] = %+v, expected tool-level git_reset_hard", got[2])
	}
}

func TestConfigWatcher_MCPDenyRules_Empty(t *testing.T) {
	dir := t.TempDir()
	// No mcp-deny-rules file

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if got != nil {
		t.Fatalf("expected nil MCP deny rules when file doesn't exist, got %v", got)
	}
}

func TestConfigWatcher_MCPDenyRules_HotReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	got := cw.MCPDenyRules()
	if len(got) != 1 {
		t.Fatalf("expected 1 initial rule, got %d", len(got))
	}

	// Set up reload detection
	reloaded := make(chan string, 1)
	cw.SetOnReload(func(key, value string) {
		if key == "mcp-deny-rules" {
			reloaded <- value
		}
	})

	// Update the file with more rules
	newRules := "git_push\ngit_push:branch=main\ngit_push:branch=master\ngit_reset_hard"
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte(newRules), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-reloaded:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mcp-deny-rules reload")
	}

	got = cw.MCPDenyRules()
	if len(got) != 4 {
		t.Fatalf("expected 4 rules after reload, got %d: %v", len(got), got)
	}
}

func TestConfigWatcher_MCPDenyRules_SkipsInvalidRules(t *testing.T) {
	dir := t.TempDir()
	// Mix of valid and invalid rules
	rules := "git_push\n:bad_rule\ngit_merge:branch=main\ngit_push:missing_equals\n"
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte(rules), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Stop()

	got := cw.MCPDenyRules()
	// Should only have the 2 valid rules (invalid ones are skipped with warnings)
	if len(got) != 2 {
		t.Fatalf("expected 2 valid rules (invalid skipped), got %d: %v", len(got), got)
	}
}

func TestConfigWatcher_MCPDenyRules_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp-deny-rules"), []byte("git_push:branch=main"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cw, err := NewConfigWatcher(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	cw.Start()
	defer cw.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = cw.MCPDenyRules()
			}
		}()
	}
	wg.Wait()
}
