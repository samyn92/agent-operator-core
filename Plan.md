# Implementation Plan: Dual-Runtime Agent Platform

## Vision

A Git-based AI Agentic Engineering platform with two complementary runtimes and a modular tool ecosystem:

- **OpenCode Agents** (`Agent` CRD) — Chat-based, exploratory sessions via ACP. Heavy runtime with PVC, sidecars, MCP. Always-on Deployments. For when you don't know the goal yet and want to explore interactively.
- **Pi Agents** (`PiAgent` CRD) — Workflow-optimized, purpose-built TypeScript agents. Lightweight Jobs, on-demand execution, granular event streaming. For structured, repeatable processes with rich process-feel UI.
- **Modular OCI Tool Packages** — Reusable tool libraries published as OCI artifacts. PiAgents declare tool dependencies via `toolRefs`, pulled at Job runtime via init containers. A cloud-native "app store" of composable agent capabilities.

Both runtimes coexist. Workflows can mix them per step. The console serves both paradigms: chat interface for OpenCode, process pipeline UI for Pi-powered workflows. Tool packages are independently versioned and shared across agents.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                       Agent Console (SolidJS)                        │
├────────────────────────────┬─────────────────────────────────────────┤
│  [Chat]                    │  [Workflows]                             │
│  OpenCode ACP/SSE          │  Pi Event Stream SSE                     │
│  Exploratory sessions      │  Process pipeline UI                     │
├────────────────────────────┼─────────────────────────────────────────┤
│  Agent CRD                 │  PiAgent CRD                             │
│  → Deployment (always-on)  │  → Job (on-demand)                       │
│  → PVC + Sidecars + MCP    │  → OCI artifact + TypeScript             │
│  → OpenCode runtime        │  → pi-agent-core runtime                 │
│  → HTTP polling protocol   │  → JSONL event stream                    │
│                            │  → toolRefs → OCI tool init containers   │
│  12+ config fields         │  ~5 config fields + toolRefs             │
│  Heavy, full-featured      │  Light, purpose-built, composable        │
└────────────────────────────┴─────────────────────────────────────────┘

OCI Tool Registry (ghcr.io):
  ┌──────────┐ ┌──────────┐ ┌───────────┐ ┌───────────┐ ┌──────────┐
  │ git-tools│ │file-tools│ │gitlab-tools│ │github-tools│ │kubectl   │
  │  v0.1.0  │ │  v0.1.0  │ │   v0.1.0  │ │   v0.1.0  │ │  v0.1.0  │
  └──────────┘ └──────────┘ └───────────┘ └───────────┘ └──────────┘

Workflow CRD orchestrates both:
  steps:
    - agent: research-bot        # OpenCode runtime
    - piAgent: pr-classifier     # Pi runtime (with toolRefs)

WorkflowRun controller handles output delivery:
  output:
    github:
      comment: true              # Posts result as PR comment
    gitlab:
      note: true                 # Posts result as MR note
```

---

## Phase 1: PiAgent CRD & Controller -- COMPLETE

### 1.1 PiAgent Types (`api/v1alpha1/piagent_types.go`)

New CRD that shares common types (`ProviderConfig`, `IdentityConfig`, `SecretKeySelector`) with Agent but has a fundamentally different spec.

```go
type PiAgentSpec struct {
    Model             string                        `json:"model"`
    Providers         []ProviderConfig              `json:"providers"`
    Identity          *IdentityConfig               `json:"identity,omitempty"`
    Source            PiAgentSource                 `json:"source"`
    ThinkingLevel     string                        `json:"thinkingLevel,omitempty"`
    ToolExecution     string                        `json:"toolExecution,omitempty"`
    Resources         *corev1.ResourceRequirements  `json:"resources,omitempty"`
    ServiceAccountName string                       `json:"serviceAccountName,omitempty"`
    Image             string                        `json:"image,omitempty"`
    Timeout           string                        `json:"timeout,omitempty"`
}

type PiAgentSource struct {
    OCI          *OCIArtifactRef  `json:"oci,omitempty"`
    Inline       string           `json:"inline,omitempty"`
    ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}
```

Key differences from Agent:
- **No Deployment** — PiAgent is a definition, not a running workload. It becomes a Job only when invoked by a WorkflowRun.
- **No PVC, no sidecars, no MCP** — tools are TypeScript functions in the source (or pulled via `toolRefs`).
- **Source field** — OCI artifact, inline, or ConfigMap containing the agent's TypeScript module.
- **Status is simpler** — just "is the source resolvable?" not "is the pod running?"

### 1.2 PiAgent Controller (`internal/controller/piagent_controller.go`)

Validation-only controller (279 lines):
1. Source validation — exactly one of OCI/inline/ConfigMap must be set
2. Provider secret verification — confirms referenced Secrets exist
3. Model-references-provider validation — ensures model's provider matches a configured provider
4. OCI Cosign signature verification — when `source.oci.verify` is configured
5. Status update — sets phase to Ready/Pending/Failed

### 1.3 Deliverables -- DONE
- [x] `api/v1alpha1/piagent_types.go` — PiAgent CRD types (174 lines)
- [x] `internal/controller/piagent_controller.go` — PiAgent controller (279 lines)
- [x] Registered in `cmd/operator/main.go`
- [x] Registered in `api/v1alpha1/groupversion_info.go`
- [x] Generated CRD YAML (`config/crd/bases/agents.io_piagents.yaml`)
- [x] Helm chart CRDs auto-updated (shared directory)

---

## Phase 2: Pi Runner Image -- COMPLETE

### 2.1 The Harness (`pi-runner`)

A Node.js container image (~345 lines of TypeScript) that:

1. Loads the agent TypeScript module from `/agent/index.js` or `AGENT_INLINE_CODE` env var
2. Configures the model using `@mariozechner/pi-ai` (`getModel` + `streamSimple`)
3. Creates an `Agent` instance from `@mariozechner/pi-agent-core`
4. Runs `agent.prompt(message)` and `agent.waitForIdle()`
5. Streams all 11 event types as JSONL to stdout
6. Writes `result.json` to `/output/` on completion

```
images/pi-runner/
├── Dockerfile           # 2-stage: node:22-alpine builder (tsc) + runtime with git
├── package.json         # @mariozechner/pi-agent-core, @mariozechner/pi-ai, @sinclair/typebox
├── src/runner.ts        # The harness (~345 lines, compiles to 261-line JS)
└── tsconfig.json        # ES2022 + Node16 module resolution, strict mode
```

### 2.2 Event Protocol

The runner outputs one JSON object per line to stdout:

```jsonl
{"type":"agent_start","ts":1720000000000}
{"type":"turn_start","ts":1720000000050}
{"type":"message_start","ts":1720000000060}
{"type":"message_update","ts":1720000000100,"data":{"delta":"The PR modifies..."}}
{"type":"tool_execution_start","ts":1720000000200,"data":{"toolName":"classify_pr","toolCallId":"tc_1","args":{...}}}
{"type":"tool_execution_end","ts":1720000000300,"data":{"toolName":"classify_pr","toolCallId":"tc_1","result":{...}}}
{"type":"message_end","ts":1720000000400,"data":{"text":"Classification: security, refactor"}}
{"type":"turn_end","ts":1720000000450}
{"type":"agent_end","ts":1720000000500,"data":{"totalTokens":1234,"toolCalls":2}}
```

### 2.3 Configuration via Environment

| Env Var | Source |
|---------|--------|
| `MODEL_PROVIDER` | PiAgent.spec.model (provider part) |
| `MODEL_NAME` | PiAgent.spec.model (model part) |
| `PROVIDER_API_KEY` | From PiAgent.spec.providers[].apiKeySecret |
| `THINKING_LEVEL` | PiAgent.spec.thinkingLevel (maps "off" to "minimal") |
| `TOOL_EXECUTION` | PiAgent.spec.toolExecution |
| `PROMPT` | Rendered prompt from WorkflowStep |
| `TRIGGER_DATA` | WorkflowRun.spec.triggerData |
| `AGENT_INLINE_CODE` | PiAgent.spec.source.inline (for inline sources) |

### 2.4 Technical Notes
- `getModel(provider, modelId)` requires `KnownProvider` literal types at compile time; the runner casts via `(getModel as (provider: string, modelId: string) => ReturnType<typeof getModel>)` for runtime strings.
- Pi's `ThinkingLevel` type is `"minimal" | "low" | "medium" | "high" | "xhigh"` — does NOT include `"off"`. The runner maps `"off"` to `"minimal"`.
- `agent_end` event's `messages: AgentMessage[]` contains `usage.totalTokens` on assistant messages.

### 2.5 Deliverables -- DONE
- [x] `images/pi-runner/Dockerfile` — 2-stage build
- [x] `images/pi-runner/src/runner.ts` — the harness (~345 lines)
- [x] `images/pi-runner/package.json` — 175 packages install clean
- [x] `images/pi-runner/tsconfig.json` — compiles clean
- [x] Published as part of `v0.0.16` release to `ghcr.io/samyn92/pi-runner`

---

## Phase 3: Workflow CRD Evolution -- COMPLETE

### 3.1 WorkflowStep — Dual Runtime Support

Added `piAgent` field alongside existing `agent`:

```go
type WorkflowStep struct {
    Name            string `json:"name,omitempty"`
    Agent           string `json:"agent,omitempty"`    // OpenCode runtime
    PiAgent         string `json:"piAgent,omitempty"`  // Pi runtime
    Prompt          string `json:"prompt"`
    Condition       string `json:"condition,omitempty"`
    Timeout         string `json:"timeout,omitempty"`
    ContinueOnError *bool  `json:"continueOnError,omitempty"`
}
```

### 3.2 WorkflowSpec Simple Mode — Dual Runtime

Same pattern: `Agent string` and `PiAgent string` at the top level.

### 3.3 StepResult — Pi-Specific Fields

```go
type StepResult struct {
    Name           string       `json:"name"`
    Phase          string       `json:"phase"`
    Output         string       `json:"output,omitempty"`
    StartTime      *metav1.Time `json:"startTime,omitempty"`
    CompletionTime *metav1.Time `json:"completionTime,omitempty"`
    Error          string       `json:"error,omitempty"`
    SessionID      string       `json:"sessionID,omitempty"`  // OpenCode
    JobName        string       `json:"jobName,omitempty"`    // Pi
    ToolCalls      int          `json:"toolCalls,omitempty"`  // Pi
    TokensUsed     int          `json:"tokensUsed,omitempty"` // Pi
}
```

### 3.4 Workflow Controller

Validates both `agent` and `piAgent` references. Enforces exactly one of Agent/PiAgent per step.

### 3.5 Deliverables -- DONE
- [x] Updated `api/v1alpha1/workflow_types.go` — `PiAgent` fields on WorkflowStep and WorkflowSpec
- [x] Updated `internal/controller/workflow_controller.go` — PiAgent reference validation
- [x] Regenerated CRD YAML for Workflow and WorkflowRun
- [x] Helm chart CRDs auto-updated

---

## Phase 4: WorkflowRun Controller — Pi Execution -- COMPLETE

### 4.1 Job-Based Execution (`internal/controller/workflowrun_piagent.go`)

~470 lines implementing full Pi execution lifecycle:

1. **Job Creation** — `buildPiAgentJob()` constructs a Kubernetes Job with:
   - Init container for OCI artifact pull (`crane export <ref> - | tar -xf - -C /agent`)
   - Main container running the pi-runner image
   - EmptyDir volumes for `/agent` (code) and `/output` (results)
   - Env vars from PiAgent spec + rendered prompt + trigger data
   - ServiceAccount, resources, timeout from PiAgent spec

2. **Job Polling** — Checks `job.Status.Succeeded > 0` or `job.Status.Failed > 0` on 2-second requeue intervals.

3. **Output Collection** — Reads pod logs via `k8s.io/client-go/kubernetes` clientset (not controller-runtime, which doesn't support subresource streaming). Parses JSONL to extract:
   - Final output from last `message_end` event
   - Tool call count from `tool_execution_end` events
   - Token usage from `agent_end` event

4. **Inline Source Handling** — When `source.inline` is set, injects code via `AGENT_INLINE_CODE` env var instead of OCI init container.

5. **ConfigMap Source Handling** — Mounts ConfigMap as a volume at `/agent`.

### 4.2 RBAC

Added to `config/rbac/role.yaml`:
- `batch/jobs` — create, get, list, watch, delete
- `""` (core) `pods` — get, list, watch
- `""` (core) `pods/log` — get
- `agents.io` `piagents` — get, list, watch

### 4.3 Deliverables -- DONE
- [x] `internal/controller/workflowrun_piagent.go` — Full Pi execution (~470 lines)
- [x] Updated `internal/controller/workflowrun_controller.go` — PiAgent step routing
- [x] `config/rbac/role.yaml` — batch/jobs, pods, pods/log RBAC
- [x] All generated manifests updated

---

## Phase 5: OCI Artifacts for Pi Agents -- COMPLETE (in agent-tools repo)

### 5.1 `agent-tools push piagent` Command

Implemented in `agent-tools` repo using ORAS Go library v2.6.0:

```bash
agent-tools push piagent ./my-agent/ --tag ghcr.io/myorg/pr-classifier:v1.0.0
```

### 5.2 OCI Artifact Structure

```
OCI Manifest (schemaVersion: 2, OCI 1.1):
  ArtifactType: application/vnd.agents.io.piagent.v1
  Layers:
    - application/vnd.agents.io.piagent.code.v1.tar+gzip  (agent code tarball)
    - application/vnd.agents.io.piagent.config.v1+json     (config metadata)
```

Implementation details:
- Uses `specs.Versioned{SchemaVersion: 2}` embedded struct (OCI 1.1 native `ArtifactType`)
- `oras.Copy(ctx, store, ref.tag, repo, ref.tag, oras.DefaultCopyOptions)` for push
- Reads Docker credentials from `~/.docker/config.json` for registry auth
- Automatically excludes `node_modules/`, `.git/`, `dist/`, lock files
- Pull side uses `crane export <ref> - | tar -xf - -C /agent` in init containers

### 5.3 Deliverables -- DONE
- [x] `agent-tools/pkg/piagent/pusher.go` — OCI artifact pusher (~290 lines)
- [x] `agent-tools/pkg/piagent/helpers.go` — Helpers (~95 lines)
- [x] `agent-tools/cmd/cli/push.go` — Parent push command
- [x] `agent-tools/cmd/cli/push_piagent.go` — Push piagent subcommand (~95 lines)
- [x] Published as `agent-tools` v0.0.3 (4 CLI binaries + 7 tool images)

---

## Phase 5b: Modular OCI Tool Packages

### 5b.1 Concept

Tools should be OCI artifacts rather than code baked into agent source or runner image. This creates a modular, composable ecosystem:

- **Independent versioning** — each tool package (git, file, gitlab, etc.) has its own release cycle
- **Reusable across agents** — any PiAgent can declare which tools it needs via `toolRefs`
- **Cloud-native distribution** — standard OCI registries, Cosign signing, pull policies
- **Separation of concerns** — agent source contains business logic + system prompt; tools provide capabilities

### 5b.2 `toolRefs` on PiAgentSpec

Add a new field to the PiAgent CRD:

```go
type PiAgentSpec struct {
    // ...existing fields...

    // ToolRefs is a list of OCI artifacts containing AgentTool[] modules.
    // Each toolRef becomes an init container that pulls tools into /tools/<name>/.
    // The runner scans /tools/*/index.js and merges all tool arrays.
    ToolRefs []OCIArtifactRef `json:"toolRefs,omitempty"`
}
```

Example PiAgent using toolRefs:

```yaml
apiVersion: agents.io/v1alpha1
kind: PiAgent
metadata:
  name: issue-worker
spec:
  model: anthropic/claude-sonnet-4-20250514
  providers:
    - name: anthropic
      apiKeySecret: { name: anthropic-key, key: api-key }
  source:
    oci:
      ref: ghcr.io/samyn92/agents/issue-worker:0.1.0
  toolRefs:
    - ref: ghcr.io/samyn92/agent-tools/git:0.1.0
    - ref: ghcr.io/samyn92/agent-tools/file:0.1.0
    - ref: ghcr.io/samyn92/agent-tools/gitlab:0.1.0
  thinkingLevel: medium
  timeout: 10m
```

### 5b.3 OCI Tool Artifact Structure

Each tool package is an OCI artifact containing:

```
<tool-name>/
├── index.js          # Exports AgentTool[] (compiled from TypeScript)
├── manifest.json     # Tool metadata
└── ...               # Additional support files
```

`manifest.json`:
```json
{
  "name": "git-tools",
  "version": "0.1.0",
  "description": "Git operations: clone, branch, commit, push, diff, log",
  "requiredEnv": ["GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"],
  "requiredBinaries": ["git"]
}
```

`index.js` exports:
```typescript
import { Type } from "@sinclair/typebox";
import type { AgentTool } from "@mariozechner/pi-agent-core";

export const tools: AgentTool[] = [
    {
        name: "git_clone",
        description: "Clone a git repository",
        parameters: Type.Object({
            url: Type.String({ description: "Repository URL" }),
            path: Type.Optional(Type.String({ description: "Clone destination" })),
            branch: Type.Optional(Type.String({ description: "Branch to checkout" })),
        }),
        execute: async (toolCallId, params, signal, onUpdate) => {
            // Shell out to git or use isomorphic-git
            const result = await exec(`git clone ${params.url} ${params.path || '/workspace/repo'}`);
            return { content: [{ type: "text", text: result.stdout }] };
        },
    },
    // git_branch, git_commit, git_push, git_diff, git_log, etc.
];
```

OCI media types:
```
Artifact type:  application/vnd.agents.io.tool.v1
Code layer:     application/vnd.agents.io.tool.code.v1.tar+gzip
Config layer:   application/vnd.agents.io.tool.config.v1+json
```

### 5b.4 Job Construction with toolRefs

The WorkflowRun controller generates one init container per toolRef:

```go
func (r *WorkflowRunReconciler) buildToolRefInitContainers(piAgent *PiAgent) []corev1.Container {
    var initContainers []corev1.Container
    for i, toolRef := range piAgent.Spec.ToolRefs {
        // Extract tool name from OCI ref (last path segment before tag)
        toolName := extractToolName(toolRef.Ref) // e.g., "git" from "ghcr.io/samyn92/agent-tools/git:0.1.0"
        initContainers = append(initContainers, corev1.Container{
            Name:    fmt.Sprintf("tool-%d-%s", i, toolName),
            Image:   "gcr.io/go-containerregistry/crane:latest",
            Command: []string{"sh", "-c"},
            Args:    []string{fmt.Sprintf("crane export %s - | tar -xf - -C /tools/%s/", toolRef.Ref, toolName)},
            VolumeMounts: []corev1.VolumeMount{
                {Name: "tools", MountPath: "/tools"},
            },
        })
    }
    return initContainers
}
```

Resulting Job pod spec:
```
InitContainers:
  tool-0-git:    crane export ghcr.io/.../git:0.1.0 - | tar -xf - -C /tools/git/
  tool-1-file:   crane export ghcr.io/.../file:0.1.0 - | tar -xf - -C /tools/file/
  tool-2-gitlab: crane export ghcr.io/.../gitlab:0.1.0 - | tar -xf - -C /tools/gitlab/
  agent-code:    crane export ghcr.io/.../issue-worker:0.1.0 - | tar -xf - -C /agent/

Containers:
  pi-runner:
    VolumeMounts: /agent, /tools, /output
```

### 5b.5 Runner Tool Loading

Update `images/pi-runner/src/runner.ts` to scan `/tools/*/index.js`:

```typescript
import { readdir } from 'fs/promises';
import { join } from 'path';
import type { AgentTool } from "@mariozechner/pi-agent-core";

async function loadToolRefs(): Promise<AgentTool[]> {
    const toolsDir = '/tools';
    const allTools: AgentTool[] = [];

    try {
        const entries = await readdir(toolsDir, { withFileTypes: true });
        for (const entry of entries) {
            if (!entry.isDirectory()) continue;
            const indexPath = join(toolsDir, entry.name, 'index.js');
            try {
                const mod = await import(indexPath);
                if (Array.isArray(mod.tools)) {
                    allTools.push(...mod.tools);
                    console.error(`[runner] Loaded ${mod.tools.length} tools from ${entry.name}`);
                }
            } catch (e) {
                console.error(`[runner] Failed to load tools from ${entry.name}: ${e}`);
            }
        }
    } catch {
        // /tools doesn't exist — no toolRefs configured, which is fine
    }

    return allTools;
}

// In main():
const toolRefTools = await loadToolRefs();
const agentTools = [...(agentModule.tools || []), ...toolRefTools];
// agentTools are passed to Agent constructor
```

### 5b.6 `agent-tools push tool` Command

New subcommand in `agent-tools` CLI:

```bash
# Package a tool directory as an OCI artifact
agent-tools push tool ./tools/git-tools/ --tag ghcr.io/samyn92/agent-tools/git:0.1.0

# Package with explicit name override
agent-tools push tool ./tools/git-tools/ --tag ghcr.io/samyn92/agent-tools/git:0.1.0 --name git
```

Implementation mirrors `push piagent` but uses tool-specific media types and validates:
- `index.js` exists and exports a `tools` array
- `manifest.json` exists with required fields (name, version)

### 5b.7 Planned Tool Packages

| Package | Tools | Required Binaries |
|---------|-------|-------------------|
| `git-tools` | git_clone, git_branch, git_checkout, git_commit, git_push, git_diff, git_log, git_status | git |
| `file-tools` | read_file, write_file, list_files, search_files, create_directory | none |
| `gitlab-tools` | create_mr, get_mr, add_mr_note, list_issues, update_issue, get_pipeline_status | none (HTTP API) |
| `github-tools` | create_pr, get_pr, add_pr_comment, list_issues, update_issue, get_check_runs | none (HTTP API) |
| `kubectl-tools` | kubectl_get, kubectl_apply, kubectl_logs, kubectl_describe | kubectl |

### 5b.8 Deliverables -- DONE
- [x] Add `ToolRefs []OCIArtifactRef` to `PiAgentSpec` in `api/v1alpha1/piagent_types.go`
- [x] Regenerate deep copy and CRD YAML
- [x] Update `workflowrun_piagent.go` — `configureToolRefs()` init containers per toolRef, `/tools` volume
- [x] Update `piagent_controller.go` — `validateToolRefs()` OCI ref validation + Cosign verification
- [x] Update `images/pi-runner/src/runner.ts` — `loadToolRefs()` scanning `/tools/*/index.js`
- [x] Add `agent-tools push tool` command to `agent-tools` repo (`pkg/toolpush/pusher.go` + `cmd/cli/push_tool.go`)
- [x] Create `git-tools` package — 11 tools (status, diff, log, add, commit, push, pull, branch, branch_list, show, clone)
- [ ] Create `file-tools` package (future)
- [ ] Create `gitlab-tools` package (future)

---

## Phase 5c: Output Posting in WorkflowRun Controller -- COMPLETE

### 5c.1 Design Principles

- **Agent code stays pure** — text in, text out. The agent doesn't know or care where its output goes.
- **Infrastructure handles delivery** — the WorkflowRun controller posts results based on `workflow.spec.output`.
- **Trigger data provides context** — PR number, repo, etc. are extracted from `triggerData`.

### 5c.2 Implementation

Fully implemented in `internal/controller/workflowrun_controller.go` (~450 lines of output handling):

- `succeedRun()` (line 730) — calls `getOutputForSending()` + `sendOutputs()` after workflow success
- `getOutputForSending()` (line 754) — resolves `fromStep` or defaults to last step's output
- `sendOutputs()` (line 781) — dispatches to all configured destinations (errors logged, don't fail the run)
- `sendGitHubOutput()` (line 912) — Posts PR/issue comment via GitHub API (`POST /repos/{owner}/{repo}/issues/{number}/comments`)
- `extractGitHubContext()` (line 978) — Extracts repo + PR/issue number from `triggerData.payload`
- `sendGitLabOutput()` (line 1024) — Posts MR note via GitLab API (`POST /projects/{id}/merge_requests/{iid}/notes`)
- `extractGitLabContext()` (line 1088) — Extracts project ID + MR IID from `triggerData.payload`
- `sendWebhookOutput()` (line 1122) — Generic HTTP POST with optional Bearer auth
- `sendTelegramOutput()` (line 834) — Telegram Bot API with Markdown
- `sendSlackOutput()` (line 876) — Slack incoming webhook
- `getSecretValue()` (line 1166) — Reads Kubernetes Secret by `SecretKeySelector`

### 5c.3 Authentication

`TokenSecret *SecretKeySelector` on both `GitHubOutput` and `GitLabOutput`. Falls back to trigger secret if output-specific token not set.

### 5c.4 Deliverables -- DONE
- [x] `TokenSecret *SecretKeySelector` on `GitHubOutput` and `GitLabOutput`
- [x] `sendOutputs()` dispatcher — Telegram, Slack, GitHub, GitLab, Webhook
- [x] `sendGitHubOutput()` + `extractGitHubContext()` — GitHub API integration
- [x] `sendGitLabOutput()` + `extractGitLabContext()` — GitLab API integration
- [x] `sendWebhookOutput()` — generic HTTP POST
- [x] Wired into `succeedRun()` — output posted automatically on workflow success

---

## Phase 5d: Worker Agent — Issue-to-MR -- COMPLETE

### 5d.1 Concept

A "Worker Agent" acts on issues: clones a repo, creates a branch, implements changes, commits, pushes, and creates a Merge Request. With the modular OCI tool architecture, the agent source is tiny (just system prompt + business logic) and capabilities come from `toolRefs`.

### 5d.2 PiAgentSpec.Env — Extra Environment Variables

Added `env []corev1.EnvVar` field to PiAgentSpec to pass tool-specific configuration (API tokens, URLs, git identity) to the Job pod. Uses standard Kubernetes EnvVar with both literal values and secretKeyRef support.

```go
type PiAgentSpec struct {
    // ...existing fields...

    // Env defines additional environment variables for the Job pod.
    Env []corev1.EnvVar `json:"env,omitempty"`
}
```

Wired into `buildPiAgentEnv()` — user-defined env vars are appended after standard runner vars (MODEL_PROVIDER, PROMPT, etc.) so they can override defaults if needed.

### 5d.3 New Tool Packages

**file-tools** (`tools/file/index.js`) — 6 tools:
- `read_file` — Read file contents with offset/limit support
- `write_file` — Write/create files with auto-mkdir
- `edit_file` — Find-and-replace edits (surgical, without full rewrite)
- `list_files` — Directory listing with recursive + depth control
- `search_files` — grep-based content search with regex
- `create_directory` — mkdir -p

Path traversal prevention: all paths resolved within WORKSPACE with `safePath()`.

**gitlab-tools** (`tools/gitlab/index.js`) — 9 tools:
- `gitlab_create_mr` — Create merge requests
- `gitlab_get_mr` — Get MR details + optional file changes
- `gitlab_get_mr_diff` — Full unified diff of an MR
- `gitlab_add_mr_note` — Add comments to MRs
- `gitlab_list_mrs` — List MRs filtered by state
- `gitlab_list_issues` — List issues filtered by state/labels
- `gitlab_get_issue` — Get full issue details
- `gitlab_add_issue_note` — Add comments to issues
- `gitlab_get_pipeline` — Get latest pipeline status

Uses GitLab REST API v4 via Node.js built-in `fetch`. No external dependencies. Auth via `GITLAB_TOKEN` env var.

### 5d.4 Issue Worker Agent Source

Minimal agent source (`agents/issue-worker/index.js`) — no tools defined, all capabilities come from toolRefs. Exports only:
- `config.systemPrompt` — Detailed workflow instructions for issue implementation
- `tools = []` — Empty array, tools provided by git/file/gitlab toolRefs

### 5d.5 Multi-Step Workflow: Issue Worker + Reviewer

```yaml
apiVersion: agents.io/v1alpha1
kind: Workflow
metadata:
  name: issue-to-mr
spec:
  trigger:
    gitlab:
      events: [Issue Hook]
      actions: [open]
      labels: [agent-task]
  steps:
    - name: implement
      piAgent: issue-worker
      prompt: "Implement the following GitLab issue..."
      timeout: "10m"
    - name: review
      piAgent: pr-reviewer
      prompt: "Review the MR created in the previous step..."
      timeout: "5m"
  output:
    gitlab:
      note: true
```

### 5d.6 Human Approval via MR Process

No need to build approval gates — the MR review process IS the approval mechanism:
1. Worker agent creates MR
2. Reviewer agent posts review comments
3. Human reviews both the code and the AI review
4. Human merges or requests changes
5. If changes requested, a new WorkflowRun can be triggered

### 5d.7 Deliverables -- DONE
- [x] Add `Env []corev1.EnvVar` to `PiAgentSpec` in `api/v1alpha1/piagent_types.go`
- [x] Wire into `buildPiAgentEnv()` in `internal/controller/workflowrun_piagent.go`
- [x] Regenerate deep copy and CRD YAML
- [x] Create `file-tools` package (6 tools) — `agent-tools/tools/file/index.js`
- [x] Create `gitlab-tools` package (9 tools) — `agent-tools/tools/gitlab/index.js`
- [x] Create `issue-worker` agent source — `agent-tools/agents/issue-worker/index.js`
- [x] Create `issue-to-mr` Workflow definition — Flux deployment
- [x] Create `issue-worker` PiAgent definition — Flux deployment with env vars + toolRefs
- [x] Update release workflow to auto-publish agent source packages
- [x] Update Flux kustomization.yaml
- [ ] Test end-to-end: issue creation → branch → MR → review comment

---

## Phase 6: Console UI — Workflow Process View

### 6.1 Tab Bar Redesign -- DONE (in agent-console repo)

The sidebar now has a tab bar at the very top switching between "Chats" and "Workflows":
- `sidebarTab` is a local SolidJS signal (`createSignal<"chats" | "workflows">("chats")`)
- Tab bar uses `text-sm font-semibold`, `py-3`, `w-4 h-4` icons, `bg-surface` background
- `AgentDetailPanel` conditionally shown only in "chats" mode
- Center content switches: "chats" shows chat interface, "workflows" shows placeholder

### 6.2 New Components (TODO)

```
web/src/components/workflow/
├── WorkflowPanel.tsx           # Existing — enhance as gallery/list view
├── WorkflowDesigner.tsx        # Card-based step builder
├── WorkflowRunView.tsx         # Horizontal pipeline + live streaming
├── WorkflowStepCard.tsx        # Reusable step card (designer + run view)
├── WorkflowTriggerConfig.tsx   # Trigger type selector
├── WorkflowOutputConfig.tsx    # Output destination config
├── WorkflowTemplates.tsx       # Template gallery
└── WorkflowEventStream.tsx     # Live Pi event consumer
```

### 6.3 Workflow Run View — Process Pipeline

```
┌──────────────────────────────────────────────────────────────┐
│  Run: pr-security-review-run-7xk2j                           │
│  Triggered by: GitHub PR #142 (opened) by @developer         │
│                                                               │
│  [v Triage]────────[* Analyzing]────────[o Post]             │
│   2.3s               Running...          Pending             │
│                                                               │
│  ── Step 2: Analyze (PiAgent: security-reviewer) ─────────── │
│  │                                                          │ │
│  │  [tool] classify_pr  v 0.8s                              │ │
│  │  [tool] check_deps   * running...                        │ │
│  │                                                          │ │
│  │  The PR modifies authentication middleware in            │ │
│  │  src/auth/jwt.go. Key concerns:                          │ │
│  │  - Token validation bypass possible when ...             │ │
│  └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

### 6.4 SSE Integration

New event types on the SSE stream:

```typescript
interface WorkflowStepEvent {
    type:
        | "workflow.step.started"
        | "workflow.step.token"
        | "workflow.step.tool_start"
        | "workflow.step.tool_progress"
        | "workflow.step.tool_end"
        | "workflow.step.completed"
        | "workflow.step.failed";
    workflowRun: string;
    step: string;
    agent: string;
    runtime: "pi" | "opencode";
    data: any;
}
```

### 6.5 Deliverables
- [ ] `WorkflowRunView.tsx` — horizontal pipeline with live streaming
- [ ] `WorkflowStepCard.tsx` — reusable step card component
- [ ] `WorkflowDesigner.tsx` — card-based step builder
- [ ] `WorkflowEventStream.tsx` — SSE consumer for Pi events
- [ ] `WorkflowTemplates.tsx` — template gallery
- [ ] Backend SSE endpoint for Pi workflow events
- [ ] Wire workflow views into main navigation tabs

---

## Phase 7: Console Backend — Event Bridge

### 7.1 Log Streaming Service

The console backend forwards Pi runner pod logs to the SSE stream:

```go
func (s *Server) streamWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
    runName := chi.URLParam(r, "name")
    pod := s.findActiveJobPod(ctx, runName)

    stream, _ := s.k8sClient.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
        Follow: true,
    }).Stream(ctx)

    for scanner.Scan() {
        event := parsePiEvent(scanner.Text())
        fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, event.JSON())
        flusher.Flush()
    }
}
```

### 7.2 Deliverables
- [ ] Add `/api/v1/workflowruns/:name/events` SSE endpoint
- [ ] Implement pod log streaming for Pi Jobs
- [ ] Parse JSONL events and forward as SSE
- [ ] Add to `agent-console` backend router

---

## Phase 8: Helm Chart Integration

### 8.1 agent-factory Updates

Add PiAgent and tool package support to the Helm chart:

```yaml
# values.yaml
piAgents:
  items:
    pr-classifier:
      source:
        oci: ghcr.io/myorg/pr-classifier:v1.0.0
      model: anthropic/claude-sonnet-4-20250514
      toolRefs:
        - ghcr.io/myorg/agent-tools/git:0.1.0
      thinkingLevel: low
      timeout: 30s

workflows:
  items:
    pr-review:
      trigger:
        github:
          events: [pull_request]
          actions: [opened, synchronize]
      piAgent: pr-classifier
      prompt: "Review this PR: {{.trigger}}"
      output:
        github:
          comment: true
```

### 8.2 Deliverables
- [ ] Add PiAgent template to `agent-factory/helm/agent-factory/templates/`
- [ ] Add PiAgent values schema
- [ ] Create example workflows using mixed runtimes and toolRefs
- [ ] Update `agent-factory` documentation

---

## Phase 9: Future Enhancements

### 9.1 Parallel Step Groups

Steps with the same `group` field execute concurrently:

```yaml
steps:
  - name: code-review
    group: reviews
    piAgent: code-reviewer
  - name: security-scan
    group: reviews
    piAgent: security-scanner
  - name: summarize
    piAgent: summarizer
    prompt: "Combine: {{.steps.code-review.output}} + {{.steps.security-scan.output}}"
```

### 9.2 CEL Condition Evaluation

Replace string-contains with proper CEL:

```yaml
condition: "steps.classify.output.contains('critical') && steps.classify.phase == 'Succeeded'"
```

### 9.3 Manual Approval Steps

```yaml
steps:
  - name: review
    piAgent: reviewer
  - name: approval
    type: approval
    message: "Review the analysis before posting"
  - name: post
    piAgent: poster
```

### 9.4 Pi Agent Hot-Reload (Development Mode)

Support a mode where the PiAgent watches a ConfigMap for changes and re-runs, enabling rapid iteration without OCI push cycles.

### 9.5 Multi-Agent Pi Teams

Nested Pi agents where one agent can invoke others:

```typescript
export const tools: AgentTool[] = [
    {
        name: "delegate_to_specialist",
        execute: async (id, params, signal) => {
            const result = await invokeAgent("security-specialist", params.task);
            return { content: [{ type: "text", text: result }] };
        },
    },
];
```

### 9.6 Tool Package Discovery & Validation

- PiAgent controller validates toolRefs are pullable (like source OCI validation)
- Console UI shows available tool packages with descriptions
- `agent-tools list tools` command to query a registry

---

## Implementation Order (Updated)

**Phases 1-5, 5b, 5c, 5d are COMPLETE.** The remaining work:

| # | Phase | Effort | Dependencies | Impact |
|---|-------|--------|-------------|--------|
| ~~5c~~ | ~~Output Posting~~ | ~~DONE~~ | ~~None~~ | ~~Already built with Phase 4~~ |
| ~~5b~~ | ~~Modular OCI Tool Packages~~ | ~~DONE~~ | ~~None~~ | ~~Enables modular tools~~ |
| ~~5d~~ | ~~Issue Worker PiAgent~~ | ~~DONE~~ | ~~5b, 5c~~ | ~~End-to-end issue-to-MR~~ |
| 6 | Console UI — Process View | 2 weeks | Phase 4 | The UX differentiator |
| 7 | Console Backend — Event Bridge | 3 days | Phase 4 | Enables live UI |
| 8 | Helm Chart Integration | 2 days | 5b, 5d | Deployment story |
| 9 | Future Enhancements | Ongoing | All | Parallel groups, CEL, approvals |

**Next immediate action: Phase 6 (Console UI) or Phase 7 (Console Backend).**

---

## Release History

| Tag | Repo | Date | Highlights |
|-----|------|------|-----------|
| v0.0.19 | agent-operator-core | — | PiAgentSpec.Env field for tool-specific env vars |
| v0.0.18 | agent-operator-core | — | Fix crash: piagents RBAC + cluster-scoped toggle |
| v0.0.17 | agent-operator-core | — | Modular OCI toolRefs: CRD field, controller validation, Job init containers, runner loading |
| v0.0.16 | agent-operator-core | — | PiAgent CRD, Pi runner, Workflow dual-runtime, WorkflowRun Pi execution |
| v0.0.6 | agent-tools | — | file-tools (6), gitlab-tools (9), issue-worker agent, agent package publishing |
| v0.0.5 | agent-tools | — | Automated release: CLI binaries on GH release + tool OCI artifacts to ghcr.io |
| v0.0.4 | agent-tools | — | `push tool` OCI command, git tool package (11 tools) |
| v0.0.3 | agent-tools | — | `push piagent` OCI command, 4 CLI binaries, 7 tool images |
| v0.0.13 | agent-console | — | Tab bar redesign, workflow placeholder |

---

## File Changes Summary

### agent-operator-core (this repo)

**Completed:**
- `api/v1alpha1/piagent_types.go` — PiAgent CRD types with `ToolRefs []OCIArtifactRef` + `Env []corev1.EnvVar`
- `api/v1alpha1/workflow_types.go` — Added `PiAgent` fields to WorkflowStep/WorkflowSpec, Pi fields to StepResult
- `api/v1alpha1/zz_generated.deepcopy.go` — Regenerated (handles ToolRefs slice + Env slice)
- `internal/controller/piagent_controller.go` — Validation controller + `validateToolRefs()` (302 lines)
- `internal/controller/workflowrun_piagent.go` — Pi execution + `configureToolRefs()` + `extractToolName()` + user env vars (~750 lines)
- `internal/controller/workflowrun_controller.go` — PiAgent step routing + full output posting
- `internal/controller/workflow_controller.go` — PiAgent ref validation
- `cmd/operator/main.go` — PiAgent controller registration
- `config/crd/bases/agents.io_piagents.yaml` — Generated (includes toolRefs + env)
- `config/crd/bases/agents.io_workflows.yaml` — Updated
- `config/crd/bases/agents.io_workflowruns.yaml` — Updated
- `config/rbac/role.yaml` — batch/jobs, pods, pods/log, piagents
- `images/pi-runner/` — Complete runner image with `loadToolRefs()` (Dockerfile, runner.ts, package.json, tsconfig.json)
- `.github/workflows/ci.yaml` — Fixed YAML parsing, CRD drift now passes

### agent-tools
**Completed:**
- `cmd/cli/push.go` — Parent push command (piagent + tool)
- `cmd/cli/push_piagent.go` — Push piagent subcommand
- `cmd/cli/push_tool.go` — Push tool subcommand
- `pkg/piagent/pusher.go`, `pkg/piagent/helpers.go` — PiAgent OCI pusher
- `pkg/toolpush/pusher.go` — Tool OCI pusher (application/vnd.agents.io.tool.v1)
- `tools/git/index.js` — Git tool package (11 tools, pure JS, no dependencies)
- `tools/file/index.js` — File tool package (6 tools: read, write, edit, list, search, mkdir)
- `tools/gitlab/index.js` — GitLab tool package (9 tools: MR CRUD, issue CRUD, pipeline status)
- `agents/issue-worker/index.js` — Issue worker agent source (system prompt only, tools via toolRefs)
- `.github/workflows/release.yaml` — Auto-publishes tool packages + agent packages + CLI binaries

### agent-console
**Completed:**
- `web/src/pages/MainApp.tsx` — Tab bar at top of sidebar

**Remaining:**
- Backend: SSE endpoint for workflow Pi events
- Frontend: WorkflowRunView, WorkflowDesigner, WorkflowStepCard, WorkflowEventStream components

### agent-factory
**Remaining:**
- Helm: PiAgent templates + values schema with toolRefs support
- Examples: Mixed-runtime workflow examples

### Flux deployment (`/home/samy/dev/gitlab.com/homecluster/flux/apps/agent-platform/`)
**Created but NOT committed:**
- `piagent-pr-reviewer.yaml` — Inline PiAgent for PR review
- `piagent-issue-worker.yaml` — Issue worker PiAgent with toolRefs + env vars
- `workflow-pr-review.yaml` — GitHub pull_request trigger workflow
- `workflow-issue-to-mr.yaml` — GitLab issue trigger → implement + review pipeline
- `kustomization.yaml` — Updated with all new entries
