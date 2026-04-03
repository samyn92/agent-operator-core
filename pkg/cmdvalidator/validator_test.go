package cmdvalidator

import (
	"testing"
)

func TestCheckShellMeta(t *testing.T) {
	tests := []struct {
		command string
		blocked bool
		desc    string
	}{
		{"echo hello", false, "basic safe command"},
		{"kubectl get pods -A", false, "safe kubectl command"},
		{"git push origin main", false, "safe git command"},
		{"echo hello; rm -rf /", true, "semicolon injection"},
		{"echo hello && rm -rf /", true, "AND injection"},
		{"echo hello || rm -rf /", true, "OR injection"},
		{"echo hello | cat", true, "pipe injection"},
		{"echo `whoami`", true, "backtick injection"},
		{"echo $(whoami)", true, "command substitution"},
		{"echo hello > /etc/passwd", true, "redirect injection"},
		{"echo hello >> /tmp/log", true, "append redirect injection"},
		{"echo hello < /etc/passwd", true, "stdin redirect injection"},
		{"echo hello &", false, "bare ampersand is safe (no shell involved in exec)"},
		{`glab api "projects/foo/repository/tree?path=flux/apps&recursive=true"`, false, "ampersand in quoted URL arg"},
		{"echo hello\nrm -rf /", true, "newline injection"},
		{"echo hello\rrm -rf /", true, "carriage return injection"},
		{"echo ${PATH}", true, "variable expansion injection"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			result := CheckShellMeta(tc.command)
			if tc.blocked && result == "" {
				t.Errorf("command %q: expected blocked, got allowed", tc.command)
			}
			if !tc.blocked && result != "" {
				t.Errorf("command %q: expected allowed, got blocked: %s", tc.command, result)
			}
		})
	}
}

func TestDangerousPatternsCompleteness(t *testing.T) {
	// Ensure we have a reasonable set of dangerous patterns
	if len(DangerousPatterns) < 10 {
		t.Errorf("expected at least 10 dangerous patterns, got %d", len(DangerousPatterns))
	}
}
