package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// HashConfigMapData computes a deterministic hash of a ConfigMap's data map.
// Previously used for the configmap-hash pod annotation to trigger rollouts.
// Retained as a utility for comparing ConfigMap data in reconciliation logic.
func HashConfigMapData(data map[string]string) string {
	raw, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:8]) // 16 hex chars for brevity
}

// SourceInfo contains resolved Source information for config generation
type SourceInfo struct {
	// Name is the source name
	Name string
	// Type is the source type (kubernetes, github, gitlab, git, custom)
	Type string
	// Description explains what the source provides
	Description string
	// ServiceURL is the resolved service URL (e.g., http://agent-kubectl.default.svc:8080)
	ServiceURL string
	// CommandPrefix is prepended to commands (e.g., "kubectl ")
	CommandPrefix string
	// Instructions are additional usage instructions
	Instructions string
	// Allow patterns for command validation
	Allow []string
	// Deny patterns for command validation
	Deny []string
	// ApproveRules are patterns that require human approval before execution
	ApproveRules []ApprovalRuleInfo
}

// ApprovalRuleInfo contains approval rule info for config generation
type ApprovalRuleInfo struct {
	Pattern  string
	Message  string
	Severity string
	Timeout  int32
}

// OpenCodeConfig represents the opencode.json configuration structure
type OpenCodeConfig struct {
	Model        string                           `json:"model"`
	Provider     map[string]OpenCodeProviderEntry `json:"provider,omitempty"`
	Server       ServerConfig                     `json:"server,omitempty"`
	Instructions []string                         `json:"instructions,omitempty"`
	Plugin       []string                         `json:"plugin,omitempty"`
	Tools        map[string]bool                  `json:"tools,omitempty"`
	Permission   map[string]interface{}           `json:"permission,omitempty"`
	Agent        map[string]AgentModeEntry        `json:"agent,omitempty"`
	MCP          map[string]MCPEntry              `json:"mcp,omitempty"`
}

// OpenCodeProviderEntry represents a provider configuration in opencode.json.
// Used for custom/local providers (Ollama, LM Studio, etc.) and for overriding
// cloud provider settings (e.g., custom baseURL for proxies).
type OpenCodeProviderEntry struct {
	NPM     string                        `json:"npm,omitempty"`
	Name    string                        `json:"name,omitempty"`
	Options *OpenCodeProviderOptions      `json:"options,omitempty"`
	Models  map[string]OpenCodeModelEntry `json:"models,omitempty"`
}

// OpenCodeProviderOptions represents provider connection options
type OpenCodeProviderOptions struct {
	BaseURL string            `json:"baseURL,omitempty"`
	APIKey  string            `json:"apiKey,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// OpenCodeModelEntry represents a model definition within a provider
type OpenCodeModelEntry struct {
	Name  string               `json:"name,omitempty"`
	Limit *OpenCodeModelLimits `json:"limit,omitempty"`
}

// OpenCodeModelLimits defines token limits for a model
type OpenCodeModelLimits struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// ServerConfig represents server settings for opencode serve
type ServerConfig struct {
	Port     int    `json:"port,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// AgentModeEntry represents an agent mode configuration
type AgentModeEntry struct {
	Mode        string   `json:"mode,omitempty"`
	MaxSteps    *int     `json:"maxSteps,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// MCPEntry represents an MCP server configuration
type MCPEntry struct {
	Type    string            `json:"type"`
	Command []string          `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

// TelemetryPluginCode is the embedded OpenCode telemetry plugin that sends
// tool execution traces to the Agent Operator Console
const TelemetryPluginCode = `/**
 * OpenCode Telemetry Plugin for Agent Operator Console
 * Captures tool execution traces and sends to console backend
 * Sends events both when tools START and when they COMPLETE for real-time UI
 */

const CONSOLE_URL = process.env.CONSOLE_TELEMETRY_URL || "http://agent-console.agent-system.svc/api/v1/telemetry/spans"
const ENABLED = process.env.TELEMETRY_ENABLED !== "false"

function randomHex(bytes) {
  const array = new Uint8Array(bytes)
  crypto.getRandomValues(array)
  return Array.from(array).map(b => b.toString(16).padStart(2, "0")).join("")
}

const spans = new Map()
let currentKey = null

async function sendTelemetry(data) {
  if (!ENABLED) return
  try {
    await fetch(CONSOLE_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    })
  } catch (e) { /* ignore send errors */ }
}

export const TelemetryPlugin = async ({ client }) => {
  if (!ENABLED) return {}
  
  return {
    "tool.execute.before": async (input, output) => {
      const key = input.tool + "-" + Date.now() + "-" + randomHex(4)
      currentKey = key
      const span = {
        traceId: randomHex(16),
        spanId: randomHex(8),
        startTime: Date.now(),
        tool: input.tool,
        args: { ...output.args },
      }
      spans.set(key, span)
      
      // Send START event immediately - UI shows spinner
      const attrs = { "tool.name": input.tool }
      for (const [k, v] of Object.entries(span.args)) {
        const s = typeof v === "string" ? v : JSON.stringify(v)
        if (s.length <= 500) attrs["tool.args." + k] = s
      }
      if (input.context?.sessionID) attrs["opencode.session_id"] = input.context.sessionID
      if (input.context?.messageID) attrs["opencode.message_id"] = input.context.messageID
      
      await sendTelemetry({
        traceId: span.traceId,
        spanId: span.spanId,
        name: "tool." + input.tool,
        startTimeUnixNano: String(span.startTime * 1000000),
        status: "running",
        attributes: attrs,
      })
    },
    
    "tool.execute.after": async (input, output) => {
      let span = currentKey ? spans.get(currentKey) : null
      if (!span) {
        for (const [k, s] of spans) {
          if (s.tool === input.tool) { span = s; currentKey = k; break }
        }
      }
      if (!span) return
      
      spans.delete(currentKey)
      currentKey = null
      
      const endTime = Date.now()
      const isError = output.error != null
      
      const attrs = { "tool.name": input.tool, "tool.duration_ms": String(endTime - span.startTime) }
      for (const [k, v] of Object.entries(span.args)) {
        const s = typeof v === "string" ? v : JSON.stringify(v)
        if (s.length <= 500) attrs["tool.args." + k] = s
      }
      if (isError) attrs["error.message"] = String(output.error)
      if (input.context?.sessionID) attrs["opencode.session_id"] = input.context.sessionID
      if (input.context?.messageID) attrs["opencode.message_id"] = input.context.messageID
      
      // Send COMPLETE event - UI shows success/error
      await sendTelemetry({
        traceId: span.traceId,
        spanId: span.spanId,
        name: "tool." + input.tool,
        startTimeUnixNano: String(span.startTime * 1000000),
        endTimeUnixNano: String(endTime * 1000000),
        durationMs: endTime - span.startTime,
        status: isError ? "error" : "ok",
        attributes: attrs,
      })
    },
  }
}

export default TelemetryPlugin
`

// GenerateSourceTool generates a TypeScript file for an OpenCode custom tool.
// This makes the source appear as a first-class tool to the LLM.
// Uses OpenCode's native ctx.ask() for permission prompts (Allow Once / Always Allow / Deny).
// The capability-gateway sidecar remains as a security backstop for deny-list enforcement.
func GenerateSourceTool(src SourceInfo) string {
	argName := "command"

	// Build the command construction logic
	commandExpr := fmt.Sprintf("args.%s", argName)
	if src.CommandPrefix != "" {
		commandExpr = fmt.Sprintf(`"%s" + args.%s`, src.CommandPrefix, argName)
	}

	// Escape any special characters in the description for JavaScript string
	description := strings.ReplaceAll(src.Description, "`", "\\`")
	description = strings.ReplaceAll(description, "$", "\\$")

	argDescription := fmt.Sprintf("The %s command to execute", src.Name)

	return fmt.Sprintf(`/**
 * OpenCode Custom Tool: %s
 * Auto-generated by Agent Operator
 * Uses OpenCode's native permission system (ctx.ask) for approval prompts.
 * The capability-gateway sidecar enforces deny-lists as a security backstop.
 */
import { tool } from "@opencode-ai/plugin"

const SERVICE_URL = "%s"

/**
 * Generate the "Always Allow" pattern from a command.
 * Uses BashArity-style logic: take the first 2 tokens (e.g. "kubectl get")
 * and append " *" so clicking "Always Allow" approves all similar commands.
 * Example: "kubectl get pods -A -o wide" -> "kubectl get *"
 */
function alwaysPattern(command: string): string {
  const tokens = command.trim().split(/\s+/)
  // For commands with a CLI prefix (like "kubectl"), arity is typically 2
  // meaning "kubectl get" is the meaningful prefix
  const prefixLen = Math.min(2, tokens.length)
  return tokens.slice(0, prefixLen).join(" ") + " *"
}

export default tool({
  description: `+"`%s`"+`,
  args: {
    %s: tool.schema.string().describe(`+"`%s`"+`),
  },
  async execute(args, ctx) {
    const command = %s

    // Ask OpenCode for permission — shows inline UI prompt with
    // "Allow Once / Always Allow / Deny" buttons on the tool call card.
    // OpenCode evaluates against permission rules in opencode.json.
    // If rules say "allow", this resolves immediately (no prompt).
    // If rules say "deny", this throws DeniedError (no prompt).
    // If rules say "ask" (or no rule matches), the user sees the prompt.
    await ctx.ask({
      permission: "%s",
      patterns: [command],
      always: [alwaysPattern(command)],
      metadata: { command },
    })

    // Execute via capability-gateway sidecar (security backstop).
    // The gateway enforces deny-lists and blocks shell metacharacters
    // even if the permission prompt was bypassed.
    const response = await fetch(SERVICE_URL + "/exec", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command }),
    })

    const result = await response.json()

    if (!response.ok) {
      throw new Error(result.error || response.statusText)
    }

    if (result.stderr && result.stderr.length > 0) {
      return result.stdout + "\n\nSTDERR:\n" + result.stderr
    }
    return result.stdout || ""
  },
})
`, src.Name, src.ServiceURL, description, argName, argDescription, commandExpr, src.Name)
}

// AgentConfigMap creates a ConfigMap for the agent
// sources contains resolved Container capability information to inject into systemPrompt
// mcpEntries contains resolved MCP capability entries for opencode.json
// skillFiles contains resolved Skill capability content (name -> SKILL.md content)
// toolFiles contains resolved Tool capability code (name -> TypeScript code)
// pluginFiles contains resolved Plugin capability code (name -> TypeScript code)
// pluginPackages contains npm package names for Plugin capabilities
func AgentConfigMap(agent *agentsv1alpha1.Agent, sources []SourceInfo, mcpEntries map[string]MCPEntry, skillFiles map[string]string, toolFiles map[string]string, pluginFiles map[string]string, pluginPackages []string) *corev1.ConfigMap {
	// Build opencode.json config
	// Note: Provider API keys are passed via environment variables (e.g., ANTHROPIC_API_KEY)
	// OpenCode auto-enables providers when their API keys are present
	config := OpenCodeConfig{
		Model: agent.Spec.Model,
		Server: ServerConfig{
			Port:     4096,
			Hostname: "0.0.0.0",
		},
		// Point to AGENTS.md in the workspace root for custom instructions
		Instructions: []string{"AGENTS.md"},
		// Load the telemetry plugin that sends tool traces to the console
		Plugin: []string{"./.opencode/plugins/telemetry.ts"},
	}

	// Add tools configuration if specified
	if agent.Spec.Tools != nil {
		config.Tools = make(map[string]bool)
		if agent.Spec.Tools.Bash != nil {
			config.Tools["bash"] = *agent.Spec.Tools.Bash
		}
		if agent.Spec.Tools.Write != nil {
			config.Tools["write"] = *agent.Spec.Tools.Write
		}
		if agent.Spec.Tools.Edit != nil {
			config.Tools["edit"] = *agent.Spec.Tools.Edit
		}
		if agent.Spec.Tools.Read != nil {
			config.Tools["read"] = *agent.Spec.Tools.Read
		}
		if agent.Spec.Tools.Glob != nil {
			config.Tools["glob"] = *agent.Spec.Tools.Glob
		}
		if agent.Spec.Tools.Grep != nil {
			config.Tools["grep"] = *agent.Spec.Tools.Grep
		}
		if agent.Spec.Tools.WebFetch != nil {
			config.Tools["webfetch"] = *agent.Spec.Tools.WebFetch
		}
		if agent.Spec.Tools.Task != nil {
			config.Tools["task"] = *agent.Spec.Tools.Task
		}
		// Remove empty map
		if len(config.Tools) == 0 {
			config.Tools = nil
		}
	}

	// Add permissions configuration if specified
	if agent.Spec.Permissions != nil {
		config.Permission = make(map[string]interface{})

		// Helper to add permission rule
		// If only default is set with no patterns, output as string (e.g., "bash": "allow")
		// If patterns are set, output as object (e.g., "bash": {"*": "deny", "git *": "allow"})
		addPermissionRule := func(name string, rule *agentsv1alpha1.PermissionRule) {
			if rule == nil {
				return
			}
			// If no patterns, just output the default as a string
			if len(rule.Patterns) == 0 && rule.Default != "" {
				config.Permission[name] = rule.Default
				return
			}
			// Otherwise build an object with patterns
			permMap := make(map[string]string)
			if rule.Default != "" {
				permMap["*"] = rule.Default
			}
			for pattern, perm := range rule.Patterns {
				permMap[pattern] = perm
			}
			if len(permMap) > 0 {
				config.Permission[name] = permMap
			}
		}

		addPermissionRule("bash", agent.Spec.Permissions.Bash)
		addPermissionRule("edit", agent.Spec.Permissions.Edit)
		addPermissionRule("read", agent.Spec.Permissions.Read)
		addPermissionRule("write", agent.Spec.Permissions.Write)
		addPermissionRule("webfetch", agent.Spec.Permissions.WebFetch)
		addPermissionRule("glob", agent.Spec.Permissions.Glob)
		addPermissionRule("grep", agent.Spec.Permissions.Grep)
		addPermissionRule("task", agent.Spec.Permissions.Task)

		// Remove empty map
		if len(config.Permission) == 0 {
			config.Permission = nil
		}
	}

	// Add security configuration if specified
	if agent.Spec.Security != nil {
		if config.Permission == nil {
			config.Permission = make(map[string]interface{})
		}
		if agent.Spec.Security.ExternalDirectory != "" {
			config.Permission["external_directory"] = agent.Spec.Security.ExternalDirectory
		}
		if agent.Spec.Security.DoomLoop != "" {
			config.Permission["doom_loop"] = agent.Spec.Security.DoomLoop
		}
		// Wire protectedPaths as deny rules for file-access tools.
		// OpenCode's PermissionNext.evaluate() uses findLast(), so deny patterns
		// appended last get highest priority — they override any allow rules.
		if len(agent.Spec.Security.ProtectedPaths) > 0 {
			for _, tool := range []string{"read", "edit", "write"} {
				config.Permission[tool] = mergeProtectedPaths(
					config.Permission[tool], agent.Spec.Security.ProtectedPaths,
				)
			}
		}
	}

	// Add agent mode configuration if specified
	if agent.Spec.Agent != nil {
		entry := AgentModeEntry{
			Mode:        agent.Spec.Agent.Mode,
			MaxSteps:    agent.Spec.Agent.MaxSteps,
			Temperature: agent.Spec.Agent.Temperature,
		}
		// Only add if at least one field is set
		if entry.Mode != "" || entry.MaxSteps != nil || entry.Temperature != nil {
			config.Agent = map[string]AgentModeEntry{
				"build": entry,
			}
		}
	}

	// Add MCP servers configuration — merge inline Agent.spec.mcp with MCP Capabilities
	if len(agent.Spec.MCP) > 0 || len(mcpEntries) > 0 {
		config.MCP = make(map[string]MCPEntry)
		// Inline MCP servers from Agent spec
		for name, server := range agent.Spec.MCP {
			config.MCP[name] = MCPEntry{
				Type:    server.Type,
				Command: server.Command,
				URL:     server.URL,
				Env:     server.Env,
				Enabled: server.Enabled,
			}
		}
		// MCP Capability entries (override inline if same name)
		for name, entry := range mcpEntries {
			config.MCP[name] = entry
		}
	}

	// Add additional providers configuration.
	// Cloud providers (anthropic, openai, google) auto-discover from env vars
	// and only need a config entry if they have custom settings (e.g., baseURL proxy).
	// Custom providers (ollama, lm-studio, etc.) always need a config entry with
	// npm package, baseURL, and model definitions.
	if len(agent.Spec.Providers) > 0 {
		config.Provider = make(map[string]OpenCodeProviderEntry)
		for _, p := range agent.Spec.Providers {
			entry := buildProviderEntry(p)
			if entry != nil {
				config.Provider[p.Name] = *entry
			}
		}
		if len(config.Provider) == 0 {
			config.Provider = nil
		}
	}

	// Generate custom tool files for Container capability sources
	// These become first-class tools in OpenCode via auto-discovery from tools/ directory
	toolCodeMap := make(map[string]string)
	for _, src := range sources {
		toolCode := GenerateSourceTool(src)
		if toolCode != "" {
			toolCodeMap[src.Name+".ts"] = toolCode

			// Generate OpenCode permission rules for this tool.
			// These rules are evaluated by PermissionNext.evaluate() when the tool calls ctx.ask().
			// Allow patterns -> auto-approve (no prompt), deny -> auto-reject, ask -> show UI prompt.
			if len(src.Allow) > 0 || len(src.Deny) > 0 || len(src.ApproveRules) > 0 {
				if config.Permission == nil {
					config.Permission = make(map[string]interface{})
				}
				config.Permission[src.Name] = buildSourcePermission(src)
			}
		}
	}

	// Add Tool capability files (custom OpenCode tools from Capability CRDs)
	for name, code := range toolFiles {
		toolCodeMap[name+".ts"] = code
	}

	// Add Plugin capability packages to the plugin list
	for _, pkg := range pluginPackages {
		config.Plugin = append(config.Plugin, pkg)
	}

	// Add Plugin capability files (inline plugin code from Capability CRDs)
	pluginCodeMap := make(map[string]string)
	for name, code := range pluginFiles {
		pluginCodeMap[name+".ts"] = code
		// Register the plugin path in config
		config.Plugin = append(config.Plugin, "./.opencode/plugins/"+name+".ts")
	}

	// Add Skill capability files (SKILL.md content from Capability CRDs)
	skillContentMap := make(map[string]string)
	for name, content := range skillFiles {
		skillContentMap[name] = content
	}

	configJSON, _ := json.MarshalIndent(config, "", "  ")

	// Build AGENTS.md - OpenCode's standard file for custom instructions/system prompt
	agentsMD := buildAgentsMarkdown(agent, sources)

	// Build the ConfigMap data
	data := map[string]string{
		"opencode.json": string(configJSON),
		"AGENTS.md":     agentsMD,
		"telemetry.ts":  TelemetryPluginCode,
	}

	// Add tool files (Container capability tools + Tool capability code)
	for filename, code := range toolCodeMap {
		data["tool-"+filename] = code
	}

	// Add plugin files (Plugin capability inline code)
	for filename, code := range pluginCodeMap {
		data["plugin-"+filename] = code
	}

	// Add skill files (Skill capability SKILL.md content)
	for name, content := range skillContentMap {
		data["skill-"+name+"-SKILL.md"] = content
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name + "-config",
			Namespace: agent.Namespace,
			Labels:    commonLabels(agent),
		},
		Data: data,
	}
}

func buildAgentsMarkdown(agent *agentsv1alpha1.Agent, sources []SourceInfo) string {
	name := agent.Name
	systemPrompt := ""

	if agent.Spec.Identity != nil {
		if agent.Spec.Identity.Name != "" {
			name = agent.Spec.Identity.Name
		}
		systemPrompt = agent.Spec.Identity.SystemPrompt
	}

	if systemPrompt == "" {
		systemPrompt = fmt.Sprintf("You are %s, a helpful AI assistant.", name)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n%s\n", name, systemPrompt))

	// Document sources (they're first-class tools, just mention them)
	if len(sources) > 0 {
		sb.WriteString("\n## Tools\n\n")

		// Collect tool names for the routing instruction
		var toolNames []string
		for _, src := range sources {
			toolNames = append(toolNames, src.Name)
		}

		// Add explicit tool routing instruction
		sb.WriteString("**IMPORTANT**: The following custom tools run in dedicated sidecar containers. ")
		sb.WriteString("Their CLI commands (e.g., glab, helm, kubectl) are NOT available via the built-in `bash` tool. ")
		sb.WriteString("You MUST use the specific tool listed below to run those commands.\n\n")

		for _, src := range sources {
			sb.WriteString(fmt.Sprintf("### %s\n\n", src.Name))
			if src.Instructions != "" {
				sb.WriteString(src.Instructions)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// mergeProtectedPaths takes an existing permission value for a file-access tool (read/edit/write)
// and appends deny rules for each protected path. This ensures protectedPaths always override
// any existing allow rules, since OpenCode's findLast() gives later rules higher priority.
//
// The existing value can be:
//   - nil (no existing rules)
//   - string (e.g., "allow") — a simple default
//   - map[string]string (pattern-based rules)
//   - json.RawMessage (ordered rules from buildSourcePermission)
//
// Returns a json.RawMessage with ordered entries to preserve findLast() semantics.
func mergeProtectedPaths(existing interface{}, protectedPaths []string) json.RawMessage {
	var sb strings.Builder
	sb.WriteString("{")

	first := true
	writeEntry := func(pattern, action string) {
		if !first {
			sb.WriteString(",")
		}
		first = false
		keyJSON, _ := json.Marshal(pattern)
		valJSON, _ := json.Marshal(action)
		sb.WriteString(string(keyJSON))
		sb.WriteString(":")
		sb.WriteString(string(valJSON))
	}

	// Carry forward existing rules
	switch v := existing.(type) {
	case nil:
		// No existing rules — start with default "ask"
		writeEntry("*", "ask")
	case string:
		// Simple default like "allow" or "deny"
		writeEntry("*", v)
	case map[string]string:
		for pattern, action := range v {
			writeEntry(pattern, action)
		}
	case map[string]interface{}:
		for pattern, action := range v {
			if actionStr, ok := action.(string); ok {
				writeEntry(pattern, actionStr)
			}
		}
	case json.RawMessage:
		// Already ordered JSON — strip the outer braces and prepend
		raw := strings.TrimSpace(string(v))
		if len(raw) > 2 { // more than just "{}"
			inner := raw[1 : len(raw)-1] // strip { and }
			sb.WriteString(inner)
			first = false
		}
	}

	// Append deny rules for each protected path LAST (highest priority via findLast).
	// For file-access tools, the pattern is the file path itself.
	// Use glob-style: "/etc/secrets/*" denies anything under that path.
	for _, path := range protectedPaths {
		// Deny exact path
		writeEntry(path, "deny")
		// Deny everything under the path (if it looks like a directory)
		if !strings.Contains(path, "*") {
			writeEntry(path+"/*", "deny")
		}
	}

	sb.WriteString("}")
	return json.RawMessage(sb.String())
}

// buildSourcePermission generates OpenCode permission rules for a custom tool source.
// These rules are evaluated by OpenCode's PermissionNext.evaluate() when the tool calls ctx.ask().
//
// OpenCode's pattern matching (Wildcard.match):
//   - "*" matches everything (like ".*" in regex)
//   - "kubectl get *" matches "kubectl get pods", "kubectl get pods -A", etc.
//   - Trailing " *" becomes optional: "kubectl get *" also matches "kubectl get"
//
// OpenCode's rule evaluation (PermissionNext.evaluate):
//   - Uses findLast() — last matching rule wins
//   - Actions: "allow" (proceed silently), "deny" (throw DeniedError), "ask" (show UI prompt)
//   - Default when no rule matches: "ask"
//
// Strategy:
//  1. Default "*": "ask" FIRST (if no more-specific rule matches, user gets prompted)
//  2. Allow patterns → action "allow" (auto-approve matching commands)
//  3. Approve patterns → action "ask" (explicitly prompt user, even if allow would match)
//  4. Deny patterns → action "deny" LAST (override everything for dangerous commands)
//
// Since findLast() is used, ORDER MATTERS. We return json.RawMessage to preserve
// insertion order (Go maps randomize iteration order, which would break findLast semantics).
func buildSourcePermission(src SourceInfo) json.RawMessage {
	var sb strings.Builder
	sb.WriteString("{")

	first := true
	writeEntry := func(pattern, action string) {
		if !first {
			sb.WriteString(",")
		}
		first = false
		// JSON-encode the pattern key (handles special chars)
		keyJSON, _ := json.Marshal(pattern)
		valJSON, _ := json.Marshal(action)
		sb.WriteString(string(keyJSON))
		sb.WriteString(":")
		sb.WriteString(string(valJSON))
	}

	// 1. Default: ask for everything (safe default — user sees prompt for unrecognized commands)
	writeEntry("*", "ask")

	// 2. Allow patterns — auto-approve matching commands (no prompt shown).
	// Capability CRD patterns don't include the prefix (e.g., "get *" not "kubectl get *"),
	// so we prepend CommandPrefix to match what ctx.ask() sends as patterns.
	for _, pattern := range src.Allow {
		fullPattern := pattern
		if src.CommandPrefix != "" {
			fullPattern = src.CommandPrefix + pattern
		}
		writeEntry(fullPattern, "allow")
	}

	// 3. Approve patterns — explicitly prompt user (show UI with Allow/Deny).
	// These override allow patterns for commands that need human review.
	// Placed between allow and deny so deny still takes highest priority.
	for _, rule := range src.ApproveRules {
		fullPattern := rule.Pattern
		if src.CommandPrefix != "" {
			fullPattern = src.CommandPrefix + rule.Pattern
		}
		writeEntry(fullPattern, "ask")
	}

	// 4. Deny patterns — auto-reject matching commands (DeniedError thrown, no prompt).
	// These come LAST because findLast() gives them highest priority — they override
	// any allow pattern that might also match the same command.
	for _, pattern := range src.Deny {
		fullPattern := pattern
		if src.CommandPrefix != "" {
			fullPattern = src.CommandPrefix + pattern
		}
		writeEntry(fullPattern, "deny")
	}

	sb.WriteString("}")
	return json.RawMessage(sb.String())
}

// isCloudProvider returns true if the provider name is a built-in cloud provider.
// Cloud providers auto-discover from env vars and don't need explicit config
// unless custom settings (baseURL, headers) are specified.
func isCloudProvider(name string) bool {
	switch name {
	case "anthropic", "openai", "google":
		return true
	}
	return false
}

// buildProviderEntry generates an OpenCode provider config entry for a ProviderConfig.
// Returns nil if the provider doesn't need a config entry (cloud provider with no custom settings).
func buildProviderEntry(p agentsv1alpha1.ProviderConfig) *OpenCodeProviderEntry {
	// Cloud providers only need config if they have custom settings
	if isCloudProvider(p.Name) && p.BaseURL == "" && len(p.Headers) == 0 {
		return nil
	}

	entry := &OpenCodeProviderEntry{}

	// Set display name
	if p.DisplayName != "" {
		entry.Name = p.DisplayName
	}

	// Set npm package for custom providers
	if p.NPM != "" {
		entry.NPM = p.NPM
	} else if !isCloudProvider(p.Name) {
		// Default to openai-compatible for non-cloud providers
		entry.NPM = "@ai-sdk/openai-compatible"
	}

	// Set options (baseURL, apiKey reference, headers)
	if p.BaseURL != "" || p.APIKeySecret != nil || len(p.Headers) > 0 {
		opts := &OpenCodeProviderOptions{}
		if p.BaseURL != "" {
			opts.BaseURL = p.BaseURL
		}
		// For providers with API keys, reference the env var using OpenCode's {env:VAR} syntax.
		// The actual env var is injected by deployment.go.
		if p.APIKeySecret != nil {
			envVarName := providerEnvVarName(p.Name)
			opts.APIKey = "{env:" + envVarName + "}"
		}
		if len(p.Headers) > 0 {
			opts.Headers = p.Headers
		}
		entry.Options = opts
	}

	// Set model definitions
	if len(p.Models) > 0 {
		entry.Models = make(map[string]OpenCodeModelEntry)
		for _, m := range p.Models {
			me := OpenCodeModelEntry{}
			if m.Name != "" {
				me.Name = m.Name
			} else {
				me.Name = m.ID
			}
			// Only emit limit when BOTH context and output are set.
			// OpenCode's config schema (Zod) requires both fields when limit
			// is present — partial() only makes the top-level limit key
			// optional, not its inner properties. If only contextLimit is
			// set in the CRD, omit limit entirely; OpenCode handles this
			// gracefully and the LLM gateway enforces its own limits.
			if m.ContextLimit != nil && m.OutputLimit != nil {
				me.Limit = &OpenCodeModelLimits{
					Context: *m.ContextLimit,
					Output:  *m.OutputLimit,
				}
			}
			entry.Models[m.ID] = me
		}
	}

	return entry
}

// providerEnvVarName returns the environment variable name for a provider's API key.
// E.g., "anthropic" -> "ANTHROPIC_API_KEY", "my-provider" -> "MY_PROVIDER_API_KEY"
func providerEnvVarName(providerName string) string {
	name := strings.ToUpper(providerName)
	name = strings.ReplaceAll(name, "-", "_")
	return name + "_API_KEY"
}

func commonLabels(agent *agentsv1alpha1.Agent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "agent",
		"app.kubernetes.io/instance":   agent.Name,
		"app.kubernetes.io/managed-by": "agent-operator",
	}
}
