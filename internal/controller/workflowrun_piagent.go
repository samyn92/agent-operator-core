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
	"github.com/samyn92/agent-operator-core/internal/resources"
	"github.com/samyn92/agent-operator-core/pkg/oci"
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
	env, err := r.buildPiAgentEnv(ctx, run, piAgent, step, stepIndex, prompt)
	if err != nil {
		return ctrl.Result{}, r.failStep(ctx, run, stepIndex, fmt.Sprintf("Failed to build env: %v", err))
	}

	// Resolve GitWorkspace references to PVC info for volume mounting
	gitWorkspaces := r.resolveGitWorkspacesForPiAgent(ctx, piAgent)

	// Build the Job
	job := r.buildPiAgentJob(run, piAgent, step, stepIndex, env, gitWorkspaces)

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
		output, toolCalls, tokensUsed, logEvents, err := r.fetchPiAgentJobOutput(ctx, job)
		if err != nil {
			logger.Error(err, "Failed to fetch Job output, using empty output", "job", stepResult.JobName)
			output = "[Failed to retrieve agent output]"
		}

		stepResult.Phase = "Succeeded"
		stepResult.Output = output
		stepResult.ToolCalls = toolCalls
		stepResult.TokensUsed = tokensUsed
		stepResult.CompletionTime = &metav1.Time{Time: time.Now()}

		// Merge log-parsed events with any callback events already stored.
		// If callback events exist, prefer them (they were written in real-time).
		// If no callback events, use the log-parsed ones as a fallback.
		if len(stepResult.Events) == 0 && len(logEvents) > 0 {
			stepResult.Events = logEvents
		}

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
			output, toolCalls, tokensUsed, logEvents, _ := r.fetchPiAgentJobOutput(ctx, job)
			stepResult.Output = output
			stepResult.ToolCalls = toolCalls
			stepResult.TokensUsed = tokensUsed
			if len(stepResult.Events) == 0 && len(logEvents) > 0 {
				stepResult.Events = logEvents
			}

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
	gitWorkspaces []resources.GitWorkspaceInfo,
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
				{Name: "workspace", MountPath: "/workspace"},
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
			{
				Name: "workspace",
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

	// Mount GitWorkspace PVCs if the PiAgent has workspaceRefs.
	// These provide pre-cloned Git repositories managed by GitWorkspace Deployments.
	for i, gws := range gitWorkspaces {
		volName := fmt.Sprintf("git-workspace-%d", i)
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: gws.PVCName,
				},
			},
		})
		podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: gws.MountPath,
			ReadOnly:  gws.ReadOnly,
		})
	}

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
		// Inline source: pass inline code as an env var and let the runner write it to disk.
		// The runner handles AGENT_INLINE_CODE env var → writes to /agent/index.js
		// The agent-code volume must be writable for this to work.
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{
			Name:  "AGENT_INLINE_CODE",
			Value: source.Inline,
		})
		// Make agent-code volume writable for inline sources (runner writes to /agent/index.js)
		for i := range podSpec.Containers[0].VolumeMounts {
			if podSpec.Containers[0].VolumeMounts[i].Name == "agent-code" {
				podSpec.Containers[0].VolumeMounts[i].ReadOnly = false
				break
			}
		}
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
			Image: "gcr.io/go-containerregistry/crane:debug",
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

	// Track which pull secrets we've already added as volumes to avoid duplicates
	// (multiple toolRefs might reference the same pull secret).
	pullSecretVolumes := make(map[string]bool)

	for i, toolRef := range piAgent.Spec.ToolRefs {
		toolName := oci.ExtractToolName(toolRef.Ref)
		toolDir := fmt.Sprintf("/tools/%s", toolName)

		initContainer := corev1.Container{
			Name:  fmt.Sprintf("tool-%d-%s", i, toolName),
			Image: "gcr.io/go-containerregistry/crane:debug",
			Command: []string{
				"sh", "-c",
				fmt.Sprintf("mkdir -p %s && crane export %s - | tar -xf - -C %s", toolDir, toolRef.Ref, toolDir),
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "tools", MountPath: "/tools"},
			},
		}

		// Add PullSecret support — mount the docker config secret and set
		// DOCKER_CONFIG so crane authenticates to private registries.
		// Same pattern as MCP capability crane init containers.
		if toolRef.PullSecret != nil && toolRef.PullSecret.Name != "" {
			secretName := toolRef.PullSecret.Name
			volName := fmt.Sprintf("pull-secret-%s", secretName)

			// Add the secret volume if we haven't already
			if !pullSecretVolumes[secretName] {
				podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
					Name: volName,
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: secretName,
							Items: []corev1.KeyToPath{
								{
									Key:  ".dockerconfigjson",
									Path: "config.json",
								},
							},
							Optional: boolPtr(true),
						},
					},
				})
				pullSecretVolumes[secretName] = true
			}

			// Mount at a unique path per init container to avoid conflicts
			dockerConfigPath := fmt.Sprintf("/docker-config/%s", secretName)
			initContainer.VolumeMounts = append(initContainer.VolumeMounts, corev1.VolumeMount{
				Name:      volName,
				MountPath: dockerConfigPath,
				ReadOnly:  true,
			})
			initContainer.Env = append(initContainer.Env, corev1.EnvVar{
				Name:  "DOCKER_CONFIG",
				Value: dockerConfigPath,
			})
		}

		podSpec.InitContainers = append(podSpec.InitContainers, initContainer)
	}
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
	step agentsv1alpha1.WorkflowStep,
	stepIndex int,
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
		{Name: "HOME", Value: "/workspace"},
		{Name: "MODEL_PROVIDER", Value: providerName},
		{Name: "MODEL_NAME", Value: modelName},
		{Name: "THINKING_LEVEL", Value: piAgent.Spec.ThinkingLevel},
		{Name: "TOOL_EXECUTION", Value: piAgent.Spec.ToolExecution},
		{Name: "PROMPT", Value: prompt},
		{Name: "WORKSPACE", Value: "/workspace"},
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

	// Add callback URL for real-time tracing.
	// Pi-runner POSTs events to this URL during execution.
	if r.CallbackBaseURL != "" {
		stepName := step.Name
		if stepName == "" {
			stepName = fmt.Sprintf("step-%d", stepIndex)
		}
		callbackURL := fmt.Sprintf("%s/%s/%s/%s", r.CallbackBaseURL, run.Namespace, run.Name, stepName)
		env = append(env, corev1.EnvVar{
			Name:  "CALLBACK_URL",
			Value: callbackURL,
		})
	}

	return env, nil
}

// =============================================================================
// OUTPUT COLLECTION
// =============================================================================

// fetchPiAgentJobOutput reads the pod logs of a completed PiAgent Job and extracts
// the structured output. Returns the text output, tool call count, tokens used, and trace events.
//
// The pi-runner writes JSONL events to stdout. We parse the last `agent_end` event
// to get the final messages, and also read `/output/result.json` if available.
// Falls back to extracting text from message_end events in the JSONL stream.
func (r *WorkflowRunReconciler) fetchPiAgentJobOutput(
	ctx context.Context,
	job *batchv1.Job,
) (output string, toolCalls int, tokensUsed int, events []agentsv1alpha1.StepEvent, err error) {
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
		return "", 0, 0, nil, fmt.Errorf("failed to list Job pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return "", 0, 0, nil, fmt.Errorf("no pods found for Job %s", job.Name)
	}

	// Use the first pod (Jobs with backoffLimit=0 create at most one pod)
	pod := &podList.Items[0]
	logger.V(1).Info("Reading logs from Job pod", "pod", pod.Name, "job", job.Name)

	// Read pod logs using the Kubernetes client-go API
	// We need to get a raw kubernetes clientset from the controller-runtime client
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("failed to get REST config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	logOpts := &corev1.PodLogOptions{
		Container: "pi-runner",
	}
	logStream, err := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, logOpts).Stream(ctx)
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("failed to stream pod logs: %w", err)
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
// It extracts text from message_end events, aggregates tool/token stats,
// and collects individual StepEvents for tool calls.
func parsePiRunnerLogs(reader interface{ Read([]byte) (int, error) }) (output string, toolCalls int, tokensUsed int, events []agentsv1alpha1.StepEvent, err error) {
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
						events = append(events, agentsv1alpha1.StepEvent{
							Type:      "message",
							Timestamp: event.Ts,
							Content:   text,
						})
					}
				}
			}

		case "tool_execution_end":
			toolCalls++
			// Extract tool call details
			stepEvent := agentsv1alpha1.StepEvent{
				Type:      "tool_call",
				Timestamp: event.Ts,
			}
			if event.Data != nil {
				if name, ok := event.Data["toolName"].(string); ok {
					stepEvent.ToolName = name
				}
				// Serialize result as JSON string (can be any type)
				if result, ok := event.Data["result"]; ok {
					if resultBytes, err := json.Marshal(result); err == nil {
						stepEvent.ToolResult = string(resultBytes)
					}
				}
				// Extract duration if present
				if dur, ok := event.Data["duration"].(float64); ok {
					stepEvent.Duration = int64(dur)
				}
			}
			events = append(events, stepEvent)

		case "agent_end":
			// Try to extract token usage from agent_end messages
			if event.Data != nil {
				if msgs, ok := event.Data["messages"]; ok {
					tokensUsed = extractTokenUsage(msgs)
				}
			}

		case "error":
			// Capture errors as events
			if event.Data != nil {
				errMsg := ""
				if msg, ok := event.Data["message"].(string); ok {
					errMsg = msg
				}
				events = append(events, agentsv1alpha1.StepEvent{
					Type:      "error",
					Timestamp: event.Ts,
					Content:   errMsg,
				})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", 0, 0, nil, fmt.Errorf("error reading log stream: %w", err)
	}

	output = strings.Join(messages, "\n")
	return output, toolCalls, tokensUsed, events, nil
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

// resolveGitWorkspacesForPiAgent resolves a PiAgent's workspaceRefs to concrete
// PVC mount info. Supports both explicit (name) and agent-driven (gitRepo+repository)
// modes. For agent-driven refs, auto-creates the GitWorkspace if it doesn't exist.
// Returns an empty slice if no workspaceRefs are configured or if workspaces
// can't be resolved (non-blocking — PiAgent Jobs still run, just without workspace mounts).
func (r *WorkflowRunReconciler) resolveGitWorkspacesForPiAgent(ctx context.Context, piAgent *agentsv1alpha1.PiAgent) []resources.GitWorkspaceInfo {
	if len(piAgent.Spec.WorkspaceRefs) == 0 {
		return nil
	}

	logger := log.FromContext(ctx)
	var result []resources.GitWorkspaceInfo

	for _, ref := range piAgent.Spec.WorkspaceRefs {
		// For agent-driven refs, ensure the GitWorkspace exists
		if IsAgentDriven(ref) {
			if err := EnsureGitWorkspace(ctx, r.Client, ref, piAgent.Namespace); err != nil {
				logger.Error(err, "Failed to ensure GitWorkspace for PiAgent", "repository", ref.Repository, "piAgent", piAgent.Name)
				continue
			}
		}

		wsName := WorkspaceRefName(ref)
		var ws agentsv1alpha1.GitWorkspace
		if err := r.Get(ctx, types.NamespacedName{
			Name:      wsName,
			Namespace: piAgent.Namespace,
		}, &ws); err != nil {
			logger.Error(err, "Failed to resolve GitWorkspace for PiAgent", "workspace", wsName, "piAgent", piAgent.Name)
			continue
		}

		// Only mount workspaces that are Ready
		if ws.Status.Phase != agentsv1alpha1.GitWorkspacePhaseReady {
			logger.Info("Skipping non-ready GitWorkspace for PiAgent", "workspace", wsName, "phase", ws.Status.Phase)
			continue
		}

		// Determine mount path
		mountPath := ref.MountPath
		if mountPath == "" {
			repoName := ws.Spec.Repository
			if parts := strings.Split(repoName, "/"); len(parts) > 0 {
				repoName = parts[len(parts)-1]
			}
			mountPath = fmt.Sprintf("/workspaces/%s", repoName)
		}

		result = append(result, resources.GitWorkspaceInfo{
			PVCName:   resources.GitWorkspacePVCName(&ws),
			MountPath: mountPath,
			ReadOnly:  ref.Access == "readonly",
		})
	}

	return result
}
