package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// Server handles incoming webhooks and creates WorkflowRuns
type Server struct {
	client    client.Client
	namespace string
}

// NewServer creates a new webhook server
func NewServer(c client.Client, namespace string) *Server {
	return &Server{
		client:    c,
		namespace: namespace,
	}
}

// ServeHTTP handles incoming webhook requests
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	logger := log.FromContext(ctx)

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "Failed to read request body")
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	// Determine webhook type from headers
	var webhookType string
	var event string

	if r.Header.Get("X-GitHub-Event") != "" {
		webhookType = "github"
		event = r.Header.Get("X-GitHub-Event")
	} else if r.Header.Get("X-Gitlab-Event") != "" {
		webhookType = "gitlab"
		event = r.Header.Get("X-Gitlab-Event")
	} else {
		webhookType = "generic"
		event = "webhook"
	}

	logger.Info("Received webhook", "type", webhookType, "event", event, "path", r.URL.Path)

	// Find matching workflows
	workflows, err := s.findMatchingWorkflows(ctx, webhookType, event, body, r)
	if err != nil {
		logger.Error(err, "Failed to find matching workflows")
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if len(workflows) == 0 {
		logger.Info("No matching workflows found")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"triggered": 0}`))
		return
	}

	// Create WorkflowRuns for each matching workflow
	triggered := 0
	for _, wf := range workflows {
		if err := s.createWorkflowRun(ctx, &wf, webhookType, event, body); err != nil {
			logger.Error(err, "Failed to create WorkflowRun", "workflow", wf.Name)
			continue
		}
		triggered++
		logger.Info("Created WorkflowRun", "workflow", wf.Name)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`{"triggered": %d}`, triggered)))
}

func (s *Server) findMatchingWorkflows(ctx context.Context, webhookType, event string, body []byte, r *http.Request) ([]agentsv1alpha1.Workflow, error) {
	// List all workflows
	workflows := &agentsv1alpha1.WorkflowList{}
	if err := s.client.List(ctx, workflows); err != nil {
		return nil, err
	}

	var matched []agentsv1alpha1.Workflow

	for _, wf := range workflows.Items {
		// Skip suspended workflows
		if wf.Spec.Suspend != nil && *wf.Spec.Suspend {
			continue
		}

		switch webhookType {
		case "github":
			if wf.Spec.Trigger.GitHub != nil {
				if s.matchGitHubTrigger(ctx, &wf, event, body, r) {
					matched = append(matched, wf)
				}
			}
		case "gitlab":
			if wf.Spec.Trigger.GitLab != nil {
				if s.matchGitLabTrigger(ctx, &wf, event, body, r) {
					matched = append(matched, wf)
				}
			}
		case "generic":
			if wf.Spec.Trigger.Webhook != nil {
				if s.matchWebhookTrigger(ctx, &wf, r) {
					matched = append(matched, wf)
				}
			}
		}
	}

	return matched, nil
}

func (s *Server) matchGitHubTrigger(ctx context.Context, wf *agentsv1alpha1.Workflow, event string, body []byte, r *http.Request) bool {
	trigger := wf.Spec.Trigger.GitHub

	// Validate signature if secret is configured
	if trigger.Secret != nil {
		secret, err := s.getSecret(ctx, wf.Namespace, trigger.Secret)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to get GitHub secret")
			return false
		}
		if !s.validateGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), secret) {
			log.FromContext(ctx).Info("GitHub signature validation failed", "workflow", wf.Name)
			return false
		}
	}

	// Check event type
	if !contains(trigger.Events, event) {
		return false
	}

	// Parse payload for additional filtering
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}

	// Filter by repository
	if len(trigger.Repos) > 0 {
		repo := getNestedString(payload, "repository", "full_name")
		if !contains(trigger.Repos, repo) {
			return false
		}
	}

	// Filter by branch
	if len(trigger.Branches) > 0 {
		var branch string
		if event == "push" {
			ref := getNestedString(payload, "ref")
			branch = strings.TrimPrefix(ref, "refs/heads/")
		} else if event == "pull_request" {
			branch = getNestedString(payload, "pull_request", "head", "ref")
		}
		if branch != "" && !matchesAnyPattern(branch, trigger.Branches) {
			return false
		}
	}

	// Filter by action
	if len(trigger.Actions) > 0 {
		action := getNestedString(payload, "action")
		if action != "" && !contains(trigger.Actions, action) {
			return false
		}
	}

	// Filter by labels
	if len(trigger.Labels) > 0 {
		payloadLabels := extractGitHubLabels(payload)
		if !hasAllLabels(payloadLabels, trigger.Labels) {
			return false
		}
	}

	return true
}

func (s *Server) matchGitLabTrigger(ctx context.Context, wf *agentsv1alpha1.Workflow, event string, body []byte, r *http.Request) bool {
	trigger := wf.Spec.Trigger.GitLab

	// Validate token if secret is configured
	if trigger.Secret != nil {
		secret, err := s.getSecret(ctx, wf.Namespace, trigger.Secret)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to get GitLab secret")
			return false
		}
		// GitLab uses X-Gitlab-Token header
		if r.Header.Get("X-Gitlab-Token") != secret {
			log.FromContext(ctx).Info("GitLab token validation failed", "workflow", wf.Name)
			return false
		}
	}

	// Normalize GitLab event names (they come as "Push Hook", "Merge Request Hook", etc.)
	normalizedEvent := normalizeGitLabEvent(event)

	// Check event type — normalize both sides so users can write "Issue Hook" or "issue"
	if !containsNormalized(trigger.Events, normalizedEvent) {
		log.FromContext(ctx).Info("GitLab event not matched", "workflow", wf.Name,
			"incomingEvent", event, "normalizedEvent", normalizedEvent, "triggerEvents", trigger.Events)
		return false
	}

	// Parse payload for additional filtering
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}

	// Filter by project
	if len(trigger.Projects) > 0 {
		projectPath := getNestedString(payload, "project", "path_with_namespace")
		projectID := fmt.Sprintf("%v", getNestedValue(payload, "project", "id"))
		if !contains(trigger.Projects, projectPath) && !contains(trigger.Projects, projectID) {
			return false
		}
	}

	// Filter by branch
	if len(trigger.Branches) > 0 {
		var branch string
		if normalizedEvent == "push" {
			ref := getNestedString(payload, "ref")
			branch = strings.TrimPrefix(ref, "refs/heads/")
		} else if normalizedEvent == "merge_request" {
			branch = getNestedString(payload, "object_attributes", "source_branch")
		}
		if branch != "" && !matchesAnyPattern(branch, trigger.Branches) {
			return false
		}
	}

	// Filter by action
	if len(trigger.Actions) > 0 {
		action := getNestedString(payload, "object_attributes", "action")
		if action != "" && !contains(trigger.Actions, action) {
			return false
		}
	}

	// Filter by labels
	if len(trigger.Labels) > 0 {
		payloadLabels := extractGitLabLabels(payload)
		if !hasAllLabels(payloadLabels, trigger.Labels) {
			log.FromContext(ctx).Info("GitLab labels not matched", "workflow", wf.Name,
				"payloadLabels", payloadLabels, "requiredLabels", trigger.Labels)
			return false
		}
	}

	return true
}

func (s *Server) matchWebhookTrigger(ctx context.Context, wf *agentsv1alpha1.Workflow, r *http.Request) bool {
	trigger := wf.Spec.Trigger.Webhook

	// Check path
	expectedPath := trigger.Path
	if expectedPath == "" {
		expectedPath = "/workflow/" + wf.Name
	}

	return r.URL.Path == expectedPath
}

func (s *Server) createWorkflowRun(ctx context.Context, wf *agentsv1alpha1.Workflow, webhookType, event string, body []byte) error {
	// Create trigger data
	triggerData := map[string]interface{}{
		"type":    webhookType,
		"event":   event,
		"payload": json.RawMessage(body),
	}
	triggerJSON, _ := json.Marshal(triggerData)

	run := &agentsv1alpha1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: wf.Name + "-",
			Namespace:    wf.Namespace,
			Labels: map[string]string{
				"agents.io/workflow": wf.Name,
				"agents.io/trigger":  webhookType,
			},
		},
		Spec: agentsv1alpha1.WorkflowRunSpec{
			WorkflowRef: wf.Name,
			TriggerData: string(triggerJSON),
		},
	}

	return s.client.Create(ctx, run)
}

func (s *Server) getSecret(ctx context.Context, namespace string, ref *agentsv1alpha1.SecretKeySelector) (string, error) {
	secret := &corev1.Secret{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, secret); err != nil {
		return "", err
	}
	return string(secret.Data[ref.Key]), nil
}

func (s *Server) validateGitHubSignature(body []byte, signature string, secret string) bool {
	if signature == "" {
		return false
	}

	// GitHub sends signature as "sha256=<hex>"
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expected))
}

// normalizeGitLabEvent converts GitLab event names to simple form
func normalizeGitLabEvent(event string) string {
	event = strings.ToLower(event)
	event = strings.TrimSuffix(event, " hook")
	event = strings.ReplaceAll(event, " ", "_")
	return event
}

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// containsNormalized checks if any item in the slice, when normalized as a GitLab event,
// matches the already-normalized item. This allows users to write "Issue Hook" or "issue"
// in their Workflow specs.
func containsNormalized(slice []string, normalizedItem string) bool {
	for _, s := range slice {
		if normalizeGitLabEvent(s) == normalizedItem {
			return true
		}
	}
	return false
}

// extractGitLabLabels extracts label titles from a GitLab webhook payload.
// GitLab issue/MR payloads include labels at object_attributes.labels[] or at the top-level labels[].
func extractGitLabLabels(payload map[string]interface{}) []string {
	var labels []string

	// Try object_attributes.labels first (issue/MR events)
	if objAttrs, ok := payload["object_attributes"].(map[string]interface{}); ok {
		if lbls, ok := objAttrs["labels"].([]interface{}); ok {
			for _, l := range lbls {
				if lMap, ok := l.(map[string]interface{}); ok {
					if title, ok := lMap["title"].(string); ok {
						labels = append(labels, title)
					}
				}
			}
			if len(labels) > 0 {
				return labels
			}
		}
	}

	// Fallback: top-level labels array
	if lbls, ok := payload["labels"].([]interface{}); ok {
		for _, l := range lbls {
			if lMap, ok := l.(map[string]interface{}); ok {
				if title, ok := lMap["title"].(string); ok {
					labels = append(labels, title)
				}
			}
		}
	}

	return labels
}

// hasAllLabels checks that all required labels are present in the payload labels
func hasAllLabels(payloadLabels []string, requiredLabels []string) bool {
	labelSet := make(map[string]bool, len(payloadLabels))
	for _, l := range payloadLabels {
		labelSet[l] = true
	}
	for _, required := range requiredLabels {
		if !labelSet[required] {
			return false
		}
	}
	return true
}

// extractGitHubLabels extracts label names from a GitHub webhook payload.
// GitHub issue/PR payloads include labels at issue.labels[] or pull_request.labels[].
func extractGitHubLabels(payload map[string]interface{}) []string {
	var labels []string

	// Try issue.labels, then pull_request.labels
	for _, key := range []string{"issue", "pull_request"} {
		if obj, ok := payload[key].(map[string]interface{}); ok {
			if lbls, ok := obj["labels"].([]interface{}); ok {
				for _, l := range lbls {
					if lMap, ok := l.(map[string]interface{}); ok {
						if name, ok := lMap["name"].(string); ok {
							labels = append(labels, name)
						}
					}
				}
				if len(labels) > 0 {
					return labels
				}
			}
		}
	}

	return labels
}

func matchesAnyPattern(s string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, s); matched {
			return true
		}
		// Also support ** for recursive matching
		if strings.Contains(pattern, "**") {
			// Simple ** handling: replace ** with * and match
			simplePattern := strings.ReplaceAll(pattern, "**", "*")
			if matched, _ := filepath.Match(simplePattern, s); matched {
				return true
			}
		}
	}
	return false
}

func getNestedString(data map[string]interface{}, keys ...string) string {
	val := getNestedValue(data, keys...)
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

func getNestedValue(data map[string]interface{}, keys ...string) interface{} {
	if len(keys) == 0 {
		return nil
	}

	val, ok := data[keys[0]]
	if !ok {
		return nil
	}

	if len(keys) == 1 {
		return val
	}

	if nested, ok := val.(map[string]interface{}); ok {
		return getNestedValue(nested, keys[1:]...)
	}

	return nil
}
