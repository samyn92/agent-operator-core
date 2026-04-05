package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/resources"
)

// =============================================================================
// GITREPO CONTROLLER
// =============================================================================
// The GitRepoReconciler resolves repository patterns into concrete repository
// lists. It is metadata-only — it never clones or stores repository data.
//
// Reconciliation flow:
//   1. Fetch GitRepo CR
//   2. Validate spec (provider matches sub-spec, credentials accessible)
//   3. Resolve patterns → discover repositories via provider API
//   4. Update status.repositories with discovered list
//   5. Requeue on syncInterval for periodic re-resolution
//
// The controller uses the forge APIs (GitHub REST, GitLab REST, Gitea REST)
// to expand wildcard patterns and list repositories.

// +kubebuilder:rbac:groups=agents.io,resources=gitrepoes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=gitrepoes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=gitworkspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

type GitRepoReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// GitHubClient provides GitHub API access for repo discovery
	GitHubClient GitHubAPIClient
	// GitLabClient provides GitLab API access for repo discovery
	GitLabClient GitLabAPIClient
}

// GitHubAPIClient abstracts GitHub API calls for testability.
type GitHubAPIClient interface {
	// ListOrgRepos lists all repositories for an organization.
	ListOrgRepos(ctx context.Context, token, org string) ([]RepoInfo, error)
	// ListUserRepos lists all repositories for a user.
	ListUserRepos(ctx context.Context, token, user string) ([]RepoInfo, error)
	// GetRepo fetches a single repository's metadata.
	GetRepo(ctx context.Context, token, owner, repo string) (*RepoInfo, error)
}

// GitLabAPIClient abstracts GitLab API calls for testability.
type GitLabAPIClient interface {
	// ListGroupProjects lists all projects in a group (with optional recursion).
	ListGroupProjects(ctx context.Context, token, domain, group string, recursive bool) ([]RepoInfo, error)
	// ListUserProjects lists all projects for a user.
	ListUserProjects(ctx context.Context, token, domain, user string) ([]RepoInfo, error)
	// GetProject fetches a single project's metadata.
	GetProject(ctx context.Context, token, domain, projectPath string) (*RepoInfo, error)
}

// RepoInfo is the unified repository metadata returned by provider API clients.
type RepoInfo struct {
	FullName      string
	CloneURL      string
	SSHURL        string
	DefaultBranch string
	Description   string
	Visibility    string
	Archived      bool
	LastActivity  time.Time
	Topics        []string
}

func (r *GitRepoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch GitRepo
	var gitRepo agentsv1alpha1.GitRepo
	if err := r.Get(ctx, req.NamespacedName, &gitRepo); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Validate spec
	if err := r.validateSpec(&gitRepo); err != nil {
		return r.setFailed(ctx, &gitRepo, "ValidationFailed", err.Error())
	}

	// 3. Update phase to Syncing
	if gitRepo.Status.Phase != agentsv1alpha1.GitRepoPhaseSyncing {
		gitRepo.Status.Phase = agentsv1alpha1.GitRepoPhaseSyncing
		if err := r.Status().Update(ctx, &gitRepo); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Resolve credentials
	token, err := r.resolveCredentials(ctx, &gitRepo)
	if err != nil {
		return r.setFailed(ctx, &gitRepo, "CredentialsError", err.Error())
	}

	// 5. Discover repositories based on provider
	var repos []agentsv1alpha1.DiscoveredRepository
	switch gitRepo.Spec.Provider {
	case agentsv1alpha1.GitRepoProviderGitHub:
		repos, err = r.discoverGitHubRepos(ctx, token, &gitRepo)
	case agentsv1alpha1.GitRepoProviderGitLab:
		repos, err = r.discoverGitLabRepos(ctx, token, &gitRepo)
	case agentsv1alpha1.GitRepoProviderGitea:
		repos, err = r.discoverGiteaRepos(ctx, token, &gitRepo)
	case agentsv1alpha1.GitRepoProviderGeneric:
		repos, err = r.resolveGenericRepos(&gitRepo)
	default:
		err = fmt.Errorf("unsupported provider: %s", gitRepo.Spec.Provider)
	}
	if err != nil {
		return r.setFailed(ctx, &gitRepo, "DiscoveryFailed", err.Error())
	}

	// 6. Count workspaces referencing this GitRepo
	workspaceCount, err := r.countWorkspaces(ctx, &gitRepo)
	if err != nil {
		logger.Error(err, "failed to count workspaces")
	}

	// 7. Update status
	now := metav1.Now()
	gitRepo.Status.Phase = agentsv1alpha1.GitRepoPhaseReady
	gitRepo.Status.Repositories = repos
	gitRepo.Status.RepositoryCount = len(repos)
	gitRepo.Status.LastSyncTime = &now
	gitRepo.Status.WorkspaceCount = workspaceCount
	gitRepo.Status.Conditions = setCondition(gitRepo.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "DiscoveryComplete",
		Message:            fmt.Sprintf("Discovered %d repositories", len(repos)),
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, &gitRepo); err != nil {
		return ctrl.Result{}, err
	}

	// 8. Requeue on syncInterval
	syncInterval := parseDuration(gitRepo.Spec.SyncInterval, 5*time.Minute)
	logger.Info("GitRepo reconciled", "repos", len(repos), "requeueAfter", syncInterval)
	return ctrl.Result{RequeueAfter: syncInterval}, nil
}

func (r *GitRepoReconciler) validateSpec(repo *agentsv1alpha1.GitRepo) error {
	switch repo.Spec.Provider {
	case agentsv1alpha1.GitRepoProviderGitHub:
		if repo.Spec.GitHub == nil || len(repo.Spec.GitHub.Sources) == 0 {
			return fmt.Errorf("github sources required when provider is github")
		}
	case agentsv1alpha1.GitRepoProviderGitLab:
		if repo.Spec.GitLab == nil || len(repo.Spec.GitLab.Sources) == 0 {
			return fmt.Errorf("gitlab sources required when provider is gitlab")
		}
	case agentsv1alpha1.GitRepoProviderGitea:
		if repo.Spec.Gitea == nil || len(repo.Spec.Gitea.Sources) == 0 {
			return fmt.Errorf("gitea sources required when provider is gitea")
		}
	case agentsv1alpha1.GitRepoProviderGeneric:
		if repo.Spec.Generic == nil || len(repo.Spec.Generic.Repositories) == 0 {
			return fmt.Errorf("generic repositories required when provider is generic")
		}
	}
	return nil
}

func (r *GitRepoReconciler) resolveCredentials(ctx context.Context, repo *agentsv1alpha1.GitRepo) (string, error) {
	if repo.Spec.CredentialsRef == nil {
		return "", nil // anonymous access
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name:      repo.Spec.CredentialsRef.Name,
		Namespace: repo.Namespace,
	}, &secret); err != nil {
		return "", fmt.Errorf("credentials secret %q not found: %w", repo.Spec.CredentialsRef.Name, err)
	}
	token, ok := secret.Data[repo.Spec.CredentialsRef.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", repo.Spec.CredentialsRef.Key, repo.Spec.CredentialsRef.Name)
	}
	return string(token), nil
}

func (r *GitRepoReconciler) discoverGitHubRepos(ctx context.Context, token string, repo *agentsv1alpha1.GitRepo) ([]agentsv1alpha1.DiscoveredRepository, error) {
	seen := make(map[string]bool)
	var result []agentsv1alpha1.DiscoveredRepository

	for _, source := range repo.Spec.GitHub.Sources {
		var infos []RepoInfo

		if len(source.Repositories) > 0 {
			// Explicit repo list
			for _, fullName := range source.Repositories {
				owner, name := splitOwnerRepo(fullName)
				info, err := r.GitHubClient.GetRepo(ctx, token, owner, name)
				if err != nil {
					return nil, fmt.Errorf("failed to get repo %s: %w", fullName, err)
				}
				infos = append(infos, *info)
			}
		} else if source.Owner != "" {
			// Owner + pattern expansion
			orgRepos, err := r.GitHubClient.ListOrgRepos(ctx, token, source.Owner)
			if err != nil {
				// Fall back to user repos
				orgRepos, err = r.GitHubClient.ListUserRepos(ctx, token, source.Owner)
				if err != nil {
					return nil, fmt.Errorf("failed to list repos for %s: %w", source.Owner, err)
				}
			}
			// Filter by pattern
			pattern := source.Pattern
			if pattern == "" {
				pattern = "*"
			}
			for _, info := range orgRepos {
				_, repoName := splitOwnerRepo(info.FullName)
				matched, _ := filepath.Match(pattern, repoName)
				if !matched {
					continue
				}
				// Apply filters
				if source.Visibility != "all" && info.Visibility != source.Visibility {
					continue
				}
				if !source.Archived && info.Archived {
					continue
				}
				if len(source.Topics) > 0 && !hasAllTopics(info.Topics, source.Topics) {
					continue
				}
				infos = append(infos, info)
			}
		}

		// Deduplicate and convert
		for _, info := range infos {
			if seen[info.FullName] {
				continue
			}
			seen[info.FullName] = true
			discovered := repoInfoToDiscovered(info)
			result = append(result, discovered)
		}
	}

	return result, nil
}

func (r *GitRepoReconciler) discoverGitLabRepos(ctx context.Context, token string, repo *agentsv1alpha1.GitRepo) ([]agentsv1alpha1.DiscoveredRepository, error) {
	domain := repo.Spec.Domain
	if domain == "" {
		domain = "gitlab.com"
	}

	seen := make(map[string]bool)
	var result []agentsv1alpha1.DiscoveredRepository

	for _, source := range repo.Spec.GitLab.Sources {
		var infos []RepoInfo

		if source.Project != "" {
			// Explicit single project
			info, err := r.GitLabClient.GetProject(ctx, token, domain, source.Project)
			if err != nil {
				return nil, fmt.Errorf("failed to get project %s: %w", source.Project, err)
			}
			infos = append(infos, *info)
		} else if source.Group != "" {
			// Group + pattern expansion (with optional recursion into subgroups)
			groupProjects, err := r.GitLabClient.ListGroupProjects(ctx, token, domain, source.Group, source.Recursive)
			if err != nil {
				return nil, fmt.Errorf("failed to list projects for group %s: %w", source.Group, err)
			}
			pattern := source.Pattern
			if pattern == "" {
				pattern = "*"
			}
			for _, info := range groupProjects {
				_, projectName := splitGroupProject(info.FullName)
				matched, _ := filepath.Match(pattern, projectName)
				if !matched {
					continue
				}
				if source.Visibility != "all" && info.Visibility != source.Visibility {
					continue
				}
				if !source.Archived && info.Archived {
					continue
				}
				if len(source.Topics) > 0 && !hasAllTopics(info.Topics, source.Topics) {
					continue
				}
				infos = append(infos, info)
			}
		} else if source.User != "" {
			// User projects + pattern
			userProjects, err := r.GitLabClient.ListUserProjects(ctx, token, domain, source.User)
			if err != nil {
				return nil, fmt.Errorf("failed to list projects for user %s: %w", source.User, err)
			}
			pattern := source.Pattern
			if pattern == "" {
				pattern = "*"
			}
			for _, info := range userProjects {
				_, projectName := splitGroupProject(info.FullName)
				matched, _ := filepath.Match(pattern, projectName)
				if !matched {
					continue
				}
				infos = append(infos, info)
			}
		}

		for _, info := range infos {
			if seen[info.FullName] {
				continue
			}
			seen[info.FullName] = true
			result = append(result, repoInfoToDiscovered(info))
		}
	}

	return result, nil
}

func (r *GitRepoReconciler) discoverGiteaRepos(ctx context.Context, token string, repo *agentsv1alpha1.GitRepo) ([]agentsv1alpha1.DiscoveredRepository, error) {
	// TODO: Implement Gitea API discovery
	// Follows same pattern as GitHub (owner + pattern or explicit repos)
	return nil, fmt.Errorf("gitea provider not yet implemented")
}

func (r *GitRepoReconciler) resolveGenericRepos(repo *agentsv1alpha1.GitRepo) ([]agentsv1alpha1.DiscoveredRepository, error) {
	var result []agentsv1alpha1.DiscoveredRepository
	for _, entry := range repo.Spec.Generic.Repositories {
		name := entry.Name
		if name == "" {
			name = repoNameFromURL(entry.URL)
		}
		result = append(result, agentsv1alpha1.DiscoveredRepository{
			Name:     name,
			CloneURL: entry.URL,
		})
	}
	return result, nil
}

func (r *GitRepoReconciler) countWorkspaces(ctx context.Context, repo *agentsv1alpha1.GitRepo) (int, error) {
	var workspaces agentsv1alpha1.GitWorkspaceList
	if err := r.List(ctx, &workspaces, client.InNamespace(repo.Namespace)); err != nil {
		return 0, err
	}
	count := 0
	for _, ws := range workspaces.Items {
		if ws.Spec.GitRepoRef == repo.Name {
			count++
		}
	}
	return count, nil
}

func (r *GitRepoReconciler) setFailed(ctx context.Context, repo *agentsv1alpha1.GitRepo, reason, message string) (ctrl.Result, error) {
	repo.Status.Phase = agentsv1alpha1.GitRepoPhaseFailed
	now := metav1.Now()
	repo.Status.Conditions = setCondition(repo.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
	if err := r.Status().Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil // no requeue on validation errors
}

func (r *GitRepoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.GitRepo{}).
		// Watch GitWorkspaces to update workspaceCount
		Watches(&agentsv1alpha1.GitWorkspace{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				ws, ok := obj.(*agentsv1alpha1.GitWorkspace)
				if !ok {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name:      ws.Spec.GitRepoRef,
						Namespace: ws.Namespace,
					},
				}}
			},
		)).
		Complete(r)
}

// =============================================================================
// GITWORKSPACE CONTROLLER
// =============================================================================
// The GitWorkspaceReconciler materializes Git repository working copies.
// It creates a PVC and a standalone Deployment per workspace. The Deployment
// runs a single pod with:
//   - Init container: bare clone + main worktree setup (idempotent)
//   - Main container: sync loop (periodic fetch, main reset, worktree cleanup)
//
// Consumer pods (Agent Deployments, PiAgent Jobs) mount the same RWX PVC
// to access the workspace. They never run git clone/fetch — that's the
// workspace Deployment's job.
//
// Reconciliation flow:
//   1. Fetch GitWorkspace CR
//   2. Validate: ensure GitRepo exists and repository is in discovered list
//   3. Reconcile PVC (create-only, immutable after creation)
//   4. Reconcile workspace Deployment (init clone + sync loop)
//   5. Determine phase from Deployment readiness (Pending/Cloning/Ready/Error)
//   6. Track consumers (Agents/PiAgents that reference this workspace)
//   7. Handle TTL garbage collection
//   8. Update status

// +kubebuilder:rbac:groups=agents.io,resources=gitworkspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=gitworkspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=gitrepoes,verbs=get;list;watch
// +kubebuilder:rbac:groups=agents.io,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agents.io,resources=piagents,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

type GitWorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *GitWorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch GitWorkspace
	var workspace agentsv1alpha1.GitWorkspace
	if err := r.Get(ctx, req.NamespacedName, &workspace); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Validate: check GitRepo exists and repo is in discovered list
	gitRepo, repoEntry, err := r.validateGitRepo(ctx, &workspace)
	if err != nil {
		return r.setWorkspaceFailed(ctx, &workspace, "ValidationFailed", err.Error())
	}

	// Resolve the clone URL — prefer SSH if SSH key is configured, else HTTPS
	cloneURL := r.resolveCloneURL(gitRepo, repoEntry)
	if cloneURL == "" {
		return r.setWorkspaceFailed(ctx, &workspace, "ValidationFailed",
			fmt.Sprintf("no clone URL available for repository %q", workspace.Spec.Repository))
	}

	// 3. Check if suspended — still reconcile PVC and Deployment, but sync
	// container checks the suspend flag itself via its script logic
	_ = workspace.Spec.Suspend // suspend is handled by the sync container script

	// 4. Reconcile PVC (create-only, immutable)
	if err := r.reconcilePVC(ctx, &workspace); err != nil {
		return r.setWorkspaceFailed(ctx, &workspace, "PVCFailed", err.Error())
	}

	// 5. Reconcile workspace Deployment
	if err := r.reconcileDeployment(ctx, &workspace, gitRepo, cloneURL); err != nil {
		return r.setWorkspaceFailed(ctx, &workspace, "DeploymentFailed", err.Error())
	}

	// 6. Determine phase from Deployment readiness
	phase, reason := r.determinePhase(ctx, &workspace)

	// 7. Track consumers
	consumers, err := r.findConsumers(ctx, &workspace)
	if err != nil {
		logger.Error(err, "failed to find consumers")
	}

	// 8. Handle TTL garbage collection
	if workspace.Spec.TTL != "" && len(consumers) == 0 && phase == agentsv1alpha1.GitWorkspacePhaseReady {
		ttlDuration := parseDuration(workspace.Spec.TTL, 0)
		if ttlDuration > 0 && workspace.Status.LastFetchTime != nil {
			idleSince := workspace.Status.LastFetchTime.Time
			if time.Since(idleSince) > ttlDuration {
				logger.Info("GitWorkspace TTL expired, deleting", "idle", time.Since(idleSince))
				return ctrl.Result{}, r.Delete(ctx, &workspace)
			}
		}
	}

	// 9. Update status
	r.updateStatus(ctx, &workspace, phase, reason, cloneURL, repoEntry, consumers)

	// Requeue interval: faster when not yet ready, normal sync interval when ready
	requeueAfter := 10 * time.Second
	if phase == agentsv1alpha1.GitWorkspacePhaseReady {
		requeueAfter = r.getSyncInterval(&workspace)
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// validateGitRepo ensures the referenced GitRepo exists, is Ready, and contains
// the specified repository in its discovered list.
func (r *GitWorkspaceReconciler) validateGitRepo(ctx context.Context, ws *agentsv1alpha1.GitWorkspace) (*agentsv1alpha1.GitRepo, *agentsv1alpha1.DiscoveredRepository, error) {
	var gitRepo agentsv1alpha1.GitRepo
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ws.Spec.GitRepoRef,
		Namespace: ws.Namespace,
	}, &gitRepo); err != nil {
		return nil, nil, fmt.Errorf("GitRepo %q not found: %w", ws.Spec.GitRepoRef, err)
	}

	if gitRepo.Status.Phase != agentsv1alpha1.GitRepoPhaseReady {
		return nil, nil, fmt.Errorf("GitRepo %q is not ready (phase: %s)", ws.Spec.GitRepoRef, gitRepo.Status.Phase)
	}

	// Find the specific repository in the GitRepo's discovered list
	for i, repo := range gitRepo.Status.Repositories {
		if repo.Name == ws.Spec.Repository {
			return &gitRepo, &gitRepo.Status.Repositories[i], nil
		}
	}

	return nil, nil, fmt.Errorf("repository %q not found in GitRepo %q (has %d repos)",
		ws.Spec.Repository, ws.Spec.GitRepoRef, len(gitRepo.Status.Repositories))
}

// resolveCloneURL determines which clone URL to use. Prefers SSH when an SSH
// key is configured, otherwise falls back to HTTPS.
func (r *GitWorkspaceReconciler) resolveCloneURL(gitRepo *agentsv1alpha1.GitRepo, repoEntry *agentsv1alpha1.DiscoveredRepository) string {
	if gitRepo.Spec.SSHKeyRef != nil && repoEntry.SSHURL != "" {
		return repoEntry.SSHURL
	}
	return repoEntry.CloneURL
}

// reconcilePVC creates the workspace PVC if it doesn't exist.
// PVCs are immutable after creation — we only create, never update.
func (r *GitWorkspaceReconciler) reconcilePVC(ctx context.Context, ws *agentsv1alpha1.GitWorkspace) error {
	pvcName := resources.GitWorkspacePVCName(ws)

	var existingPVC corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ws.Namespace}, &existingPVC); err == nil {
		return nil // PVC already exists
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check PVC %s: %w", pvcName, err)
	}

	// Create new PVC
	pvc := resources.GitWorkspacePVC(ws)

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(ws, pvc, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on PVC: %w", err)
	}

	if err := r.Create(ctx, pvc); err != nil {
		return fmt.Errorf("failed to create PVC %s: %w", pvcName, err)
	}

	log.FromContext(ctx).Info("Created workspace PVC", "pvc", pvcName)
	return nil
}

// reconcileDeployment creates or updates the workspace Deployment.
// The Deployment runs a single pod with init clone + sync loop.
func (r *GitWorkspaceReconciler) reconcileDeployment(ctx context.Context, ws *agentsv1alpha1.GitWorkspace, gitRepo *agentsv1alpha1.GitRepo, cloneURL string) error {
	depName := resources.GitWorkspaceDeploymentName(ws)
	desired := resources.GitWorkspaceDeployment(ws, gitRepo, cloneURL)

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(ws, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on Deployment: %w", err)
	}

	// Check if Deployment already exists
	var existing appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: depName, Namespace: ws.Namespace}, &existing); err != nil {
		if errors.IsNotFound(err) {
			// Create new Deployment
			if err := r.Create(ctx, desired); err != nil {
				return fmt.Errorf("failed to create Deployment %s: %w", depName, err)
			}
			log.FromContext(ctx).Info("Created workspace Deployment", "deployment", depName)
			return nil
		}
		return fmt.Errorf("failed to check Deployment %s: %w", depName, err)
	}

	// Update if spec changed (using hash comparison like the Agent controller)
	desiredHash := desired.Annotations[resources.DesiredSpecHashAnnotation]
	existingHash := existing.Annotations[resources.DesiredSpecHashAnnotation]
	if desiredHash != existingHash {
		existing.Spec = desired.Spec
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		existing.Annotations[resources.DesiredSpecHashAnnotation] = desiredHash
		if err := r.Update(ctx, &existing); err != nil {
			return fmt.Errorf("failed to update Deployment %s: %w", depName, err)
		}
		log.FromContext(ctx).Info("Updated workspace Deployment", "deployment", depName)
	}

	return nil
}

// determinePhase checks the workspace Deployment's status to determine the
// GitWorkspace phase. The init container doing the clone must complete before
// the sync container starts, so Deployment readiness implies clone is done.
func (r *GitWorkspaceReconciler) determinePhase(ctx context.Context, ws *agentsv1alpha1.GitWorkspace) (agentsv1alpha1.GitWorkspacePhase, string) {
	depName := resources.GitWorkspaceDeploymentName(ws)

	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: depName, Namespace: ws.Namespace}, &dep); err != nil {
		if errors.IsNotFound(err) {
			return agentsv1alpha1.GitWorkspacePhasePending, "Waiting for workspace Deployment to be created"
		}
		return agentsv1alpha1.GitWorkspacePhaseError, fmt.Sprintf("Failed to check Deployment: %v", err)
	}

	// Check if Deployment has available replicas
	if dep.Status.AvailableReplicas > 0 {
		return agentsv1alpha1.GitWorkspacePhaseReady, "Workspace is ready"
	}

	// Check if the Deployment has any pods at all
	if dep.Status.Replicas == 0 {
		return agentsv1alpha1.GitWorkspacePhasePending, "Waiting for workspace pod to be scheduled"
	}

	// Pod exists but not ready — likely init container is running (cloning)
	// Check for failure conditions
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			return agentsv1alpha1.GitWorkspacePhaseError, fmt.Sprintf("Deployment failure: %s", cond.Message)
		}
	}

	// Init container is probably still running the bare clone
	return agentsv1alpha1.GitWorkspacePhaseCloning, "Clone in progress (init container running)"
}

// findConsumers lists all Agents and PiAgents that reference this workspace.
func (r *GitWorkspaceReconciler) findConsumers(ctx context.Context, ws *agentsv1alpha1.GitWorkspace) ([]agentsv1alpha1.GitWorkspaceConsumer, error) {
	var consumers []agentsv1alpha1.GitWorkspaceConsumer

	// Find Agents referencing this workspace
	var agents agentsv1alpha1.AgentList
	if err := r.List(ctx, &agents, client.InNamespace(ws.Namespace)); err != nil {
		return nil, err
	}
	for _, agent := range agents.Items {
		for _, ref := range agent.Spec.WorkspaceRefs {
			if ref.Name == ws.Name {
				access := ref.Access
				if access == "" {
					access = "readwrite"
				}
				consumers = append(consumers, agentsv1alpha1.GitWorkspaceConsumer{
					Kind:   "Agent",
					Name:   agent.Name,
					Access: access,
				})
			}
		}
	}

	// Find PiAgents referencing this workspace
	var piAgents agentsv1alpha1.PiAgentList
	if err := r.List(ctx, &piAgents, client.InNamespace(ws.Namespace)); err != nil {
		return nil, err
	}
	for _, piAgent := range piAgents.Items {
		for _, ref := range piAgent.Spec.WorkspaceRefs {
			if ref.Name == ws.Name {
				access := ref.Access
				if access == "" {
					access = "readwrite"
				}
				consumers = append(consumers, agentsv1alpha1.GitWorkspaceConsumer{
					Kind:   "PiAgent",
					Name:   piAgent.Name,
					Access: access,
				})
			}
		}
	}

	return consumers, nil
}

// updateStatus writes the current workspace status. Gets a fresh copy first
// to avoid conflicts, following the pattern from agent_controller.go.
func (r *GitWorkspaceReconciler) updateStatus(
	ctx context.Context,
	ws *agentsv1alpha1.GitWorkspace,
	phase agentsv1alpha1.GitWorkspacePhase,
	reason string,
	cloneURL string,
	repoEntry *agentsv1alpha1.DiscoveredRepository,
	consumers []agentsv1alpha1.GitWorkspaceConsumer,
) {
	logger := log.FromContext(ctx)

	// Get fresh copy to avoid update conflicts
	var fresh agentsv1alpha1.GitWorkspace
	if err := r.Get(ctx, types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}, &fresh); err != nil {
		logger.Error(err, "failed to get fresh GitWorkspace for status update")
		return
	}

	// Update fields
	fresh.Status.Phase = phase
	fresh.Status.PVCName = resources.GitWorkspacePVCName(ws)
	fresh.Status.CloneURL = cloneURL
	fresh.Status.Consumers = consumers

	if repoEntry != nil && repoEntry.DefaultBranch != "" {
		fresh.Status.DefaultBranch = repoEntry.DefaultBranch
	}

	// Set condition
	now := metav1.Now()
	condStatus := metav1.ConditionTrue
	condReason := "WorkspaceReady"
	condMessage := reason
	if phase != agentsv1alpha1.GitWorkspacePhaseReady {
		condStatus = metav1.ConditionFalse
		if phase == agentsv1alpha1.GitWorkspacePhaseError {
			condReason = "WorkspaceError"
		} else {
			condReason = "WorkspaceNotReady"
		}
	}
	fresh.Status.Conditions = setCondition(fresh.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMessage,
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, &fresh); err != nil {
		logger.Error(err, "failed to update GitWorkspace status")
	}
}

func (r *GitWorkspaceReconciler) getSyncInterval(ws *agentsv1alpha1.GitWorkspace) time.Duration {
	if ws.Spec.Sync != nil && ws.Spec.Sync.Interval != "" {
		return parseDuration(ws.Spec.Sync.Interval, 5*time.Minute)
	}
	return 5 * time.Minute
}

func (r *GitWorkspaceReconciler) setWorkspaceFailed(ctx context.Context, ws *agentsv1alpha1.GitWorkspace, reason, message string) (ctrl.Result, error) {
	ws.Status.Phase = agentsv1alpha1.GitWorkspacePhaseError
	now := metav1.Now()
	ws.Status.Conditions = setCondition(ws.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
	if err := r.Status().Update(ctx, ws); err != nil {
		return ctrl.Result{}, err
	}
	// Requeue with backoff for transient errors (e.g., GitRepo not ready yet)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *GitWorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.GitWorkspace{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		// Re-reconcile when the referenced GitRepo changes (e.g., repo list updated)
		Watches(&agentsv1alpha1.GitRepo{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				gitRepo, ok := obj.(*agentsv1alpha1.GitRepo)
				if !ok {
					return nil
				}
				// Find all GitWorkspaces referencing this GitRepo
				var workspaces agentsv1alpha1.GitWorkspaceList
				if err := mgr.GetClient().List(ctx, &workspaces, client.InNamespace(gitRepo.Namespace)); err != nil {
					return nil
				}
				var requests []reconcile.Request
				for _, ws := range workspaces.Items {
					if ws.Spec.GitRepoRef == gitRepo.Name {
						requests = append(requests, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      ws.Name,
								Namespace: ws.Namespace,
							},
						})
					}
				}
				return requests
			},
		)).
		// Re-reconcile when Agents change (to update consumer tracking)
		Watches(&agentsv1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(
			r.findWorkspacesForConsumer,
		)).
		// Re-reconcile when PiAgents change (to update consumer tracking)
		Watches(&agentsv1alpha1.PiAgent{}, handler.EnqueueRequestsFromMapFunc(
			r.findWorkspacesForConsumer,
		)).
		Complete(r)
}

// findWorkspacesForConsumer maps an Agent or PiAgent to the GitWorkspaces it references.
// Used by the watch handlers to re-reconcile workspaces when consumers change.
func (r *GitWorkspaceReconciler) findWorkspacesForConsumer(ctx context.Context, obj client.Object) []reconcile.Request {
	var workspaceNames []string

	switch consumer := obj.(type) {
	case *agentsv1alpha1.Agent:
		for _, ref := range consumer.Spec.WorkspaceRefs {
			workspaceNames = append(workspaceNames, ref.Name)
		}
	case *agentsv1alpha1.PiAgent:
		for _, ref := range consumer.Spec.WorkspaceRefs {
			workspaceNames = append(workspaceNames, ref.Name)
		}
	default:
		return nil
	}

	var requests []reconcile.Request
	for _, name := range workspaceNames {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      name,
				Namespace: obj.GetNamespace(),
			},
		})
	}
	return requests
}

// =============================================================================
// HELPERS
// =============================================================================

func repoInfoToDiscovered(info RepoInfo) agentsv1alpha1.DiscoveredRepository {
	var lastActivity *metav1.Time
	if !info.LastActivity.IsZero() {
		t := metav1.NewTime(info.LastActivity)
		lastActivity = &t
	}
	return agentsv1alpha1.DiscoveredRepository{
		Name:          info.FullName,
		CloneURL:      info.CloneURL,
		SSHURL:        info.SSHURL,
		DefaultBranch: info.DefaultBranch,
		Description:   info.Description,
		Visibility:    info.Visibility,
		Archived:      info.Archived,
		LastActivity:  lastActivity,
	}
}

func splitOwnerRepo(fullName string) (string, string) {
	for i, c := range fullName {
		if c == '/' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return fullName, ""
}

func splitGroupProject(fullPath string) (string, string) {
	// GitLab projects can have nested groups: "org/platform/api"
	// Return everything before the last / as group, and the last segment as project
	for i := len(fullPath) - 1; i >= 0; i-- {
		if fullPath[i] == '/' {
			return fullPath[:i], fullPath[i+1:]
		}
	}
	return "", fullPath
}

func repoNameFromURL(url string) string {
	// Extract repo name from URL: https://github.com/org/repo.git -> repo
	base := filepath.Base(url)
	if ext := filepath.Ext(base); ext == ".git" {
		base = base[:len(base)-4]
	}
	return base
}

func hasAllTopics(repoTopics, requiredTopics []string) bool {
	topicSet := make(map[string]bool)
	for _, t := range repoTopics {
		topicSet[t] = true
	}
	for _, required := range requiredTopics {
		if !topicSet[required] {
			return false
		}
	}
	return true
}

func setCondition(conditions []metav1.Condition, newCondition metav1.Condition) []metav1.Condition {
	for i, c := range conditions {
		if c.Type == newCondition.Type {
			conditions[i] = newCondition
			return conditions
		}
	}
	return append(conditions, newCondition)
}

func parseDuration(s string, defaultDuration time.Duration) time.Duration {
	if s == "" {
		return defaultDuration
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultDuration
	}
	return d
}
