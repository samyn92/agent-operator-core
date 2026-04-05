package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// GITREPO CRD
// =============================================================================
// A GitRepo declares a set of Git repositories from a single provider.
// It is the "registry" of repositories — the single source of truth for what
// repos exist and how to access them. GitRepos are metadata-only resources:
// they do NOT clone or store repository data. They resolve patterns to concrete
// repository lists and provide credentials for accessing them.
//
// GitRepos serve three purposes:
//   1. Discovery — Expand patterns like "org/platform/*" into concrete repos.
//   2. Credentials — Centralize authentication (tokens, SSH keys) per provider.
//   3. Registry — The console lists GitRepos to show what's available. Agents,
//      PiAgents, and Workflows reference GitRepos for repository awareness.
//
// A GitWorkspace references a GitRepo to materialize a working copy.
// Multiple GitWorkspaces can reference the same GitRepo (different repos, branches).
//
// Examples:
//
//   # GitHub org — all repos in an org
//   apiVersion: agents.io/v1alpha1
//   kind: GitRepo
//   metadata:
//     name: acme-github
//   spec:
//     provider: github
//     credentialsRef: {name: github-token, key: token}
//     github:
//       sources:
//         - owner: acme-corp
//           pattern: "*"     # all repos under acme-corp
//
//   # GitLab subgroups — two specific subgroups + one standalone project
//   apiVersion: agents.io/v1alpha1
//   kind: GitRepo
//   metadata:
//     name: platform-repos
//   spec:
//     provider: gitlab
//     domain: gitlab.com
//     credentialsRef: {name: gitlab-token, key: token}
//     gitlab:
//       sources:
//         - group: org/platform
//           pattern: "*"        # all projects in org/platform
//           recursive: true     # include subgroups
//         - group: org/shared
//           pattern: "lib-*"    # only projects matching lib-*
//         - project: org/infra/terraform  # explicit single project

// GitRepoProvider identifies the Git hosting provider.
// +kubebuilder:validation:Enum=github;gitlab;gitea;generic
type GitRepoProvider string

const (
	GitRepoProviderGitHub  GitRepoProvider = "github"
	GitRepoProviderGitLab  GitRepoProvider = "gitlab"
	GitRepoProviderGitea   GitRepoProvider = "gitea"
	GitRepoProviderGeneric GitRepoProvider = "generic"
)

// GitRepoSpec defines the desired state of GitRepo.
type GitRepoSpec struct {
	// Provider identifies the Git hosting platform.
	// Determines which sub-spec (github, gitlab, gitea, generic) is active,
	// how repositories are discovered, and which forge API is used.
	// +kubebuilder:validation:Required
	Provider GitRepoProvider `json:"provider"`

	// Domain is the hostname of the Git provider instance.
	// Defaults based on provider: "github.com", "gitlab.com", "gitea.com".
	// Override for self-hosted instances (e.g., "gitlab.example.com").
	// +optional
	Domain string `json:"domain,omitempty"`

	// CredentialsRef references a Secret containing the API/Git token.
	// The secret key should contain a personal access token, project token,
	// or app installation token with sufficient scope for the operations needed.
	//
	// For GitHub: needs "repo" scope (or fine-grained equivalent).
	// For GitLab: needs "api" and "read_repository" scopes.
	// For generic: the token is used in HTTPS basic auth (username is ignored).
	//
	// If not specified, repos are accessed anonymously (public repos only).
	// GitWorkspaces that reference this GitRepo inherit these credentials
	// unless they override with their own credentialsRef.
	// +optional
	CredentialsRef *SecretKeySelector `json:"credentialsRef,omitempty"`

	// SSHKeyRef references a Secret containing an SSH private key for Git operations.
	// When set, Git operations use SSH transport instead of HTTPS.
	// The secret key should contain the PEM-encoded private key.
	// +optional
	SSHKeyRef *SecretKeySelector `json:"sshKeyRef,omitempty"`

	// Author configures the default git commit author for all workspaces
	// created from this GitRepo. Can be overridden per GitWorkspace.
	// +optional
	Author *GitAuthor `json:"author,omitempty"`

	// ==========================================================================
	// PROVIDER-SPECIFIC SOURCE CONFIGURATION (exactly one must match provider)
	// ==========================================================================

	// GitHub configures GitHub-specific repository sources.
	// Required when provider is "github".
	// +optional
	GitHub *GitHubRepoSource `json:"github,omitempty"`

	// GitLab configures GitLab-specific repository sources.
	// Required when provider is "gitlab".
	// +optional
	GitLab *GitLabRepoSource `json:"gitlab,omitempty"`

	// Gitea configures Gitea-specific repository sources.
	// Required when provider is "gitea".
	// +optional
	Gitea *GiteaRepoSource `json:"gitea,omitempty"`

	// Generic configures provider-agnostic repository sources.
	// Used for self-hosted Git servers without a forge API.
	// Required when provider is "generic".
	// +optional
	Generic *GenericRepoSource `json:"generic,omitempty"`

	// ==========================================================================
	// SYNC CONFIGURATION
	// ==========================================================================

	// SyncInterval is how often to re-resolve repository patterns and refresh
	// the discovered repo list. Default: 5m.
	// Uses Go duration format (e.g., "5m", "1h", "30s").
	// +kubebuilder:default="5m"
	// +optional
	SyncInterval string `json:"syncInterval,omitempty"`

	// WorkspaceDefaults provides default configuration for GitWorkspaces
	// that are auto-created by agents in agent-driven mode.
	// When an Agent or PiAgent references this GitRepo with a repository name,
	// the operator creates a GitWorkspace using these defaults.
	// Individual fields can be overridden per-workspace if needed.
	// +optional
	WorkspaceDefaults *GitRepoWorkspaceDefaults `json:"workspaceDefaults,omitempty"`
}

// =============================================================================
// GITHUB SOURCES
// =============================================================================

// GitHubRepoSource configures repository discovery from GitHub.
type GitHubRepoSource struct {
	// Sources defines one or more GitHub repository sources.
	// Each source can target an organization, user, or specific repositories.
	// All sources are resolved and merged into a single deduplicated list.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Sources []GitHubSource `json:"sources"`
}

// GitHubSource defines a single GitHub repository source.
// Exactly one of owner or repositories must be specified.
type GitHubSource struct {
	// Owner is a GitHub organization or user name.
	// When set, the operator lists all repositories for this owner
	// and filters them by pattern.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Pattern filters repositories by name when used with owner.
	// Uses glob syntax (e.g., "*", "api-*", "service-??").
	// Default: "*" (all repositories).
	// +kubebuilder:default="*"
	// +optional
	Pattern string `json:"pattern,omitempty"`

	// Repositories lists specific repositories by full name ("owner/repo").
	// Use this instead of owner+pattern when you want explicit control.
	// +optional
	Repositories []string `json:"repositories,omitempty"`

	// Visibility filters by repository visibility.
	// +kubebuilder:validation:Enum=all;public;private
	// +kubebuilder:default="all"
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// Archived controls whether to include archived repositories.
	// +kubebuilder:default=false
	// +optional
	Archived bool `json:"archived,omitempty"`

	// Topics filters repositories to only those with ALL specified topics.
	// +optional
	Topics []string `json:"topics,omitempty"`
}

// =============================================================================
// GITLAB SOURCES
// =============================================================================

// GitLabRepoSource configures repository discovery from GitLab.
type GitLabRepoSource struct {
	// Sources defines one or more GitLab project sources.
	// Each source can target a group, subgroup, user, or specific projects.
	// All sources are resolved and merged into a single deduplicated list.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Sources []GitLabSource `json:"sources"`
}

// GitLabSource defines a single GitLab project source.
// Specify group, user, or project to select the source type.
type GitLabSource struct {
	// Group is a GitLab group path (e.g., "org/platform", "company/team/backend").
	// When set, the operator lists all projects in this group and filters by pattern.
	// +optional
	Group string `json:"group,omitempty"`

	// User is a GitLab username.
	// When set, the operator lists all projects owned by this user.
	// +optional
	User string `json:"user,omitempty"`

	// Project is a specific GitLab project path (e.g., "org/platform/api").
	// Use this for explicit single-project references.
	// +optional
	Project string `json:"project,omitempty"`

	// Pattern filters projects by name when used with group or user.
	// Uses glob syntax (e.g., "*", "service-*", "lib-??").
	// Default: "*" (all projects).
	// +kubebuilder:default="*"
	// +optional
	Pattern string `json:"pattern,omitempty"`

	// Recursive includes projects from subgroups when used with group.
	// Default: false (only direct children of the group).
	// +kubebuilder:default=false
	// +optional
	Recursive bool `json:"recursive,omitempty"`

	// Visibility filters by project visibility.
	// +kubebuilder:validation:Enum=all;public;internal;private
	// +kubebuilder:default="all"
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// Archived controls whether to include archived projects.
	// +kubebuilder:default=false
	// +optional
	Archived bool `json:"archived,omitempty"`

	// Topics filters projects to only those with ALL specified topics.
	// +optional
	Topics []string `json:"topics,omitempty"`
}

// =============================================================================
// GITEA SOURCES
// =============================================================================

// GiteaRepoSource configures repository discovery from Gitea.
type GiteaRepoSource struct {
	// Sources defines one or more Gitea repository sources.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Sources []GiteaSource `json:"sources"`
}

// GiteaSource defines a single Gitea repository source.
type GiteaSource struct {
	// Owner is a Gitea organization or user name.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Pattern filters repositories by name when used with owner.
	// +kubebuilder:default="*"
	// +optional
	Pattern string `json:"pattern,omitempty"`

	// Repositories lists specific repositories by full name ("owner/repo").
	// +optional
	Repositories []string `json:"repositories,omitempty"`
}

// =============================================================================
// GENERIC SOURCES (no forge API)
// =============================================================================

// GenericRepoSource configures raw Git repository URLs.
// Used for self-hosted Git servers without a forge API (e.g., Gitolite, bare SSH servers).
type GenericRepoSource struct {
	// Repositories lists Git repository URLs.
	// Each URL is used as-is for cloning (HTTPS or SSH).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Repositories []GenericRepoEntry `json:"repositories"`
}

// GenericRepoEntry defines a single repository in the generic source.
type GenericRepoEntry struct {
	// URL is the Git clone URL (HTTPS or SSH).
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Name is a human-readable name for this repository.
	// If not specified, derived from the URL.
	// +optional
	Name string `json:"name,omitempty"`
}

// =============================================================================
// WORKSPACE DEFAULTS (for agent-driven auto-creation)
// =============================================================================

// GitRepoWorkspaceDefaults provides default configuration for auto-created
// GitWorkspaces. When an Agent or PiAgent uses agent-driven mode (gitRepo +
// repository), the operator creates a GitWorkspace using these defaults.
type GitRepoWorkspaceDefaults struct {
	// Storage configures the PVC for auto-created workspaces.
	// +optional
	Storage *GitWorkspaceStorage `json:"storage,omitempty"`

	// Sync configures automatic fetching for auto-created workspaces.
	// +optional
	Sync *GitSyncConfig `json:"sync,omitempty"`

	// Clone configures initial clone behavior for auto-created workspaces.
	// +optional
	Clone *GitCloneConfig `json:"clone,omitempty"`

	// TTL sets a time-to-live on auto-created workspaces.
	// When set, workspaces with no consumers are garbage collected after this duration.
	// Uses Go duration format (e.g., "24h", "168h").
	// +optional
	TTL string `json:"ttl,omitempty"`
}

// =============================================================================
// STATUS
// =============================================================================

// GitRepoStatus defines the observed state of GitRepo.
type GitRepoStatus struct {
	// Phase is the current phase of the GitRepo.
	// +kubebuilder:validation:Enum=Pending;Syncing;Ready;Failed
	Phase GitRepoPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Repositories is the resolved list of concrete repositories.
	// Populated by the controller after resolving all patterns/sources.
	// +optional
	Repositories []DiscoveredRepository `json:"repositories,omitempty"`

	// RepositoryCount is the total number of discovered repositories.
	// +optional
	RepositoryCount int `json:"repositoryCount,omitempty"`

	// LastSyncTime is the last time repositories were resolved.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// WorkspaceCount is the number of GitWorkspaces referencing this GitRepo.
	// +optional
	WorkspaceCount int `json:"workspaceCount,omitempty"`
}

// DiscoveredRepository represents a single resolved repository.
type DiscoveredRepository struct {
	// Name is the full repository name (e.g., "org/platform/api").
	Name string `json:"name"`

	// CloneURL is the HTTPS clone URL.
	CloneURL string `json:"cloneUrl"`

	// SSHURL is the SSH clone URL (if available).
	// +optional
	SSHURL string `json:"sshUrl,omitempty"`

	// DefaultBranch is the repository's default branch.
	// +optional
	DefaultBranch string `json:"defaultBranch,omitempty"`

	// Description is the repository's description.
	// +optional
	Description string `json:"description,omitempty"`

	// Visibility is the repository's visibility (public, private, internal).
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// Archived indicates if the repository is archived.
	// +optional
	Archived bool `json:"archived,omitempty"`

	// LastActivity is the last push/commit time.
	// +optional
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`
}

// GitRepoPhase represents the phase of a GitRepo.
type GitRepoPhase string

const (
	GitRepoPhasePending GitRepoPhase = "Pending"
	GitRepoPhaseSyncing GitRepoPhase = "Syncing"
	GitRepoPhaseReady   GitRepoPhase = "Ready"
	GitRepoPhaseFailed  GitRepoPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Repos",type=integer,JSONPath=`.status.repositoryCount`
// +kubebuilder:printcolumn:name="Workspaces",type=integer,JSONPath=`.status.workspaceCount`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitRepo is the Schema for the gitrepos API.
// A GitRepo declares a set of Git repositories from a single provider.
// It resolves patterns and wildcards into concrete repository lists,
// centralizes credentials, and serves as the discovery registry for
// the console and agents.
type GitRepo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitRepoSpec   `json:"spec,omitempty"`
	Status GitRepoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitRepoList contains a list of GitRepo.
type GitRepoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitRepo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitRepo{}, &GitRepoList{})
}
