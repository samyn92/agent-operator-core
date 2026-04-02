package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChannelSpec defines the desired state of Channel
type ChannelSpec struct {
	// Type is the channel type (telegram, slack, discord, github, gitlab, webhook)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=telegram;slack;discord;github;gitlab;webhook
	Type string `json:"type"`

	// AgentRef references the Agent to forward messages to
	// +kubebuilder:validation:Required
	AgentRef string `json:"agentRef"`

	// Image is the channel container image
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// ImagePullPolicy is the image pull policy
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Replicas is the number of channel instances
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// ==========================================================================
	// CHANNEL-SPECIFIC CONFIGURATION
	// ==========================================================================

	// Telegram configuration (required when type=telegram)
	// +optional
	Telegram *TelegramChannelConfig `json:"telegram,omitempty"`

	// Slack configuration (required when type=slack)
	// +optional
	Slack *SlackChannelConfig `json:"slack,omitempty"`

	// GitHub configuration (required when type=github)
	// +optional
	GitHub *GitHubChannelConfig `json:"github,omitempty"`

	// GitLab configuration (required when type=gitlab)
	// +optional
	GitLab *GitLabChannelConfig `json:"gitlab,omitempty"`

	// Webhook configuration (for external access)
	// +optional
	Webhook *ChannelWebhookConfig `json:"webhook,omitempty"`

	// ==========================================================================
	// INFRASTRUCTURE
	// ==========================================================================

	// Resources defines compute resources for the channel pod
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName is the service account to use
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// TelegramChannelConfig configures a Telegram channel
type TelegramChannelConfig struct {
	// BotTokenSecret references the secret containing the Telegram bot token
	// +kubebuilder:validation:Required
	BotTokenSecret SecretKeySelector `json:"botTokenSecret"`

	// AllowedUsers is a list of Telegram user IDs that can interact with the bot
	// If empty, all users are allowed (not recommended for production)
	// +optional
	AllowedUsers []string `json:"allowedUsers,omitempty"`

	// AllowedChats is a list of Telegram chat IDs (groups/channels) that can interact
	// If empty, all chats are allowed (not recommended for production)
	// +optional
	AllowedChats []string `json:"allowedChats,omitempty"`
}

// SlackChannelConfig configures a Slack channel
type SlackChannelConfig struct {
	// BotTokenSecret references the secret containing the Slack bot token
	// +kubebuilder:validation:Required
	BotTokenSecret SecretKeySelector `json:"botTokenSecret"`

	// SigningSecret references the secret for verifying Slack requests
	// +optional
	SigningSecret *SecretKeySelector `json:"signingSecret,omitempty"`

	// AllowedChannels is a list of Slack channel IDs that can interact
	// +optional
	AllowedChannels []string `json:"allowedChannels,omitempty"`
}

// GitHubChannelConfig configures a GitHub webhook channel
type GitHubChannelConfig struct {
	// WebhookSecret references the secret for GitHub webhook HMAC validation
	// +optional
	WebhookSecret *SecretKeySelector `json:"webhookSecret,omitempty"`

	// TokenSecret references the GitHub token for API access (for posting comments, etc.)
	// +optional
	TokenSecret *SecretKeySelector `json:"tokenSecret,omitempty"`

	// Events is the list of GitHub events to accept
	// Examples: push, pull_request, issues, issue_comment, pull_request_review
	// If empty, all events are accepted
	// +optional
	Events []string `json:"events,omitempty"`

	// Actions filters to specific actions within events
	// Examples for pull_request: opened, closed, synchronize, reopened
	// +optional
	Actions []string `json:"actions,omitempty"`

	// Repos filters to specific repositories (format: "owner/repo")
	// If empty, accepts webhooks from all repos
	// +optional
	Repos []string `json:"repos,omitempty"`
}

// GitLabChannelConfig configures a GitLab webhook channel
type GitLabChannelConfig struct {
	// WebhookSecret references the secret for GitLab webhook token validation
	// +optional
	WebhookSecret *SecretKeySelector `json:"webhookSecret,omitempty"`

	// TokenSecret references the GitLab token for API access
	// +optional
	TokenSecret *SecretKeySelector `json:"tokenSecret,omitempty"`

	// Events is the list of GitLab events to accept
	// Examples: push, merge_request, issue, note, pipeline
	// If empty, all events are accepted
	// +optional
	Events []string `json:"events,omitempty"`

	// Actions filters to specific actions within events
	// Examples for merge_request: open, close, merge, update
	// +optional
	Actions []string `json:"actions,omitempty"`

	// Projects filters to specific projects (format: "group/project" or project ID)
	// If empty, accepts webhooks from all projects
	// +optional
	Projects []string `json:"projects,omitempty"`
}

// ChannelWebhookConfig configures external webhook access for the channel
type ChannelWebhookConfig struct {
	// Host is the hostname for the webhook (e.g., "telegram-bot.example.com")
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Path is the URL path for the webhook (default: /)
	// +optional
	Path string `json:"path,omitempty"`

	// TLS configures TLS for the webhook
	// +optional
	TLS *TLSConfig `json:"tls,omitempty"`

	// IngressClassName is the ingress class to use
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// Annotations to add to the ingress
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ChannelStatus defines the observed state of Channel
type ChannelStatus struct {
	// Phase is the current phase of the channel
	// +kubebuilder:validation:Enum=Pending;Ready;Failed
	Phase ChannelPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ServiceURL is the internal service URL for agents to connect
	ServiceURL string `json:"serviceURL,omitempty"`

	// WebhookURL is the external webhook URL (if configured)
	WebhookURL string `json:"webhookURL,omitempty"`

	// Replicas is the number of ready replicas
	Replicas int32 `json:"replicas,omitempty"`
}

// ChannelPhase represents the phase of a channel
type ChannelPhase string

const (
	ChannelPhasePending ChannelPhase = "Pending"
	ChannelPhaseReady   ChannelPhase = "Ready"
	ChannelPhaseFailed  ChannelPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.serviceURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Channel is the Schema for the channels API
// Channels are the bridge between external platforms (Telegram, Slack, etc.) and Agents
type Channel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChannelSpec   `json:"spec,omitempty"`
	Status ChannelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChannelList contains a list of Channel
type ChannelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Channel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Channel{}, &ChannelList{})
}
