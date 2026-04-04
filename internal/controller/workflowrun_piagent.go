package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// =============================================================================
// CONSTANTS
// =============================================================================

const (
	// DefaultPiRunnerImage is the default container image for the pi-runner.
	DefaultPiRunnerImage = "ghcr.io/samyn92/pi-runner:latest"

	// piAgentJobTTL is the time-to-live for completed Jobs (auto-cleanup).
	piAgentJobTTL int32 = 300 // 5 minutes after completion

	// piAgentBackoffLimit is the number of retries for failed Jobs.
	piAgentBackoffLimit int32 = 0 // no retries — fail fast

	// piAgentPollInterval is how often we requeue to check Job status.
	piAgentPollInterval = 2 * time.Second
)

// =============================================================================
// RECONCILE PI AGENT STEP
// =============================================================================

// reconcilePiAgentStep handles execution of a workflow step that references a PiAgent.
// It implements a state machine:
//   - If no Job exists yet (stepResult.JobName == ""), create one
//   - If a Job exists, poll its status until completion
//   - On completion, fetch the output from pod logs
func (r *WorkflowRunReconciler) reconcilePiAgentStep(
	ctx context.Context,
	run *agentsv1alpha1.WorkflowRun,
	step agentsv1alpha1.WorkflowStep,
	stepIndex int,
	stepResult *agentsv1alpha1.StepResult,
) (ctrl.Result, error) {
	// Fetch the PiAgent definition
	piAgent := &agentsv1alpha1.PiAgent{}
	if err := r.Get(ctx, types.NamespacedName{Name: step.PiAgent, Namespace: run.Namespace}, piAgent); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failStep(ctx, run, stepIndex, fmt.Sprintf("PiAgent %s not found", step.PiAgent))
		}
		return ctrl.Result{}, err
	}

	// Validate PiAgent is in Ready phase
	if piAgent.Status.Phase != agentsv1alpha1.PiAgentPhaseReady {
		return ctrl.Result{}, r.failStep(ctx, run, stepIndex,
			fmt.Sprintf("PiAgent %s is not ready (phase: %s)", step.PiAgent, piAgent.Status.Phase))
	}

	// STATE 1: No Job yet → create it
	if stepResult.JobName == "" {
		return r.createPiAgentJob(ctx, run, piAgent, step, stepIndex, stepResult)
	}

	// STATE 2: Job exists → poll status
	return r.pollPiAgentJob(ctx, run, stepIndex, stepResult)
}

// createPiAgentJob builds and creates the Kubernetes Job for a PiAgent step.
func (r *WorkflowRunReconciler) createPiAgentJob(
	ctx context.Context,
	run *agentsv1alpha1.WorkflowRun,
	piAgent *agentsv1alpha1.PiAgent,
	step agentsv1alpha1.WorkflowStep,
	stepIndex int,
	stepResult *agentsv1alpha1.StepResult,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Render the prompt template
	prompt, err := r.renderPrompt(step.Prompt, run)
	if err != nil {
		return ctrl.Result{}, r.failStep(ctx, run, stepIndex, fmt.Sprintf("Failed to render prompt: %v", err))
	}

	// Build the env vars (needs secret lookup for API key)
	env, err := r.buildPiAgentEnv(ctx, run, piAgent, prompt)
	if err != nil {
		return ctrl.Result{}, r.failStep(ctx, run, stepIndex, fmt.Sprintf("Failed to build env: %v", err))
	}

	// Build the Job
	job := r.buildPiAgentJob(run, piAgent, step, stepIndex, env)

	// Set owner reference so the Job is cleaned up with the WorkflowRun
	if err := ctrl.SetControllerReference(run, job, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference on Job: %w", err)
	}

	logger.Info("Creating PiAgent Job",
		"job", job.Name,
		"piAgent", piAgent.Name,
		"step", stepIndex,
		"image", job.Spec.Template.Spec.Containers[0].Image,
	)

	if err := r.Create(ctx, job); err != nil {
		if errors.IsAlreadyExists(err) {
			// Job already exists — another reconcile must have created it.
			// Record the name and requeue to poll.
			stepResult.JobName = job.Name
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: piAgentPollInterval}, nil
		}
		return ctrl.Result{}, r.failStep(ctx, run, stepIndex, fmt.Sprintf("Failed to create Job: %v", err))
	}

	stepResult.JobName = job.Name
	stepResult.Phase = "Running"
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("PiAgent Job created, polling for completion", "job", job.Name)
	return ctrl.Result{RequeueAfter: piAgentPollInterval}, nil
}

// pollPiAgentJob checks the status of an existing PiAgent Job.
func (r *WorkflowRunReconciler) pollPiAgentJob(
	ctx context.Context,
	run *agentsv1alpha1.WorkflowRun,
	stepIndex int,
	stepResult *agentsv1alpha1.StepResult,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: stepResult.JobName, Namespace: run.Namespace}, job); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failStep(ctx, run, stepIndex,
				fmt.Sprintf("Job %s not found (may have been cleaned up)", stepResult.JobName))
		}
		return ctrl.Result{}, err
	}

	// Check for Job completion
	if job.Status.Succeeded > 0 {
		logger.Info("PiAgent Job succeeded", "job", stepResult.JobName, "step", stepIndex)

		// Fetch output from pod logs
		output, toolCalls, tokensUsed, err := r.fetchPiAgentJobOutput(ctx, job)
		if err != nil {
			logger.Error(err, "Failed to fetch Job output, using empty output", "job", stepResult.JobName)
			output = "[Failed to retrieve agent output]"
		}

		stepResult.Phase = "Succeeded"
		stepResult.Output = output
		stepResult.ToolCalls = toolCalls
		stepResult.TokensUsed = tokensUsed
		stepResult.CompletionTime = &metav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Check for Job failure
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			errMsg := fmt.Sprintf("Job %s failed: %s", stepResult.JobName, condition.Message)
			logger.Info("PiAgent Job failed", "job", stepResult.JobName, "reason", condition.Reason, "message", condition.Message)

			// Try to get output even from failed jobs (partial results)
			output, toolCalls, tokensUsed, _ := r.fetchPiAgentJobOutput(ctx, job)
			stepResult.Output = output
			stepResult.ToolCalls = toolCalls
			stepResult.TokensUsed = tokensUsed

			return ctrl.Result{}, r.failStep(ctx, run, stepIndex, errMsg)
		}
	}

	// Job still running
	logger.V(1).Info("PiAgent Job still running", "job", stepResult.JobName, "active", job.Status.Active)
	return ctrl.Result{RequeueAfter: piAgentPollInterval}, nil
}

// =============================================================================
// JOB CONSTRUCTION
// =============================================================================

// buildPiAgentJob constructs the Kubernetes Job spec for a PiAgent step.
func (r *WorkflowRunReconciler) buildPiAgentJob(
	run *agentsv1alpha1.WorkflowRun,
	piAgent *agentsv1alpha1.PiAgent,
	step agentsv1alpha1.WorkflowStep,
	stepIndex int,
	env []corev1.EnvVar,
) *batchv1.Job {
	image := piAgent.Spec.Image
	if image == "" {
		image = DefaultPiRunnerImage
	}

	// Generate a deterministic Job name
	stepName := step.Name
	if stepName == "" {
		stepName = fmt.Sprintf("step-%d", stepIndex)
	}
	jobName := fmt.Sprintf("%s-%s", run.Name, stepName)
	// Truncate to 63 chars (K8s name limit)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	// Build labels for Job and Pod identification
	jobLabels := map[string]string{
		"agents.io/workflowrun": run.Name,
		"agents.io/piagent":     piAgent.Name,
		"agents.io/step":        stepName,
		"agents.io/runtime":     "pi",
	}

	// Build the pod spec
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:  "pi-runner",
			Image: image,
			Env:   env,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "agent-code", MountPath: "/agent", ReadOnly: true},
				{Name: "output", MountPath: "/output"},
				{Name: "tools", MountPath: "/tools", ReadOnly: true},
			},
		}},
		Volumes: []corev1.Volume{
			{
				Name: "agent-code",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "output",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "tools",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
	}

	// Apply resource requirements if specified
	if piAgent.Spec.Resources != nil {
		podSpec.Containers[0].Resources = *piAgent.Spec.Resources
	}

	// Apply ServiceAccount if specified
	if piAgent.Spec.ServiceAccountName != "" {
		podSpec.ServiceAccountName = piAgent.Spec.ServiceAccountName
	}

	// Handle agent source: inline, ConfigMap, or OCI
	r.configurePiAgentSource(piAgent, &podSpec)

	// Handle tool refs: add init containers for each toolRef OCI artifact
	r.configureToolRefs(piAgent, &podSpec)

	ttl := piAgentJobTTL
	backoff := piAgentBackoffLimit

	// Parse timeout from PiAgent spec for activeDeadlineSeconds
	var activeDeadline *int64
	if piAgent.Spec.Timeout != "" {
		if d, err := time.ParseDuration(piAgent.Spec.Timeout); err == nil {
			seconds := int64(d.Seconds())
			activeDeadline = &seconds
		}
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: run.Namespace,
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: jobLabels,
				},
				Spec: podSpec,
			},
		},
	}
}

// configurePiAgentSource sets up the agent code volume based on the PiAgent source type.
func (r *WorkflowRunReconciler) configurePiAgentSource(piAgent *agentsv1alpha1.PiAgent, podSpec *corev1.PodSpec) {
	source := piAgent.Spec.Source

	if source.Inline != "" {
		// Inline source: create a ConfigMap-style volume with the code as a projected item.
		// The controller creates a ConfigMap with the inline code and mounts it.
		// For simplicity, we use a downwardAPI-like approach: mount inline as an env var
		// and have the runner read from AGENT_MODULE_PATH.
		//
		// Better approach: mount inline code via a ConfigMap volume.
		// The PiAgent controller already validates the source, so we know it's set.
		// We'll use a ConfigMap that the workflowrun controller creates alongside the Job.
		//
		// For now, pass inline code as an env var and let the runner write it to disk.
		// The runner handles AGENT_INLINE_CODE env var → writes to /agent/index.js
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{
			Name:  "AGENT_INLINE_CODE",
			Value: source.Inline,
		})
		return
	}

	if source.ConfigMapRef != nil {
		// ConfigMap source: mount the referenced ConfigMap as the agent code volume
		podSpec.Volumes[0] = corev1.Volume{
			Name: "agent-code",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: source.ConfigMapRef.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  source.ConfigMapRef.Key,
							Path: "index.js",
						},
					},
				},
			},
		}
		return
	}

	if source.OCI != nil {
		// OCI source: add an init container that pulls the artifact into the shared volume.
		// Uses crane (or a lightweight OCI puller) to extract the artifact.
		initContainer := corev1.Container{
			Name:  "oci-pull",
			Image: "gcr.io/go-containerregistry/crane:latest",
			Command: []string{
				"sh", "-c",
				fmt.Sprintf(
					"crane export %s - | tar -xf - -C /agent",
					source.OCI.Ref,
				),
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "agent-code", MountPath: "/agent"},
			},
		}
		podSpec.InitContainers = append(podSpec.InitContainers, initContainer)
		// Remove ReadOnly from agent-code mount since init container needs to write
		podSpec.Containers[0].VolumeMounts[0] = corev1.VolumeMount{
			Name:      "agent-code",
			MountPath: "/agent",
			ReadOnly:  true,
		}
		return
	}
}

// configureToolRefs adds init containers for each toolRef OCI artifact.
// Each toolRef is extracted into /tools/<name>/ where <name> is derived from
// the last path segment of the OCI reference (before the tag/digest).
// The pi-runner scans /tools/*/index.js at startup and merges all tool arrays.
func (r *WorkflowRunReconciler) configureToolRefs(piAgent *agentsv1alpha1.PiAgent, podSpec *corev1.PodSpec) {
	if len(piAgent.Spec.ToolRefs) == 0 {
		return
	}

	// The tools volume needs to be writable by init containers
	// Update the main container's mount to ReadOnly (init containers write, runner reads)
	for i, vm := range podSpec.Containers[0].VolumeMounts {
		if vm.Name == "tools" {
			podSpec.Containers[0].VolumeMounts[i].ReadOnly = true
			break
		}
	}

	for i, toolRef := range piAgent.Spec.ToolRefs {
		toolName := extractToolName(toolRef.Ref)
		toolDir := fmt.Sprintf("/tools/%s", toolName)

		initContainer := corev1.Container{
			Name:  fmt.Sprintf("tool-%d-%s", i, toolName),
			Image: "gcr.io/go-containerregistry/crane:latest",
			Command: []string{
				"sh", "-c",
				fmt.Sprintf("mkdir -p %s && crane export %s - | tar -xf - -C %s", toolDir, toolRef.Ref, toolDir),
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "tools", MountPath: "/tools"},
			},
		}

		podSpec.InitContainers = append(podSpec.InitContainers, initContainer)
	}
}

// extractToolName extracts the tool name from an OCI reference.
// It uses the last path segment before the tag/digest as the name.
// Examples:
//
//	"ghcr.io/samyn92/agent-tools/git:0.1.0"    → "git"
//	"ghcr.io/samyn92/agent-tools/file:0.1.0"   → "file"
//	"ghcr.io/org/tools/gitlab@sha256:abc..."    → "gitlab"
//	"registry.io/tool:latest"                    → "tool"
func extractToolName(ref string) string {
	// Remove tag (:...) or digest (@sha256:...)
	name := ref
	if idx := strings.LastIndex(name, "@"); idx != -1 {
		name = name[:idx]
	}
	if idx := strings.LastIndex(name, ":"); idx != -1 {
		// Make sure this isn't the port separator (e.g., "registry:5000/foo")
		// by checking if there's a "/" after it
		afterColon := name[idx+1:]
		if !strings.Contains(afterColon, "/") {
			name = name[:idx]
		}
	}

	// Get the last path segment
	if idx := strings.LastIndex(name, "/"); idx != -1 {
		name = name[idx+1:]
	}

	// Sanitize for Kubernetes naming (lowercase, alphanumeric + hyphens)
	name = strings.ToLower(name)
	var sanitized []byte
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sanitized = append(sanitized, c)
		}
	}
	if len(sanitized) == 0 {
		return "tool"
	}
	return string(sanitized)
}

// =============================================================================
// ENV VAR CONSTRUCTION
// =============================================================================

// buildPiAgentEnv constructs the environment variables for the pi-runner container.
// It maps PiAgent spec fields to the env vars that the runner expects.
func (r *WorkflowRunReconciler) buildPiAgentEnv(
	ctx context.Context,
	run *agentsv1alpha1.WorkflowRun,
	piAgent *agentsv1alpha1.PiAgent,
	prompt string,
) ([]corev1.EnvVar, error) {
	// Parse provider/model from the spec.model field
	parts := strings.SplitN(piAgent.Spec.Model, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid model format %q, expected 'provider/model'", piAgent.Spec.Model)
	}
	providerName := parts[0]
	modelName := parts[1]

	// Find the matching provider config
	var provider *agentsv1alpha1.ProviderConfig
	for i := range piAgent.Spec.Providers {
		if piAgent.Spec.Providers[i].Name == providerName {
			provider = &piAgent.Spec.Providers[i]
			break
		}
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found in PiAgent providers", providerName)
	}

	env := []corev1.EnvVar{
		{Name: "MODEL_PROVIDER", Value: providerName},
		{Name: "MODEL_NAME", Value: modelName},
		{Name: "THINKING_LEVEL", Value: piAgent.Spec.ThinkingLevel},
		{Name: "TOOL_EXECUTION", Value: piAgent.Spec.ToolExecution},
		{Name: "PROMPT", Value: prompt},
	}

	// Add trigger data if present
	if run.Spec.TriggerData != "" {
		env = append(env, corev1.EnvVar{
			Name:  "TRIGGER_DATA",
			Value: run.Spec.TriggerData,
		})
	}

	// Add API key from secret reference (if configured)
	if provider.APIKeySecret != nil {
		env = append(env, corev1.EnvVar{
			Name: "PROVIDER_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: provider.APIKeySecret.Name,
					},
					Key: provider.APIKeySecret.Key,
				},
			},
		})
	}

	// Append user-defined env vars from PiAgent.spec.env.
	// These come after the standard vars so they can override defaults if needed.
	if len(piAgent.Spec.Env) > 0 {
		env = append(env, piAgent.Spec.Env...)
	}

	return env, nil
}

// =============================================================================
// OUTPUT COLLECTION
// =============================================================================

// fetchPiAgentJobOutput reads the pod logs of a completed PiAgent Job and extracts
// the structured output. Returns the text output, tool call count, and tokens used.
//
// The pi-runner writes JSONL events to stdout. We parse the last `agent_end` event
// to get the final messages, and also read `/output/result.json` if available.
// Falls back to extracting text from message_end events in the JSONL stream.
func (r *WorkflowRunReconciler) fetchPiAgentJobOutput(
	ctx context.Context,
	job *batchv1.Job,
) (output string, toolCalls int, tokensUsed int, err error) {
	logger := log.FromContext(ctx)

	// List pods belonging to this Job
	podList := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(labels.Set{
		"job-name": job.Name,
	})
	if err := r.List(ctx, podList, &client.ListOptions{
		Namespace:     job.Namespace,
		LabelSelector: labelSelector,
	}); err != nil {
		return "", 0, 0, fmt.Errorf("failed to list Job pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return "", 0, 0, fmt.Errorf("no pods found for Job %s", job.Name)
	}

	// Use the first pod (Jobs with backoffLimit=0 create at most one pod)
	pod := &podList.Items[0]
	logger.V(1).Info("Reading logs from Job pod", "pod", pod.Name, "job", job.Name)

	// Read pod logs using the Kubernetes client-go API
	// We need to get a raw kubernetes clientset from the controller-runtime client
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to get REST config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to create clientset: %w", err)
	}

	logOpts := &corev1.PodLogOptions{
		Container: "pi-runner",
	}
	logStream, err := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, logOpts).Stream(ctx)
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to stream pod logs: %w", err)
	}
	defer logStream.Close()

	// Parse JSONL events from the log stream
	return parsePiRunnerLogs(logStream)
}

// piRunnerResult represents the structured output from the pi-runner.
type piRunnerResult struct {
	Success   bool     `json:"success"`
	Messages  []string `json:"messages"`
	ToolCalls []struct {
		Name   string      `json:"name"`
		Args   interface{} `json:"args"`
		Result interface{} `json:"result"`
	} `json:"toolCalls"`
	TokensUsed int    `json:"tokensUsed"`
	Error      string `json:"error,omitempty"`
}

// piRunnerEvent represents a single JSONL event line from the pi-runner.
type piRunnerEvent struct {
	Type string                 `json:"type"`
	Ts   int64                  `json:"ts"`
	Data map[string]interface{} `json:"data,omitempty"`
}

// parsePiRunnerLogs parses JSONL events from the pi-runner stdout.
// It extracts text from message_end events and aggregates tool/token stats.
func parsePiRunnerLogs(reader interface{ Read([]byte) (int, error) }) (output string, toolCalls int, tokensUsed int, err error) {
	scanner := bufio.NewScanner(reader)
	// Increase buffer size for potentially large events
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var messages []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event piRunnerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Not a valid JSON line — skip (could be stderr leaking)
			continue
		}

		switch event.Type {
		case "message_end":
			// Extract text content from the message data
			if event.Data != nil {
				if msg, ok := event.Data["message"]; ok {
					text := extractTextFromMessage(msg)
					if text != "" {
						messages = append(messages, text)
					}
				}
			}

		case "tool_execution_end":
			toolCalls++

		case "agent_end":
			// Try to extract token usage from agent_end messages
			if event.Data != nil {
				if msgs, ok := event.Data["messages"]; ok {
					tokensUsed = extractTokenUsage(msgs)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", 0, 0, fmt.Errorf("error reading log stream: %w", err)
	}

	output = strings.Join(messages, "\n")
	return output, toolCalls, tokensUsed, nil
}

// extractTextFromMessage extracts text content from an agent message object.
// The message can be a string or an object with a `content` array containing text blocks.
func extractTextFromMessage(msg interface{}) string {
	// If it's a simple string
	if s, ok := msg.(string); ok {
		return s
	}

	// If it's an object with content array
	msgMap, ok := msg.(map[string]interface{})
	if !ok {
		return ""
	}

	content, ok := msgMap["content"]
	if !ok {
		return ""
	}

	// Content can be a string
	if s, ok := content.(string); ok {
		return s
	}

	// Content can be an array of content blocks
	contentArr, ok := content.([]interface{})
	if !ok {
		return ""
	}

	var texts []string
	for _, block := range contentArr {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if blockMap["type"] == "text" {
			if text, ok := blockMap["text"].(string); ok {
				texts = append(texts, text)
			}
		}
	}

	return strings.Join(texts, "")
}

// extractTokenUsage sums up totalTokens from all assistant messages in the agent_end event.
func extractTokenUsage(msgs interface{}) int {
	msgsArr, ok := msgs.([]interface{})
	if !ok {
		return 0
	}

	total := 0
	for _, m := range msgsArr {
		msgMap, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		usage, ok := msgMap["usage"].(map[string]interface{})
		if !ok {
			continue
		}
		if totalTokens, ok := usage["totalTokens"].(float64); ok {
			total += int(totalTokens)
		}
	}
	return total
}
