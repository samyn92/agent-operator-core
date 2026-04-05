package resources

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// =============================================================================
// NAMING CONVENTIONS
// =============================================================================

const (
	// DefaultGitImage is the container image used for clone init containers
	// and sync sidecars. Must contain git, sh, and standard Unix tools.
	DefaultGitImage = "alpine/git:latest"

	// DefaultGitWorkspaceSize is the default PVC size for workspace storage.
	DefaultGitWorkspaceSize = "10Gi"

	// WorkspaceVolumeName is the volume name for the workspace PVC in consumer pods.
	WorkspaceVolumeName = "git-workspace"
)

// GitWorkspacePVCName returns the PVC name for a GitWorkspace.
// Convention: <workspace-name>-workspace
func GitWorkspacePVCName(ws *agentsv1alpha1.GitWorkspace) string {
	return ws.Name + "-workspace"
}

// GitWorkspaceDeploymentName returns the Deployment name for a GitWorkspace.
// Convention: <workspace-name>-ws
func GitWorkspaceDeploymentName(ws *agentsv1alpha1.GitWorkspace) string {
	return ws.Name + "-ws"
}

// gitWorkspaceLabels returns standard labels for GitWorkspace-owned resources.
func gitWorkspaceLabels(ws *agentsv1alpha1.GitWorkspace) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "git-workspace",
		"app.kubernetes.io/instance":   ws.Name,
		"app.kubernetes.io/managed-by": "agent-operator",
		"agents.io/workspace":          ws.Name,
	}
}

// =============================================================================
// PVC
// =============================================================================

// GitWorkspacePVC creates a PersistentVolumeClaim for a GitWorkspace.
// The PVC holds the bare clone, worktrees, and workspace metadata.
// Default access mode is ReadWriteMany to support multi-agent sharing.
func GitWorkspacePVC(ws *agentsv1alpha1.GitWorkspace) *corev1.PersistentVolumeClaim {
	labels := gitWorkspaceLabels(ws)

	// Defaults
	storageSize := resource.MustParse(DefaultGitWorkspaceSize)
	accessMode := corev1.ReadWriteMany
	var storageClass *string

	if ws.Spec.Storage != nil {
		if !ws.Spec.Storage.Size.IsZero() {
			storageSize = ws.Spec.Storage.Size
		}
		if ws.Spec.Storage.StorageClass != "" {
			sc := ws.Spec.Storage.StorageClass
			storageClass = &sc
		}
		switch ws.Spec.Storage.AccessMode {
		case "ReadWriteOnce":
			accessMode = corev1.ReadWriteOnce
		case "ReadWriteMany":
			accessMode = corev1.ReadWriteMany
		}
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GitWorkspacePVCName(ws),
			Namespace: ws.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}
}

// =============================================================================
// DEPLOYMENT — standalone workspace pod (init clone + sync loop)
// =============================================================================

// GitWorkspaceDeployment creates a Deployment that owns the git operations for
// a GitWorkspace. This is the core architectural decision: one pod per workspace
// handles all git I/O (clone, fetch, worktree cleanup), and consumer pods
// (Agent Deployments, PiAgent Jobs) just mount the same RWX PVC.
//
// The Deployment has:
//   - 1 replica (singleton — only one pod should manage a workspace's git state)
//   - Init container: bare clone + main worktree setup (idempotent on restart)
//   - Main container: sync loop (periodic fetch, main worktree reset, cleanup)
//   - PVC: workspace storage (bare clone + worktrees + metadata)
//
// The controller watches this Deployment's readiness to determine when the
// GitWorkspace transitions from Cloning → Ready.
func GitWorkspaceDeployment(ws *agentsv1alpha1.GitWorkspace, gitRepo *agentsv1alpha1.GitRepo, cloneURL string) *appsv1.Deployment {
	labels := gitWorkspaceLabels(ws)
	replicas := int32(1)

	// Build init container (bare clone + main worktree)
	initContainer := GitWorkspaceInitContainer(ws, cloneURL)

	// Build sync container (periodic fetch + worktree cleanup)
	syncContainer := GitWorkspaceSyncContainer(ws)

	// Inject credential env vars from GitRepo into both containers
	credEnvVars := GitWorkspaceCredentialEnvVars(gitRepo)
	if len(credEnvVars) > 0 {
		initContainer.Env = append(initContainer.Env, credEnvVars...)
		syncContainer.Env = append(syncContainer.Env, credEnvVars...)
	}

	// Inject CLONE_URL into sync container (init already has it)
	syncContainer.Env = append(syncContainer.Env, GitWorkspaceCloneURLEnvVar(cloneURL))

	// Inject GitRepo-level author if workspace doesn't override
	if ws.Spec.Author == nil && gitRepo.Spec.Author != nil {
		authorEnvVars := []corev1.EnvVar{
			{Name: "GIT_AUTHOR_NAME", Value: gitRepo.Spec.Author.Name},
			{Name: "GIT_AUTHOR_EMAIL", Value: gitRepo.Spec.Author.Email},
			{Name: "GIT_COMMITTER_NAME", Value: gitRepo.Spec.Author.Name},
			{Name: "GIT_COMMITTER_EMAIL", Value: gitRepo.Spec.Author.Email},
		}
		initContainer.Env = append(initContainer.Env, authorEnvVars...)
		syncContainer.Env = append(syncContainer.Env, authorEnvVars...)
	}

	// Build volumes
	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: GitWorkspacePVCName(ws),
				},
			},
		},
		{
			Name: "tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Add SSH key volume if configured
	sshVol, sshMount := GitWorkspaceSSHVolume(gitRepo)
	if sshVol != nil {
		volumes = append(volumes, *sshVol)
		initContainer.VolumeMounts = append(initContainer.VolumeMounts, *sshMount)
		syncContainer.VolumeMounts = append(syncContainer.VolumeMounts, *sshMount)
		// Set GIT_SSH_COMMAND to use the mounted key
		sshEnv := corev1.EnvVar{
			Name:  "GIT_SSH_COMMAND",
			Value: "ssh -i /etc/git-ssh/id_rsa -o StrictHostKeyChecking=no",
		}
		initContainer.Env = append(initContainer.Env, sshEnv)
		syncContainer.Env = append(syncContainer.Env, sshEnv)
	}

	// The workspace Deployment uses Recreate strategy because:
	// 1. Only one pod should manage git state at a time
	// 2. Even with RWX, two pods doing fetch simultaneously is wasteful
	// 3. The init container is idempotent — safe to restart
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GitWorkspaceDeploymentName(ws),
			Namespace: ws.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{initContainer},
					Containers:     []corev1.Container{syncContainer},
					Volumes:        volumes,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						RunAsUser:    int64Ptr(1000),
						RunAsGroup:   int64Ptr(1000),
						FSGroup:      int64Ptr(1000),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
		},
	}

	// Compute and store a hash of the desired spec for change detection
	specHash := HashDeploymentSpec(dep)
	dep.Annotations = map[string]string{
		DesiredSpecHashAnnotation: specHash,
	}

	return dep
}

// =============================================================================
// INIT CONTAINER — bare clone + main worktree
// =============================================================================

// GitWorkspaceInitContainer creates the init container that performs the initial
// bare clone and sets up the main worktree. This blocks pod startup until the
// repository is cloned and ready.
//
// The init container:
//  1. Creates the workspace directory structure
//  2. Performs a bare clone into /workspace/.bare/
//  3. Creates the main worktree at /workspace/main/ tracking the default branch
//  4. Writes initial status metadata to /workspace/.gitworkspace/status.json
//
// The clone URL and credentials are passed via environment variables.
// On subsequent pod restarts, the init container detects the existing clone
// and only does a fetch to ensure it's up to date.
func GitWorkspaceInitContainer(ws *agentsv1alpha1.GitWorkspace, cloneURL string) corev1.Container {
	image, pullPolicy := getGitImageConfig(ws)

	// Build clone flags
	cloneFlags := ""
	if ws.Spec.Clone != nil {
		if ws.Spec.Clone.Depth > 0 {
			cloneFlags += fmt.Sprintf(" --depth %d", ws.Spec.Clone.Depth)
		}
	}

	// Determine default branch name for the main worktree.
	// If we already know it from status, use it; otherwise detect from remote HEAD.
	defaultBranch := ws.Status.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main" // fallback, script detects actual default
	}

	// The init script is idempotent: if the bare clone already exists, it fetches
	// instead of re-cloning. This handles pod restarts gracefully.
	script := fmt.Sprintf(`set -e
WORKSPACE="/workspace"
BARE_DIR="$WORKSPACE/.bare"
MAIN_DIR="$WORKSPACE/main"
BRANCH_DIR="$WORKSPACE/branches"
META_DIR="$WORKSPACE/.gitworkspace"

# Configure git credentials if token is available
if [ -n "$GIT_TOKEN" ]; then
  # Configure credential helper for HTTPS authentication
  git config --global credential.helper 'store'
  # Extract host from clone URL and write credentials
  CLONE_HOST=$(echo "$CLONE_URL" | sed -n 's|https\?://\([^/]*\).*|\1|p')
  if [ -n "$CLONE_HOST" ]; then
    echo "https://oauth2:${GIT_TOKEN}@${CLONE_HOST}" > /tmp/.git-credentials
    git config --global credential.helper 'store --file=/tmp/.git-credentials'
  fi
fi

# Configure git author if provided
if [ -n "$GIT_AUTHOR_NAME" ]; then
  git config --global user.name "$GIT_AUTHOR_NAME"
fi
if [ -n "$GIT_AUTHOR_EMAIL" ]; then
  git config --global user.email "$GIT_AUTHOR_EMAIL"
fi

# Create directory structure
mkdir -p "$BRANCH_DIR" "$META_DIR"

if [ -d "$BARE_DIR/objects" ]; then
  echo "Bare clone already exists, fetching updates..."
  git -C "$BARE_DIR" fetch --all --prune
else
  echo "Performing initial bare clone..."
  git clone --bare%s "$CLONE_URL" "$BARE_DIR"
fi

# Detect default branch from remote HEAD
DEFAULT_BRANCH=$(git -C "$BARE_DIR" symbolic-ref HEAD 2>/dev/null | sed 's|refs/heads/||' || echo "%s")
echo "Default branch: $DEFAULT_BRANCH"

# Create or update main worktree
if [ -d "$MAIN_DIR/.git" ]; then
  echo "Main worktree exists, resetting to origin/$DEFAULT_BRANCH..."
  git -C "$MAIN_DIR" fetch origin
  git -C "$MAIN_DIR" reset --hard "origin/$DEFAULT_BRANCH" 2>/dev/null || true
else
  echo "Creating main worktree..."
  # Remove stale directory if it exists but isn't a valid worktree
  rm -rf "$MAIN_DIR"
  git -C "$BARE_DIR" worktree add "$MAIN_DIR" "$DEFAULT_BRANCH" 2>/dev/null || \
    git -C "$BARE_DIR" worktree add "$MAIN_DIR" "origin/$DEFAULT_BRANCH" --detach
fi

# Handle submodules if configured
if [ "%s" = "recursive" ]; then
  echo "Initializing submodules (recursive)..."
  git -C "$MAIN_DIR" submodule update --init --recursive
elif [ "%s" = "shallow" ]; then
  echo "Initializing submodules (shallow)..."
  git -C "$MAIN_DIR" submodule update --init --depth 1
fi

# Write status metadata
REMOTE_HEAD=$(git -C "$BARE_DIR" rev-parse "refs/heads/$DEFAULT_BRANCH" 2>/dev/null || echo "unknown")
cat > "$META_DIR/status.json" <<STATUSEOF
{
  "defaultBranch": "$DEFAULT_BRANCH",
  "remoteHeadCommit": "$REMOTE_HEAD",
  "cloneUrl": "$CLONE_URL",
  "initComplete": true,
  "initTime": "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)"
}
STATUSEOF

echo "Init complete: bare clone at $BARE_DIR, main worktree at $MAIN_DIR"`,
		cloneFlags,
		defaultBranch,
		getSubmoduleMode(ws),
		getSubmoduleMode(ws),
	)

	envVars := []corev1.EnvVar{
		{Name: "CLONE_URL", Value: cloneURL},
		{Name: "HOME", Value: "/tmp"},
	}

	// Git author configuration
	author := resolveGitAuthor(ws)
	if author != nil {
		if author.Name != "" {
			envVars = append(envVars, corev1.EnvVar{Name: "GIT_AUTHOR_NAME", Value: author.Name})
		}
		if author.Email != "" {
			envVars = append(envVars, corev1.EnvVar{Name: "GIT_AUTHOR_EMAIL", Value: author.Email})
		}
	}

	return corev1.Container{
		Name:            "init-clone",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		WorkingDir:      "/workspace",
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{script},
		Env:             envVars,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		SecurityContext: hardenedSecurityContext(),
	}
}

// =============================================================================
// SYNC SIDECAR — periodic fetch + worktree cleanup + status reporting
// =============================================================================

// GitWorkspaceSyncContainer creates the main container for the workspace
// Deployment. It runs a continuous sync loop that performs:
//  1. Periodic `git fetch --all` on the bare clone
//  2. Main worktree reset (if strategy is "reset")
//  3. Cleanup of merged-branch worktrees (if cleanupPolicy is "onMerge")
//  4. Status metadata updates (/workspace/.gitworkspace/status.json)
//
// This container never modifies branch worktrees — those are owned by agents.
// It only touches the bare clone (fetch) and the main worktree (reset).
// Runs as the sole container in the workspace Deployment (not as a sidecar).
func GitWorkspaceSyncContainer(ws *agentsv1alpha1.GitWorkspace) corev1.Container {
	image, pullPolicy := getGitImageConfig(ws)

	// Sync interval
	interval := "300" // 5m default in seconds
	if ws.Spec.Sync != nil && ws.Spec.Sync.Interval != "" {
		interval = durationToSeconds(ws.Spec.Sync.Interval)
	}

	// Main worktree strategy
	mainStrategy := "reset"
	if ws.Spec.Sync != nil && ws.Spec.Sync.MainWorktreeStrategy != "" {
		mainStrategy = ws.Spec.Sync.MainWorktreeStrategy
	}

	// Cleanup policy
	cleanupPolicy := "onMerge"
	if ws.Spec.Worktree != nil && ws.Spec.Worktree.CleanupPolicy != "" {
		cleanupPolicy = ws.Spec.Worktree.CleanupPolicy
	}

	// Suspended check
	suspended := "false"
	if ws.Spec.Suspend != nil && *ws.Spec.Suspend {
		suspended = "true"
	}

	script := fmt.Sprintf(`set -e
WORKSPACE="/workspace"
BARE_DIR="$WORKSPACE/.bare"
MAIN_DIR="$WORKSPACE/main"
BRANCH_DIR="$WORKSPACE/branches"
META_DIR="$WORKSPACE/.gitworkspace"
INTERVAL=%s
MAIN_STRATEGY="%s"
CLEANUP_POLICY="%s"
SUSPENDED="%s"

# Configure git credentials if token is available
if [ -n "$GIT_TOKEN" ]; then
  CLONE_HOST=$(echo "$CLONE_URL" | sed -n 's|https\?://\([^/]*\).*|\1|p')
  if [ -n "$CLONE_HOST" ]; then
    echo "https://oauth2:${GIT_TOKEN}@${CLONE_HOST}" > /tmp/.git-credentials
    git config --global credential.helper 'store --file=/tmp/.git-credentials'
  fi
fi

echo "Sync sidecar started (interval=${INTERVAL}s, mainStrategy=$MAIN_STRATEGY, cleanup=$CLEANUP_POLICY)"

while true; do
  sleep "$INTERVAL"

  if [ "$SUSPENDED" = "true" ]; then
    echo "Sync suspended, skipping..."
    continue
  fi

  # Use advisory lock to prevent concurrent fetches (multi-pod RWX scenario)
  LOCK_FILE="$META_DIR/sync.lock"

  # Simple file-based lock with timeout
  if [ -f "$LOCK_FILE" ]; then
    LOCK_AGE=$(( $(date +%%s) - $(stat -c %%Y "$LOCK_FILE" 2>/dev/null || echo 0) ))
    if [ "$LOCK_AGE" -lt "$INTERVAL" ]; then
      echo "Another sync is running (lock age: ${LOCK_AGE}s), skipping..."
      continue
    fi
    echo "Stale lock detected (age: ${LOCK_AGE}s), removing..."
    rm -f "$LOCK_FILE"
  fi

  echo "$$" > "$LOCK_FILE"
  trap 'rm -f "$LOCK_FILE"' EXIT

  echo "Fetching from remote..."
  if git -C "$BARE_DIR" fetch --all --prune 2>&1; then
    echo "Fetch complete."
  else
    echo "Fetch failed, will retry next interval."
    rm -f "$LOCK_FILE"
    continue
  fi

  # Detect default branch
  DEFAULT_BRANCH=$(git -C "$BARE_DIR" symbolic-ref HEAD 2>/dev/null | sed 's|refs/heads/||' || echo "main")

  # Update main worktree if strategy is "reset"
  if [ "$MAIN_STRATEGY" = "reset" ] && [ -d "$MAIN_DIR/.git" ]; then
    echo "Resetting main worktree to origin/$DEFAULT_BRANCH..."
    git -C "$MAIN_DIR" reset --hard "origin/$DEFAULT_BRANCH" 2>/dev/null || true
    # Clean untracked files in main worktree
    git -C "$MAIN_DIR" clean -fd 2>/dev/null || true
  fi

  # Cleanup merged branch worktrees (onMerge policy)
  if [ "$CLEANUP_POLICY" = "onMerge" ] && [ -d "$BRANCH_DIR" ]; then
    for worktree_dir in "$BRANCH_DIR"/*/; do
      [ -d "$worktree_dir" ] || continue
      branch_name=$(basename "$worktree_dir")
      # Check if the remote tracking branch still exists
      if ! git -C "$BARE_DIR" show-ref --verify --quiet "refs/heads/$branch_name" 2>/dev/null && \
         ! git -C "$BARE_DIR" show-ref --verify --quiet "refs/remotes/origin/$branch_name" 2>/dev/null; then
        echo "Branch '$branch_name' no longer exists on remote, cleaning up worktree..."
        git -C "$BARE_DIR" worktree remove --force "$worktree_dir" 2>/dev/null || rm -rf "$worktree_dir"
        echo "Cleaned up worktree: $branch_name"
      fi
    done
    # Prune stale worktree references
    git -C "$BARE_DIR" worktree prune 2>/dev/null || true
  fi

  # Collect worktree info for status
  WORKTREES="[]"
  if command -v git >/dev/null 2>&1; then
    WORKTREES="["
    FIRST=true
    for wt in $(git -C "$BARE_DIR" worktree list --porcelain 2>/dev/null | grep "^worktree " | sed 's/^worktree //'); do
      # Skip the bare dir itself
      [ "$wt" = "$BARE_DIR" ] && continue
      wt_name=$(basename "$wt")
      wt_branch=$(git -C "$wt" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "detached")
      wt_commit=$(git -C "$wt" rev-parse --short HEAD 2>/dev/null || echo "unknown")
      wt_dirty="false"
      if [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ]; then
        wt_dirty="true"
      fi
      if [ "$FIRST" = "true" ]; then FIRST=false; else WORKTREES="$WORKTREES,"; fi
      wt_path=$(echo "$wt" | sed "s|^$WORKSPACE/||")
      WORKTREES="$WORKTREES{\"name\":\"$wt_name\",\"branch\":\"$wt_branch\",\"path\":\"$wt_path\",\"headCommit\":\"$wt_commit\",\"dirty\":$wt_dirty}"
    done
    WORKTREES="$WORKTREES]"
  fi

  # Update status metadata
  REMOTE_HEAD=$(git -C "$BARE_DIR" rev-parse "refs/heads/$DEFAULT_BRANCH" 2>/dev/null || echo "unknown")
  DISK_USAGE=$(du -sh "$WORKSPACE" 2>/dev/null | cut -f1 || echo "unknown")
  cat > "$META_DIR/status.json" <<STATUSEOF
{
  "defaultBranch": "$DEFAULT_BRANCH",
  "remoteHeadCommit": "$REMOTE_HEAD",
  "lastFetchTime": "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)",
  "diskUsage": "$DISK_USAGE",
  "activeWorktrees": $WORKTREES
}
STATUSEOF

  rm -f "$LOCK_FILE"
  echo "Sync cycle complete."
done`, interval, mainStrategy, cleanupPolicy, suspended)

	envVars := []corev1.EnvVar{
		{Name: "HOME", Value: "/tmp"},
	}

	return corev1.Container{
		Name:            "sync",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		WorkingDir:      "/workspace",
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{script},
		Env:             envVars,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		SecurityContext: hardenedSecurityContext(),
	}
}

// =============================================================================
// CONSUMER MOUNTING — volumes and mounts for Agent/PiAgent pods
// =============================================================================

// GitWorkspaceVolume creates a Volume entry for mounting a GitWorkspace PVC
// into a consumer pod (Agent Deployment or PiAgent Job).
func GitWorkspaceVolume(ws *agentsv1alpha1.GitWorkspace, index int) corev1.Volume {
	return corev1.Volume{
		Name: fmt.Sprintf("git-workspace-%d", index),
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: GitWorkspacePVCName(ws),
			},
		},
	}
}

// GitWorkspaceVolumeMount creates a VolumeMount for a GitWorkspace in a consumer
// container. The mount path defaults to /workspaces/<repo-short-name> but can
// be overridden via WorkspaceRef.MountPath.
func GitWorkspaceVolumeMount(ws *agentsv1alpha1.GitWorkspace, ref agentsv1alpha1.WorkspaceRef, index int) corev1.VolumeMount {
	mountPath := ref.MountPath
	if mountPath == "" {
		// Default: /workspaces/<short-repo-name>
		// e.g., "org/platform/api" → "api"
		repoName := ws.Spec.Repository
		if parts := strings.Split(repoName, "/"); len(parts) > 0 {
			repoName = parts[len(parts)-1]
		}
		mountPath = fmt.Sprintf("/workspaces/%s", repoName)
	}

	readOnly := ref.Access == "readonly"

	return corev1.VolumeMount{
		Name:      fmt.Sprintf("git-workspace-%d", index),
		MountPath: mountPath,
		ReadOnly:  readOnly,
	}
}

// GitWorkspaceVolumesForAgent returns all Volume and VolumeMount entries needed
// to mount workspace PVCs into an Agent Deployment pod. This is called by the
// Agent deployment builder to add workspace volumes alongside existing volumes.
//
// workspaces is a map of WorkspaceRef.Name → resolved GitWorkspace object.
// refs is the Agent's workspaceRefs list (preserves ordering).
func GitWorkspaceVolumesForAgent(refs []agentsv1alpha1.WorkspaceRef, workspaces map[string]*agentsv1alpha1.GitWorkspace) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	for i, ref := range refs {
		ws, ok := workspaces[ref.Name]
		if !ok {
			continue // Skip unresolved references (controller should block this)
		}

		volumes = append(volumes, GitWorkspaceVolume(ws, i))
		mounts = append(mounts, GitWorkspaceVolumeMount(ws, ref, i))
	}

	return volumes, mounts
}

// =============================================================================
// CREDENTIAL INJECTION
// =============================================================================

// GitWorkspaceCredentialEnvVars returns environment variables for Git credential
// injection into init containers and sync sidecars. The token is sourced from
// the GitRepo's credentialsRef secret.
func GitWorkspaceCredentialEnvVars(gitRepo *agentsv1alpha1.GitRepo) []corev1.EnvVar {
	if gitRepo.Spec.CredentialsRef == nil {
		return nil
	}

	return []corev1.EnvVar{
		{
			Name: "GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: gitRepo.Spec.CredentialsRef.Name,
					},
					Key: gitRepo.Spec.CredentialsRef.Key,
				},
			},
		},
	}
}

// GitWorkspaceCloneURLEnvVar returns the CLONE_URL environment variable
// for init containers and sync sidecars.
func GitWorkspaceCloneURLEnvVar(cloneURL string) corev1.EnvVar {
	return corev1.EnvVar{
		Name:  "CLONE_URL",
		Value: cloneURL,
	}
}

// =============================================================================
// SSH KEY VOLUME
// =============================================================================

// GitWorkspaceSSHVolume creates a Volume and VolumeMount for SSH key injection
// when the GitRepo uses SSH transport. Returns nil if no SSH key is configured.
func GitWorkspaceSSHVolume(gitRepo *agentsv1alpha1.GitRepo) (*corev1.Volume, *corev1.VolumeMount) {
	if gitRepo.Spec.SSHKeyRef == nil {
		return nil, nil
	}

	vol := &corev1.Volume{
		Name: "ssh-key",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  gitRepo.Spec.SSHKeyRef.Name,
				DefaultMode: int32Ptr(0400),
				Items: []corev1.KeyToPath{
					{Key: gitRepo.Spec.SSHKeyRef.Key, Path: "id_rsa"},
				},
			},
		},
	}

	mount := &corev1.VolumeMount{
		Name:      "ssh-key",
		MountPath: "/etc/git-ssh",
		ReadOnly:  true,
	}

	return vol, mount
}

// =============================================================================
// HELPERS
// =============================================================================

// getGitImageConfig returns the git image and pull policy from workspace spec.
func getGitImageConfig(ws *agentsv1alpha1.GitWorkspace) (string, corev1.PullPolicy) {
	image := DefaultGitImage
	pullPolicy := corev1.PullIfNotPresent

	if ws.Spec.Images != nil {
		if ws.Spec.Images.Git != "" {
			image = ws.Spec.Images.Git
		}
		if ws.Spec.Images.PullPolicy != "" {
			pullPolicy = corev1.PullPolicy(ws.Spec.Images.PullPolicy)
		}
	}

	return image, pullPolicy
}

// getSubmoduleMode returns the submodule mode string from workspace spec.
func getSubmoduleMode(ws *agentsv1alpha1.GitWorkspace) string {
	if ws.Spec.Clone != nil && ws.Spec.Clone.Submodules != "" {
		return ws.Spec.Clone.Submodules
	}
	return "none"
}

// resolveGitAuthor returns the effective git author for a workspace,
// checking workspace-level override first, then falling back to GitRepo-level.
// Returns nil if no author is configured at any level.
func resolveGitAuthor(ws *agentsv1alpha1.GitWorkspace) *agentsv1alpha1.GitAuthor {
	if ws.Spec.Author != nil {
		return ws.Spec.Author
	}
	// GitRepo-level author is resolved by the controller and passed via env vars,
	// not directly accessible from the workspace spec. Return nil here.
	return nil
}

// durationToSeconds converts a Go duration string (e.g., "5m", "1h", "30s")
// to seconds as a string. Falls back to "300" (5m) on parse error.
func durationToSeconds(d string) string {
	// Simple parser for common patterns to avoid importing time package
	// in the resource builder (keep it dependency-light).
	if d == "" {
		return "300"
	}

	// Handle common suffixes
	if strings.HasSuffix(d, "s") {
		val := strings.TrimSuffix(d, "s")
		if _, err := fmt.Sscanf(val, "%d", new(int)); err == nil {
			return val
		}
	}
	if strings.HasSuffix(d, "m") {
		val := strings.TrimSuffix(d, "m")
		var mins int
		if _, err := fmt.Sscanf(val, "%d", &mins); err == nil {
			return fmt.Sprintf("%d", mins*60)
		}
	}
	if strings.HasSuffix(d, "h") {
		val := strings.TrimSuffix(d, "h")
		var hours int
		if _, err := fmt.Sscanf(val, "%d", &hours); err == nil {
			return fmt.Sprintf("%d", hours*3600)
		}
	}

	return "300" // default 5m
}

// int32Ptr returns a pointer to an int32 value.
func int32Ptr(i int32) *int32 { return &i }
