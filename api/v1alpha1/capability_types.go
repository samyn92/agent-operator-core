package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// CAPABILITY CRD
// =============================================================================
// A Capability is a reusable extension that can be attached to Agents.
// Capabilities define what an agent CAN DO, including tools, permissions, and guardrails.
//
// Each Capability has a type that determines how it integrates with the agent:
//
//   - Container: Runs a sidecar container with a CLI tool (e.g., kubectl, gh, glab)
//     Each sidecar gets RBAC, secrets, and a capability-gateway for command execution.
//
//   - MCP: Configures an MCP (Model Context Protocol) server connection.
//     Can be local (subprocess) or remote (HTTP URL with optional OAuth).
//     Injected into the agent's opencode.json under the "mcp" key.
//
//   - Skill: An OpenCode Agent Skill (SKILL.md file with instructions).
//     Mounted into the agent pod at .opencode/skills/<name>/SKILL.md.
//     Loaded on-demand via the native skill tool.
//
//   - Tool: An OpenCode Custom Tool (TypeScript/JS).
//     Mounted into the agent pod at .opencode/tools/<name>.ts.
//     Auto-discovered by OpenCode's tool system.
//
//   - Plugin: An OpenCode Plugin (TypeScript/JS module or npm package).
//     Hooks into agent lifecycle events (tool.execute.before, tool.execute.after, etc.).
//     Mounted into .opencode/plugins/ or installed from npm.

// CapabilityType identifies the kind of capability
// +kubebuilder:validation:Enum=Container;MCP;Skill;Tool;Plugin
type CapabilityType string

const (
	CapabilityTypeContainer CapabilityType = "Container"
	CapabilityTypeMCP       CapabilityType = "MCP"
	CapabilityTypeSkill     CapabilityType = "Skill"
	CapabilityTypeTool      CapabilityType = "Tool"
	CapabilityTypePlugin    CapabilityType = "Plugin"
)

// CapabilitySpec defines the desired state of a Capability
type CapabilitySpec struct {
	// Type identifies the kind of capability.
	// Determines which sub-spec is active and how the capability integrates with the agent.
	// +kubebuilder:validation:Required
	Type CapabilityType `json:"type"`

	// Description explains what this capability provides.
	// Shown to the LLM to help it understand when to use this capability.
	// +kubebuilder:validation:Required
	Description string `json:"description"`

	// ==========================================================================
	// TYPE-SPECIFIC SUB-SPECS (only one active based on type)
	// ==========================================================================

	// Container configures a sidecar container capability.
	// Required when type is "Container".
	// +optional
	Container *ContainerCapabilitySpec `json:"container,omitempty"`

	// MCP configures an MCP server connection capability.
	// Required when type is "MCP".
	// +optional
	MCP *MCPCapabilitySpec `json:"mcp,omitempty"`

	// Skill configures an OpenCode Agent Skill capability.
	// Required when type is "Skill".
	// +optional
	Skill *SkillCapabilitySpec `json:"skill,omitempty"`

	// Tool configures an OpenCode Custom Tool capability.
	// Required when type is "Tool".
	// +optional
	Tool *ToolCapabilitySpec `json:"tool,omitempty"`

	// Plugin configures an OpenCode Plugin capability.
	// Required when type is "Plugin".
	// +optional
	Plugin *PluginCapabilitySpec `json:"plugin,omitempty"`

	// ==========================================================================
	// SHARED FIELDS (apply to all capability types)
	// ==========================================================================

	// Permissions defines the three-tier permission model for commands.
	// For Container type: controls command allow/approve/deny patterns.
	// For MCP type: controls which MCP tools can be invoked.
	// For Skill/Tool/Plugin: controls execution permissions.
	// +optional
	Permissions *CapabilityPermissions `json:"permissions,omitempty"`

	// RateLimit configures rate limiting for this capability.
	// +optional
	RateLimit *CapabilityRateLimit `json:"rateLimit,omitempty"`

	// Audit enables audit logging for capability usage.
	// +kubebuilder:default=false
	// +optional
	Audit bool `json:"audit,omitempty"`

	// Secrets are environment variables injected into the capability's context.
	// For Container type: injected into the sidecar container.
	// For MCP type: available as environment variables for local MCP servers.
	// The agent never sees these credentials.
	// +optional
	Secrets []SecretEnvVar `json:"secrets,omitempty"`

	// Resources defines compute resources for the capability.
	// Primarily used for Container type (sidecar resource limits).
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Instructions are additional context injected into the agent's prompt.
	// Use for usage tips, examples, or domain-specific guidance.
	// +optional
	Instructions string `json:"instructions,omitempty"`
}

// =============================================================================
// CONTAINER CAPABILITY
// =============================================================================

// ContainerCapabilitySpec configures a sidecar container capability.
// Runs a CLI tool in a sidecar container
// with isolated credentials, RBAC, and a capability-gateway for command execution.
// Sidecars share the agent's /data/workspace volume, making them ideal for
// file-operating tools (git, kubectl apply -f, terraform, helm).
type ContainerCapabilitySpec struct {
	// Image is the container image with the CLI tool (e.g., "bitnami/kubectl:1.30")
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// ServiceAccountName is the ServiceAccount for the sidecar container.
	// This SA should have the RBAC permissions needed by the tool.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// CommandPrefix is prepended to commands before execution.
	// For example, "kubectl " for kubectl, "gh " for GitHub CLI.
	// +optional
	CommandPrefix string `json:"commandPrefix,omitempty"`

	// Workspace configures a workspace volume for this capability.
	// Use this for capabilities that need persistent or ephemeral storage (e.g., git clones).
	// +optional
	Workspace *CapabilityWorkspace `json:"workspace,omitempty"`

	// Config provides capability-specific configuration.
	// Used for git repos, kubernetes namespaces, and other tool-specific settings.
	// +optional
	Config *CapabilityConfig `json:"config,omitempty"`

	// ContainerType identifies the kind of container tool for UI display.
	// +kubebuilder:validation:Enum=kubernetes;helm;github;gitlab;git;custom
	// +optional
	ContainerType string `json:"containerType,omitempty"`
}

// =============================================================================
// MCP CAPABILITY
// =============================================================================

// MCPCapabilitySpec configures an MCP (Model Context Protocol) server connection.
// MCP servers provide tools, resources, and prompts to the agent via a standardized protocol.
// Unlike inline Agent.spec.mcp, MCP Capabilities are reusable across agents.
//
// Three modes are supported:
//   - local: MCP server runs as a subprocess inside the agent pod (stdio transport).
//   - remote: Agent connects to an external, user-managed MCP server via HTTP URL.
//   - server: The operator deploys the MCP server as a standalone pod with a stdio-to-SSE
//     bridge (capability-gateway in MCP mode). Credentials stay in the server pod — the agent
//     only gets the Service URL. This gives you MCP's rich tool ecosystem with Container-level
//     credential isolation, without requiring shared filesystem access.
type MCPCapabilitySpec struct {
	// Mode is the MCP server connection mode.
	// "local" runs the MCP server as a subprocess (command-based).
	// "remote" connects to an external MCP server via HTTP URL.
	// "server" deploys the MCP server as an operator-managed pod with credential isolation.
	// +kubebuilder:validation:Enum=local;remote;server
	// +kubebuilder:validation:Required
	Mode string `json:"mode"`

	// Command is the command and args to run for local/server MCP servers.
	// Example: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"]
	// Required when mode is "local" or "server".
	// +optional
	Command []string `json:"command,omitempty"`

	// URL is the endpoint URL for remote MCP servers.
	// Example: "https://mcp.example.com/sse"
	// Required when mode is "remote".
	// For "server" mode, this is auto-populated by the operator — do not set it manually.
	// +optional
	URL string `json:"url,omitempty"`

	// Server configures the operator-managed MCP server deployment.
	// Only used when mode is "server".
	// +optional
	Server *MCPServerDeploymentSpec `json:"server,omitempty"`

	// Environment variables passed to the MCP server process.
	// For local mode: passed to the subprocess.
	// For server mode: injected into the MCP server pod (NOT the agent pod).
	// For remote mode: not applicable (use headers instead).
	// +optional
	Environment map[string]string `json:"environment,omitempty"`

	// Headers are custom HTTP headers sent with requests to remote MCP servers.
	// Useful for authentication tokens, API keys, etc.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// OAuth configures OAuth 2.0 authentication for remote MCP servers.
	// Supports dynamic client registration (RFC 7591).
	// +optional
	OAuth *MCPOAuthConfig `json:"oauth,omitempty"`

	// Timeout is the connection/request timeout in milliseconds.
	// +kubebuilder:default=10000
	// +optional
	Timeout *int32 `json:"timeout,omitempty"`

	// Enabled controls whether this MCP server is active.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// MCPServerDeploymentSpec configures the operator-managed MCP server deployment.
// When mode is "server", the operator creates a Deployment + Service that runs the
// MCP server with the capability-gateway as a stdio-to-SSE bridge. An init container
// copies the gateway binary into a shared emptyDir volume; the main container uses the
// user-specified image (for runtime dependencies) and runs the gateway binary, which
// spawns the MCP server command as a subprocess.
// The agent connects via the auto-generated Service URL.
type MCPServerDeploymentSpec struct {
	// Image is the container image for the MCP server.
	// Must contain the MCP server binary/runtime.
	// Example: "node:22-slim" (if using npx to run the server from Command)
	// Example: "mcp/gitlab" (if using a pre-built Docker image)
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Port is the port the SSE bridge listens on inside the server pod.
	// Defaults to 8080. The operator creates a Service on this port.
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// Resources defines compute resources for the MCP server pod.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// MCPOAuthConfig configures OAuth 2.0 for remote MCP servers.
type MCPOAuthConfig struct {
	// ClientIDSecret references a secret containing the OAuth client ID.
	// +kubebuilder:validation:Required
	ClientIDSecret SecretKeySelector `json:"clientIdSecret"`

	// ClientSecretSecret references a secret containing the OAuth client secret.
	// +kubebuilder:validation:Required
	ClientSecretSecret SecretKeySelector `json:"clientSecretSecret"`

	// Scope is the OAuth scope to request.
	// +optional
	Scope string `json:"scope,omitempty"`
}

// =============================================================================
// SKILL CAPABILITY (OpenCode SKILL.md)
// =============================================================================

// SkillCapabilitySpec configures an OpenCode Agent Skill.
// Skills are SKILL.md files with YAML frontmatter that provide specialized instructions
// and workflows. They are loaded on-demand via OpenCode's native skill tool.
type SkillCapabilitySpec struct {
	// Content is the inline SKILL.md content.
	// Should include YAML frontmatter with name and description.
	// Either content or configMapRef must be specified.
	// +optional
	Content string `json:"content,omitempty"`

	// ConfigMapRef references a ConfigMap containing the SKILL.md content.
	// Either content or configMapRef must be specified.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// =============================================================================
// TOOL CAPABILITY (OpenCode Custom Tool)
// =============================================================================

// ToolCapabilitySpec configures an OpenCode Custom Tool.
// Custom Tools are TypeScript/JS files that use the tool() helper from @opencode-ai/plugin.
// They are auto-discovered from .opencode/tools/ directory.
type ToolCapabilitySpec struct {
	// Code is the inline TypeScript/JavaScript source code for the tool.
	// Should export a default using tool() from @opencode-ai/plugin.
	// Either code or configMapRef must be specified.
	// +optional
	Code string `json:"code,omitempty"`

	// ConfigMapRef references a ConfigMap containing the tool source code.
	// Either code or configMapRef must be specified.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// =============================================================================
// PLUGIN CAPABILITY (OpenCode Plugin)
// =============================================================================

// PluginCapabilitySpec configures an OpenCode Plugin.
// Plugins hook into agent lifecycle events like tool.execute.before,
// tool.execute.after, shell.env, session.idle, etc.
type PluginCapabilitySpec struct {
	// Code is the inline TypeScript/JavaScript source code for the plugin.
	// Should export a default plugin function.
	// Either code, configMapRef, or package must be specified.
	// +optional
	Code string `json:"code,omitempty"`

	// ConfigMapRef references a ConfigMap containing the plugin source code.
	// Either code, configMapRef, or package must be specified.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`

	// Package is an npm package name to install as a plugin.
	// Example: "@company/opencode-plugin-audit"
	// Either code, configMapRef, or package must be specified.
	// +optional
	Package string `json:"package,omitempty"`
}

// =============================================================================
// SHARED TYPES
// =============================================================================

// ConfigMapKeyRef references a key in a ConfigMap
type ConfigMapKeyRef struct {
	// Name is the name of the ConfigMap
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key in the ConfigMap
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// CapabilityPermissions defines the three-tier permission model.
// Commands are evaluated in order: deny -> approve -> allow -> default deny
type CapabilityPermissions struct {
	// Allow lists command patterns that are automatically permitted.
	// Patterns use glob syntax (e.g., "get *", "describe pod *").
	// +optional
	Allow []string `json:"allow,omitempty"`

	// Approve lists command patterns that require human approval before execution.
	// When a command matches, the agent pauses and asks the user for permission.
	// +optional
	Approve []ApprovalRule `json:"approve,omitempty"`

	// Deny lists command patterns that are always blocked.
	// Deny is checked first and always takes precedence.
	// +optional
	Deny []string `json:"deny,omitempty"`
}

// ApprovalRule defines a command pattern that requires human approval.
type ApprovalRule struct {
	// Pattern is the glob pattern to match commands (e.g., "delete pod *").
	// +kubebuilder:validation:Required
	Pattern string `json:"pattern"`

	// Message is shown to the user when requesting approval.
	// Should explain what the command does and any risks.
	// +optional
	Message string `json:"message,omitempty"`

	// Severity indicates the risk level of the command.
	// Affects how the approval dialog is displayed in the UI.
	// +kubebuilder:validation:Enum=info;warning;critical
	// +kubebuilder:default=warning
	// +optional
	Severity string `json:"severity,omitempty"`

	// Timeout is how long to wait for approval before auto-denying (in seconds).
	// Default is 300 seconds (5 minutes).
	// +kubebuilder:default=300
	// +optional
	Timeout int32 `json:"timeout,omitempty"`
}

// CapabilityRateLimit configures rate limiting for a capability
type CapabilityRateLimit struct {
	// RequestsPerMinute is the maximum requests per minute.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=60
	// +optional
	RequestsPerMinute int32 `json:"requestsPerMinute,omitempty"`
}

// CapabilityWorkspace configures workspace storage for a capability.
// Use this for capabilities that need to store files (e.g., git clones, build artifacts).
type CapabilityWorkspace struct {
	// Enabled provisions a workspace volume for this capability.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Size of the workspace volume (default: 10Gi)
	// +kubebuilder:default="10Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// Persistent keeps the workspace across pod restarts.
	// If false (default), uses ephemeral storage (emptyDir).
	// If true, creates a PVC for the workspace.
	// +kubebuilder:default=false
	// +optional
	Persistent bool `json:"persistent,omitempty"`
}

// CapabilityConfig provides capability-specific configuration.
type CapabilityConfig struct {
	// Git configures git-specific settings.
	// +optional
	Git *GitConfig `json:"git,omitempty"`

	// Kubernetes configures kubernetes-specific settings.
	// +optional
	Kubernetes *KubernetesConfig `json:"kubernetes,omitempty"`

	// GitHub configures GitHub-specific settings.
	// +optional
	GitHub *GitHubConfig `json:"github,omitempty"`

	// GitLab configures GitLab-specific settings.
	// +optional
	GitLab *GitLabConfig `json:"gitlab,omitempty"`

	// Helm configures Helm-specific settings.
	// +optional
	Helm *HelmConfig `json:"helm,omitempty"`
}

// =============================================================================
// STATUS
// =============================================================================

// CapabilityStatus defines the observed state of Capability
type CapabilityStatus struct {
	// Phase is the current phase of the capability
	// +kubebuilder:validation:Enum=Pending;Ready;Failed
	Phase CapabilityPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// UsedBy lists agents currently using this capability
	// +optional
	UsedBy []string `json:"usedBy,omitempty"`
}

// CapabilityPhase represents the phase of a capability
type CapabilityPhase string

const (
	CapabilityPhasePending CapabilityPhase = "Pending"
	CapabilityPhaseReady   CapabilityPhase = "Ready"
	CapabilityPhaseFailed  CapabilityPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Capability is the Schema for the capabilities API.
// A Capability is a reusable extension that can be attached to Agents.
// Capabilities define what an agent CAN DO, including tools, permissions, and guardrails.
type Capability struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CapabilitySpec   `json:"spec,omitempty"`
	Status CapabilityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CapabilityList contains a list of Capability
type CapabilityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Capability `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Capability{}, &CapabilityList{})
}
