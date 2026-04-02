package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkflowSpec defines the desired state of Workflow
type WorkflowSpec struct {
	// Trigger defines what starts the workflow
	// +kubebuilder:validation:Required
	Trigger WorkflowTrigger `json:"trigger"`

	// ==========================================================================
	// SIMPLE MODE: Direct agent invocation (no steps needed)
	// ==========================================================================

	// Agent is the agent to invoke (simple mode - alternative to Steps)
	// When set, the workflow directly calls this agent with the Prompt.
	// +optional
	Agent string `json:"agent,omitempty"`

	// Prompt is the message to send to the agent (simple mode)
	// Supports templating with {{.trigger}} for trigger data.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// ==========================================================================
	// ADVANCED MODE: Multi-step orchestration
	// ==========================================================================

	// Steps are sequential agent invocations (advanced mode)
	// Use this for multi-step workflows that need conditional logic or multiple agents.
	// Either use (Agent + Prompt) OR Steps, not both.
	// +optional
	Steps []WorkflowStep `json:"steps,omitempty"`

	// ==========================================================================
	// OUTPUT & CONTROL
	// ==========================================================================

	// Output configures where to send workflow results
	// +optional
	Output *WorkflowOutput `json:"output,omitempty"`

	// Suspend stops the workflow from being triggered
	// +kubebuilder:default=false
	// +optional
	Suspend *bool `json:"suspend,omitempty"`
}

// WorkflowOutput configures where to send workflow results
type WorkflowOutput struct {
	// FromStep specifies which step's output to send (default: last step)
	// +optional
	FromStep string `json:"fromStep,omitempty"`

	// Notify sends output to a notification channel (for scheduled/manual workflows)
	// +optional
	Notify *NotifyOutput `json:"notify,omitempty"`

	// GitHub posts output back to GitHub (for PR/issue triggers)
	// +optional
	GitHub *GitHubOutput `json:"github,omitempty"`

	// GitLab posts output back to GitLab (for MR/issue triggers)
	// +optional
	GitLab *GitLabOutput `json:"gitlab,omitempty"`

	// Webhook sends output to an HTTP endpoint
	// +optional
	Webhook *WebhookOutput `json:"webhook,omitempty"`
}

// NotifyOutput sends workflow output to a notification channel
type NotifyOutput struct {
	// Telegram sends output via Telegram
	// +optional
	Telegram *TelegramOutput `json:"telegram,omitempty"`

	// Slack sends output via Slack webhook
	// +optional
	Slack *SlackOutput `json:"slack,omitempty"`
}

// TelegramOutput configures Telegram notification
type TelegramOutput struct {
	// BotTokenSecret references the secret containing the bot token
	// +kubebuilder:validation:Required
	BotTokenSecret SecretKeySelector `json:"botTokenSecret"`

	// ChatID is the Telegram chat/user ID to send to
	// +kubebuilder:validation:Required
	ChatID string `json:"chatId"`
}

// SlackOutput configures Slack notification
type SlackOutput struct {
	// WebhookSecret references the secret containing the Slack webhook URL
	// +kubebuilder:validation:Required
	WebhookSecret SecretKeySelector `json:"webhookSecret"`
}

// GitHubOutput configures posting results back to GitHub
type GitHubOutput struct {
	// Comment posts output as a PR/issue comment
	// +kubebuilder:default=true
	// +optional
	Comment *bool `json:"comment,omitempty"`

	// TokenSecret references the GitHub token for API access
	// If not set, uses the trigger's secret
	// +optional
	TokenSecret *SecretKeySelector `json:"tokenSecret,omitempty"`
}

// GitLabOutput configures posting results back to GitLab
type GitLabOutput struct {
	// Comment posts output as a MR/issue comment
	// +kubebuilder:default=true
	// +optional
	Comment *bool `json:"comment,omitempty"`

	// TokenSecret references the GitLab token for API access
	// If not set, uses the trigger's secret
	// +optional
	TokenSecret *SecretKeySelector `json:"tokenSecret,omitempty"`
}

// WebhookOutput sends workflow output to an HTTP endpoint
type WebhookOutput struct {
	// URL is the endpoint to POST the output to
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Secret references a secret for webhook authentication (sent as Authorization header)
	// +optional
	Secret *SecretKeySelector `json:"secret,omitempty"`
}

// WorkflowTrigger defines what can trigger a workflow
type WorkflowTrigger struct {
	// Webhook enables triggering via HTTP POST
	// +optional
	Webhook *WebhookTrigger `json:"webhook,omitempty"`

	// GitHub enables triggering from GitHub events
	// +optional
	GitHub *GitHubTrigger `json:"github,omitempty"`

	// GitLab enables triggering from GitLab events
	// +optional
	GitLab *GitLabTrigger `json:"gitlab,omitempty"`

	// Schedule enables cron-based triggering
	// +optional
	Schedule *ScheduleTrigger `json:"schedule,omitempty"`
}

// WebhookTrigger configures HTTP webhook triggering
type WebhookTrigger struct {
	// Path is the URL path for the webhook (default: /workflow/<name>)
	// +optional
	Path string `json:"path,omitempty"`

	// Secret is the reference to a secret for webhook validation
	// +optional
	Secret *SecretKeySelector `json:"secret,omitempty"`
}

// GitHubTrigger configures GitHub event triggering
type GitHubTrigger struct {
	// Events is the list of GitHub events to trigger on
	// Examples: push, pull_request, issues, issue_comment, pull_request_review
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Events []string `json:"events"`

	// Repos filters to specific repositories (optional, format: "owner/repo")
	// If empty, triggers on all repos that send webhooks
	// +optional
	Repos []string `json:"repos,omitempty"`

	// Branches filters to specific branches (only for push/pull_request events)
	// Supports glob patterns like "main", "release/*", "feature/**"
	// +optional
	Branches []string `json:"branches,omitempty"`

	// Actions filters to specific actions within an event
	// Examples for pull_request: opened, closed, synchronize, reopened
	// Examples for issues: opened, closed, labeled
	// +optional
	Actions []string `json:"actions,omitempty"`

	// Secret references the GitHub webhook secret for HMAC validation
	// +optional
	Secret *SecretKeySelector `json:"secret,omitempty"`
}

// GitLabTrigger configures GitLab event triggering
type GitLabTrigger struct {
	// Events is the list of GitLab events to trigger on
	// Examples: push, merge_request, issue, note, pipeline
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Events []string `json:"events"`

	// Projects filters to specific projects (optional, format: "group/project" or project ID)
	// If empty, triggers on all projects that send webhooks
	// +optional
	Projects []string `json:"projects,omitempty"`

	// Branches filters to specific branches (only for push/merge_request events)
	// Supports glob patterns like "main", "release/*"
	// +optional
	Branches []string `json:"branches,omitempty"`

	// Actions filters to specific actions within an event
	// Examples for merge_request: open, close, merge, update
	// +optional
	Actions []string `json:"actions,omitempty"`

	// Secret references the GitLab webhook token for validation
	// +optional
	Secret *SecretKeySelector `json:"secret,omitempty"`
}

// ScheduleTrigger configures cron-based triggering
type ScheduleTrigger struct {
	// Cron is the cron schedule expression
	// +kubebuilder:validation:Required
	Cron string `json:"cron"`

	// Timezone for the schedule (default: UTC)
	// +kubebuilder:default="UTC"
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// WorkflowStep defines a single step in the workflow
type WorkflowStep struct {
	// Name is the step identifier (auto-generated if not provided)
	// +optional
	Name string `json:"name,omitempty"`

	// Agent references the Agent to invoke
	// +kubebuilder:validation:Required
	Agent string `json:"agent"`

	// Prompt is the message to send to the agent
	// Supports templating with {{.trigger}} and {{.steps.<name>.output}}
	// +kubebuilder:validation:Required
	Prompt string `json:"prompt"`

	// Condition is a CEL expression that must evaluate to true for the step to run
	// Available variables: trigger, steps (map of previous step outputs)
	// +optional
	Condition string `json:"condition,omitempty"`

	// Timeout is the maximum time to wait for the step (default: 5m)
	// +kubebuilder:default="5m"
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// ContinueOnError allows the workflow to continue if this step fails
	// +kubebuilder:default=false
	// +optional
	ContinueOnError *bool `json:"continueOnError,omitempty"`
}

// WorkflowStatus defines the observed state of Workflow
type WorkflowStatus struct {
	// WebhookURL is the URL to trigger this workflow
	// +optional
	WebhookURL string `json:"webhookURL,omitempty"`

	// LastTriggered is the last time the workflow was triggered
	// +optional
	LastTriggered *metav1.Time `json:"lastTriggered,omitempty"`

	// RunCount is the total number of workflow runs
	// +optional
	RunCount int `json:"runCount,omitempty"`

	// LastRunStatus is the status of the last run
	// +optional
	LastRunStatus string `json:"lastRunStatus,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Steps",type=integer,JSONPath=`.spec.steps`
// +kubebuilder:printcolumn:name="Last Run",type=date,JSONPath=`.status.lastTriggered`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.runCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workflow is the Schema for the workflows API
type Workflow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkflowSpec   `json:"spec,omitempty"`
	Status WorkflowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkflowList contains a list of Workflow
type WorkflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workflow `json:"items"`
}

// WorkflowRun represents a single execution of a workflow
// This is a separate resource to track individual runs

// WorkflowRunSpec defines the desired state of WorkflowRun
type WorkflowRunSpec struct {
	// WorkflowRef references the Workflow being run
	// +kubebuilder:validation:Required
	WorkflowRef string `json:"workflowRef"`

	// TriggerData contains the data from the trigger event
	// +optional
	TriggerData string `json:"triggerData,omitempty"`
}

// WorkflowRunStatus defines the observed state of WorkflowRun
type WorkflowRunStatus struct {
	// Phase is the current phase of the run
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	Phase string `json:"phase,omitempty"`

	// StartTime is when the run started
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the run completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// CurrentStep is the index of the currently executing step
	// +optional
	CurrentStep int `json:"currentStep,omitempty"`

	// StepResults contains the results of each step
	// +optional
	StepResults []StepResult `json:"stepResults,omitempty"`

	// Error contains error message if the run failed
	// +optional
	Error string `json:"error,omitempty"`
}

// StepResult contains the result of a workflow step
type StepResult struct {
	// Name is the step name
	Name string `json:"name"`

	// Phase is the step status
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped
	Phase string `json:"phase"`

	// Output is the agent's response
	// +optional
	Output string `json:"output,omitempty"`

	// StartTime is when the step started
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the step completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Error contains error message if the step failed
	// +optional
	Error string `json:"error,omitempty"`

	// SessionID is the OpenCode session ID for async polling
	// +optional
	SessionID string `json:"sessionID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workflow",type=string,JSONPath=`.spec.workflowRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Step",type=integer,JSONPath=`.status.currentStep`
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkflowRun is the Schema for the workflowruns API
type WorkflowRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkflowRunSpec   `json:"spec,omitempty"`
	Status WorkflowRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkflowRunList contains a list of WorkflowRun
type WorkflowRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkflowRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workflow{}, &WorkflowList{})
	SchemeBuilder.Register(&WorkflowRun{}, &WorkflowRunList{})
}
