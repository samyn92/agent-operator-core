package cmdvalidator

import (
	"testing"
)

// =============================================================================
// WildcardMatch TESTS
// =============================================================================

func TestWildcardMatch_Basic(t *testing.T) {
	tests := []struct {
		str     string
		pattern string
		match   bool
		desc    string
	}{
		// Exact matches
		{"hello", "hello", true, "exact match"},
		{"hello", "world", false, "no match"},

		// Star wildcard
		{"anything", "*", true, "star matches anything"},
		{"", "*", true, "star matches empty string"},
		{"kubectl get pods", "kubectl get *", true, "star matches rest of command"},
		{"kubectl get pods -A -o wide", "kubectl get *", true, "star matches multiple words"},
		{"kubectl get", "kubectl get *", true, "trailing star+space is optional"},

		// Star in middle
		{"git -C /data/workspace push origin main", "git -C * push * main", true, "stars in middle match"},
		{"git -C /long/path/here push origin main", "git -C * push * main", true, "star matches path segments"},

		// Question mark
		{"abc", "a?c", true, "question mark matches one char"},
		{"abbc", "a?c", false, "question mark matches exactly one char"},

		// Regex-special characters in pattern should be escaped
		{"file.txt", "file.txt", true, "dot is literal"},
		{"fileTtxt", "file.txt", false, "dot is not regex wildcard"},
		{"foo(bar)", "foo(bar)", true, "parens are literal"},

		// Backslash normalization
		{"path\\to\\file", "path/to/file", true, "backslash normalized to forward slash"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := WildcardMatch(tc.str, tc.pattern); got != tc.match {
				t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.str, tc.pattern, got, tc.match)
			}
		})
	}
}

// TestWildcardMatch_GitDenyPatterns tests the exact patterns from the gitlab-contributor
// capability to verify they correctly block pushes to protected branches.
func TestWildcardMatch_GitDenyPatterns(t *testing.T) {
	tests := []struct {
		command string
		pattern string
		match   bool
		desc    string
	}{
		// Direct push to main/master (no -C flag)
		{"git push origin main", "git push * main", true, "push origin main blocked"},
		{"git push upstream main", "git push * main", true, "push upstream main blocked"},
		{"git push origin master", "git push * master", true, "push origin master blocked"},
		{"git push origin feat/branch", "git push * main", false, "push to feature branch allowed"},

		// Push with -C flag (repo path)
		{"git -C /data/workspace/homecluster push origin main", "git -C * push * main", true, "push with -C to main blocked"},
		{"git -C /data/workspace/repo push origin master", "git -C * push * master", true, "push with -C to master blocked"},
		{"git -C /data/workspace/repo push origin feat/branch", "git -C * push * main", false, "push with -C to feature branch allowed"},

		// Bare push (no remote/branch)
		{"git push", "git push", true, "bare git push blocked"},
		{"git -C /data/workspace/repo push", "git -C * push", true, "bare git -C push blocked"},

		// Force push patterns
		{"git push --force origin main", "git push --force*", true, "force push blocked"},
		{"git push origin main --force", "git push *--force*", true, "force push at end blocked"},

		// Safe operations that should NOT match deny patterns
		{"git -C /data/workspace/repo add test.txt", "git -C * push * main", false, "git add not blocked by push deny"},
		{"git -C /data/workspace/repo commit -m msg", "git -C * push * main", false, "git commit not blocked by push deny"},
		{"git -C /data/workspace/repo push -u origin feat/branch", "git -C * push * main", false, "push to feature branch with -u not blocked"},
		{"git clone https://github.com/foo/bar.git", "git -C * push * main", false, "git clone not blocked by push deny"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := WildcardMatch(tc.command, tc.pattern); got != tc.match {
				t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.command, tc.pattern, got, tc.match)
			}
		})
	}
}

// =============================================================================
// CheckDenyPatterns TESTS
// =============================================================================

func TestCheckDenyPatterns(t *testing.T) {
	// These are the deny patterns from the gitlab-contributor capability
	denyPatterns := []string{
		"git push",
		"git push * main",
		"git push * main *",
		"git push * master",
		"git push * master *",
		"git -C * push",
		"git -C * push * main",
		"git -C * push * main *",
		"git -C * push * master",
		"git -C * push * master *",
		"git clean -f*",
		"git rebase *",
		"glab repo delete *",
		"glab auth login*",
		"glab auth logout*",
		"glab auth token*",
		"glab ssh-key *",
		"git clone git@*",
		"git clone ssh://*",
	}

	tests := []struct {
		command string
		blocked bool
		desc    string
	}{
		// Should be BLOCKED
		{"git push origin main", true, "push to main"},
		{"git push origin master", true, "push to master"},
		{"git -C /data/workspace/homecluster push origin main", true, "push with -C to main"},
		{"git -C /data/workspace/repo push origin master", true, "push with -C to master"},
		{"git push", true, "bare push"},
		{"git -C /data/workspace/repo push", true, "bare push with -C"},
		{"git clean -fd", true, "git clean"},
		{"git rebase main", true, "git rebase"},
		{"glab repo delete foo/bar", true, "repo delete"},
		{"glab auth login", true, "auth login"},
		{"glab auth logout", true, "auth logout"},
		{"glab auth token", true, "auth token"},
		{"glab ssh-key add", true, "ssh key"},
		{"git clone git@github.com:foo/bar.git", true, "SSH clone git@"},
		{"git clone ssh://git@github.com/foo/bar.git", true, "SSH clone ssh://"},

		// Should be ALLOWED
		{"git -C /data/workspace/repo add test.txt", false, "git add"},
		{"git -C /data/workspace/repo commit -m test", false, "git commit"},
		{"git -C /data/workspace/repo push -u origin feat/branch", false, "push to feature branch"},
		{"git -C /data/workspace/repo push origin feat/my-feature", false, "push to feature branch (no -u)"},
		{"git clone https://github.com/foo/bar.git", false, "HTTPS clone"},
		{"glab mr create -R foo/bar --title test", false, "MR create"},
		{"glab repo view foo/bar", false, "repo view"},
		{"git status", false, "git status"},
		{"git log --oneline", false, "git log"},
		{"git diff HEAD", false, "git diff"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			result := CheckDenyPatterns(tc.command, denyPatterns)
			if tc.blocked && result == "" {
				t.Errorf("command %q: expected blocked, got allowed", tc.command)
			}
			if !tc.blocked && result != "" {
				t.Errorf("command %q: expected allowed, got blocked by pattern %q", tc.command, result)
			}
		})
	}
}

// TestCheckDenyPatterns_Empty tests that an empty deny list allows everything.
func TestCheckDenyPatterns_Empty(t *testing.T) {
	result := CheckDenyPatterns("git push origin main", nil)
	if result != "" {
		t.Errorf("expected no block with nil deny patterns, got %q", result)
	}

	result = CheckDenyPatterns("git push origin main", []string{})
	if result != "" {
		t.Errorf("expected no block with empty deny patterns, got %q", result)
	}
}

// =============================================================================
// MCP DENY RULE TESTS
// =============================================================================

func TestParseMCPDenyRule(t *testing.T) {
	tests := []struct {
		input    string
		wantTool string
		wantArg  string
		wantPat  string
		wantErr  bool
		desc     string
	}{
		// Valid rules
		{"git_push", "git_push", "", "", false, "tool-level deny"},
		{"git_push:branch=main", "git_push", "branch", "main", false, "arg-level deny"},
		{"git_push:branch=master", "git_push", "branch", "master", false, "arg-level deny master"},
		{"git_push:*=*force*", "git_push", "*", "*force*", false, "wildcard arg deny"},
		{"  git_push  ", "git_push", "", "", false, "trimmed tool-level deny"},
		{"git_push:remote=origin", "git_push", "remote", "origin", false, "remote arg deny"},

		// Invalid rules
		{"", "", "", "", true, "empty rule"},
		{":branch=main", "", "", "", true, "empty tool name"},
		{"git_push:", "", "", "", true, "missing equals"},
		{"git_push:=main", "", "", "", true, "empty arg name"},
		{"git_push:branch=", "", "", "", true, "empty pattern"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			rule, err := ParseMCPDenyRule(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rule.Tool != tc.wantTool {
				t.Errorf("Tool = %q, want %q", rule.Tool, tc.wantTool)
			}
			if rule.ArgName != tc.wantArg {
				t.Errorf("ArgName = %q, want %q", rule.ArgName, tc.wantArg)
			}
			if rule.ArgPattern != tc.wantPat {
				t.Errorf("ArgPattern = %q, want %q", rule.ArgPattern, tc.wantPat)
			}
		})
	}
}

func TestParseMCPDenyRules(t *testing.T) {
	input := `
# Block all git_push calls
git_push

# Block git_merge to main/master
git_merge:branch=main
git_merge:branch=master

# Block force operations
git_push:*=*force*

# This is an invalid rule
:bad_rule

# Another invalid rule
git_push:missing_equals
`

	rules, errs := ParseMCPDenyRules(input)

	if len(rules) != 4 {
		t.Fatalf("expected 4 valid rules, got %d", len(rules))
	}
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(errs))
	}

	// Verify first rule
	if rules[0].Tool != "git_push" || rules[0].ArgName != "" {
		t.Errorf("rule[0] = %+v, expected tool-level git_push deny", rules[0])
	}

	// Verify arg-level rules
	if rules[1].Tool != "git_merge" || rules[1].ArgName != "branch" || rules[1].ArgPattern != "main" {
		t.Errorf("rule[1] = %+v, expected git_merge:branch=main", rules[1])
	}
	if rules[2].Tool != "git_merge" || rules[2].ArgName != "branch" || rules[2].ArgPattern != "master" {
		t.Errorf("rule[2] = %+v, expected git_merge:branch=master", rules[2])
	}

	// Verify wildcard arg rule
	if rules[3].Tool != "git_push" || rules[3].ArgName != "*" || rules[3].ArgPattern != "*force*" {
		t.Errorf("rule[3] = %+v, expected git_push:*=*force*", rules[3])
	}
}

func TestParseMCPDenyRules_EmptyInput(t *testing.T) {
	rules, errs := ParseMCPDenyRules("")
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d", len(errs))
	}
}

func TestParseMCPDenyRules_CommentsOnly(t *testing.T) {
	rules, errs := ParseMCPDenyRules("# comment 1\n# comment 2\n\n")
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d", len(errs))
	}
}

func TestCheckMCPDenyRules_ToolLevel(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "git_push"},
	}

	// Any call to git_push should be denied
	result := CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
		"branch": "feat/my-feature",
	}, rules)
	if result == "" {
		t.Error("expected git_push to be denied, got allowed")
	}

	// Other tools should be allowed
	result = CheckMCPDenyRules("git_commit", map[string]interface{}{
		"message": "test commit",
	}, rules)
	if result != "" {
		t.Errorf("expected git_commit to be allowed, got denied: %s", result)
	}
}

func TestCheckMCPDenyRules_ArgLevel(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "git_push", ArgName: "branch", ArgPattern: "main"},
		{Tool: "git_push", ArgName: "branch", ArgPattern: "master"},
	}

	// Push to main should be denied
	result := CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
		"branch": "main",
	}, rules)
	if result == "" {
		t.Error("expected git_push to main to be denied")
	}

	// Push to master should be denied
	result = CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
		"branch": "master",
	}, rules)
	if result == "" {
		t.Error("expected git_push to master to be denied")
	}

	// Push to feature branch should be allowed
	result = CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
		"branch": "feat/my-feature",
	}, rules)
	if result != "" {
		t.Errorf("expected git_push to feat/my-feature to be allowed, got denied: %s", result)
	}

	// Push without branch arg should be allowed (arg doesn't exist)
	result = CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
	}, rules)
	if result != "" {
		t.Errorf("expected git_push without branch to be allowed, got denied: %s", result)
	}
}

func TestCheckMCPDenyRules_WildcardArg(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "git_push", ArgName: "*", ArgPattern: "*force*"},
	}

	// Any arg containing "force" should be denied
	result := CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote":  "origin",
		"branch":  "feat/my-feature",
		"options": "--force",
	}, rules)
	if result == "" {
		t.Error("expected git_push with --force option to be denied")
	}

	// Without force in any arg
	result = CheckMCPDenyRules("git_push", map[string]interface{}{
		"remote": "origin",
		"branch": "feat/my-feature",
	}, rules)
	if result != "" {
		t.Errorf("expected git_push without force to be allowed, got denied: %s", result)
	}
}

func TestCheckMCPDenyRules_WildcardArgPattern(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "git_push", ArgName: "branch", ArgPattern: "release/*"},
	}

	// release/v1.0 should be denied
	result := CheckMCPDenyRules("git_push", map[string]interface{}{
		"branch": "release/v1.0",
	}, rules)
	if result == "" {
		t.Error("expected git_push to release/v1.0 to be denied")
	}

	// main should be allowed
	result = CheckMCPDenyRules("git_push", map[string]interface{}{
		"branch": "main",
	}, rules)
	if result != "" {
		t.Errorf("expected git_push to main to be allowed, got denied: %s", result)
	}
}

func TestCheckMCPDenyRules_NilRules(t *testing.T) {
	result := CheckMCPDenyRules("git_push", map[string]interface{}{
		"branch": "main",
	}, nil)
	if result != "" {
		t.Errorf("expected allowed with nil rules, got denied: %s", result)
	}
}

func TestCheckMCPDenyRules_NilArguments(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "git_push", ArgName: "branch", ArgPattern: "main"},
	}

	// Nil arguments — arg-level rule can't match
	result := CheckMCPDenyRules("git_push", nil, rules)
	if result != "" {
		t.Errorf("expected allowed with nil arguments, got denied: %s", result)
	}

	// Tool-level deny should still match with nil arguments
	rules = []MCPDenyRule{
		{Tool: "git_push"},
	}
	result = CheckMCPDenyRules("git_push", nil, rules)
	if result == "" {
		t.Error("expected tool-level deny to match even with nil arguments")
	}
}

func TestCheckMCPDenyRules_NonStringArgValues(t *testing.T) {
	rules := []MCPDenyRule{
		{Tool: "create_branch", ArgName: "protected", ArgPattern: "true"},
	}

	// Boolean argument
	result := CheckMCPDenyRules("create_branch", map[string]interface{}{
		"name":      "release/v1",
		"protected": true,
	}, rules)
	if result == "" {
		t.Error("expected boolean true to match pattern 'true'")
	}

	// Numeric argument
	rules = []MCPDenyRule{
		{Tool: "set_limit", ArgName: "value", ArgPattern: "0"},
	}
	result = CheckMCPDenyRules("set_limit", map[string]interface{}{
		"value": float64(0), // JSON numbers are float64
	}, rules)
	if result == "" {
		t.Error("expected numeric 0 to match pattern '0'")
	}
}

// TestCheckMCPDenyRules_RealWorldScenario tests a realistic deny ruleset
// for a git MCP server protecting main/master branches.
func TestCheckMCPDenyRules_RealWorldScenario(t *testing.T) {
	rules := []MCPDenyRule{
		// Block all pushes to protected branches
		{Tool: "git_push", ArgName: "branch", ArgPattern: "main"},
		{Tool: "git_push", ArgName: "branch", ArgPattern: "master"},
		{Tool: "git_push", ArgName: "branch", ArgPattern: "release/*"},
		// Block force operations
		{Tool: "git_push", ArgName: "*", ArgPattern: "*force*"},
		// Block direct merge to protected branches
		{Tool: "git_merge", ArgName: "branch", ArgPattern: "main"},
		{Tool: "git_merge", ArgName: "branch", ArgPattern: "master"},
		// Block destructive operations entirely
		{Tool: "git_reset_hard"},
		{Tool: "git_clean"},
	}

	tests := []struct {
		tool    string
		args    map[string]interface{}
		blocked bool
		desc    string
	}{
		// BLOCKED
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "main"}, true, "push to main"},
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "master"}, true, "push to master"},
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "release/v2.0"}, true, "push to release branch"},
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "feat/x", "options": "--force"}, true, "force push"},
		{"git_merge", map[string]interface{}{"branch": "main"}, true, "merge to main"},
		{"git_merge", map[string]interface{}{"branch": "master"}, true, "merge to master"},
		{"git_reset_hard", map[string]interface{}{"ref": "HEAD~1"}, true, "hard reset"},
		{"git_reset_hard", nil, true, "hard reset no args"},
		{"git_clean", map[string]interface{}{"force": true}, true, "git clean"},

		// ALLOWED
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "feat/my-feature"}, false, "push to feature branch"},
		{"git_push", map[string]interface{}{"remote": "origin", "branch": "fix/bug-123"}, false, "push to fix branch"},
		{"git_commit", map[string]interface{}{"message": "fix: resolve bug"}, false, "commit"},
		{"git_add", map[string]interface{}{"files": []string{"test.go"}}, false, "git add"},
		{"git_status", nil, false, "git status"},
		{"git_log", map[string]interface{}{"count": float64(10)}, false, "git log"},
		{"git_diff", map[string]interface{}{"ref": "HEAD"}, false, "git diff"},
		{"git_branch", map[string]interface{}{"name": "feat/new"}, false, "create branch"},
		{"git_merge", map[string]interface{}{"branch": "feat/done"}, false, "merge feature branch"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			result := CheckMCPDenyRules(tc.tool, tc.args, rules)
			if tc.blocked && result == "" {
				t.Errorf("tool %q args %v: expected blocked, got allowed", tc.tool, tc.args)
			}
			if !tc.blocked && result != "" {
				t.Errorf("tool %q args %v: expected allowed, got blocked: %s", tc.tool, tc.args, result)
			}
		})
	}
}
