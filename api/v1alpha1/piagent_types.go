package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// PIAGENT CRD
// =============================================================================
// PiAgent is a lightweight, workflow-optimized AI agent that runs as an on-demand
// Kubernetes Job using the pi-agent-core runtime. Unlike Agent (which creates an
// always-on Deployment with PVC, sidecars, and MCP), PiAgent is a definition only —
// it becomes a Job only when invoked by a WorkflowRun.
//
// Tools are TypeScript functions in the agent source, not sidecar containers.
// There is no persistent storage, no MCP protocol, no ACP — pure TypeScript control.
//
// PiAgent is purpose-built for structured, repeatable processes:
//   - PR classification and review
//   - Security scanning pipelines
//   - Incident response workflows
//   - Scheduled reporting tasks

// PiAgentSpec defines the desired state of PiAgent.
type PiAgentSpec struct {
	// Model is the AI model to use, in "provider/model" format.
	// The provider portion must match a name in the providers list.
	// Examples: "anthropic/claude-sonnet-4-20250514", "openai/gpt-4o"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+/[a-zA-Z0-9._-]+$`
	Model string `json:"model"`

	// Providers configures AI providers available to this agent.
	// At least one provider must be configured, and the provider referenced
	// in spec.model must exist in this list.
	// +kubebuilder:validation:MinItems=1
	Providers []ProviderConfig `json:"providers"`

	// Identity configures the agent's personality (display name, system prompt).
	// +optional
	Identity *IdentityConfig `json:"identity,omitempty"`

	// Source defines the agent's TypeScript code.
	// The source must export tools (AgentTool[]) and config ({ systemPrompt }).
	// Exactly one of oci, inline, or configMapRef must be set.
	// +kubebuilder:validation:Required
	Source PiAgentSource `json:"source"`

	// ==========================================================================
	// AGENT BEHAVIOR
	// ==========================================================================

	// ThinkingLevel controls the model's reasoning depth.
	// Higher levels use more tokens but produce better results for complex tasks.
	// +kubebuilder:validation:Enum=off;minimal;low;medium;high;xhigh
	// +kubebuilder:default=off
	// +optional
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	// ToolExecution controls whether tools run in parallel or sequentially.
	// Parallel execution is faster but may cause issues with tools that have side effects.
	// +kubebuilder:validation:Enum=parallel;sequential
	// +kubebuilder:default=parallel
	// +optional
	ToolExecution string `json:"toolExecution,omitempty"`

	// ==========================================================================
	// MODULAR TOOL PACKAGES
	// ==========================================================================

	// ToolRefs is a list of OCI artifacts containing reusable tool packages.
	// Each toolRef is pulled at Job runtime via an init container and extracted
	// into /tools/<name>/. The pi-runner scans /tools/*/index.js and merges
	// all exported AgentTool[] arrays with the agent's own tools.
	//
	// This enables a modular, composable tool ecosystem where agents declare
	// capabilities declaratively and tool packages are independently versioned.
	//
	// Example:
	//   toolRefs:
	//     - ref: ghcr.io/samyn92/agent-tools/git:0.1.0
	//     - ref: ghcr.io/samyn92/agent-tools/file:0.1.0
	// +optional
	ToolRefs []OCIArtifactRef `json:"toolRefs,omitempty"`

	// ==========================================================================
	// GIT WORKSPACES
	// ==========================================================================

	// WorkspaceRefs references GitWorkspaces to mount in the Job pod.
	// Each workspace is mounted as a volume at /workspaces/<repo-name>,
	// providing the PiAgent with pre-cloned, operator-managed Git repository
	// working copies. This eliminates the need to clone at Job startup,
	// resulting in faster startup and less bandwidth usage.
	//
	// When a Workflow creates a WorkflowRun, it can also dynamically create
	// ephemeral GitWorkspaces (e.g., for PR review of a specific ref) that
	// are cleaned up after the Job completes.
	//
	// Example:
	//   workspaceRefs:
	//     - name: platform-api
	//       access: readwrite
	// +optional
	WorkspaceRefs []WorkspaceRef `json:"workspaceRefs,omitempty"`

	// ==========================================================================
	// ENVIRONMENT
	// ==========================================================================

	// Env defines additional environment variables for the Job pod.
	// These are merged with the runner's standard env vars (MODEL_PROVIDER, PROMPT, etc.).
	// Use this for tool-specific configuration like API tokens, URLs, and git config.
	//
	// Supports both literal values and Kubernetes Secret references:
	//   env:
	//     - name: GITLAB_TOKEN
	//       valueFrom:
	//         secretKeyRef:
	//           name: gitlab-token
	//           key: token
	//     - name: GITLAB_URL
	//       value: "https://gitlab.example.com"
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// ==========================================================================
	// INFRASTRUCTURE
	// ==========================================================================

	// Resources defines compute resources for the Job pod.
	// If not set, uses the pi-runner image defaults.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName is the Kubernetes ServiceAccount for the Job pod.
	// Use this when tools need access to Kubernetes APIs (e.g., kubectl, helm).
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Image overrides the default pi-runner base image.
	// Default: ghcr.io/samyn92/pi-runner:latest
	// +optional
	Image string `json:"image,omitempty"`

	// Timeout is the maximum execution time for a single invocation.
	// The Job is terminated if it exceeds this duration.
	// +kubebuilder:default="5m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// PiAgentSource defines where the agent's TypeScript code comes from.
// Exactly one field must be set.
type PiAgentSource struct {
	// OCI references an OCI artifact containing the agent TypeScript module.
	// The artifact is pulled into the Job pod via an init container.
	// +optional
	OCI *OCIArtifactRef `json:"oci,omitempty"`

	// Inline contains the agent TypeScript code directly in the CRD.
	// Useful for simple agents that fit in a few hundred lines.
	// The code is mounted as a ConfigMap volume in the Job pod.
	// +optional
	Inline string `json:"inline,omitempty"`

	// ConfigMapRef references a ConfigMap containing the agent code.
	// The key should contain the TypeScript module.
	// +optional
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// =============================================================================
// STATUS
// =============================================================================

// PiAgentStatus defines the observed state of PiAgent.
type PiAgentStatus struct {
	// Phase is the current phase of the PiAgent definition.
	// Ready means the source is resolved and the agent can be invoked.
	// +kubebuilder:validation:Enum=Ready;Pending;Failed
	Phase PiAgentPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PiAgentPhase represents the phase of a PiAgent definition.
type PiAgentPhase string

const (
	// PiAgentPhaseReady means the source is resolved and the agent can be invoked.
	PiAgentPhaseReady PiAgentPhase = "Ready"

	// PiAgentPhasePending means the source has not yet been validated.
	PiAgentPhasePending PiAgentPhase = "Pending"

	// PiAgentPhaseFailed means the source resolution failed
	// (e.g., OCI artifact not found, signature verification failed).
	PiAgentPhaseFailed PiAgentPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PiAgent is the Schema for the piagents API.
// PiAgent defines a lightweight, workflow-optimized AI agent that runs as an
// on-demand Kubernetes Job using the pi-agent-core runtime.
type PiAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PiAgentSpec   `json:"spec,omitempty"`
	Status PiAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PiAgentList contains a list of PiAgent
type PiAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PiAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PiAgent{}, &PiAgentList{})
}
