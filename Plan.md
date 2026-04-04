# Implementation Plan: Dual-Runtime Agent Platform

## Vision

A Git-based AI Agentic Engineering platform with two complementary runtimes:

- **OpenCode Agents** (`Agent` CRD) — Chat-based, exploratory sessions via ACP. Heavy runtime with PVC, sidecars, MCP. Always-on Deployments. For when you don't know the goal yet and want to explore interactively.
- **Pi Agents** (`PiAgent` CRD) — Workflow-optimized, purpose-built TypeScript agents. Lightweight Jobs, on-demand execution, granular event streaming. For structured, repeatable processes with rich process-feel UI.

Both runtimes coexist. Workflows can mix them per step. The console serves both paradigms: chat interface for OpenCode, process pipeline UI for Pi-powered workflows.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                     Agent Console (SolidJS)                   │
├────────────────────────────┬─────────────────────────────────┤
│  [Chat]                    │  [Workflows]                     │
│  OpenCode ACP/SSE          │  Pi Event Stream SSE             │
│  Exploratory sessions      │  Process pipeline UI             │
├────────────────────────────┼─────────────────────────────────┤
│  Agent CRD                 │  PiAgent CRD                     │
│  → Deployment (always-on)  │  → Job (on-demand)               │
│  → PVC + Sidecars + MCP    │  → OCI artifact + TypeScript     │
│  → OpenCode runtime        │  → pi-agent-core runtime         │
│  → HTTP polling protocol   │  → JSONL event stream            │
│                            │                                  │
│  12+ config fields         │  ~5 config fields                │
│  Heavy, full-featured      │  Light, purpose-built            │
└────────────────────────────┴─────────────────────────────────┘

Workflow CRD orchestrates both:
  steps:
    - agent: research-bot        # OpenCode runtime
    - piAgent: pr-classifier     # Pi runtime
```

---

## Phase 1: PiAgent CRD & Controller

### 1.1 PiAgent Types (`api/v1alpha1/piagent_types.go`)

New CRD that shares common types (`ProviderConfig`, `IdentityConfig`, `SecretKeySelector`) with Agent but has a fundamentally different spec.

```go
// PiAgentSpec defines the desired state of PiAgent.
// PiAgent is a lightweight, purpose-built AI agent that runs as an on-demand
// Kubernetes Job using the pi-agent-core runtime. Tools are defined in
// TypeScript, not as sidecars.
type PiAgentSpec struct {
    // Model in "provider/model" format (shared type with Agent)
    Model string `json:"model"`

    // Providers configures AI providers (shared type with Agent)
    Providers []ProviderConfig `json:"providers"`

    // Identity configures the agent's personality (shared type with Agent)
    Identity *IdentityConfig `json:"identity,omitempty"`

    // Source defines the agent's TypeScript code
    Source PiAgentSource `json:"source"`

    // ThinkingLevel controls the model's reasoning depth
    // +kubebuilder:validation:Enum=off;minimal;low;medium;high;xhigh
    // +kubebuilder:default=off
    ThinkingLevel string `json:"thinkingLevel,omitempty"`

    // ToolExecution controls whether tools run in parallel or sequentially
    // +kubebuilder:validation:Enum=parallel;sequential
    // +kubebuilder:default=parallel
    ToolExecution string `json:"toolExecution,omitempty"`

    // Resources for the Job pod
    Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

    // ServiceAccountName for RBAC (e.g., if tools need kubectl access)
    ServiceAccountName string `json:"serviceAccountName,omitempty"`

    // Image overrides the default pi-runner base image
    Image string `json:"image,omitempty"`

    // Timeout is the maximum execution time for a single invocation
    // +kubebuilder:default="5m"
    Timeout string `json:"timeout,omitempty"`
}

// PiAgentSource defines where the agent's TypeScript code comes from.
// Exactly one field must be set.
type PiAgentSource struct {
    // OCI references an OCI artifact containing the agent TypeScript module
    OCI *OCIArtifactRef `json:"oci,omitempty"`

    // Inline contains the agent TypeScript code directly in the CRD
    Inline string `json:"inline,omitempty"`

    // ConfigMapRef references a ConfigMap containing the agent code
    ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// PiAgentStatus defines the observed state of PiAgent
type PiAgentStatus struct {
    Phase      PiAgentPhase       `json:"phase,omitempty"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type PiAgentPhase string
const (
    PiAgentPhaseReady    PiAgentPhase = "Ready"     // Source resolved, image available
    PiAgentPhasePending  PiAgentPhase = "Pending"   // Source not yet resolved
    PiAgentPhaseFailed   PiAgentPhase = "Failed"    // Source resolution failed
)
```

Key differences from Agent:
- **No Deployment** — PiAgent is a definition, not a running workload. It becomes a Job only when invoked by a WorkflowRun.
- **No PVC, no sidecars, no MCP** — tools are TypeScript functions in the source.
- **No `tools`, `permissions`, `security`** — the TypeScript code IS the tool and permission layer.
- **Source field** — OCI artifact, inline, or ConfigMap containing the agent's TypeScript module.
- **Status is simpler** — just "is the source resolvable?" not "is the pod running?"

### 1.2 PiAgent Controller (`internal/controller/piagent_controller.go`)

Lightweight controller that validates the PiAgent:

1. **Source Resolution** — Verify the OCI artifact is pullable (with optional Cosign verification), or inline/ConfigMap content is valid.
2. **Provider Validation** — Confirm referenced secrets exist.
3. **Status Update** — Set phase to Ready/Failed.

No Deployment, Service, or ConfigMap creation. The controller just validates.

### 1.3 Deliverables
- [ ] `api/v1alpha1/piagent_types.go` — PiAgent CRD types
- [ ] `internal/controller/piagent_controller.go` — PiAgent controller
- [ ] Register in `cmd/operator/main.go`
- [ ] Register in `api/v1alpha1/groupversion_info.go`
- [ ] Generate CRD YAML
- [ ] Add to Helm chart crds/

---

## Phase 2: Pi Runner Image

### 2.1 The Harness (`pi-runner`)

A lightweight Node.js container image (~100-200 lines of TypeScript) that:

1. Loads the agent TypeScript module from a mounted volume
2. Configures the model using `@mariozechner/pi-ai`
3. Runs `agentLoop()` from `@mariozechner/pi-agent-core`
4. Streams all events as JSONL to stdout

```
pi-runner/
├── Dockerfile
├── package.json          # @mariozechner/pi-agent-core, @mariozechner/pi-ai, @sinclair/typebox
├── runner.ts             # The harness: load module, run agentLoop, stream events
└── tsconfig.json
```

### 2.2 Event Protocol

The runner outputs one JSON object per line to stdout:

```jsonl
{"type":"agent_start","ts":1720000000000}
{"type":"message_update","ts":1720000000100,"data":{"delta":"The PR modifies..."}}
{"type":"tool_execution_start","ts":1720000000200,"data":{"name":"classify_pr","args":{...}}}
{"type":"tool_execution_end","ts":1720000000300,"data":{"name":"classify_pr","result":{...}}}
{"type":"message_end","ts":1720000000400,"data":{"text":"Classification: security, refactor"}}
{"type":"agent_end","ts":1720000000500}
```

Pi's 11 event types provide granular hooks for the UI.

### 2.3 Configuration via Environment

The WorkflowRun controller passes config to the Job via env vars:

| Env Var | Source |
|---------|--------|
| `MODEL_PROVIDER` | PiAgent.spec.model (provider part) |
| `MODEL_NAME` | PiAgent.spec.model (model part) |
| `PROVIDER_API_KEY` | From PiAgent.spec.providers[].apiKeySecret |
| `THINKING_LEVEL` | PiAgent.spec.thinkingLevel |
| `TOOL_EXECUTION` | PiAgent.spec.toolExecution |
| `PROMPT` | Rendered prompt from WorkflowStep |
| `TRIGGER_DATA` | WorkflowRun.spec.triggerData |

### 2.4 Deliverables
- [ ] `images/pi-runner/Dockerfile`
- [ ] `images/pi-runner/runner.ts` — the harness
- [ ] `images/pi-runner/package.json`
- [ ] Publish to `ghcr.io/samyn92/pi-runner:latest`

---

## Phase 3: Workflow CRD Evolution

### 3.1 WorkflowStep — Dual Runtime Support

Add `piAgent` field alongside existing `agent`:

```go
type WorkflowStep struct {
    Name string `json:"name,omitempty"`

    // Agent references an Agent CRD (OpenCode runtime)
    // Exactly one of Agent or PiAgent must be set.
    Agent string `json:"agent,omitempty"`

    // PiAgent references a PiAgent CRD (Pi runtime)
    // Exactly one of Agent or PiAgent must be set.
    PiAgent string `json:"piAgent,omitempty"`

    Prompt          string `json:"prompt"`
    Condition       string `json:"condition,omitempty"`
    Timeout         string `json:"timeout,omitempty"`
    ContinueOnError *bool  `json:"continueOnError,omitempty"`
}
```

### 3.2 WorkflowSpec Simple Mode — Dual Runtime

Same pattern for simple mode:

```go
type WorkflowSpec struct {
    Trigger WorkflowTrigger `json:"trigger"`

    // Simple mode: use one of Agent or PiAgent
    Agent   string `json:"agent,omitempty"`
    PiAgent string `json:"piAgent,omitempty"`
    Prompt  string `json:"prompt,omitempty"`

    // Advanced mode
    Steps []WorkflowStep `json:"steps,omitempty"`

    Output  *WorkflowOutput `json:"output,omitempty"`
    Suspend *bool           `json:"suspend,omitempty"`
}
```

### 3.3 StepResult — Event Tracking

Extend StepResult to support Pi event data:

```go
type StepResult struct {
    Name           string       `json:"name"`
    Phase          string       `json:"phase"`
    Output         string       `json:"output,omitempty"`
    StartTime      *metav1.Time `json:"startTime,omitempty"`
    CompletionTime *metav1.Time `json:"completionTime,omitempty"`
    Error          string       `json:"error,omitempty"`

    // OpenCode-specific
    SessionID string `json:"sessionID,omitempty"`

    // Pi-specific
    JobName    string   `json:"jobName,omitempty"`
    ToolCalls  int      `json:"toolCalls,omitempty"`
    TokensUsed int     `json:"tokensUsed,omitempty"`
}
```

### 3.4 Parallel Step Groups (Future Enhancement)

Add a `group` field for fan-out/fan-in:

```go
type WorkflowStep struct {
    // ...existing fields...

    // Group assigns this step to a parallel execution group.
    // Steps with the same group name execute concurrently.
    // The group completes when all its steps finish.
    Group string `json:"group,omitempty"`
}
```

### 3.5 Workflow Controller Updates

The workflow controller validates both `agent` and `piAgent` references:

```go
// Verify Agent references
if step.Agent != "" {
    agent := &Agent{}
    if err := r.Get(ctx, ..., agent); err != nil { ... }
}
// Verify PiAgent references
if step.PiAgent != "" {
    piAgent := &PiAgent{}
    if err := r.Get(ctx, ..., piAgent); err != nil { ... }
}
```

### 3.6 Deliverables
- [ ] Update `api/v1alpha1/workflow_types.go` — add `PiAgent` fields to WorkflowStep and WorkflowSpec
- [ ] Update `internal/controller/workflow_controller.go` — validate PiAgent references
- [ ] Update CRD YAML for Workflow
- [ ] Update Helm chart CRD

---

## Phase 4: WorkflowRun Controller — Pi Execution

### 4.1 Job-Based Execution

When a step references a `piAgent`, the WorkflowRun controller creates a Kubernetes Job:

```go
func (r *WorkflowRunReconciler) reconcilePiAgentStep(ctx context.Context, run *WorkflowRun, step WorkflowStep, stepResult *StepResult) (ctrl.Result, error) {
    piAgent := &PiAgent{}
    r.Get(ctx, types.NamespacedName{Name: step.PiAgent, Namespace: run.Namespace}, piAgent)

    if stepResult.JobName == "" {
        // Create the Job
        job := r.buildPiAgentJob(run, piAgent, step)
        r.Create(ctx, job)
        stepResult.JobName = job.Name
        stepResult.Phase = "Running"
        return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
    }

    // Poll Job status
    job := &batchv1.Job{}
    r.Get(ctx, types.NamespacedName{Name: stepResult.JobName, Namespace: run.Namespace}, job)

    if job.Status.Succeeded > 0 {
        output := r.fetchJobOutput(ctx, job) // read from pod logs
        stepResult.Output = output
        stepResult.Phase = "Succeeded"
        // ...
    }
}
```

### 4.2 Job Pod Spec

```go
func (r *WorkflowRunReconciler) buildPiAgentJob(run *WorkflowRun, piAgent *PiAgent, step WorkflowStep) *batchv1.Job {
    return &batchv1.Job{
        Spec: batchv1.JobSpec{
            Template: corev1.PodTemplateSpec{
                Spec: corev1.PodSpec{
                    RestartPolicy:      corev1.RestartPolicyNever,
                    ServiceAccountName: piAgent.Spec.ServiceAccountName,
                    InitContainers: []corev1.Container{
                        // Pull OCI artifact into /agent volume
                        r.buildOCIPullInitContainer(piAgent),
                    },
                    Containers: []corev1.Container{{
                        Name:  "pi-runner",
                        Image: piAgent.Spec.Image, // or default pi-runner image
                        Env:   r.buildPiAgentEnv(piAgent, step, run),
                        VolumeMounts: []corev1.VolumeMount{
                            {Name: "agent-code", MountPath: "/agent"},
                        },
                        Resources: piAgent.Spec.Resources,
                    }},
                    Volumes: []corev1.Volume{
                        {Name: "agent-code", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
                    },
                },
            },
        },
    }
}
```

### 4.3 Output Collection

Two approaches for getting the Job output:

**Option A: Pod logs (simpler)**
```go
func (r *WorkflowRunReconciler) fetchJobOutput(ctx context.Context, job *batchv1.Job) string {
    // Get pods for the Job, read logs, parse last agent_end event
    pods := r.listJobPods(ctx, job)
    logs := r.readPodLogs(ctx, pods[0])
    return parseAgentOutput(logs) // extract from JSONL events
}
```

**Option B: ConfigMap result (more robust)**
The pi-runner writes its final output to a ConfigMap before exiting. Controller reads the ConfigMap.

Recommendation: **Option A** for simplicity. Pod logs are sufficient and require no additional resources.

### 4.4 Live Event Streaming (for UI)

While the Job runs, the controller (or a separate sidecar/watcher) tails the pod logs and forwards Pi events to the console via SSE:

```
Pi Runner (stdout JSONL) → Pod logs → Log streamer → Console SSE endpoint
```

New SSE event types for the console:

```go
type WorkflowStepEvent struct {
    Type        string `json:"type"`     // workflow.step.token, workflow.step.tool_start, etc.
    WorkflowRun string `json:"run"`
    Step        string `json:"step"`
    Data        any    `json:"data"`
}
```

### 4.5 Comparison: OpenCode vs Pi Step Execution

| Aspect | OpenCode Step (existing) | Pi Step (new) |
|--------|-------------------------|---------------|
| K8s resource | Calls existing Deployment | Creates ephemeral Job |
| Lifecycle | Pod always running | Pod starts, runs, terminates |
| Communication | HTTP session API + polling | JSONL stdout + pod logs |
| Latency | ~3s poll intervals | Real-time event stream |
| Cost | Permanent resource allocation | Pay-per-execution |
| Cleanup | Nothing (pod stays) | Job + Pod cleaned up via TTL |
| Events to UI | None during execution | 11 granular event types |

### 4.6 Deliverables
- [ ] Add `reconcilePiAgentStep()` to `internal/controller/workflowrun_controller.go`
- [ ] Add `buildPiAgentJob()` — Job construction
- [ ] Add `fetchJobOutput()` — output extraction from pod logs
- [ ] Add RBAC for Jobs (already in Helm chart, add to config/rbac/role.yaml)
- [ ] Update step routing: `if step.PiAgent != "" { reconcilePiAgentStep() } else { reconcileOpencodeStep() }`

---

## Phase 5: OCI Artifacts for Pi Agents

### 5.1 Agent Code Structure

A Pi agent packaged as an OCI artifact contains:

```
my-agent/
├── index.ts          # Agent config, tools, exports
├── utils.ts          # Helper functions (optional)
└── package.json      # Extra dependencies (optional)
```

The `index.ts` must export a standard interface:

```typescript
import { Type } from "@sinclair/typebox";
import type { AgentTool } from "@mariozechner/pi-agent-core";

// Required: tools the agent can use
export const tools: AgentTool[] = [
    {
        name: "classify_pr",
        description: "Classify a PR by change type",
        parameters: Type.Object({
            diff: Type.String({ description: "The PR diff" }),
        }),
        execute: async (toolCallId, params, signal, onUpdate) => {
            // Pure TypeScript logic — no sidecars needed
            const categories = analyzeDiff(params.diff);
            return {
                content: [{ type: "text", text: categories.join(", ") }],
                details: { categories },
            };
        },
    },
];

// Required: agent configuration
export const config = {
    systemPrompt: "You classify pull requests into: security, refactor, feature, bugfix.",
};

// Optional: thinking level override
export const thinkingLevel = "low";
```

### 5.2 Packaging with agent-tools

Extend the `agent-tools` CLI:

```bash
# Package a Pi agent as an OCI artifact
agent-tools push piagent ./my-agent/ \
  --tag ghcr.io/myorg/pr-classifier:v1.0.0

# Sign with Cosign
cosign sign ghcr.io/myorg/pr-classifier:v1.0.0

# Reference in PiAgent CRD
apiVersion: agents.io/v1alpha1
kind: PiAgent
metadata:
  name: pr-classifier
spec:
  source:
    oci:
      ref: ghcr.io/myorg/pr-classifier:v1.0.0
      verify:
        keyless:
          issuer: https://token.actions.githubusercontent.com
          identity: https://github.com/myorg/...
  model: anthropic/claude-sonnet-4-20250514
  providers:
    - name: anthropic
      apiKeySecret: { name: anthropic-key, key: api-key }
```

### 5.3 OCI Artifact Media Types

```
Application type:  application/vnd.agents.io.piagent.v1
Layer media type:  application/vnd.agents.io.piagent.code.v1.tar+gzip
```

### 5.4 Deliverables
- [ ] Define OCI artifact media types for Pi agents
- [ ] Add `agent-tools push piagent` command to `agent-tools` repo
- [ ] Add OCI pull logic to PiAgent controller (reuse existing `pkg/oci` code)
- [ ] Add Cosign verification (reuse existing verification code from Agent controller)

---

## Phase 6: Console UI — Workflow Process View

### 6.1 New Components

```
web/src/components/workflow/
├── WorkflowPanel.tsx           # Existing — enhance as gallery/list view
├── WorkflowDesigner.tsx        # NEW — card-based step builder
├── WorkflowRunView.tsx         # NEW — horizontal pipeline + live streaming
├── WorkflowStepCard.tsx        # NEW — reusable step card (designer + run view)
├── WorkflowTriggerConfig.tsx   # NEW — trigger type selector
├── WorkflowOutputConfig.tsx    # NEW — output destination config
├── WorkflowTemplates.tsx       # NEW — template gallery from agent-factory examples
└── WorkflowEventStream.tsx     # NEW — live Pi event consumer
```

### 6.2 Workflow Run View — Process Pipeline

```
┌──────────────────────────────────────────────────────────────┐
│  Run: pr-security-review-run-7xk2j                           │
│  Triggered by: GitHub PR #142 (opened) by @developer         │
│                                                               │
│  [✓ Triage]────────[● Analyzing]────────[○ Post]             │
│   2.3s               Running...          Pending             │
│                                                               │
│  ┌─ Step 2: Analyze (PiAgent: security-reviewer) ──────────┐ │
│  │                                                          │ │
│  │  🔧 classify_pr  ✓ 0.8s                                │ │
│  │  🔧 check_deps   ● running...                          │ │
│  │                                                          │ │
│  │  The PR modifies authentication middleware in            │ │
│  │  src/auth/jwt.go. Key concerns:                          │ │
│  │  - Token validation bypass possible when █               │ │
│  └──────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

Key UI features powered by Pi events:
- **Horizontal step pipeline** — CI/CD-style visual (not a chat log)
- **Live token streaming** — `message_update` events render as typewriter text
- **Tool execution chips** — `tool_execution_start/end` show tool-by-tool progress
- **Click-to-expand** — Any completed step shows full output, prompt, timing

### 6.3 Workflow Designer — Card Builder

```
┌──────────────────────────────────────────────────────────────┐
│  Workflow: PR Security Pipeline                               │
│  Trigger: [GitHub ▾] pull_request — opened, synchronize      │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌─────────────┐     ┌─────────────┐     ┌────────────────┐ │
│  │ 1. Classify  │────>│ 2. Review   │────>│ 3. Post Result │ │
│  │              │     │              │     │                │ │
│  │ PiAgent ▾    │     │ PiAgent ▾    │     │ Output:        │ │
│  │ classifier   │     │ sec-review   │     │ [GitHub] ▾     │ │
│  │              │     │              │     │                │ │
│  │ Prompt:      │     │ Condition:   │     │ From step:     │ │
│  │ "Classify.." │     │ classify     │     │ review         │ │
│  └─────────────┘     │ contains     │     └────────────────┘ │
│                       │ 'security'  │                         │
│  [+ Add Step]        └─────────────┘                         │
└──────────────────────────────────────────────────────────────┘
```

Each card:
- Runtime selector: Agent (OpenCode) or PiAgent (Pi)
- Agent dropdown populated from CRDs in namespace
- Prompt editor with `{{.trigger}}` and `{{.steps.<name>.output}}` autocomplete
- Condition builder
- Timeout, continueOnError toggles

### 6.4 SSE Integration

New event types on the `/api/v1/agents/events` SSE stream:

```typescript
// Pi workflow step events
interface WorkflowStepEvent {
    type:
        | "workflow.step.started"
        | "workflow.step.token"          // text delta from agent
        | "workflow.step.tool_start"     // tool execution began
        | "workflow.step.tool_progress"  // tool progress update
        | "workflow.step.tool_end"       // tool execution finished
        | "workflow.step.completed"
        | "workflow.step.failed";
    workflowRun: string;
    step: string;
    agent: string;          // PiAgent name
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

The console backend needs to forward Pi runner pod logs to the SSE stream:

```go
// In agent-console backend
func (s *Server) streamWorkflowRunEvents(w http.ResponseWriter, r *http.Request) {
    runName := chi.URLParam(r, "name")

    // Find the active Job pod
    pod := s.findActiveJobPod(ctx, runName)

    // Tail pod logs and forward as SSE
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

Add PiAgent support to the Helm chart:

```yaml
# values.yaml
piAgents:
  items:
    pr-classifier:
      source:
        oci: ghcr.io/myorg/pr-classifier:v1.0.0
      model: anthropic/claude-sonnet-4-20250514
      thinkingLevel: low
      timeout: 30s
```

### 8.2 Workflow Templates with Mixed Runtimes

```yaml
# templates/workflow-pr-review.yaml
apiVersion: agents.io/v1alpha1
kind: Workflow
metadata:
  name: {{ .Release.Name }}-pr-review
spec:
  trigger:
    github:
      events: [pull_request]
      actions: [opened, synchronize]
  steps:
    - name: classify
      piAgent: {{ .Release.Name }}-classifier
      prompt: "Classify this PR: {{`{{.trigger}}`}}"
      timeout: 30s
    - name: review
      piAgent: {{ .Release.Name }}-reviewer
      prompt: "Review: {{`{{.steps.classify.output}}`}}"
      condition: "steps.classify.output contains 'needs-review'"
      timeout: 2m
  output:
    github:
      comment: true
```

### 8.3 Deliverables
- [ ] Add PiAgent template to `agent-factory/helm/agent-factory/templates/`
- [ ] Add PiAgent values schema
- [ ] Create example workflows using mixed runtimes
- [ ] Update `agent-factory` documentation

---

## Phase 9: Future Enhancements

### 9.1 Parallel Step Groups

Steps with the same `group` field execute concurrently:

```yaml
steps:
  - name: code-review
    group: reviews        # parallel
    piAgent: code-reviewer
  - name: security-scan
    group: reviews        # parallel
    piAgent: security-scanner
  - name: summarize       # sequential (after group completes)
    piAgent: summarizer
    prompt: "Combine: {{.steps.code-review.output}} + {{.steps.security-scan.output}}"
```

### 9.2 CEL Condition Evaluation

Replace the current string-contains hack with proper CEL:

```yaml
condition: "steps.classify.output.contains('critical') && steps.classify.phase == 'Succeeded'"
```

### 9.3 Manual Approval Steps

```yaml
steps:
  - name: review
    piAgent: reviewer
  - name: approval
    type: approval          # Pauses until human approves in UI
    message: "Review the analysis before posting"
  - name: post
    piAgent: poster
```

### 9.4 Pi Agent Hot-Reload (Development Mode)

For development, support a mode where the PiAgent watches a ConfigMap for changes and re-runs, enabling rapid iteration without OCI push cycles.

### 9.5 Multi-Agent Pi Teams

Inspired by IndyDevDan's CEO/Board pattern, support nested Pi agents where one agent can invoke others:

```typescript
export const tools: AgentTool[] = [
    {
        name: "delegate_to_specialist",
        execute: async (id, params, signal) => {
            // Invoke another PiAgent in the same namespace
            const result = await invokeAgent("security-specialist", params.task);
            return { content: [{ type: "text", text: result }] };
        },
    },
];
```

---

## Implementation Order

| # | Phase | Effort | Dependencies | Impact |
|---|-------|--------|-------------|--------|
| 1 | PiAgent CRD & Controller | 1 week | None | Foundation for everything |
| 2 | Pi Runner Image | 1 week | Phase 1 | Enables Pi execution |
| 3 | Workflow CRD Evolution | 3 days | Phase 1 | Connects Pi to workflows |
| 4 | WorkflowRun Controller | 1 week | Phase 2, 3 | End-to-end Pi workflow execution |
| 5 | OCI Artifacts for Pi | 3 days | Phase 2 | Production packaging |
| 6 | Console UI — Process View | 2 weeks | Phase 4 | The UX differentiator |
| 7 | Console Backend — Event Bridge | 3 days | Phase 4 | Enables live UI |
| 8 | Helm Chart Integration | 2 days | Phase 3, 5 | Deployment story |
| 9 | Future Enhancements | Ongoing | All phases | Parallel groups, CEL, approvals |

**Total estimated effort: ~6-7 weeks for phases 1-8.**

---

## File Changes Summary

### agent-operator-core (this repo)
- `api/v1alpha1/piagent_types.go` — **NEW**
- `api/v1alpha1/workflow_types.go` — Add `PiAgent` fields
- `api/v1alpha1/groupversion_info.go` — Register PiAgent
- `internal/controller/piagent_controller.go` — **NEW**
- `internal/controller/workflow_controller.go` — Validate PiAgent refs
- `internal/controller/workflowrun_controller.go` — Add Pi step execution
- `cmd/operator/main.go` — Register PiAgent controller
- `config/crd/bases/agents.io_piagents.yaml` — **NEW**
- `config/crd/bases/agents.io_workflows.yaml` — Updated
- `config/rbac/role.yaml` — Add piagents, jobs RBAC
- `helm/agent-operator/crds/agents.io_piagents.yaml` — **NEW**
- `helm/agent-operator/templates/rbac.yaml` — Add piagents, jobs

### agent-console
- Backend: New SSE endpoint for workflow Pi events
- Frontend: WorkflowRunView, WorkflowDesigner, WorkflowStepCard components
- Frontend: WorkflowEventStream SSE consumer

### agent-factory
- Helm: PiAgent templates + values schema
- Examples: Mixed-runtime workflow examples

### agent-tools
- New `push piagent` command for OCI packaging

### images/ (new)
- `images/pi-runner/` — Pi runner container image
