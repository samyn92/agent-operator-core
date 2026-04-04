package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// WorkflowRunReconciler reconciles a WorkflowRun object
type WorkflowRunReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client
}

// +kubebuilder:rbac:groups=agents.io,resources=workflowruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=workflowruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=workflowruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=agents.io,resources=piagents,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

func (r *WorkflowRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the WorkflowRun instance
	run := &agentsv1alpha1.WorkflowRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Skip if already completed
	if run.Status.Phase == "Succeeded" || run.Status.Phase == "Failed" {
		return ctrl.Result{}, nil
	}

	logger.Info("Reconciling WorkflowRun", "name", req.Name, "namespace", req.Namespace, "phase", run.Status.Phase, "step", run.Status.CurrentStep)

	// Fetch the referenced Workflow
	workflow := &agentsv1alpha1.Workflow{}
	if err := r.Get(ctx, types.NamespacedName{Name: run.Spec.WorkflowRef, Namespace: run.Namespace}, workflow); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("Workflow %s not found", run.Spec.WorkflowRef))
		}
		return ctrl.Result{}, err
	}

	// Determine if this is simple mode (agent/piAgent + prompt) or advanced mode (steps)
	isSimpleMode := (workflow.Spec.Agent != "" || workflow.Spec.PiAgent != "") && workflow.Spec.Prompt != ""

	// Initialize run if not started
	if run.Status.Phase == "" {
		run.Status.Phase = "Running"
		run.Status.StartTime = &metav1.Time{Time: time.Now()}
		run.Status.CurrentStep = 0

		if isSimpleMode {
			// Simple mode: single step
			run.Status.StepResults = []agentsv1alpha1.StepResult{
				{
					Name:  "main",
					Phase: "Pending",
				},
			}
		} else {
			// Advanced mode: multiple steps
			run.Status.StepResults = make([]agentsv1alpha1.StepResult, len(workflow.Spec.Steps))
			for i, step := range workflow.Spec.Steps {
				name := step.Name
				if name == "" {
					name = fmt.Sprintf("step-%d", i)
				}
				run.Status.StepResults[i] = agentsv1alpha1.StepResult{
					Name:  name,
					Phase: "Pending",
				}
			}
		}
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue to start execution
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Handle simple mode
	if isSimpleMode {
		return r.reconcileSimpleMode(ctx, run, workflow)
	}

	// Handle advanced mode (steps)
	return r.reconcileStepsMode(ctx, run, workflow)
}

// reconcileSimpleMode handles workflows with direct agent + prompt
func (r *WorkflowRunReconciler) reconcileSimpleMode(ctx context.Context, run *agentsv1alpha1.WorkflowRun, workflow *agentsv1alpha1.Workflow) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if len(run.Status.StepResults) == 0 {
		return ctrl.Result{}, r.failRun(ctx, run, "No step results initialized")
	}

	stepResult := &run.Status.StepResults[0]

	// Mark step as running
	if stepResult.Phase == "Pending" {
		logger.Info("Starting simple workflow", "agent", workflow.Spec.Agent)
		stepResult.Phase = "Running"
		stepResult.StartTime = &metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	if stepResult.Phase != "Running" {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Branch: PiAgent (Job-based) vs Agent (HTTP-based)
	if workflow.Spec.PiAgent != "" {
		// Build a synthetic step for the simple-mode PiAgent execution
		simpleStep := agentsv1alpha1.WorkflowStep{
			Name:    "main",
			PiAgent: workflow.Spec.PiAgent,
			Prompt:  workflow.Spec.Prompt,
		}
		result, err := r.reconcilePiAgentStep(ctx, run, simpleStep, 0, stepResult)
		if err != nil {
			return result, err
		}
		// Check if the step completed (succeeded or failed) — if so, finish the run
		if stepResult.Phase == "Succeeded" {
			return ctrl.Result{}, r.succeedRun(ctx, run)
		}
		return result, nil
	}

	// Get the agent (OpenCode runtime)
	agent := &agentsv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Spec.Agent, Namespace: run.Namespace}, agent); err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("Agent %s not found", workflow.Spec.Agent))
	}
	agentURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:4096/session", agent.Name, agent.Namespace)

	// Async state machine: no sessionID yet → start; has sessionID → poll
	if stepResult.SessionID == "" {
		// Render the prompt
		prompt, err := r.renderPrompt(workflow.Spec.Prompt, run)
		if err != nil {
			return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("Failed to render prompt: %v", err))
		}

		logger.Info("Starting async agent call", "agent", workflow.Spec.Agent, "url", agentURL)
		sessionID, err := r.startAgentSession(ctx, agentURL, prompt)
		if err != nil {
			return ctrl.Result{}, r.failRun(ctx, run, err.Error())
		}
		stepResult.SessionID = sessionID
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// Poll for completion
	idle, err := r.pollAgentStatus(ctx, agentURL, stepResult.SessionID)
	if err != nil {
		logger.Error(err, "Failed to poll agent status, will retry", "sessionID", stepResult.SessionID)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !idle {
		logger.Info("Agent still busy, requeuing", "sessionID", stepResult.SessionID)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// Agent finished — fetch the response
	output, err := r.fetchAgentResponse(ctx, agentURL, stepResult.SessionID)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, run, err.Error())
	}

	// Success
	logger.Info("Simple workflow completed successfully", "outputLength", len(output))
	stepResult.Phase = "Succeeded"
	stepResult.Output = output
	stepResult.CompletionTime = &metav1.Time{Time: time.Now()}

	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.succeedRun(ctx, run)
}

// reconcileStepsMode handles workflows with multiple steps
func (r *WorkflowRunReconciler) reconcileStepsMode(ctx context.Context, run *agentsv1alpha1.WorkflowRun, workflow *agentsv1alpha1.Workflow) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Execute current step
	currentStep := run.Status.CurrentStep
	if currentStep >= len(workflow.Spec.Steps) {
		// All steps completed
		return ctrl.Result{}, r.succeedRun(ctx, run)
	}

	step := workflow.Spec.Steps[currentStep]
	stepResult := &run.Status.StepResults[currentStep]

	// Check condition if specified
	if step.Condition != "" {
		shouldRun, err := r.evaluateCondition(step.Condition, run)
		if err != nil {
			logger.Error(err, "Failed to evaluate condition", "step", currentStep)
			// Skip step on condition error
			stepResult.Phase = "Skipped"
			stepResult.Error = fmt.Sprintf("Condition error: %v", err)
			run.Status.CurrentStep++
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if !shouldRun {
			stepResult.Phase = "Skipped"
			stepResult.CompletionTime = &metav1.Time{Time: time.Now()}
			run.Status.CurrentStep++
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	}

	// Mark step as running and return - next reconcile will execute it
	// This prevents race conditions when multiple reconciles happen concurrently
	if stepResult.Phase == "Pending" {
		logger.Info("Starting step", "step", currentStep, "name", step.Name, "agent", step.Agent)
		stepResult.Phase = "Running"
		stepResult.StartTime = &metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		// Return and requeue - the actual execution happens on next reconcile
		// This ensures we don't race with concurrent reconciles
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	// Only execute if step is in Running phase (set by previous reconcile)
	if stepResult.Phase != "Running" {
		// Step already completed or failed, move on
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Branch: PiAgent (Job-based) vs Agent (HTTP-based)
	if step.PiAgent != "" {
		result, err := r.reconcilePiAgentStep(ctx, run, step, currentStep, stepResult)
		if err != nil {
			return result, err
		}
		// Check if the Pi step completed — advance to next step
		if stepResult.Phase == "Succeeded" {
			run.Status.CurrentStep++
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if stepResult.Phase == "Failed" {
			continueOnError := step.ContinueOnError != nil && *step.ContinueOnError
			if continueOnError {
				run.Status.CurrentStep++
				if err := r.Status().Update(ctx, run); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			// failStep already called by reconcilePiAgentStep
			return result, nil
		}
		return result, nil
	}

	// Get the agent (OpenCode runtime)
	agent := &agentsv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: step.Agent, Namespace: run.Namespace}, agent); err != nil {
		return ctrl.Result{}, r.failStep(ctx, run, currentStep, fmt.Sprintf("Agent %s not found", step.Agent))
	}
	agentURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:4096/session", agent.Name, agent.Namespace)

	// Async state machine: no sessionID yet → start; has sessionID → poll
	if stepResult.SessionID == "" {
		// Build the prompt with template substitution
		prompt, err := r.renderPrompt(step.Prompt, run)
		if err != nil {
			return ctrl.Result{}, r.failStep(ctx, run, currentStep, fmt.Sprintf("Failed to render prompt: %v", err))
		}

		logger.Info("Starting async agent call", "step", currentStep, "agent", step.Agent, "url", agentURL)
		sessionID, err := r.startAgentSession(ctx, agentURL, prompt)
		if err != nil {
			continueOnError := step.ContinueOnError != nil && *step.ContinueOnError
			if continueOnError {
				stepResult.Phase = "Failed"
				stepResult.Error = err.Error()
				stepResult.CompletionTime = &metav1.Time{Time: time.Now()}
				run.Status.CurrentStep++
				if err := r.Status().Update(ctx, run); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, r.failStep(ctx, run, currentStep, err.Error())
		}
		stepResult.SessionID = sessionID
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// Poll for completion
	idle, err := r.pollAgentStatus(ctx, agentURL, stepResult.SessionID)
	if err != nil {
		logger.Error(err, "Failed to poll agent status, will retry", "step", currentStep, "sessionID", stepResult.SessionID)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !idle {
		logger.Info("Agent still busy, requeuing", "step", currentStep, "sessionID", stepResult.SessionID)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// Agent finished — fetch the response
	output, err := r.fetchAgentResponse(ctx, agentURL, stepResult.SessionID)
	if err != nil {
		continueOnError := step.ContinueOnError != nil && *step.ContinueOnError
		if continueOnError {
			stepResult.Phase = "Failed"
			stepResult.Error = err.Error()
			stepResult.CompletionTime = &metav1.Time{Time: time.Now()}
			run.Status.CurrentStep++
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, r.failStep(ctx, run, currentStep, err.Error())
	}

	// Step succeeded
	logger.Info("Step completed successfully", "step", currentStep, "name", step.Name, "outputLength", len(output))
	stepResult.Phase = "Succeeded"
	stepResult.Output = output
	stepResult.CompletionTime = &metav1.Time{Time: time.Now()}
	run.Status.CurrentStep++

	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	// Continue to next step
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// startAgentSession creates a new OpenCode session and sends the prompt asynchronously.
// Returns the session ID for subsequent polling.
func (r *WorkflowRunReconciler) startAgentSession(ctx context.Context, baseURL, prompt string) (string, error) {
	logger := log.FromContext(ctx)
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	// Step 1: Create a new session
	logger.Info("Creating agent session", "url", baseURL)
	sessionBytes, _ := json.Marshal(map[string]interface{}{})

	sessionReq, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(sessionBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create session request: %w", err)
	}
	sessionReq.Header.Set("Content-Type", "application/json")

	sessionResp, err := client.Do(sessionReq)
	if err != nil {
		return "", fmt.Errorf("session creation failed: %w", err)
	}
	defer sessionResp.Body.Close()

	sessionRespBody, err := io.ReadAll(sessionResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read session response: %w", err)
	}

	if sessionResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("session creation returned status %d: %s", sessionResp.StatusCode, string(sessionRespBody))
	}

	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sessionRespBody, &session); err != nil {
		return "", fmt.Errorf("failed to parse session response: %w", err)
	}
	if session.ID == "" {
		return "", fmt.Errorf("session response missing ID: %s", string(sessionRespBody))
	}
	logger.Info("Agent session created", "sessionID", session.ID)

	// Step 2: Send prompt asynchronously via prompt_async
	asyncURL := fmt.Sprintf("%s/%s/prompt_async", baseURL, session.ID)
	messageBody := map[string]interface{}{
		"parts": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	messageBytes, _ := json.Marshal(messageBody)

	asyncReq, err := http.NewRequestWithContext(ctx, "POST", asyncURL, bytes.NewReader(messageBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create async prompt request: %w", err)
	}
	asyncReq.Header.Set("Content-Type", "application/json")

	asyncResp, err := client.Do(asyncReq)
	if err != nil {
		return "", fmt.Errorf("async prompt request failed: %w", err)
	}
	defer asyncResp.Body.Close()

	if asyncResp.StatusCode != http.StatusOK && asyncResp.StatusCode != http.StatusAccepted && asyncResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(asyncResp.Body)
		return "", fmt.Errorf("async prompt returned status %d: %s", asyncResp.StatusCode, string(body))
	}

	logger.Info("Async prompt sent successfully", "sessionID", session.ID)
	return session.ID, nil
}

// pollAgentStatus checks whether the OpenCode session has finished processing.
// Returns true if the session is idle (finished), false if still busy.
func (r *WorkflowRunReconciler) pollAgentStatus(ctx context.Context, baseURL, sessionID string) (bool, error) {
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// GET /session/status returns a map of sessionID -> {type: "idle"|"busy"}
	statusURL := strings.TrimSuffix(baseURL, "/session") + "/session/status"
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create status request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("status request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("status request returned %d: %s", resp.StatusCode, string(body))
	}

	var statuses map[string]struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		return false, fmt.Errorf("failed to decode status response: %w", err)
	}

	if status, ok := statuses[sessionID]; ok {
		return status.Type == "idle", nil
	}

	// If session not found in statuses, assume idle (it may have already completed)
	return true, nil
}

// fetchAgentResponse retrieves the assistant's text response from a completed session.
func (r *WorkflowRunReconciler) fetchAgentResponse(ctx context.Context, baseURL, sessionID string) (string, error) {
	logger := log.FromContext(ctx)
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	// GET /session/{id}/message returns messages for the session
	messagesURL := fmt.Sprintf("%s/%s/message", baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, "GET", messagesURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create messages request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("messages request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read messages response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("messages request returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse messages — OpenCode returns a map or array of messages
	// We need the last assistant message, then fetch its parts
	var messages []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}

	// Try array first
	if err := json.Unmarshal(body, &messages); err != nil {
		// Try map format
		var messagesMap map[string]struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		if err2 := json.Unmarshal(body, &messagesMap); err2 != nil {
			return "", fmt.Errorf("failed to parse messages: %w (also tried map: %w)", err, err2)
		}
		for _, m := range messagesMap {
			messages = append(messages, m)
		}
	}

	// Find the last assistant message
	var lastAssistantID string
	for _, msg := range messages {
		if msg.Role == "assistant" {
			lastAssistantID = msg.ID
		}
	}
	if lastAssistantID == "" {
		return "[No assistant message found in session]", nil
	}

	// Fetch parts for this session: GET /session/{id}/messages/parts
	partsURL := fmt.Sprintf("%s/%s/messages/parts", baseURL, sessionID)
	partsReq, err := http.NewRequestWithContext(ctx, "GET", partsURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create parts request: %w", err)
	}
	partsReq.Header.Set("Accept", "application/json")

	partsResp, err := client.Do(partsReq)
	if err != nil {
		return "", fmt.Errorf("parts request failed: %w", err)
	}
	defer partsResp.Body.Close()

	partsBody, err := io.ReadAll(partsResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read parts response: %w", err)
	}

	if partsResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("parts request returned status %d: %s", partsResp.StatusCode, string(partsBody))
	}

	// Parse parts — array of parts, filter to text parts for the last assistant message
	var parts []struct {
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
	}

	if err := json.Unmarshal(partsBody, &parts); err != nil {
		// Try map format
		var partsMap map[string]struct {
			MessageID string `json:"messageID"`
			Type      string `json:"type"`
			Text      string `json:"text"`
		}
		if err2 := json.Unmarshal(partsBody, &partsMap); err2 != nil {
			return "", fmt.Errorf("failed to parse parts: %w (also tried map: %w)", err, err2)
		}
		for _, p := range partsMap {
			parts = append(parts, p)
		}
	}

	var texts []string
	for _, part := range parts {
		if part.MessageID == lastAssistantID && part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}

	if len(texts) == 0 {
		logger.Info("No text parts found for assistant message", "sessionID", sessionID, "messageID", lastAssistantID)
		return "[No text parts in assistant response]", nil
	}

	logger.Info("Agent response fetched", "sessionID", sessionID, "textParts", len(texts), "totalLength", len(strings.Join(texts, "\n")))
	return strings.Join(texts, "\n"), nil
}

func (r *WorkflowRunReconciler) renderPrompt(promptTemplate string, run *agentsv1alpha1.WorkflowRun) (string, error) {
	// Parse trigger data from JSON
	var triggerData map[string]interface{}
	if run.Spec.TriggerData != "" {
		if err := json.Unmarshal([]byte(run.Spec.TriggerData), &triggerData); err != nil {
			return "", fmt.Errorf("failed to parse trigger data: %w", err)
		}
	}

	// Flatten the trigger data: merge payload fields into trigger for easier access
	// This allows templates to use {{.trigger.repository}} instead of {{.trigger.payload.repository}}
	trigger := make(map[string]interface{})
	if triggerData != nil {
		// First copy top-level fields (type, event)
		for k, v := range triggerData {
			if k != "payload" {
				trigger[k] = v
			}
		}
		// Then merge payload fields into trigger (payload fields take precedence)
		if payload, ok := triggerData["payload"].(map[string]interface{}); ok {
			for k, v := range payload {
				trigger[k] = v
			}
		}
	}

	// Build template data
	data := map[string]interface{}{
		"trigger": trigger,
		"steps":   make(map[string]interface{}),
	}

	// Add previous step outputs
	steps := data["steps"].(map[string]interface{})
	for _, result := range run.Status.StepResults {
		if result.Phase == "Succeeded" {
			steps[result.Name] = map[string]interface{}{
				"output": result.Output,
				"phase":  result.Phase,
			}
		}
	}

	tmpl, err := template.New("prompt").Parse(promptTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (r *WorkflowRunReconciler) evaluateCondition(condition string, run *agentsv1alpha1.WorkflowRun) (bool, error) {
	// Simple condition evaluation
	// Format: "steps.<name>.output contains '<text>'"
	// TODO: Use a proper CEL evaluator for more complex conditions

	if strings.Contains(condition, "contains") {
		parts := strings.SplitN(condition, "contains", 2)
		if len(parts) != 2 {
			return false, fmt.Errorf("invalid condition format")
		}

		path := strings.TrimSpace(parts[0])
		search := strings.Trim(strings.TrimSpace(parts[1]), "'\"")

		// Parse path like "steps[0].output" or "steps.step-0.output"
		if strings.HasPrefix(path, "steps") {
			for _, result := range run.Status.StepResults {
				if strings.Contains(path, result.Name) || strings.Contains(path, fmt.Sprintf("[%d]", 0)) {
					return strings.Contains(result.Output, search), nil
				}
			}
		}
	}

	// Default to true if condition can't be evaluated
	return true, nil
}

func (r *WorkflowRunReconciler) failStep(ctx context.Context, run *agentsv1alpha1.WorkflowRun, stepIndex int, errMsg string) error {
	run.Status.StepResults[stepIndex].Phase = "Failed"
	run.Status.StepResults[stepIndex].Error = errMsg
	run.Status.StepResults[stepIndex].CompletionTime = &metav1.Time{Time: time.Now()}
	run.Status.Phase = "Failed"
	run.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	run.Status.Error = fmt.Sprintf("Step %d failed: %s", stepIndex, errMsg)
	return r.Status().Update(ctx, run)
}

func (r *WorkflowRunReconciler) failRun(ctx context.Context, run *agentsv1alpha1.WorkflowRun, errMsg string) error {
	run.Status.Phase = "Failed"
	run.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	run.Status.Error = errMsg
	return r.Status().Update(ctx, run)
}

func (r *WorkflowRunReconciler) succeedRun(ctx context.Context, run *agentsv1alpha1.WorkflowRun) error {
	logger := log.FromContext(ctx)

	// Fetch the workflow to get output config
	workflow := &agentsv1alpha1.Workflow{}
	if err := r.Get(ctx, types.NamespacedName{Name: run.Spec.WorkflowRef, Namespace: run.Namespace}, workflow); err != nil {
		logger.Error(err, "Failed to fetch workflow for output handling")
	} else if workflow.Spec.Output != nil {
		// Get the output to send
		output := r.getOutputForSending(workflow, run)
		if output != "" {
			if err := r.sendOutputs(ctx, workflow, run, output); err != nil {
				logger.Error(err, "Failed to send workflow output")
				// Don't fail the run, just log the error
			}
		}
	}

	run.Status.Phase = "Succeeded"
	run.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	return r.Status().Update(ctx, run)
}

// getOutputForSending returns the output text to send based on workflow config
func (r *WorkflowRunReconciler) getOutputForSending(workflow *agentsv1alpha1.Workflow, run *agentsv1alpha1.WorkflowRun) string {
	if workflow.Spec.Output == nil {
		return ""
	}

	// Determine which step's output to send
	fromStep := workflow.Spec.Output.FromStep
	if fromStep == "" {
		// Default to last step
		if len(run.Status.StepResults) > 0 {
			lastStep := run.Status.StepResults[len(run.Status.StepResults)-1]
			return lastStep.Output
		}
		return ""
	}

	// Find the specified step
	for _, result := range run.Status.StepResults {
		if result.Name == fromStep {
			return result.Output
		}
	}

	return ""
}

// sendOutputs sends the workflow output to configured destinations
func (r *WorkflowRunReconciler) sendOutputs(ctx context.Context, workflow *agentsv1alpha1.Workflow, run *agentsv1alpha1.WorkflowRun, output string) error {
	logger := log.FromContext(ctx)
	var errs []error

	outputConfig := workflow.Spec.Output

	// Send to Telegram
	if outputConfig.Notify != nil && outputConfig.Notify.Telegram != nil {
		if err := r.sendTelegramOutput(ctx, run.Namespace, outputConfig.Notify.Telegram, output); err != nil {
			logger.Error(err, "Failed to send Telegram notification")
			errs = append(errs, err)
		}
	}

	// Send to Slack
	if outputConfig.Notify != nil && outputConfig.Notify.Slack != nil {
		if err := r.sendSlackOutput(ctx, run.Namespace, outputConfig.Notify.Slack, output); err != nil {
			logger.Error(err, "Failed to send Slack notification")
			errs = append(errs, err)
		}
	}

	// Post to GitHub
	if outputConfig.GitHub != nil {
		if err := r.sendGitHubOutput(ctx, run, workflow, output); err != nil {
			logger.Error(err, "Failed to post GitHub comment")
			errs = append(errs, err)
		}
	}

	// Post to GitLab
	if outputConfig.GitLab != nil {
		if err := r.sendGitLabOutput(ctx, run, workflow, output); err != nil {
			logger.Error(err, "Failed to post GitLab comment")
			errs = append(errs, err)
		}
	}

	// Send to webhook
	if outputConfig.Webhook != nil {
		if err := r.sendWebhookOutput(ctx, run.Namespace, outputConfig.Webhook, run, output); err != nil {
			logger.Error(err, "Failed to send webhook output")
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to send some outputs: %v", errs)
	}
	return nil
}

// sendTelegramOutput sends output via Telegram Bot API
func (r *WorkflowRunReconciler) sendTelegramOutput(ctx context.Context, namespace string, config *agentsv1alpha1.TelegramOutput, output string) error {
	// Get bot token from secret
	token, err := r.getSecretValue(ctx, namespace, config.BotTokenSecret)
	if err != nil {
		return fmt.Errorf("failed to get telegram bot token: %w", err)
	}

	// Send message via Telegram API
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	body := map[string]interface{}{
		"chat_id":    config.ChatID,
		"text":       output,
		"parse_mode": "Markdown",
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// sendSlackOutput sends output via Slack webhook
func (r *WorkflowRunReconciler) sendSlackOutput(ctx context.Context, namespace string, config *agentsv1alpha1.SlackOutput, output string) error {
	// Get webhook URL from secret
	webhookURL, err := r.getSecretValue(ctx, namespace, config.WebhookSecret)
	if err != nil {
		return fmt.Errorf("failed to get slack webhook url: %w", err)
	}

	body := map[string]string{"text": output}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// sendGitHubOutput posts output as a GitHub comment
func (r *WorkflowRunReconciler) sendGitHubOutput(ctx context.Context, run *agentsv1alpha1.WorkflowRun, workflow *agentsv1alpha1.Workflow, output string) error {
	config := workflow.Spec.Output.GitHub
	if config.Comment != nil && !*config.Comment {
		return nil // Comment posting disabled
	}

	// Get token - prefer output config, fall back to trigger secret
	var tokenSecret *agentsv1alpha1.SecretKeySelector
	if config.TokenSecret != nil {
		tokenSecret = config.TokenSecret
	} else if workflow.Spec.Trigger.GitHub != nil && workflow.Spec.Trigger.GitHub.Secret != nil {
		tokenSecret = workflow.Spec.Trigger.GitHub.Secret
	} else {
		return fmt.Errorf("no GitHub token configured")
	}

	token, err := r.getSecretValue(ctx, run.Namespace, *tokenSecret)
	if err != nil {
		return fmt.Errorf("failed to get github token: %w", err)
	}

	// Parse trigger data to get repo and PR/issue number
	var triggerData map[string]interface{}
	if err := json.Unmarshal([]byte(run.Spec.TriggerData), &triggerData); err != nil {
		return fmt.Errorf("failed to parse trigger data: %w", err)
	}

	// Extract repo and issue/PR number from trigger data
	repo, issueNumber, err := r.extractGitHubContext(triggerData)
	if err != nil {
		return fmt.Errorf("failed to extract github context: %w", err)
	}

	// Post comment via GitHub API
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, issueNumber)
	body := map[string]string{"body": output}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// extractGitHubContext extracts repo and issue/PR number from trigger data
func (r *WorkflowRunReconciler) extractGitHubContext(triggerData map[string]interface{}) (repo string, number int, err error) {
	// The trigger data structure is {type, event, payload: {...}}
	// We need to look inside payload for the actual GitHub webhook data
	data := triggerData
	if payload, ok := triggerData["payload"].(map[string]interface{}); ok {
		data = payload
	}

	// Try pull_request
	if pr, ok := data["pull_request"].(map[string]interface{}); ok {
		if n, ok := pr["number"].(float64); ok {
			number = int(n)
		}
	}

	// Try issue
	if number == 0 {
		if issue, ok := data["issue"].(map[string]interface{}); ok {
			if n, ok := issue["number"].(float64); ok {
				number = int(n)
			}
		}
	}

	// Try top-level number (for some webhook payloads)
	if number == 0 {
		if n, ok := data["number"].(float64); ok {
			number = int(n)
		}
	}

	// Get repo
	if repository, ok := data["repository"].(map[string]interface{}); ok {
		if fullName, ok := repository["full_name"].(string); ok {
			repo = fullName
		}
	}

	if repo == "" || number == 0 {
		return "", 0, fmt.Errorf("could not extract repo (%s) or number (%d) from trigger data", repo, number)
	}

	return repo, number, nil
}

// sendGitLabOutput posts output as a GitLab comment
func (r *WorkflowRunReconciler) sendGitLabOutput(ctx context.Context, run *agentsv1alpha1.WorkflowRun, workflow *agentsv1alpha1.Workflow, output string) error {
	config := workflow.Spec.Output.GitLab
	if config.Comment != nil && !*config.Comment {
		return nil // Comment posting disabled
	}

	// Get token
	var tokenSecret *agentsv1alpha1.SecretKeySelector
	if config.TokenSecret != nil {
		tokenSecret = config.TokenSecret
	} else if workflow.Spec.Trigger.GitLab != nil && workflow.Spec.Trigger.GitLab.Secret != nil {
		tokenSecret = workflow.Spec.Trigger.GitLab.Secret
	} else {
		return fmt.Errorf("no GitLab token configured")
	}

	token, err := r.getSecretValue(ctx, run.Namespace, *tokenSecret)
	if err != nil {
		return fmt.Errorf("failed to get gitlab token: %w", err)
	}

	// Parse trigger data
	var triggerData map[string]interface{}
	if err := json.Unmarshal([]byte(run.Spec.TriggerData), &triggerData); err != nil {
		return fmt.Errorf("failed to parse trigger data: %w", err)
	}

	// Extract project and MR IID
	projectID, mrIID, err := r.extractGitLabContext(triggerData)
	if err != nil {
		return fmt.Errorf("failed to extract gitlab context: %w", err)
	}

	// Post comment via GitLab API
	url := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/merge_requests/%d/notes", projectID, mrIID)
	body := map[string]string{"body": output}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", token)

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitlab API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// extractGitLabContext extracts project ID and MR IID from trigger data
func (r *WorkflowRunReconciler) extractGitLabContext(triggerData map[string]interface{}) (projectID string, mrIID int, err error) {
	// The trigger data structure is {type, event, payload: {...}}
	// We need to look inside payload for the actual GitLab webhook data
	data := triggerData
	if payload, ok := triggerData["payload"].(map[string]interface{}); ok {
		data = payload
	}

	// Get project ID
	if project, ok := data["project"].(map[string]interface{}); ok {
		if id, ok := project["id"].(float64); ok {
			projectID = fmt.Sprintf("%d", int(id))
		} else if pathWithNS, ok := project["path_with_namespace"].(string); ok {
			// URL encode the path
			projectID = strings.ReplaceAll(pathWithNS, "/", "%2F")
		}
	}

	// Get MR IID
	if objAttrs, ok := data["object_attributes"].(map[string]interface{}); ok {
		if iid, ok := objAttrs["iid"].(float64); ok {
			mrIID = int(iid)
		}
	}

	if projectID == "" || mrIID == 0 {
		return "", 0, fmt.Errorf("could not extract project ID (%s) or MR IID (%d) from trigger data", projectID, mrIID)
	}

	return projectID, mrIID, nil
}

// sendWebhookOutput sends output to a webhook URL
func (r *WorkflowRunReconciler) sendWebhookOutput(ctx context.Context, namespace string, config *agentsv1alpha1.WebhookOutput, run *agentsv1alpha1.WorkflowRun, output string) error {
	body := map[string]interface{}{
		"workflow":    run.Spec.WorkflowRef,
		"workflowRun": run.Name,
		"output":      output,
		"status":      "succeeded",
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", config.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Add authorization if secret is provided
	if config.Secret != nil {
		authToken, err := r.getSecretValue(ctx, namespace, *config.Secret)
		if err != nil {
			return fmt.Errorf("failed to get webhook auth secret: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	}

	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// getSecretValue retrieves a value from a Kubernetes secret
func (r *WorkflowRunReconciler) getSecretValue(ctx context.Context, namespace string, selector agentsv1alpha1.SecretKeySelector) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: selector.Name, Namespace: namespace}, secret); err != nil {
		return "", err
	}

	value, ok := secret.Data[selector.Key]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s", selector.Key, selector.Name)
	}

	return string(value), nil
}

func (r *WorkflowRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.WorkflowRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
