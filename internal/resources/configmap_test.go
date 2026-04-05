package resources

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// =============================================================================
// HELPERS
// =============================================================================

// minimalAgent creates a minimal valid Agent for testing
func minimalAgent(name string) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "anthropic/claude-sonnet-4-20250514",
			Providers: []agentsv1alpha1.ProviderConfig{
				{
					Name: "anthropic",
					APIKeySecret: &agentsv1alpha1.SecretKeySelector{
						Name: "api-key",
						Key:  "key",
					},
				},
			},
		},
	}
}

// parseOpenCodeConfig extracts and parses opencode.json from a ConfigMap
func parseOpenCodeConfig(t *testing.T, data map[string]string) OpenCodeConfig {
	t.Helper()
	raw, ok := data["opencode.json"]
	if !ok {
		t.Fatal("ConfigMap missing opencode.json key")
	}
	var config OpenCodeConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("failed to parse opencode.json: %s", err)
	}
	return config
}

// =============================================================================
// AgentConfigMap — BASIC STRUCTURE TESTS
// =============================================================================

func TestAgentConfigMap_BasicStructure(t *testing.T) {
	agent := minimalAgent("test-agent")

	cm := AgentConfigMap(agent, nil, nil, nil, nil)

	if cm.Name != "test-agent-config" {
		t.Fatalf("expected name 'test-agent-config', got %q", cm.Name)
	}
	if cm.Namespace != "default" {
		t.Fatalf("expected namespace 'default', got %q", cm.Namespace)
	}

	// Must have core keys
	for _, key := range []string{"opencode.json", "AGENTS.md", "telemetry.ts"} {
		if _, ok := cm.Data[key]; !ok {
			t.Errorf("missing required key %q", key)
		}
	}

	config := parseOpenCodeConfig(t, cm.Data)

	if config.Model != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("expected model, got %q", config.Model)
	}
	if config.Server.Port != 4096 {
		t.Fatalf("expected port 4096, got %d", config.Server.Port)
	}
	if config.Server.Hostname != "0.0.0.0" {
		t.Fatalf("expected hostname 0.0.0.0, got %q", config.Server.Hostname)
	}
	if len(config.Instructions) != 1 || config.Instructions[0] != "AGENTS.md" {
		t.Fatalf("expected instructions [AGENTS.md], got %v", config.Instructions)
	}
	// Telemetry plugin is always in the plugin list
	if len(config.Plugin) < 1 || config.Plugin[0] != "./.opencode/plugins/telemetry.ts" {
		t.Fatalf("expected telemetry plugin path, got %v", config.Plugin)
	}
}

func TestAgentConfigMap_Labels(t *testing.T) {
	agent := minimalAgent("test-agent")
	cm := AgentConfigMap(agent, nil, nil, nil, nil)

	expected := map[string]string{
		"app.kubernetes.io/name":       "agent",
		"app.kubernetes.io/instance":   "test-agent",
		"app.kubernetes.io/managed-by": "agent-operator",
	}
	for k, v := range expected {
		if cm.Labels[k] != v {
			t.Errorf("label %q: expected %q, got %q", k, v, cm.Labels[k])
		}
	}
}

// =============================================================================
// AgentConfigMap — MCP MERGING TESTS
// =============================================================================

func TestAgentConfigMap_InlineMCPServers(t *testing.T) {
	agent := minimalAgent("test-agent")
	enabled := true
	agent.Spec.MCP = map[string]agentsv1alpha1.MCPServerConfig{
		"filesystem": {
			Type:    "local",
			Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem"},
			Enabled: &enabled,
		},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if len(config.MCP) != 1 {
		t.Fatalf("expected 1 MCP entry, got %d", len(config.MCP))
	}
	fs, ok := config.MCP["filesystem"]
	if !ok {
		t.Fatal("expected MCP entry 'filesystem'")
	}
	if fs.Type != "local" {
		t.Fatalf("expected type 'local', got %q", fs.Type)
	}
	if len(fs.Command) != 3 || fs.Command[0] != "npx" {
		t.Fatalf("expected command, got %v", fs.Command)
	}
}

func TestAgentConfigMap_CapabilityMCPEntries(t *testing.T) {
	agent := minimalAgent("test-agent")

	mcpEntries := map[string]MCPEntry{
		"postgres": {
			Type:    "local",
			Command: []string{"npx", "-y", "@modelcontextprotocol/server-postgres"},
			Env:     map[string]string{"PG_HOST": "localhost"},
		},
	}

	cm := AgentConfigMap(agent, mcpEntries, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if len(config.MCP) != 1 {
		t.Fatalf("expected 1 MCP entry, got %d", len(config.MCP))
	}
	pg, ok := config.MCP["postgres"]
	if !ok {
		t.Fatal("expected MCP entry 'postgres'")
	}
	if pg.Env["PG_HOST"] != "localhost" {
		t.Fatalf("expected env PG_HOST=localhost, got %v", pg.Env)
	}
}

func TestAgentConfigMap_MCPMerging_CapabilityOverridesInline(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.MCP = map[string]agentsv1alpha1.MCPServerConfig{
		"shared-server": {
			Type:    "local",
			Command: []string{"old-command"},
		},
	}

	mcpEntries := map[string]MCPEntry{
		"shared-server": {
			Type:    "local",
			Command: []string{"new-command"},
		},
	}

	cm := AgentConfigMap(agent, mcpEntries, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if len(config.MCP) != 1 {
		t.Fatalf("expected 1 MCP entry (merged), got %d", len(config.MCP))
	}
	entry := config.MCP["shared-server"]
	if len(entry.Command) != 1 || entry.Command[0] != "new-command" {
		t.Fatalf("expected capability MCP to override inline, got command %v", entry.Command)
	}
}

func TestAgentConfigMap_MCPMerging_MixedSources(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.MCP = map[string]agentsv1alpha1.MCPServerConfig{
		"inline-server": {
			Type:    "local",
			Command: []string{"inline-cmd"},
		},
	}

	mcpEntries := map[string]MCPEntry{
		"cap-server": {
			Type: "remote",
			URL:  "https://mcp.example.com",
		},
	}

	cm := AgentConfigMap(agent, mcpEntries, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if len(config.MCP) != 2 {
		t.Fatalf("expected 2 MCP entries (inline + capability), got %d", len(config.MCP))
	}
	if _, ok := config.MCP["inline-server"]; !ok {
		t.Fatal("missing inline MCP entry")
	}
	if _, ok := config.MCP["cap-server"]; !ok {
		t.Fatal("missing capability MCP entry")
	}
}

// =============================================================================
// AgentConfigMap — SKILL / PLUGIN FILE INJECTION TESTS
// =============================================================================

func TestAgentConfigMap_SkillFileInjection(t *testing.T) {
	agent := minimalAgent("test-agent")

	skillFiles := map[string]string{
		"incident-responder": "---\nname: incident-responder\n---\n# Incident Response Skill",
	}

	cm := AgentConfigMap(agent, nil, skillFiles, nil, nil)

	// Skill files are prefixed with "skill-" and suffixed with "-SKILL.md"
	skillContent, ok := cm.Data["skill-incident-responder-SKILL.md"]
	if !ok {
		t.Fatal("expected skill file 'skill-incident-responder-SKILL.md' in ConfigMap")
	}
	if !strings.Contains(skillContent, "Incident Response") {
		t.Fatal("skill content should contain actual content")
	}
}

func TestAgentConfigMap_PluginFileInjection(t *testing.T) {
	agent := minimalAgent("test-agent")

	pluginFiles := map[string]string{
		"audit-log": `const plugin = (api) => { api.hook("tool.execute.before", async () => {}) }; export default plugin`,
	}

	cm := AgentConfigMap(agent, nil, nil, pluginFiles, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	// Plugin files are prefixed with "plugin-" and suffixed with ".ts"
	pluginCode, ok := cm.Data["plugin-audit-log.ts"]
	if !ok {
		t.Fatal("expected plugin file 'plugin-audit-log.ts' in ConfigMap")
	}
	if !strings.Contains(pluginCode, "tool.execute.before") {
		t.Fatal("plugin code should contain hook")
	}

	// Plugin path should be in config.Plugin list
	found := false
	for _, p := range config.Plugin {
		if p == "./.opencode/plugins/audit-log.ts" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected plugin path in config.Plugin, got %v", config.Plugin)
	}
}

func TestAgentConfigMap_PluginPackageInjection(t *testing.T) {
	agent := minimalAgent("test-agent")

	pluginPackages := []string{"@company/opencode-plugin-audit", "@company/opencode-plugin-metrics"}

	cm := AgentConfigMap(agent, nil, nil, nil, pluginPackages)
	config := parseOpenCodeConfig(t, cm.Data)

	// Plugin packages should be in config.Plugin list (after telemetry)
	if len(config.Plugin) < 3 {
		t.Fatalf("expected at least 3 plugins (telemetry + 2 packages), got %d: %v", len(config.Plugin), config.Plugin)
	}
	found := map[string]bool{}
	for _, p := range config.Plugin {
		found[p] = true
	}
	if !found["@company/opencode-plugin-audit"] {
		t.Fatal("missing plugin package @company/opencode-plugin-audit")
	}
	if !found["@company/opencode-plugin-metrics"] {
		t.Fatal("missing plugin package @company/opencode-plugin-metrics")
	}
}

// =============================================================================
// AgentConfigMap — AGENTS.MD GENERATION
// =============================================================================

func TestAgentConfigMap_AgentsMD_DefaultIdentity(t *testing.T) {
	agent := minimalAgent("my-agent")

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	agentsMD := cm.Data["AGENTS.md"]

	if !strings.Contains(agentsMD, "# my-agent") {
		t.Fatal("expected agent name as heading")
	}
	if !strings.Contains(agentsMD, "You are my-agent, a helpful AI assistant.") {
		t.Fatal("expected default system prompt")
	}
}

func TestAgentConfigMap_AgentsMD_CustomIdentity(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Identity = &agentsv1alpha1.IdentityConfig{
		Name:         "SRE Bot",
		SystemPrompt: "You are an expert SRE engineer.",
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	agentsMD := cm.Data["AGENTS.md"]

	if !strings.Contains(agentsMD, "# SRE Bot") {
		t.Fatal("expected custom name in heading")
	}
	if !strings.Contains(agentsMD, "You are an expert SRE engineer.") {
		t.Fatal("expected custom system prompt")
	}
}

// =============================================================================
// AgentConfigMap — TOOLS CONFIGURATION
// =============================================================================

func TestAgentConfigMap_ToolsConfig(t *testing.T) {
	agent := minimalAgent("test-agent")
	bashEnabled := true
	writeDisabled := false
	agent.Spec.Tools = &agentsv1alpha1.ToolsConfig{
		Bash:  &bashEnabled,
		Write: &writeDisabled,
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Tools == nil {
		t.Fatal("expected tools config")
	}
	if v, ok := config.Tools["bash"]; !ok || v != true {
		t.Fatalf("expected bash=true, got %v", config.Tools["bash"])
	}
	if v, ok := config.Tools["write"]; !ok || v != false {
		t.Fatalf("expected write=false, got %v", config.Tools["write"])
	}
	// Unset tools should not appear
	if _, ok := config.Tools["read"]; ok {
		t.Fatal("expected read to be absent (not configured)")
	}
}

func TestAgentConfigMap_NilToolsConfig(t *testing.T) {
	agent := minimalAgent("test-agent")
	// No tools config

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Tools != nil {
		t.Fatalf("expected nil tools when not configured, got %v", config.Tools)
	}
}

// =============================================================================
// AgentConfigMap — PERMISSIONS CONFIGURATION
// =============================================================================

func TestAgentConfigMap_PermissionsConfig_SimpleDefault(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Permissions = &agentsv1alpha1.PermissionsConfig{
		Bash: &agentsv1alpha1.PermissionRule{Default: "allow"},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Permission == nil {
		t.Fatal("expected permission config")
	}
	// Simple default is stored as string
	bashPerm, ok := config.Permission["bash"]
	if !ok {
		t.Fatal("expected bash permission")
	}
	if bashPerm != "allow" {
		t.Fatalf("expected bash permission 'allow', got %v", bashPerm)
	}
}

func TestAgentConfigMap_PermissionsConfig_WithPatterns(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Permissions = &agentsv1alpha1.PermissionsConfig{
		Bash: &agentsv1alpha1.PermissionRule{
			Default: "deny",
			Patterns: map[string]string{
				"git *":  "allow",
				"npm *":  "allow",
				"rm -rf": "deny",
			},
		},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	bashPerm, ok := config.Permission["bash"]
	if !ok {
		t.Fatal("expected bash permission")
	}
	// With patterns, stored as map
	permMap, ok := bashPerm.(map[string]interface{})
	if !ok {
		t.Fatalf("expected bash permission to be a map, got %T", bashPerm)
	}
	if permMap["*"] != "deny" {
		t.Fatalf("expected default deny, got %v", permMap["*"])
	}
	if permMap["git *"] != "allow" {
		t.Fatalf("expected git allow, got %v", permMap["git *"])
	}
}

// =============================================================================
// AgentConfigMap — SECURITY CONFIGURATION
// =============================================================================

func TestAgentConfigMap_SecurityConfig_ProtectedPaths(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Security = &agentsv1alpha1.SecurityConfig{
		ProtectedPaths: []string{"/etc/secrets", "/var/credentials"},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Permission == nil {
		t.Fatal("expected permission config from protected paths")
	}
	// Protected paths should generate deny rules for read, edit, write
	for _, tool := range []string{"read", "edit", "write"} {
		perm, ok := config.Permission[tool]
		if !ok {
			t.Fatalf("expected %s permission from protected paths", tool)
		}
		raw, err := json.Marshal(perm)
		if err != nil {
			t.Fatalf("failed to marshal %s permission: %s", tool, err)
		}
		permStr := string(raw)
		if !strings.Contains(permStr, "/etc/secrets") {
			t.Fatalf("expected /etc/secrets in %s permissions, got %s", tool, permStr)
		}
		if !strings.Contains(permStr, "deny") {
			t.Fatalf("expected deny in %s permissions, got %s", tool, permStr)
		}
	}
}

func TestAgentConfigMap_SecurityConfig_ExternalDirectory(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Security = &agentsv1alpha1.SecurityConfig{
		ExternalDirectory: "deny",
		DoomLoop:          "ask",
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Permission["external_directory"] != "deny" {
		t.Fatalf("expected external_directory=deny, got %v", config.Permission["external_directory"])
	}
	if config.Permission["doom_loop"] != "ask" {
		t.Fatalf("expected doom_loop=ask, got %v", config.Permission["doom_loop"])
	}
}

// =============================================================================
// AgentConfigMap — AGENT MODE CONFIGURATION
// =============================================================================

func TestAgentConfigMap_AgentModeConfig(t *testing.T) {
	agent := minimalAgent("test-agent")
	maxSteps := 50
	temp := 0.7
	agent.Spec.Agent = &agentsv1alpha1.AgentModeConfig{
		Mode:        "primary",
		MaxSteps:    &maxSteps,
		Temperature: &temp,
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Agent == nil {
		t.Fatal("expected agent mode config")
	}
	buildEntry, ok := config.Agent["build"]
	if !ok {
		t.Fatal("expected 'build' agent entry")
	}
	if buildEntry.Mode != "primary" {
		t.Fatalf("expected mode 'primary', got %q", buildEntry.Mode)
	}
	if buildEntry.MaxSteps == nil || *buildEntry.MaxSteps != 50 {
		t.Fatalf("expected maxSteps=50, got %v", buildEntry.MaxSteps)
	}
	if buildEntry.Temperature == nil || *buildEntry.Temperature != 0.7 {
		t.Fatalf("expected temperature=0.7, got %v", buildEntry.Temperature)
	}
}

// =============================================================================
// AgentConfigMap — PROVIDERS CONFIGURATION
// =============================================================================

func TestAgentConfigMap_CustomProvider(t *testing.T) {
	agent := minimalAgent("test-agent")
	agent.Spec.Providers = []agentsv1alpha1.ProviderConfig{
		{
			Name:    "ollama",
			BaseURL: "http://ollama.infra.svc:11434/v1",
			Models: []agentsv1alpha1.ModelDefinition{
				{ID: "qwen2.5-coder", Name: "Qwen 2.5 Coder"},
			},
		},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	if config.Provider == nil {
		t.Fatal("expected provider config")
	}
	ollama, ok := config.Provider["ollama"]
	if !ok {
		t.Fatal("expected 'ollama' provider entry")
	}
	if ollama.NPM != "@ai-sdk/openai-compatible" {
		t.Fatalf("expected default npm package for custom provider, got %q", ollama.NPM)
	}
	if ollama.Options == nil || ollama.Options.BaseURL != "http://ollama.infra.svc:11434/v1" {
		t.Fatal("expected baseURL in options")
	}
	if len(ollama.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(ollama.Models))
	}
}

func TestAgentConfigMap_CloudProviderNoConfig(t *testing.T) {
	agent := minimalAgent("test-agent")
	// Cloud provider without custom settings should be omitted
	agent.Spec.Providers = []agentsv1alpha1.ProviderConfig{
		{Name: "openai", APIKeySecret: &agentsv1alpha1.SecretKeySelector{Name: "openai-key", Key: "key"}},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	// Cloud provider without baseURL/headers should not appear in config
	// (auto-discovered from env var)
	if config.Provider != nil {
		t.Fatalf("expected nil provider config for cloud provider without custom settings, got %v", config.Provider)
	}
}

func TestAgentConfigMap_ModelLimitOnlyContext(t *testing.T) {
	// When only contextLimit is set (no outputLimit), limit should be omitted
	// entirely because OpenCode's Zod schema requires both fields when limit
	// is present.
	agent := minimalAgent("test-agent")
	ctx := 262144
	agent.Spec.Providers = []agentsv1alpha1.ProviderConfig{
		{
			Name:    "custom",
			BaseURL: "http://llm.svc:8080/v1",
			Models: []agentsv1alpha1.ModelDefinition{
				{ID: "my-model", Name: "My Model", ContextLimit: &ctx},
			},
		},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	model, ok := config.Provider["custom"].Models["my-model"]
	if !ok {
		t.Fatal("expected model entry")
	}
	if model.Limit != nil {
		t.Fatalf("expected nil limit when only contextLimit is set, got %+v", model.Limit)
	}
}

func TestAgentConfigMap_ModelLimitBothSet(t *testing.T) {
	// When both contextLimit and outputLimit are set, limit should be emitted
	agent := minimalAgent("test-agent")
	ctx := 200000
	out := 65536
	agent.Spec.Providers = []agentsv1alpha1.ProviderConfig{
		{
			Name:    "custom",
			BaseURL: "http://llm.svc:8080/v1",
			Models: []agentsv1alpha1.ModelDefinition{
				{ID: "my-model", Name: "My Model", ContextLimit: &ctx, OutputLimit: &out},
			},
		},
	}

	cm := AgentConfigMap(agent, nil, nil, nil, nil)
	config := parseOpenCodeConfig(t, cm.Data)

	model, ok := config.Provider["custom"].Models["my-model"]
	if !ok {
		t.Fatal("expected model entry")
	}
	if model.Limit == nil {
		t.Fatal("expected limit when both context and output are set")
	}
	if model.Limit.Context != 200000 {
		t.Fatalf("expected context=200000, got %d", model.Limit.Context)
	}
	if model.Limit.Output != 65536 {
		t.Fatalf("expected output=65536, got %d", model.Limit.Output)
	}
}

// =============================================================================
// HashConfigMapData TESTS
// =============================================================================

func TestHashConfigMapData_Deterministic(t *testing.T) {
	data := map[string]string{
		"opencode.json": `{"model":"test"}`,
		"AGENTS.md":     "# Test",
	}

	hash1 := HashConfigMapData(data)
	hash2 := HashConfigMapData(data)

	if hash1 != hash2 {
		t.Fatalf("expected deterministic hash, got %q and %q", hash1, hash2)
	}
	if len(hash1) != 16 {
		t.Fatalf("expected 16-char hex hash, got %d chars: %q", len(hash1), hash1)
	}
}

func TestHashConfigMapData_DifferentDataDifferentHash(t *testing.T) {
	data1 := map[string]string{"key": "value1"}
	data2 := map[string]string{"key": "value2"}

	hash1 := HashConfigMapData(data1)
	hash2 := HashConfigMapData(data2)

	if hash1 == hash2 {
		t.Fatal("expected different hashes for different data")
	}
}

// =============================================================================
// mergeProtectedPaths TESTS
// =============================================================================

func TestMergeProtectedPaths_NilExisting(t *testing.T) {
	raw := mergeProtectedPaths(nil, []string{"/etc/secrets"})
	permStr := string(raw)

	// Should start with default "ask" and end with deny for protected path
	if !strings.Contains(permStr, `"*":"ask"`) {
		t.Fatalf("expected default ask, got %s", permStr)
	}
	if !strings.Contains(permStr, `"/etc/secrets":"deny"`) {
		t.Fatalf("expected deny for path, got %s", permStr)
	}
	if !strings.Contains(permStr, `"/etc/secrets/*":"deny"`) {
		t.Fatalf("expected deny for path/* glob, got %s", permStr)
	}
}

func TestMergeProtectedPaths_StringExisting(t *testing.T) {
	raw := mergeProtectedPaths("allow", []string{"/etc/secrets"})
	permStr := string(raw)

	if !strings.Contains(permStr, `"*":"allow"`) {
		t.Fatalf("expected carry-forward default allow, got %s", permStr)
	}
	if !strings.Contains(permStr, `"/etc/secrets":"deny"`) {
		t.Fatalf("expected deny for protected path, got %s", permStr)
	}
}

func TestMergeProtectedPaths_MapExisting(t *testing.T) {
	existing := map[string]string{"git *": "allow", "*": "ask"}
	raw := mergeProtectedPaths(existing, []string{"/secrets"})
	permStr := string(raw)

	if !strings.Contains(permStr, `"/secrets":"deny"`) {
		t.Fatalf("expected deny appended, got %s", permStr)
	}
	// Existing rules should be preserved (both git * and *)
	if !strings.Contains(permStr, `"allow"`) {
		t.Fatalf("expected existing allow rule preserved, got %s", permStr)
	}
}

func TestMergeProtectedPaths_GlobPatternNotDoubled(t *testing.T) {
	// If the path already contains a glob, don't add /path/*
	raw := mergeProtectedPaths(nil, []string{"/secrets/*.key"})
	permStr := string(raw)

	if !strings.Contains(permStr, `"/secrets/*.key":"deny"`) {
		t.Fatalf("expected deny for glob pattern, got %s", permStr)
	}
	// Should NOT add /secrets/*.key/*
	if strings.Contains(permStr, `"/secrets/*.key/*"`) {
		t.Fatalf("should not add /* suffix to glob patterns, got %s", permStr)
	}
}

// =============================================================================
// buildProviderEntry TESTS
// =============================================================================

func TestBuildProviderEntry_CustomProvider(t *testing.T) {
	p := agentsv1alpha1.ProviderConfig{
		Name:    "ollama",
		BaseURL: "http://ollama:11434/v1",
		Models: []agentsv1alpha1.ModelDefinition{
			{ID: "qwen2.5-coder", Name: "Qwen 2.5 Coder"},
		},
	}

	entry := buildProviderEntry(p)

	if entry == nil {
		t.Fatal("expected non-nil entry for custom provider")
	}
	if entry.NPM != "@ai-sdk/openai-compatible" {
		t.Fatalf("expected default NPM for custom provider, got %q", entry.NPM)
	}
	if entry.Options == nil || entry.Options.BaseURL != "http://ollama:11434/v1" {
		t.Fatal("expected baseURL")
	}
	if len(entry.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(entry.Models))
	}
	model := entry.Models["qwen2.5-coder"]
	if model.Name != "Qwen 2.5 Coder" {
		t.Fatalf("expected model name, got %q", model.Name)
	}
}

func TestBuildProviderEntry_CloudProviderNoCustomSettings(t *testing.T) {
	p := agentsv1alpha1.ProviderConfig{
		Name: "anthropic",
	}

	entry := buildProviderEntry(p)

	if entry != nil {
		t.Fatalf("expected nil entry for cloud provider without custom settings, got %+v", entry)
	}
}

func TestBuildProviderEntry_CloudProviderWithBaseURL(t *testing.T) {
	p := agentsv1alpha1.ProviderConfig{
		Name:    "openai",
		BaseURL: "https://proxy.example.com/v1",
	}

	entry := buildProviderEntry(p)

	if entry == nil {
		t.Fatal("expected non-nil entry for cloud provider with custom baseURL")
	}
	if entry.Options == nil || entry.Options.BaseURL != "https://proxy.example.com/v1" {
		t.Fatal("expected baseURL")
	}
}

func TestBuildProviderEntry_WithAPIKeySecret(t *testing.T) {
	p := agentsv1alpha1.ProviderConfig{
		Name:         "my-llm",
		BaseURL:      "http://local:8080/v1",
		APIKeySecret: &agentsv1alpha1.SecretKeySelector{Name: "my-secret", Key: "key"},
	}

	entry := buildProviderEntry(p)

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Options == nil || entry.Options.APIKey != "{env:MY_LLM_API_KEY}" {
		t.Fatalf("expected env reference, got %q", entry.Options.APIKey)
	}
}

func TestBuildProviderEntry_CustomNPM(t *testing.T) {
	p := agentsv1alpha1.ProviderConfig{
		Name:    "custom",
		NPM:     "@custom/ai-provider",
		BaseURL: "http://custom:8080/v1",
	}

	entry := buildProviderEntry(p)

	if entry.NPM != "@custom/ai-provider" {
		t.Fatalf("expected custom NPM, got %q", entry.NPM)
	}
}

func TestBuildProviderEntry_WithModelLimits(t *testing.T) {
	ctx := 128000
	out := 8192
	p := agentsv1alpha1.ProviderConfig{
		Name:    "custom",
		BaseURL: "http://custom:8080/v1",
		Models: []agentsv1alpha1.ModelDefinition{
			{ID: "my-model", Name: "My Model", ContextLimit: &ctx, OutputLimit: &out},
			{ID: "no-name"},
		},
	}

	entry := buildProviderEntry(p)

	myModel := entry.Models["my-model"]
	if myModel.Limit == nil {
		t.Fatal("expected limits for my-model")
	}
	if myModel.Limit.Context != 128000 {
		t.Fatalf("expected context 128000, got %d", myModel.Limit.Context)
	}
	if myModel.Limit.Output != 8192 {
		t.Fatalf("expected output 8192, got %d", myModel.Limit.Output)
	}

	// Model without name should use ID as name
	noName := entry.Models["no-name"]
	if noName.Name != "no-name" {
		t.Fatalf("expected model ID as fallback name, got %q", noName.Name)
	}
}

// =============================================================================
// isCloudProvider TESTS
// =============================================================================

func TestIsCloudProvider(t *testing.T) {
	cases := []struct {
		name     string
		expected bool
	}{
		{"anthropic", true},
		{"openai", true},
		{"google", true},
		{"ollama", false},
		{"lm-studio", false},
		{"custom", false},
	}
	for _, tc := range cases {
		if got := isCloudProvider(tc.name); got != tc.expected {
			t.Errorf("isCloudProvider(%q) = %v, want %v", tc.name, got, tc.expected)
		}
	}
}

// =============================================================================
// providerEnvVarName TESTS
// =============================================================================

func TestProviderEnvVarName(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"my-provider", "MY_PROVIDER_API_KEY"},
		{"lm-studio", "LM_STUDIO_API_KEY"},
	}
	for _, tc := range cases {
		if got := providerEnvVarName(tc.input); got != tc.expected {
			t.Errorf("providerEnvVarName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
