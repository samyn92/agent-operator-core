# GitRepo + GitWorkspace: Architecture Design

## Overview

Two new CRDs enable Git-first agent operations:

- **GitRepo** — Repository registry. Discovers repos via forge APIs, centralizes credentials. Metadata-only, no storage.
- **GitWorkspace** — Materialized working copy. Manages a bare-clone + worktree repository on a PVC via a standalone Deployment.

## Architecture

```
┌─────────────┐     resolves      ┌────────────────┐
│   GitRepo   │ ◄─────────────── │  GitWorkspace   │
│  (registry) │   gitRepoRef      │  (working copy) │
└──────┬──────┘                   └───────┬─────────┘
       │                                  │
       │ forge API                        │ owns
       ▼                                  ▼
  ┌──────────┐                  ┌──────────────────┐
  │ GitHub / │                  │   Deployment      │
  │ GitLab / │                  │  (1 replica)      │
  │ Gitea    │                  │  ┌──────────────┐ │
  └──────────┘                  │  │ init: clone  │ │
                                │  │ main: sync   │ │
                                │  └──────┬───────┘ │
                                └─────────┼─────────┘
                                          │ mounts
                                          ▼
                                ┌──────────────────┐
                                │   PVC (RWX)       │
                                │  /.bare/          │
                                │  /main/           │
                                │  /branches/       │
                                │  /.gitworkspace/  │
                                └────────┬──────────┘
                                         │ also mounted by
                           ┌─────────────┼─────────────┐
                           ▼             ▼             ▼
                     ┌──────────┐  ┌──────────┐  ┌──────────┐
                     │ Agent A  │  │ Agent B  │  │ PiAgent  │
                     │ (deploy) │  │ (deploy) │  │  (job)   │
                     └──────────┘  └──────────┘  └──────────┘
```

## Key Design Decisions

### 1. Standalone Workspace Deployment (not init containers in consumer pods)

Each GitWorkspace gets its own Deployment (1 replica) that owns all git I/O:
- **Init container**: Bare clone + main worktree setup (idempotent on restart)
- **Main container**: Sync loop (periodic fetch, main worktree reset, merged-branch cleanup, status.json writes)

Consumer pods (Agent Deployments, PiAgent Jobs) just mount the RWX PVC. No git init containers or sidecars in consumer pods.

**Why**: If 3 agents share one workspace, they would each need their own clone init container and sync sidecar — racing on the same PVC. One workspace pod eliminates the race condition entirely.

### 2. Bare Clone + Worktree Architecture

The workspace uses `git clone --bare` + `git worktree add`:
- `/.bare/` — Bare clone (shared object store, never checked out)
- `/main/` — Read-only worktree tracking the default branch (auto-reset on fetch)
- `/branches/<name>/` — Agent worktrees (created by agents via `git worktree add`)
- `/.gitworkspace/status.json` — Metadata file for sync status

**Why**: Agents never work on main. They create branches, push, and raise MRs/PRs. Git worktrees share the object store but have separate index/HEAD, providing filesystem-level isolation between concurrent agents.

### 3. RWX PVC by Default

Default access mode is `ReadWriteMany`. Multiple agents can mount the same workspace simultaneously, each working in their own worktree directory on a different branch.

### 4. Readiness Gating

Agent Deployments are blocked until all referenced GitWorkspaces reach `Ready` phase (clone complete, sync container running). This prevents pods from trying to mount PVCs for workspaces that don't exist yet.

## PVC Layout

```
/workspace/
  .bare/                     ← bare clone (shared object store)
  main/                      ← read-only worktree tracking default branch
  branches/
    fix-auth-bug/            ← Agent A's worktree
    feat-rate-limiting/      ← Agent B's worktree
    mr-123-review/           ← PiAgent C's ephemeral worktree
  .gitworkspace/
    status.json              ← sync metadata (defaultBranch, remoteHeadCommit, worktrees, etc.)
    sync.lock                ← advisory lock for fetch operations
```

## How Agents Use Workspaces

1. Agent mounts workspace PVC at `/workspaces/<repo-name>`
2. Agent reads code from `/workspaces/<repo-name>/main/` (always up-to-date default branch)
3. Agent creates worktree: `git worktree add /workspaces/<repo-name>/branches/fix-bug -b fix-bug`
4. Agent works in the worktree: edit, test, commit
5. Agent pushes and creates MR/PR via forge tools
6. Sync container detects merged branch (deleted on remote) and cleans up worktree automatically

## Reconciliation Flow

### GitRepo Controller
1. Fetch GitRepo CR
2. Validate spec (provider matches sub-spec)
3. Resolve credentials from Secret
4. Discover repos via forge API (GitHub/GitLab/Gitea/Generic)
5. Update `status.repositories` with discovered list
6. Requeue on `syncInterval` (default: 5m)

### GitWorkspace Controller
1. Fetch GitWorkspace CR
2. Validate: GitRepo exists + Ready, repository in discovered list
3. Resolve clone URL (SSH if SSH key configured, else HTTPS)
4. Reconcile PVC (create-only, immutable)
5. Reconcile Deployment (init clone + sync loop)
6. Determine phase from Deployment readiness:
   - Deployment not found → `Pending`
   - Deployment exists, no available replicas → `Cloning`
   - Deployment has available replicas → `Ready`
   - Deployment has failure condition → `Error`
7. Track consumers (Agents/PiAgents referencing this workspace)
8. Handle TTL garbage collection (delete idle workspaces)
9. Update status

### Agent Controller (workspace integration)
1. `checkWorkspaceReadiness()` — blocks if any referenced GitWorkspace is not Ready
2. `resolveGitWorkspaces()` — resolves workspaceRefs to PVC mount info
3. Passes `[]GitWorkspaceInfo` to `AgentDeployment()` builder
4. Watches `GitWorkspace` changes to re-reconcile blocked Agents

## Files Changed/Created

### New Files
| File | Purpose |
|------|---------|
| `api/v1alpha1/gitrepo_types.go` | GitRepo CRD types |
| `api/v1alpha1/gitworkspace_types.go` | GitWorkspace CRD types (worktree-first) |
| `internal/resources/gitworkspace.go` | Resource builders (PVC, Deployment, init container, sync container, consumer mounting, credentials) |
| `internal/controller/gitworkspace_controller.go` | GitRepo + GitWorkspace reconcilers |
| `config/crd/bases/agents.io_gitrepoes.yaml` | Generated CRD |
| `config/crd/bases/agents.io_gitworkspaces.yaml` | Generated CRD |

### Modified Files
| File | Change |
|------|--------|
| `api/v1alpha1/agent_types.go` | Added `WorkspaceRef` type + `workspaceRefs` field to `AgentSpec` |
| `api/v1alpha1/piagent_types.go` | Added `workspaceRefs` field to `PiAgentSpec` |
| `internal/resources/deployment.go` | Added `GitWorkspaceInfo` struct, `gitWorkspaces` parameter to `AgentDeployment()` |
| `internal/controller/agent_controller.go` | Added workspace readiness check, workspace resolution, GitWorkspace watch |
| `internal/controller/workflowrun_piagent.go` | Added workspace PVC mounting to PiAgent Jobs |
| `api/v1alpha1/zz_generated.deepcopy.go` | Regenerated |

## Example CRs

### GitRepo (GitLab)
```yaml
apiVersion: agents.io/v1alpha1
kind: GitRepo
metadata:
  name: platform-repos
spec:
  provider: gitlab
  domain: gitlab.com
  credentialsRef:
    name: gitlab-token
    key: token
  gitlab:
    sources:
      - group: org/platform
        pattern: "*"
        recursive: true
```

### GitWorkspace
```yaml
apiVersion: agents.io/v1alpha1
kind: GitWorkspace
metadata:
  name: platform-api
spec:
  gitRepoRef: platform-repos
  repository: org/platform/api
  sync:
    interval: 5m
    mainWorktreeStrategy: reset
  worktree:
    cleanupPolicy: onMerge
    maxWorktrees: 10
  storage:
    size: 10Gi
    accessMode: ReadWriteMany
```

### Agent with Workspaces
```yaml
apiVersion: agents.io/v1alpha1
kind: Agent
metadata:
  name: code-agent
spec:
  workspaceRefs:
    - name: platform-api
    - name: shared-lib
      access: readonly
  capabilityRefs:
    - name: git-tools
    - name: gitlab-tools
  # ...
```

## Future Enhancements

1. **Filesystem-based triggers**: Agents write to `/.gitworkspace/fetch-requested` to trigger immediate fetch instead of waiting for next interval
2. **REST API on workspace pod**: For more complex operations (create worktree, list branches, etc.)
3. **Capability deprecation**: Phase out `CapabilityConfig.Git/GitHub/GitLab` in favor of GitRepo + GitWorkspace
4. **Console integration**: Replace repo browser's capability-scraping with GitRepo CR listing
5. **Workflow integration**: Auto-create ephemeral GitWorkspaces for Workflow triggers
