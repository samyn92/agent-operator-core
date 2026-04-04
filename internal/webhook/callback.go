package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// CallbackHandler receives real-time events from pi-runner Jobs and writes
// them to the WorkflowRun CR's status.stepResults[].events field.
//
// Route: POST /callback/{namespace}/{workflowrun-name}/{step-name}
// Body:  StepEvent JSON
type CallbackHandler struct {
	client client.Client
}

// NewCallbackHandler creates a new callback handler.
func NewCallbackHandler(c client.Client) *CallbackHandler {
	return &CallbackHandler{client: c}
}

// callbackEventPayload is the JSON payload POSTed by the pi-runner.
type callbackEventPayload struct {
	Type       string `json:"type"`
	Timestamp  int64  `json:"ts"`
	ToolName   string `json:"toolName,omitempty"`
	ToolArgs   string `json:"toolArgs,omitempty"`
	ToolResult string `json:"toolResult,omitempty"`
	Duration   int64  `json:"duration,omitempty"`
	Content    string `json:"content,omitempty"`
}

// ServeHTTP handles incoming callback requests.
// The path after /callback/ is expected to be: {namespace}/{workflowrun}/{step}
func (h *CallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	logger := log.FromContext(ctx)

	// Parse path: expect "namespace/workflowrun-name/step-name"
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(w, "Invalid path. Expected /callback/{namespace}/{workflowrun}/{step}", http.StatusBadRequest)
		return
	}
	namespace := parts[0]
	runName := parts[1]
	stepName := parts[2]

	// Read body
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024)) // 64KB limit per event
	if err != nil {
		logger.Error(err, "Failed to read callback body")
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	// Parse the event
	var payload callbackEventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.Error(err, "Failed to parse callback event", "body", string(body))
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	logger.V(1).Info("Received pi-runner callback",
		"namespace", namespace,
		"workflowrun", runName,
		"step", stepName,
		"eventType", payload.Type,
		"toolName", payload.ToolName,
	)

	// Fetch the WorkflowRun
	run := &agentsv1alpha1.WorkflowRun{}
	if err := h.client.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, run); err != nil {
		logger.Error(err, "Failed to get WorkflowRun", "name", runName, "namespace", namespace)
		http.Error(w, fmt.Sprintf("WorkflowRun %s/%s not found", namespace, runName), http.StatusNotFound)
		return
	}

	// Find the matching step
	stepIdx := -1
	for i, s := range run.Status.StepResults {
		if s.Name == stepName {
			stepIdx = i
			break
		}
	}
	if stepIdx < 0 {
		logger.Info("Step not found in WorkflowRun", "step", stepName, "workflowrun", runName)
		http.Error(w, fmt.Sprintf("Step %q not found in WorkflowRun %s", stepName, runName), http.StatusNotFound)
		return
	}

	// Build the StepEvent
	event := agentsv1alpha1.StepEvent{
		Type:       payload.Type,
		Timestamp:  payload.Timestamp,
		ToolName:   payload.ToolName,
		ToolArgs:   payload.ToolArgs,
		ToolResult: payload.ToolResult,
		Duration:   payload.Duration,
		Content:    payload.Content,
	}

	// Append the event to the step's events list.
	// Use a retry loop to handle conflicts from concurrent updates (the reconciler
	// also updates status, so we may get a ResourceVersion conflict).
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Re-fetch the latest version
			run = &agentsv1alpha1.WorkflowRun{}
			if err := h.client.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, run); err != nil {
				logger.Error(err, "Retry: failed to get WorkflowRun")
				http.Error(w, "Internal error", http.StatusInternalServerError)
				return
			}
			// Re-find step index (may have shifted)
			stepIdx = -1
			for i, s := range run.Status.StepResults {
				if s.Name == stepName {
					stepIdx = i
					break
				}
			}
			if stepIdx < 0 {
				http.Error(w, fmt.Sprintf("Step %q not found on retry", stepName), http.StatusNotFound)
				return
			}
		}

		run.Status.StepResults[stepIdx].Events = append(run.Status.StepResults[stepIdx].Events, event)

		if err := h.client.Status().Update(ctx, run); err != nil {
			if strings.Contains(err.Error(), "the object has been modified") && attempt < maxRetries-1 {
				logger.V(1).Info("Conflict updating WorkflowRun status, retrying", "attempt", attempt+1)
				continue
			}
			logger.Error(err, "Failed to update WorkflowRun status with callback event")
			http.Error(w, "Failed to update status", http.StatusInternalServerError)
			return
		}

		// Success
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"ok":true}`))
		return
	}

	http.Error(w, "Failed after retries", http.StatusInternalServerError)
}
