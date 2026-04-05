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
//   - Plugin: An OpenCode Plugin (TypeScript/JS module or npm package).
//     Hooks into agent lifecycle events (tool.execute.before, tool.execute.after, etc.).
//     Mounted into .opencode/plugins/ or installed from npm.

// CapabilityType identifies the kind of capability
// +kubebuilder:validation:Enum=Container;MCP;Skill;Plugin
type CapabilityType string

const (
	CapabilityTypeContainer CapabilityType = "Container"
	CapabilityTypeMCP       CapabilityType = "MCP"
	CapabilityTypeSkill     CapabilityType = "Skill"
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

	// ToolRefs references OCI tool packages to load into the MCP server.
	// When specified with mode "server", the operator adds init containers to pull
	// each tool package (using crane) and the tool-bridge MCP server loads them
	// at startup. This enables sharing the same tool packages between PiAgent
	// (direct JS import) and OpenCode agents (via MCP bridge).
	//
	// Each tool package should export an AgentTool[] array from index.js.
	// Example refs: "ghcr.io/samyn92/agent-tools/git:0.1.0",
	//               "ghcr.io/samyn92/agent-tools/gitlab:0.1.0"
	//
	// When toolRefs is set and no Command is specified, the operator automatically
	// configures the tool-bridge as the MCP server command.
	// +optional
	ToolRefs []OCIArtifactRef `json:"toolRefs,omitempty"`
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

	// ServiceAccountName is the ServiceAccount for the MCP server pod.
	// Use this when the MCP server needs Kubernetes API access (e.g., kubectl, helm).
	// The SA should have appropriate RBAC permissions for the server's operations.
	// If not specified, the pod uses the namespace's default ServiceAccount.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Port is the port the SSE bridge listens on inside the server pod.
	// Defaults to 8080. The operator creates a Service on this port.
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// Resources defines compute resources for the MCP server pod.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Workspace configures shared workspace access for the MCP server pod.
	// When enabled, the MCP server pod mounts a PVC at /data/workspace, giving
	// it filesystem access to the agent's working directory.
	//
	// This is essential for MCP servers that operate on the filesystem (e.g., git
	// operations that need to add/commit/push files created by the agent).
	//
	// The PVC is shared between the agent pod and the MCP server pod:
	//   - AccessMode is ReadWriteMany (RWX), requiring an RWX-capable storage class
	//     (e.g., NFS, CephFS, Longhorn RWX).
	//   - The operator creates the PVC and mounts it in both pods automatically.
	//   - For clusters without RWX storage, use mode "local" instead (the MCP server
	//     runs as a subprocess in the agent pod, inheriting its filesystem).
	// +optional
	Workspace *MCPServerWorkspace `json:"workspace,omitempty"`
}

// MCPServerWorkspace configures shared workspace storage for an MCP server Deployment.
// Unlike Container sidecars (which share the agent pod's in-pod volume), MCP server pods
// are separate Deployments and require a PVC with ReadWriteMany access for shared filesystem.
type MCPServerWorkspace struct {
	// Enabled provisions a shared workspace PVC for this MCP server.
	// The PVC is mounted at /data/workspace in both the MCP server pod and the agent pod.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Size of the shared workspace PVC (default: 10Gi).
	// +kubebuilder:default="10Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass is the storage class for the shared PVC.
	// Must support ReadWriteMany (RWX) access mode (e.g., NFS, CephFS, Longhorn RWX).
	// If not specified, the cluster's default storage class is used.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// MountPath is the path where the workspace is mounted in the MCP server container.
	// Defaults to "/data/workspace".
	// +kubebuilder:default="/data/workspace"
	// +optional
	MountPath string `json:"mountPath,omitempty"`
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
	// Exactly one of content, configMapRef, or ociRef must be specified.
	// +optional
	Content string `json:"content,omitempty"`

	// ConfigMapRef references a ConfigMap containing the SKILL.md content.
	// Exactly one of content, configMapRef, or ociRef must be specified.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`

	// OCIRef references an OCI artifact containing the SKILL.md content.
	// The artifact must conform to the Agent Skills OCI Artifacts spec
	// (application/vnd.agentskills.skill.v1 artifact type).
	// Exactly one of content, configMapRef, or ociRef must be specified.
	// +optional
	OCIRef *OCIArtifactRef `json:"ociRef,omitempty"`
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
	// Exactly one of code, configMapRef, package, or ociRef must be specified.
	// +optional
	Code string `json:"code,omitempty"`

	// ConfigMapRef references a ConfigMap containing the plugin source code.
	// Exactly one of code, configMapRef, package, or ociRef must be specified.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`

	// Package is an npm package name to install as a plugin.
	// Example: "@company/opencode-plugin-audit"
	// Exactly one of code, configMapRef, package, or ociRef must be specified.
	// +optional
	Package string `json:"package,omitempty"`

	// OCIRef references an OCI artifact containing the plugin source code.
	// The artifact should contain a single .ts/.js file as a layer.
	// Exactly one of code, configMapRef, package, or ociRef must be specified.
	// +optional
	OCIRef *OCIArtifactRef `json:"ociRef,omitempty"`
}

// =============================================================================
// SHARED TYPES
// =============================================================================

// OCIArtifactRef references an OCI artifact in a container registry.
// Used to source Skill, Tool, and Plugin content from OCI-compliant registries.
// Follows the Agent Skills as OCI Artifacts specification by Thomas Vitale.
//
// The artifact is pulled at reconciliation time and its content is extracted
// into the agent's ConfigMap, just like inline content or ConfigMap references.
// This enables versioned, signed, and discoverable distribution of agent capabilities.
type OCIArtifactRef struct {
	// Ref is the OCI artifact reference (e.g., "ghcr.io/org/skills/my-skill:1.0.0").
	// Follows the standard OCI reference format: <registry>/<repository>:<tag> or
	// <registry>/<repository>@<digest>.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Ref string `json:"ref"`

	// Digest is an optional content digest for immutable pinning.
	// When specified, the artifact is verified against this digest after pull.
	// Format: <algorithm>:<hex> (e.g., "sha256:abc123...").
	// If both tag (in ref) and digest are specified, digest takes precedence for verification.
	// +optional
	Digest string `json:"digest,omitempty"`

	// PullSecret references a Kubernetes Secret containing registry credentials.
	// The secret should contain a .dockerconfigjson key (type kubernetes.io/dockerconfigjson)
	// or individual username/password keys.
	// If not specified, the artifact is pulled anonymously (public registries).
	// +optional
	PullSecret *SecretKeySelector `json:"pullSecret,omitempty"`

	// Verify configures optional Cosign signature verification.
	// When specified, the artifact's signature is verified before the content is used.
	// If verification fails, the capability enters a Failed phase.
	// +optional
	Verify *OCIVerification `json:"verify,omitempty"`
}

// OCIVerification configures Cosign signature verification for an OCI artifact.
// Ensures supply-chain integrity by verifying the artifact was signed by a trusted party.
type OCIVerification struct {
	// Provider is the signature verification provider.
	// Currently only "cosign" is supported.
	// +kubebuilder:validation:Enum=cosign
	// +kubebuilder:default=cosign
	// +optional
	Provider string `json:"provider,omitempty"`

	// PublicKey references a Secret containing the Cosign public key (PEM format).
	// Used for key-based verification (cosign verify --key).
	// Exactly one of publicKey or keyless must be specified.
	// +optional
	PublicKey *SecretKeySelector `json:"publicKey,omitempty"`

	// Keyless configures keyless (OIDC-based) Cosign verification.
	// Verifies the artifact was signed by a specific identity via an OIDC issuer.
	// Exactly one of publicKey or keyless must be specified.
	// +optional
	Keyless *CosignKeylessVerification `json:"keyless,omitempty"`
}

// CosignKeylessVerification configures Cosign keyless verification using OIDC identity.
// This verifies the artifact was signed by a specific identity (e.g., a GitHub Actions workflow)
// via a trusted OIDC issuer (e.g., Fulcio + Sigstore).
type CosignKeylessVerification struct {
	// Issuer is the expected OIDC issuer URL.
	// Example: "https://token.actions.githubusercontent.com" for GitHub Actions.
	// +kubebuilder:validation:Required
	Issuer string `json:"issuer"`

	// Identity is the expected signing identity (email or URI).
	// Example: "https://github.com/org/repo/.github/workflows/release.yml@refs/tags/v1.0.0"
	// +kubebuilder:validation:Required
	Identity string `json:"identity"`
}

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
