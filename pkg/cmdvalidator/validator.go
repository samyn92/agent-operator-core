// Package cmdvalidator provides shell metacharacter blocking for command security.
// It blocks dangerous shell metacharacters to prevent injection attacks.
// Allow/deny pattern matching is handled by OpenCode's native permission system.
package cmdvalidator

import (
	"fmt"
	"strings"
)

// DangerousPatterns are shell metacharacters that could enable command injection
var DangerousPatterns = []string{
	";",    // Command chaining
	"&&",   // AND chaining
	"||",   // OR chaining
	"|",    // Pipe
	"`",    // Backtick command substitution
	"$(",   // Command substitution
	")",    // End of command substitution
	">",    // Redirect stdout
	">>",   // Append stdout
	"<",    // Redirect stdin
	"2>&1", // Redirect stderr
	"\n",   // Newline
	"\r",   // Carriage return
	"${",   // Variable expansion
}

// CheckShellMeta checks for dangerous shell metacharacters in a command.
// Returns an error string if dangerous patterns are found, empty string if safe.
func CheckShellMeta(cmd string) string {
	for _, p := range DangerousPatterns {
		if strings.Contains(cmd, p) {
			return fmt.Sprintf("dangerous shell metacharacter: %q", p)
		}
	}
	return ""
}
