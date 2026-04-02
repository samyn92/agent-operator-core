package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	batchv1 "k8s.io/api/batch/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// WorkflowReconciler reconciles a Workflow object
type WorkflowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agents.io,resources=workflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=workflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=workflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=agents.io,resources=workflowruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=workflowruns/status,verbs=get;update;patch

func (r *WorkflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Workflow instance
	workflow := &agentsv1alpha1.Workflow{}
	if err := r.Get(ctx, req.NamespacedName, workflow); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Reconciling Workflow", "name", req.Name, "namespace", req.Namespace)

	// Verify all referenced agents exist
	for i, step := range workflow.Spec.Steps {
		agent := &agentsv1alpha1.Agent{}
		if err := r.Get(ctx, types.NamespacedName{Name: step.Agent, Namespace: workflow.Namespace}, agent); err != nil {
			if errors.IsNotFound(err) {
				logger.Error(err, "Referenced Agent not found", "step", i, "agent", step.Agent)
				return ctrl.Result{}, r.updateStatusError(ctx, workflow, "AgentNotFound",
					fmt.Sprintf("Step %d: Agent %s not found", i, step.Agent))
			}
			return ctrl.Result{}, err
		}
	}

	// If schedule trigger is configured, create a CronJob
	if workflow.Spec.Trigger.Schedule != nil {
		if err := r.reconcileScheduleTrigger(ctx, workflow); err != nil {
			logger.Error(err, "Failed to reconcile schedule trigger")
			return ctrl.Result{}, err
		}
	}

	// Update status
	if err := r.updateStatus(ctx, workflow); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) reconcileScheduleTrigger(ctx context.Context, workflow *agentsv1alpha1.Workflow) error {
	schedule := workflow.Spec.Trigger.Schedule

	labels := map[string]string{
		"app.kubernetes.io/name":       "workflow",
		"app.kubernetes.io/instance":   workflow.Name,
		"app.kubernetes.io/managed-by": "agent-operator",
	}

	suspend := false
	if workflow.Spec.Suspend != nil {
		suspend = *workflow.Spec.Suspend
	}

	timezone := schedule.Timezone
	if timezone == "" {
		timezone = "UTC"
	}

	successLimit := int32(3)
	failedLimit := int32(1)

	// The CronJob will create a WorkflowRun via kubectl
	// This is a simple approach - could also use a webhook
	createRunCmd := fmt.Sprintf(`
set -e
TRIGGER_TIME=$(date -Iseconds)
cat <<EOF | kubectl create -f -
apiVersion: agents.io/v1alpha1
kind: WorkflowRun
metadata:
  generateName: %s-
  namespace: %s
spec:
  workflowRef: %s
  triggerData: "{\"type\": \"schedule\", \"time\": \"${TRIGGER_TIME}\"}"
EOF
echo "WorkflowRun created"
`, workflow.Name, workflow.Namespace, workflow.Name)

	desired := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workflow.Name + "-trigger",
			Namespace: workflow.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule.Cron,
			TimeZone:                   &timezone,
			Suspend:                    &suspend,
			SuccessfulJobsHistoryLimit: &successLimit,
			FailedJobsHistoryLimit:     &failedLimit,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labels,
						},
						Spec: corev1.PodSpec{
							RestartPolicy:      corev1.RestartPolicyOnFailure,
							ServiceAccountName: "workflow-trigger", // ServiceAccount with RBAC to create WorkflowRuns
							Containers: []corev1.Container{
								{
									Name:    "trigger",
									Image:   "bitnami/kubectl:latest",
									Command: []string{"/bin/sh", "-c"},
									Args:    []string{createRunCmd},
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(workflow, desired, r.Scheme); err != nil {
		return err
	}

	existing := &batchv1.CronJob{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Only update if the spec actually changed
	if hashJSON(existing.Spec) != hashJSON(desired.Spec) {
		existing.Spec = desired.Spec
		return r.Update(ctx, existing)
	}
	return nil
}

func (r *WorkflowReconciler) updateStatus(ctx context.Context, workflow *agentsv1alpha1.Workflow) error {
	current := &agentsv1alpha1.Workflow{}
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Name, Namespace: workflow.Namespace}, current); err != nil {
		return err
	}

	// Count workflow runs
	runs := &agentsv1alpha1.WorkflowRunList{}
	if err := r.List(ctx, runs, client.InNamespace(workflow.Namespace),
		client.MatchingLabels{"agents.io/workflow": workflow.Name}); err != nil {
		return err
	}

	newRunCount := len(runs.Items)

	// Find most recent run
	var lastRun *agentsv1alpha1.WorkflowRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if lastRun == nil || run.CreationTimestamp.After(lastRun.CreationTimestamp.Time) {
			lastRun = run
		}
	}

	var newLastTriggered *metav1.Time
	var newLastRunStatus string
	if lastRun != nil {
		newLastTriggered = &lastRun.CreationTimestamp
		newLastRunStatus = lastRun.Status.Phase
	}

	// Set webhook URL if webhook trigger is configured
	var newWebhookURL string
	if workflow.Spec.Trigger.Webhook != nil {
		path := workflow.Spec.Trigger.Webhook.Path
		if path == "" {
			path = "/workflow/" + workflow.Name
		}
		newWebhookURL = path
	}

	// Only update if status actually changed
	triggerChanged := (newLastTriggered == nil) != (current.Status.LastTriggered == nil) ||
		(newLastTriggered != nil && current.Status.LastTriggered != nil && !newLastTriggered.Equal(current.Status.LastTriggered))

	if current.Status.RunCount != newRunCount ||
		triggerChanged ||
		current.Status.LastRunStatus != newLastRunStatus ||
		current.Status.WebhookURL != newWebhookURL {
		current.Status.RunCount = newRunCount
		current.Status.LastTriggered = newLastTriggered
		current.Status.LastRunStatus = newLastRunStatus
		current.Status.WebhookURL = newWebhookURL
		return r.Status().Update(ctx, current)
	}

	return nil
}

func (r *WorkflowReconciler) updateStatusError(ctx context.Context, workflow *agentsv1alpha1.Workflow, reason, message string) error {
	current := &agentsv1alpha1.Workflow{}
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Name, Namespace: workflow.Namespace}, current); err != nil {
		return err
	}

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	found := false
	for i, c := range current.Status.Conditions {
		if c.Type == "Ready" {
			current.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		current.Status.Conditions = append(current.Status.Conditions, condition)
	}

	return r.Status().Update(ctx, current)
}

func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.Workflow{}).
		Owns(&batchv1.CronJob{}).
		Complete(r)
}
