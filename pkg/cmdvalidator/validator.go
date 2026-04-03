// Package cmdvalidator provides command security for the capability-gateway.
//
// Two layers of protection:
//
//  1. CLI deny-pattern matching — blocks shell commands that match wildcard deny
//     patterns (e.g., "git push * main"). This is a hard security backstop that
//     cannot be bypassed by OpenCode's runtime "Always Allow" approvals.
//
//  2. MCP deny-rule matching — blocks MCP tools/call requests based on tool name
//     and structured argument values. Same defense-in-depth principle as CLI deny
//     patterns, but for the MCP JSON-RPC protocol.
//
// The deny-pattern matching uses the same wildcard semantics as OpenCode's
// Wildcard.match: "*" maps to ".*" in regex, trailing " *" becomes optional.
// This ensures the gateway and OpenCode agree on what a pattern matches.
//
// Note: CLI commands are executed via exec.CommandContext (no shell), so shell
// metacharacters have no special meaning. Security is enforced through command
// prefix requirements and deny-pattern matching, not character-level heuristics.
package cmdvalidator

import (
	"fmt"
	"regexp"
	"strings"
)

// CheckDenyPatterns checks if a command matches any deny pattern.
// Patterns use the same wildcard semantics as OpenCode's Wildcard.match:
//   - "*" matches any sequence of characters (equivalent to ".*" in regex)
//   - "?" matches exactly one character
//   - Trailing " *" (space + wildcard) becomes optional: "git push *" matches "git push"
//   - Match is anchored (^...$) — the entire command must match
//
// Returns the matched deny pattern if blocked, empty string if allowed.
func CheckDenyPatterns(cmd string, denyPatterns []string) string {
	for _, pattern := range denyPatterns {
		if WildcardMatch(cmd, pattern) {
			return pattern
		}
	}
	return ""
}

// WildcardMatch implements the same matching semantics as OpenCode's Wildcard.match.
// This ensures the gateway and OpenCode agree on what a pattern matches.
//
// Algorithm (mirrors opencode/packages/opencode/src/util/wildcard.ts):
//  1. Normalize backslashes to forward slashes
//  2. Escape regex-special characters in the pattern
//  3. Replace "*" with ".*" and "?" with "."
//  4. If pattern ends with " *" (space + wildcard), make trailing part optional
//  5. Anchor: ^pattern$
func WildcardMatch(str, pattern string) bool {
	// Normalize backslashes (cross-platform compat, matches OpenCode behavior)
	str = strings.ReplaceAll(str, "\\", "/")
	pattern = strings.ReplaceAll(pattern, "\\", "/")

	// Escape regex-special characters, then convert wildcards
	escaped := regexp.QuoteMeta(pattern)
	// QuoteMeta escapes "*" to "\*" and "?" to "\?" — convert them to regex wildcards
	escaped = strings.ReplaceAll(escaped, "\\*", ".*")
	escaped = strings.ReplaceAll(escaped, "\\?", ".")

	// If pattern ends with " *" (space + wildcard), make the trailing part optional.
	// This allows "ls *" to match both "ls" and "ls -la".
	if strings.HasSuffix(escaped, " .*") {
		escaped = escaped[:len(escaped)-3] + "( .*)?"
	}

	re, err := regexp.Compile("^" + escaped + "$")
	if err != nil {
		return false
	}
	return re.MatchString(str)
}

// =============================================================================
// MCP DENY RULES
// =============================================================================

// MCPDenyRule represents a parsed MCP deny rule.
// Rules are parsed from a line-based format:
//
//	toolName              — deny all calls to this tool
//	toolName:argName=pat  — deny when arguments[argName] matches the wildcard pattern
//	toolName:*=pat        — deny when ANY argument value matches the wildcard pattern
type MCPDenyRule struct {
	// Tool is the MCP tool name to match (e.g., "git_push").
	Tool string
	// ArgName is the argument to check. Empty means deny the entire tool.
	// "*" means check ALL argument values.
	ArgName string
	// ArgPattern is the wildcard pattern to match against the argument value.
	// Empty when ArgName is empty (tool-level deny).
	ArgPattern string
}

// ParseMCPDenyRule parses a single MCP deny rule from a string.
// Returns the parsed rule, or an error if the format is invalid.
//
// Supported formats:
//
//	"git_push"              → deny all calls to git_push
//	"git_push:branch=main"  → deny git_push when branch matches "main"
//	"git_push:*=*force*"    → deny git_push when any arg value matches "*force*"
func ParseMCPDenyRule(line string) (MCPDenyRule, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return MCPDenyRule{}, fmt.Errorf("empty rule")
	}

	// Split on first ":"
	colonIdx := strings.Index(line, ":")
	if colonIdx == -1 {
		// Tool-level deny: just the tool name
		return MCPDenyRule{Tool: line}, nil
	}

	tool := line[:colonIdx]
	rest := line[colonIdx+1:]

	if tool == "" {
		return MCPDenyRule{}, fmt.Errorf("empty tool name in rule %q", line)
	}

	// rest should be "argName=pattern"
	eqIdx := strings.Index(rest, "=")
	if eqIdx == -1 {
		return MCPDenyRule{}, fmt.Errorf("missing '=' in argument rule %q (expected toolName:argName=pattern)", line)
	}

	argName := rest[:eqIdx]
	argPattern := rest[eqIdx+1:]

	if argName == "" {
		return MCPDenyRule{}, fmt.Errorf("empty argument name in rule %q", line)
	}
	if argPattern == "" {
		return MCPDenyRule{}, fmt.Errorf("empty pattern in rule %q", line)
	}

	return MCPDenyRule{
		Tool:       tool,
		ArgName:    argName,
		ArgPattern: argPattern,
	}, nil
}

// ParseMCPDenyRules parses multiple MCP deny rules from newline-separated text.
// Empty lines and lines starting with "#" are ignored.
// Returns the parsed rules and any parse errors (lenient: skips bad lines, logs errors).
func ParseMCPDenyRules(text string) ([]MCPDenyRule, []error) {
	var rules []MCPDenyRule
	var errs []error

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule, err := ParseMCPDenyRule(line)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rules = append(rules, rule)
	}
	return rules, errs
}

// CheckMCPDenyRules checks if a tools/call request is denied by any MCP deny rule.
// toolName is the MCP tool name (e.g., "git_push").
// arguments is the structured arguments map from the JSON-RPC params.
//
// Returns the matched rule as a string if denied, empty string if allowed.
func CheckMCPDenyRules(toolName string, arguments map[string]interface{}, rules []MCPDenyRule) string {
	for _, rule := range rules {
		if rule.Tool != toolName {
			continue
		}

		// Tool-level deny: no argument check
		if rule.ArgName == "" {
			return rule.Tool
		}

		// Wildcard arg check: match against ALL argument values
		if rule.ArgName == "*" {
			for argName, argVal := range arguments {
				valStr := argValueToString(argVal)
				if WildcardMatch(valStr, rule.ArgPattern) {
					return fmt.Sprintf("%s:*=%s (matched arg %q=%q)", rule.Tool, rule.ArgPattern, argName, valStr)
				}
			}
			continue
		}

		// Named arg check: match against a specific argument
		argVal, exists := arguments[rule.ArgName]
		if !exists {
			continue
		}
		valStr := argValueToString(argVal)
		if WildcardMatch(valStr, rule.ArgPattern) {
			return fmt.Sprintf("%s:%s=%s (value=%q)", rule.Tool, rule.ArgName, rule.ArgPattern, valStr)
		}
	}
	return ""
}

// argValueToString converts an argument value to a string for matching.
// JSON arguments can be strings, numbers, booleans, or nested objects.
// Strings are used as-is; other types are formatted with %v.
func argValueToString(val interface{}) string {
	switch v := val.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
