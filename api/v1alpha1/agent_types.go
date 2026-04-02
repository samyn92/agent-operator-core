package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSpec defines the desired state of Agent
// Agent is a pure AI workload - it exposes an HTTP API and doesn't know who calls it.
// Use Channel CRDs to connect Agents to external platforms (Telegram, Slack, GitHub, etc.)
type AgentSpec struct {
	// Model is the AI model to use, in "provider/model" format.
	// The provider portion must match a name in the providers list.
	// Examples: "anthropic/claude-sonnet-4-20250514", "dnabot-prod/Kimi-K2.5", "ollama/qwen2.5-coder"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+/[a-zA-Z0-9._-]+$`
	Model string `json:"model"`

	// Providers configures AI providers available to this agent.
	// At least one provider must be configured, and the provider referenced
	// in spec.model must exist in this list.
	//
	// Cloud providers (anthropic, openai, google) only need name + apiKeySecret.
	// Custom providers (ollama, dnabot, lm-studio, etc.) need baseURL and model definitions.
	// Local providers (ollama without auth) can omit apiKeySecret entirely.
	//
	// Example — cloud provider:
	//   - name: anthropic
	//     apiKeySecret: {name: anthropic-key, key: api-key}
	//
	// Example — custom on-premise provider:
	//   - name: dnabot-prod
	//     baseURL: "https://gateway.example.com/v1"
	//     npm: "@ai-sdk/openai-compatible"
	//     models:
	//       - id: Kimi-K2.5
	//         name: "Kimi K2.5"
	//         contextLimit: 262144
	//
	// +kubebuilder:validation:MinItems=1
	Providers []ProviderConfig `json:"providers"`

	// Identity configures the agent's personality
	// +optional
	Identity *IdentityConfig `json:"identity,omitempty"`

	// ==========================================================================
	// AGENT BEHAVIOR
	// ==========================================================================

	// Agent configures the OpenCode agent behavior
	// +optional
	Agent *AgentModeConfig `json:"agent,omitempty"`

	// Tools configures which built-in tools are available to the agent
	// +optional
	Tools *ToolsConfig `json:"tools,omitempty"`

	// Permissions configures tool permissions with pattern-based rules
	// +optional
	Permissions *PermissionsConfig `json:"permissions,omitempty"`

	// Security configures additional security settings
	// +optional
	Security *SecurityConfig `json:"security,omitempty"`

	// NetworkPolicy configures network-level security
	// +optional
	NetworkPolicy *NetworkPolicyConfig `json:"networkPolicy,omitempty"`

	// MCP configures inline MCP (Model Context Protocol) servers.
	// For reusable MCP configurations, use Capability CRDs with type: MCP instead.
	// +optional
	MCP map[string]MCPServerConfig `json:"mcp,omitempty"`

	// CapabilityRefs references Capabilities that this agent can use.
	// Capabilities are reusable extensions defined as separate CRDs.
	// Container capabilities run as sidecar containers in the Agent pod.
	// MCP capabilities inject MCP server configs into opencode.json.
	// Skill/Tool/Plugin capabilities mount files into the agent pod.
	// +optional
	CapabilityRefs []CapabilityRef `json:"capabilityRefs,omitempty"`

	// ==========================================================================
	// INFRASTRUCTURE
	// ==========================================================================

	// Storage configures persistent storage for the agent
	// +optional
	Storage *StorageConfig `json:"storage,omitempty"`

	// Resources defines compute resources for the agent pod
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Images configures container images
	// +optional
	Images *ImagesConfig `json:"images,omitempty"`

	// Logging configures OpenCode logging
	// +optional
	Logging *LoggingConfig `json:"logging,omitempty"`

	// AdditionalVolumes adds extra volumes to the agent pod
	// Use this to mount existing PVCs, ConfigMaps, Secrets, etc.
	// +optional
	AdditionalVolumes []corev1.Volume `json:"additionalVolumes,omitempty"`

	// AdditionalVolumeMounts adds extra volume mounts to the opencode container
	// These mounts are added alongside the default /data mount
	// +optional
	AdditionalVolumeMounts []corev1.VolumeMount `json:"additionalVolumeMounts,omitempty"`

	// EnvFrom injects environment variables from Secrets or ConfigMaps into the
	// main OpenCode container. Useful for Plugin capabilities that read config
	// from process.env (e.g., OAuth2 credentials for a token-injector plugin).
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
}

// =============================================================================
// AGENT BEHAVIOR CONFIGURATION
// =============================================================================

// AgentModeConfig configures the OpenCode agent behavior
type AgentModeConfig struct {
	// Mode is the agent mode (primary or subagent)
	// +kubebuilder:validation:Enum=primary;subagent
	// +kubebuilder:default=primary
	// +optional
	Mode string `json:"mode,omitempty"`

	// MaxSteps limits the number of agentic iterations
	// +optional
	MaxSteps *int `json:"maxSteps,omitempty"`

	// Temperature controls response randomness (0.0-1.0)
	// +optional
	Temperature *float64 `json:"temperature,omitempty"`
}

// ToolsConfig configures which built-in tools are available
type ToolsConfig struct {
	// Bash enables/disables the bash tool
	// +optional
	Bash *bool `json:"bash,omitempty"`

	// Write enables/disables the write tool
	// +optional
	Write *bool `json:"write,omitempty"`

	// Edit enables/disables the edit tool
	// +optional
	Edit *bool `json:"edit,omitempty"`

	// Read enables/disables the read tool
	// +optional
	Read *bool `json:"read,omitempty"`

	// Glob enables/disables the glob tool
	// +optional
	Glob *bool `json:"glob,omitempty"`

	// Grep enables/disables the grep tool
	// +optional
	Grep *bool `json:"grep,omitempty"`

	// WebFetch enables/disables the webfetch tool
	// +optional
	WebFetch *bool `json:"webfetch,omitempty"`

	// Task enables/disables the task tool (subagent spawning)
	// +optional
	Task *bool `json:"task,omitempty"`
}

// PermissionsConfig configures tool permissions with pattern-based rules
type PermissionsConfig struct {
	// Bash permission configuration
	// +optional
	Bash *PermissionRule `json:"bash,omitempty"`

	// Edit permission configuration
	// +optional
	Edit *PermissionRule `json:"edit,omitempty"`

	// Read permission configuration
	// +optional
	Read *PermissionRule `json:"read,omitempty"`

	// Write permission configuration
	// +optional
	Write *PermissionRule `json:"write,omitempty"`

	// WebFetch permission configuration
	// +optional
	WebFetch *PermissionRule `json:"webfetch,omitempty"`

	// Glob permission configuration
	// +optional
	Glob *PermissionRule `json:"glob,omitempty"`

	// Grep permission configuration
	// +optional
	Grep *PermissionRule `json:"grep,omitempty"`

	// Task permission configuration
	// +optional
	Task *PermissionRule `json:"task,omitempty"`
}

// PermissionRule defines granular permission with patterns
type PermissionRule struct {
	// Default permission when no pattern matches (ask/allow/deny)
	// +kubebuilder:validation:Enum=ask;allow;deny
	// +kubebuilder:default=ask
	// +optional
	Default string `json:"default,omitempty"`

	// Patterns maps glob/command patterns to permissions
	// Key: pattern (e.g., "git *", "*.env", "https://github.com/*")
	// Value: permission (ask/allow/deny)
	// +optional
	Patterns map[string]string `json:"patterns,omitempty"`
}

// SecurityConfig defines additional security settings
type SecurityConfig struct {
	// ExternalDirectory controls access outside working directory (ask/allow/deny)
	// +kubebuilder:validation:Enum=ask;allow;deny
	// +kubebuilder:default=deny
	// +optional
	ExternalDirectory string `json:"externalDirectory,omitempty"`

	// DoomLoop controls repeated identical tool calls (ask/allow/deny)
	// +kubebuilder:validation:Enum=ask;allow;deny
	// +kubebuilder:default=deny
	// +optional
	DoomLoop string `json:"doomLoop,omitempty"`

	// ProtectedPaths are paths that can never be accessed
	// Applied as deny rules to read/edit/write
	// +optional
	ProtectedPaths []string `json:"protectedPaths,omitempty"`
}

// NetworkPolicyConfig defines network-level security
type NetworkPolicyConfig struct {
	// Enabled controls whether to create a NetworkPolicy
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// AllowedEgress defines allowed outbound connections
	// +optional
	AllowedEgress []EgressRule `json:"allowedEgress,omitempty"`

	// DenyAll blocks all egress except explicitly allowed
	// +kubebuilder:default=true
	// +optional
	DenyAll bool `json:"denyAll,omitempty"`
}

// EgressRule defines an allowed egress destination
type EgressRule struct {
	// Host is the DNS name or IP
	Host string `json:"host"`

	// Port is the destination port
	Port int32 `json:"port"`
}

// MCPServerConfig configures an MCP server connection
type MCPServerConfig struct {
	// Type is the MCP server type (local, remote, sse)
	// +kubebuilder:validation:Enum=local;remote;sse
	Type string `json:"type"`

	// Command is the command to run for local MCP servers
	// +optional
	Command []string `json:"command,omitempty"`

	// URL is the URL for remote MCP servers
	// +optional
	URL string `json:"url,omitempty"`

	// Env is environment variables for the MCP server
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// Enabled controls whether the MCP server is enabled
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// =============================================================================
// CAPABILITY REFERENCES
// =============================================================================

// CapabilityRef references a Capability resource that this agent can use.
type CapabilityRef struct {
	// Name is the name of the Capability resource to reference.
	// The Capability must exist in the same namespace as the Agent.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Alias is an optional alternative name for this capability.
	// If provided, this name is used in the tool name shown to the LLM.
	// Useful when you want multiple agents to use the same capability with different names.
	// +optional
	Alias string `json:"alias,omitempty"`
}

// GitHubConfig provides GitHub-specific configuration.
type GitHubConfig struct {
	// Repositories to monitor or work with.
	// Format: "owner/repo" (e.g., "kubernetes/kubernetes")
	// +optional
	Repositories []string `json:"repositories,omitempty"`
}

// GitLabConfig provides GitLab-specific configuration.
type GitLabConfig struct {
	// Domain is the GitLab instance domain (e.g., "gitlab.example.com").
	// Defaults to "gitlab.com" if not specified.
	// +kubebuilder:default="gitlab.com"
	// +optional
	Domain string `json:"domain,omitempty"`

	// Projects to monitor or work with.
	// Format: "group/project" (e.g., "myorg/myapp")
	// +optional
	Projects []string `json:"projects,omitempty"`
}

// HelmConfig provides Helm-specific configuration.
type HelmConfig struct {
	// AllowedNamespaces restricts which namespaces the agent can view releases in.
	// If empty, all namespaces are allowed (subject to RBAC).
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`

	// AllowedReleases restricts which releases the agent can interact with.
	// Supports glob patterns (e.g., "myapp-*", "staging-*").
	// If empty, all releases are allowed (subject to RBAC).
	// +optional
	AllowedReleases []string `json:"allowedReleases,omitempty"`

	// AllowWrite enables write operations (install, upgrade, rollback, uninstall).
	// By default, only read operations are allowed (list, get, status, history).
	// +kubebuilder:default=false
	// +optional
	AllowWrite bool `json:"allowWrite,omitempty"`
}

// GitConfig provides git-specific configuration.
type GitConfig struct {
	// Repositories to pre-clone into the workspace.
	// These are cloned when the capability pod starts.
	// Agent can also clone additional repos on-demand.
	// +optional
	Repositories []RepositoryConfig `json:"repositories,omitempty"`

	// Author configures the git author for commits.
	// +optional
	Author *GitAuthor `json:"author,omitempty"`
}

// RepositoryConfig defines a git repository.
type RepositoryConfig struct {
	// URL is the repository URL (e.g., "github.com/myorg/myapp")
	// +kubebuilder:validation:Required
	URL string `json:"url"`
}

// GitAuthor configures git commit author information.
type GitAuthor struct {
	// Name is the author name for commits.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Email is the author email for commits.
	// +kubebuilder:validation:Required
	Email string `json:"email"`
}

// KubernetesConfig provides kubernetes-specific configuration.
type KubernetesConfig struct {
	// AllowedNamespaces restricts which namespaces the capability can access.
	// If empty, all namespaces are allowed (subject to RBAC).
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
}

// SecretEnvVar defines an environment variable from a secret
type SecretEnvVar struct {
	// Name is the environment variable name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// ValueFrom references the secret key
	// +kubebuilder:validation:Required
	ValueFrom SecretKeySelector `json:"valueFrom"`
}

// =============================================================================
// INFRASTRUCTURE CONFIGURATION
// =============================================================================

// ImagesConfig configures container images for the agent.
// On managed platforms that enforce image registry restrictions, all images
// must be pulled through the platform's approved container registry proxy.
type ImagesConfig struct {
	// OpenCode is the OpenCode runtime image
	// +optional
	OpenCode string `json:"opencode,omitempty"`

	// Init is the image used by the init-config container.
	// Defaults to the OpenCode image when not set. Override only if using
	// a custom init image that doesn't need pre-cached npm packages.
	// +optional
	Init string `json:"init,omitempty"`

	// Gateway is the capability-gateway image (default: ghcr.io/anomalyco/capability-gateway:latest)
	// +optional
	Gateway string `json:"gateway,omitempty"`

	// PullPolicy is the image pull policy
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// LoggingConfig configures OpenCode logging
type LoggingConfig struct {
	// Level is the log level (DEBUG, INFO, WARN, ERROR)
	// +kubebuilder:validation:Enum=DEBUG;INFO;WARN;ERROR
	// +kubebuilder:default=INFO
	// +optional
	Level string `json:"level,omitempty"`

	// Enabled controls whether logs are printed to stderr
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// StorageConfig configures persistent storage
type StorageConfig struct {
	// Size is the storage size (e.g., "5Gi")
	// +kubebuilder:default="5Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass is the storage class to use
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
}

// =============================================================================
// PROVIDER CONFIGURATION
// =============================================================================

// ProviderConfig configures an AI provider.
//
// Supports three patterns:
//
// 1. Cloud provider (anthropic, openai, google) — only needs name + API key:
//
//	name: anthropic
//	apiKeySecret: {name: my-secret, key: api-key}
//
// 2. Custom provider (on-premise gateway, proxy) — needs baseURL, models, optionally auth:
//
//	name: dnabot-prod
//	baseURL: "https://gateway.example.com/v1"
//	npm: "@ai-sdk/openai-compatible"
//	models:
//	  - id: Kimi-K2.5
//	    contextLimit: 262144
//
// 3. Local provider (ollama, lm-studio) — just baseURL and models, no auth needed:
//
//	name: ollama
//	baseURL: "http://ollama.infra.svc:11434/v1"
//	models:
//	  - id: qwen2.5-coder
type ProviderConfig struct {
	// Name is the provider identifier.
	// For cloud providers, use: anthropic, openai, google.
	// For custom/local providers, use any unique identifier (e.g., "dnabot-prod", "ollama").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// APIKeySecret references a secret containing the API key.
	// Required for cloud providers (anthropic, openai, google).
	// Optional for custom/local providers that use alternative auth (e.g., OAuth2 via envFrom).
	// +optional
	APIKeySecret *SecretKeySelector `json:"apiKeySecret,omitempty"`

	// BaseURL is the API endpoint URL for custom/local providers.
	// Examples: "http://ollama.infra.svc:11434/v1", "https://gateway.example.com/v1"
	// Not needed for cloud providers — OpenCode uses their default endpoints.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// DisplayName is a human-readable name shown in the UI.
	// If not set, defaults to the Name field.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// NPM is the AI SDK package to use for this provider.
	// Defaults to "@ai-sdk/openai-compatible" for custom providers.
	// Not needed for built-in cloud providers (anthropic, openai, google).
	// +optional
	NPM string `json:"npm,omitempty"`

	// Models defines the models available from this provider.
	// Required for custom providers so OpenCode knows what models are available.
	// Not needed for cloud providers — OpenCode auto-discovers their models.
	// +optional
	Models []ModelDefinition `json:"models,omitempty"`

	// Headers are custom HTTP headers sent with each request to this provider.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`
}

// ModelDefinition defines a model available from a custom provider.
type ModelDefinition struct {
	// ID is the model identifier used in the model string (e.g., "qwen2.5-coder").
	// Referenced as "provider-name/model-id" (e.g., "ollama/qwen2.5-coder").
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// Name is the human-readable display name for this model.
	// +optional
	Name string `json:"name,omitempty"`

	// ContextLimit is the maximum input context length in tokens.
	// +optional
	ContextLimit *int `json:"contextLimit,omitempty"`

	// OutputLimit is the maximum output length in tokens.
	// +optional
	OutputLimit *int `json:"outputLimit,omitempty"`
}

// SecretKeySelector references a key in a Secret
type SecretKeySelector struct {
	// Name is the name of the secret
	Name string `json:"name"`
	// Key is the key in the secret
	Key string `json:"key"`
}

// IdentityConfig defines the agent's personality
type IdentityConfig struct {
	// Name is the agent's display name
	// +optional
	Name string `json:"name,omitempty"`

	// SystemPrompt is the system prompt for the agent
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// TLSConfig configures TLS settings
type TLSConfig struct {
	// SecretName is the name of the TLS secret
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ClusterIssuer is the cert-manager ClusterIssuer to use
	// +optional
	ClusterIssuer string `json:"clusterIssuer,omitempty"`
}

// =============================================================================
// STATUS
// =============================================================================

// AgentStatus defines the observed state of Agent
type AgentStatus struct {
	// Phase is the current phase of the agent
	// +kubebuilder:validation:Enum=Pending;Running;Failed
	Phase AgentPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ServiceURL is the internal service URL for other components to call this agent
	// Format: http://<name>.<namespace>.svc.cluster.local:4096
	ServiceURL string `json:"serviceURL,omitempty"`

	// ReadyReplicas is the number of ready pod replicas
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// AgentPhase represents the phase of an agent
type AgentPhase string

const (
	AgentPhasePending AgentPhase = "Pending"
	AgentPhaseRunning AgentPhase = "Running"
	AgentPhaseFailed  AgentPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is the Schema for the agents API
// Agent is a pure AI workload that exposes an HTTP API on port 4096.
// Connect Agents to external platforms using Channel CRDs.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
