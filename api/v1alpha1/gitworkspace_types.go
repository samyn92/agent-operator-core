package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// GITWORKSPACE CRD
// =============================================================================
// A GitWorkspace is a managed Git repository on disk, backed by a PVC.
// It uses a bare-clone + worktree architecture: one shared object store,
// with each agent/task working in its own isolated worktree directory.
//
// This is the core primitive for Git-first agent operations:
//   - Agents never work on the default branch directly. They create branches.
//   - Agents never clone repos themselves. The workspace is pre-cloned.
//   - Multiple agents CAN share one workspace (RWX PVC), each on a different worktree.
//   - The operator handles clone, fetch, worktree lifecycle, and credential injection.
//   - Agents only do local git operations: branch, add, commit, push, diff.
//
// PVC layout:
//
//   /workspace/
//     .bare/                     ← bare clone (shared object store, never checked out)
//     main/                      ← read-only worktree tracking the default branch
//     branches/
//       fix-auth-bug/            ← Agent A's worktree (created by agent via git tool)
//       feat-rate-limiting/      ← Agent B's worktree
//       mr-123-review/           ← PiAgent C's ephemeral worktree
//     .gitworkspace/
//       status.json              ← workspace metadata for the sync sidecar
//       sync.lock                ← advisory lock for fetch operations
//
// How agents use it:
//
//   1. Agent mounts the workspace PVC at /workspaces/<repo-name>
//   2. Agent reads code from /workspaces/<repo-name>/main/ (default branch)
//   3. Agent creates a worktree: git worktree add /workspaces/<repo-name>/branches/fix-bug -b fix-bug
//   4. Agent works in /workspaces/<repo-name>/branches/fix-bug/ (edit, test, commit)
//   5. Agent pushes the branch and creates an MR/PR via forge tools
//   6. Sync sidecar cleans up merged-branch worktrees automatically
//
// Examples:
//
//   # Persistent workspace for a team's repos
//   apiVersion: agents.io/v1alpha1
//   kind: GitWorkspace
//   metadata:
//     name: platform-api
//   spec:
//     gitRepoRef: platform-repos
//     repository: org/platform/api
//     sync:
//       interval: 5m
//     storage:
//       size: 10Gi
//       accessMode: ReadWriteMany
//
//   # Shared workspace for multiple agents
//   apiVersion: agents.io/v1alpha1
//   kind: GitWorkspace
//   metadata:
//     name: shared-api
//   spec:
//     gitRepoRef: platform-repos
//     repository: org/platform/api
//     storage:
//       size: 10Gi
//       accessMode: ReadWriteMany

// GitWorkspaceSpec defines the desired state of GitWorkspace.
type GitWorkspaceSpec struct {
	// GitRepoRef references the GitRepo that provides credentials and
	// the repository registry. The referenced GitRepo must be in the
	// same namespace.
	// +kubebuilder:validation:Required
	GitRepoRef string `json:"gitRepoRef"`

	// Repository is the full name of the repository to clone.
	// Must match one of the repositories discovered by the referenced GitRepo.
	// Format depends on provider: "owner/repo" (GitHub), "group/project" (GitLab),
	// or the full URL for generic providers.
	// +kubebuilder:validation:Required
	Repository string `json:"repository"`

	// ==========================================================================
	// CLONE CONFIGURATION
	// ==========================================================================

	// Clone configures how the initial bare clone is performed.
	// +optional
	Clone *GitCloneConfig `json:"clone,omitempty"`

	// ==========================================================================
	// SYNC CONFIGURATION
	// ==========================================================================

	// Sync configures automatic fetching from the remote.
	// The sync sidecar runs `git fetch --all` on the bare clone at this interval.
	// Agents see updated remote refs and can decide when to merge/rebase.
	// +optional
	Sync *GitSyncConfig `json:"sync,omitempty"`

	// ==========================================================================
	// WORKTREE LIFECYCLE
	// ==========================================================================

	// Worktree configures worktree lifecycle management.
	// +optional
	Worktree *GitWorktreeConfig `json:"worktree,omitempty"`

	// ==========================================================================
	// GIT CONFIGURATION
	// ==========================================================================

	// Author configures the default git commit author for this workspace.
	// Overrides the GitRepo-level author if set. Agents can further override
	// per-commit if needed.
	// +optional
	Author *GitAuthor `json:"author,omitempty"`

	// ProtectedBranches lists branches that agents cannot push to directly.
	// Agents must work in worktrees on feature branches and create MRs/PRs.
	// Default: ["main", "master"] (enforced by the git tool's deny patterns).
	// +optional
	ProtectedBranches []string `json:"protectedBranches,omitempty"`

	// ==========================================================================
	// STORAGE
	// ==========================================================================

	// Storage configures the PVC for the workspace.
	// +optional
	Storage *GitWorkspaceStorage `json:"storage,omitempty"`

	// ==========================================================================
	// IMAGES
	// ==========================================================================

	// Images configures the container images used for workspace operations.
	// +optional
	Images *GitWorkspaceImages `json:"images,omitempty"`

	// ==========================================================================
	// LIFECYCLE
	// ==========================================================================

	// TTL is the time-to-live after the workspace becomes idle.
	// When set, the workspace is garbage collected after no consumer
	// has mounted it for this duration. Uses Go duration format.
	// Useful for ephemeral workspaces created by Workflows.
	// +optional
	TTL string `json:"ttl,omitempty"`

	// Suspend stops the sync sidecar from fetching.
	// The PVC and data remain intact, but no fetches occur.
	// +kubebuilder:default=false
	// +optional
	Suspend *bool `json:"suspend,omitempty"`
}

// GitCloneConfig controls how the bare repository is initially cloned.
type GitCloneConfig struct {
	// Depth limits the clone history. 0 means full clone.
	// Shallow clones (depth > 0) are faster but limit log/blame operations.
	// For workspaces where agents need full history (e.g., for git log analysis), use 0.
	// For review-only workspaces, a shallow clone (depth: 50) is sufficient.
	// +kubebuilder:default=0
	// +optional
	Depth int `json:"depth,omitempty"`

	// Submodules controls whether to initialize and update submodules.
	// +kubebuilder:validation:Enum=none;shallow;recursive
	// +kubebuilder:default="none"
	// +optional
	Submodules string `json:"submodules,omitempty"`

	// LFS enables Git Large File Storage support.
	// +kubebuilder:default=false
	// +optional
	LFS bool `json:"lfs,omitempty"`
}

// GitSyncConfig configures automatic fetching from the remote.
type GitSyncConfig struct {
	// Interval is how often the sidecar runs `git fetch --all` on the bare clone.
	// Uses Go duration format. Default: "5m".
	// The fetch only updates remote refs in the bare clone — it never modifies
	// any worktree's working directory. Agents see updated remote tracking
	// branches and decide when to merge/rebase.
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`

	// MainWorktreeStrategy controls how the default-branch worktree (/workspace/main/)
	// stays in sync after a fetch.
	//   - reset: Hard reset to origin/<default-branch> after each fetch.
	//     This keeps the main worktree as a read-only reference that always
	//     reflects the latest remote state. Safe because agents never commit to main.
	//   - none: Don't touch the main worktree. Agents manually update it.
	// +kubebuilder:validation:Enum=reset;none
	// +kubebuilder:default="reset"
	// +optional
	MainWorktreeStrategy string `json:"mainWorktreeStrategy,omitempty"`
}

// GitWorktreeConfig configures worktree lifecycle management.
type GitWorktreeConfig struct {
	// CleanupPolicy controls when branch worktrees are removed.
	//   - onMerge: Auto-removed when the branch is deleted on the remote
	//     (detected during fetch). This is the common case — branch gets
	//     merged via MR/PR, remote branch is deleted, sidecar cleans up worktree.
	//   - manual: Worktrees persist until explicitly removed by an agent
	//     or by deleting the GitWorkspace.
	// +kubebuilder:validation:Enum=onMerge;manual
	// +kubebuilder:default="onMerge"
	// +optional
	CleanupPolicy string `json:"cleanupPolicy,omitempty"`

	// MaxWorktrees limits the number of concurrent branch worktrees.
	// Prevents disk exhaustion from too many parallel branches.
	// 0 means unlimited. Default: 10.
	// +kubebuilder:default=10
	// +optional
	MaxWorktrees int `json:"maxWorktrees,omitempty"`
}

// GitWorkspaceStorage configures the PVC for workspace data.
type GitWorkspaceStorage struct {
	// Size is the PVC storage size. Default: "10Gi".
	// Consider the repo size + number of concurrent worktrees.
	// Each worktree adds working-tree files but shares git objects.
	// +kubebuilder:default="10Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass is the Kubernetes storage class.
	// For shared workspaces (multiple agents), this MUST support ReadWriteMany (RWX).
	// Examples: NFS, CephFS, Longhorn RWX, EFS.
	// For single-agent workspaces, ReadWriteOnce (RWO) is sufficient.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessMode controls PVC access mode.
	// Default: ReadWriteMany (supports multi-agent sharing).
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteMany
	// +kubebuilder:default="ReadWriteMany"
	// +optional
	AccessMode string `json:"accessMode,omitempty"`
}

// GitWorkspaceImages configures container images for workspace operations.
type GitWorkspaceImages struct {
	// Git is the image used for the init container (clone) and sync sidecar (fetch).
	// Must contain the git binary. Default: "alpine/git:latest".
	// +kubebuilder:default="alpine/git:latest"
	// +optional
	Git string `json:"git,omitempty"`

	// PullPolicy is the image pull policy.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default="IfNotPresent"
	// +optional
	PullPolicy string `json:"pullPolicy,omitempty"`
}

// =============================================================================
// STATUS
// =============================================================================

// GitWorkspaceStatus defines the observed state of GitWorkspace.
type GitWorkspaceStatus struct {
	// Phase is the current phase of the workspace.
	// +kubebuilder:validation:Enum=Pending;Cloning;Ready;Error
	Phase GitWorkspacePhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ==========================================================================
	// GIT STATE
	// ==========================================================================

	// DefaultBranch is the repository's default branch (e.g., "main").
	// Determined during clone from the remote HEAD.
	// +optional
	DefaultBranch string `json:"defaultBranch,omitempty"`

	// RemoteHeadCommit is the SHA of origin/<default-branch> HEAD.
	// Updated after each fetch.
	// +optional
	RemoteHeadCommit string `json:"remoteHeadCommit,omitempty"`

	// ActiveWorktrees lists the current git worktrees in the workspace.
	// Includes the main worktree and any branch worktrees created by agents.
	// +optional
	ActiveWorktrees []WorktreeInfo `json:"activeWorktrees,omitempty"`

	// ==========================================================================
	// INFRASTRUCTURE STATE
	// ==========================================================================

	// PVCName is the name of the managed PVC.
	// +optional
	PVCName string `json:"pvcName,omitempty"`

	// CloneURL is the resolved clone URL used for this workspace.
	// +optional
	CloneURL string `json:"cloneUrl,omitempty"`

	// DiskUsage is the approximate disk usage of the workspace.
	// +optional
	DiskUsage string `json:"diskUsage,omitempty"`

	// ==========================================================================
	// SYNC STATE
	// ==========================================================================

	// LastFetchTime is the last time a successful git fetch completed.
	// +optional
	LastFetchTime *metav1.Time `json:"lastFetchTime,omitempty"`

	// ==========================================================================
	// CONSUMER TRACKING
	// ==========================================================================

	// Consumers lists the resources currently using this workspace.
	// +optional
	Consumers []GitWorkspaceConsumer `json:"consumers,omitempty"`
}

// WorktreeInfo describes an active git worktree in the workspace.
type WorktreeInfo struct {
	// Name is the worktree name (directory name under /workspace/branches/).
	// For the main worktree, this is "main" (or the default branch name).
	Name string `json:"name"`

	// Branch is the branch checked out in this worktree.
	Branch string `json:"branch"`

	// Path is the filesystem path relative to the PVC root.
	Path string `json:"path"`

	// HeadCommit is the SHA of HEAD in this worktree.
	// +optional
	HeadCommit string `json:"headCommit,omitempty"`

	// Dirty indicates if the worktree has uncommitted changes.
	// +optional
	Dirty bool `json:"dirty,omitempty"`

	// Consumer is the Agent/PiAgent name using this worktree (if tracked).
	// +optional
	Consumer string `json:"consumer,omitempty"`
}

// GitWorkspaceConsumer tracks a resource that has mounted this workspace.
type GitWorkspaceConsumer struct {
	// Kind is the consumer resource type (Agent, PiAgent).
	Kind string `json:"kind"`

	// Name is the consumer resource name.
	Name string `json:"name"`

	// Access is the access level (readonly, readwrite).
	Access string `json:"access"`

	// Since is when the consumer started using the workspace.
	// +optional
	Since *metav1.Time `json:"since,omitempty"`
}

// GitWorkspacePhase represents the phase of a GitWorkspace.
type GitWorkspacePhase string

const (
	GitWorkspacePhasePending GitWorkspacePhase = "Pending"
	GitWorkspacePhaseCloning GitWorkspacePhase = "Cloning"
	GitWorkspacePhaseReady   GitWorkspacePhase = "Ready"
	GitWorkspacePhaseError   GitWorkspacePhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="Branch",type=string,JSONPath=`.status.defaultBranch`
// +kubebuilder:printcolumn:name="Worktrees",type=integer,JSONPath=`.status.activeWorktrees`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitWorkspace is the Schema for the gitworkspaces API.
// A GitWorkspace is a bare-clone Git repository on a PVC with worktree-based
// branch isolation. Multiple agents share one workspace, each working in
// their own worktree on separate branches. The operator handles clone,
// periodic fetch, worktree cleanup, and credential injection.
type GitWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitWorkspaceSpec   `json:"spec,omitempty"`
	Status GitWorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitWorkspaceList contains a list of GitWorkspace.
type GitWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitWorkspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitWorkspace{}, &GitWorkspaceList{})
}
