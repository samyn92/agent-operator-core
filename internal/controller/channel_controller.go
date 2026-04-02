package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/resources"
)

// ChannelReconciler reconciles a Channel object
type ChannelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agents.io,resources=channels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=channels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=channels/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *ChannelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Channel instance
	channel := &agentsv1alpha1.Channel{}
	if err := r.Get(ctx, req.NamespacedName, channel); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Reconciling Channel", "name", req.Name, "namespace", req.Namespace, "type", channel.Spec.Type)

	// Resolve Agent service URL
	agentServiceURL, err := r.resolveAgentServiceURL(ctx, channel)
	if err != nil {
		logger.Error(err, "Failed to resolve Agent service URL")
		return ctrl.Result{}, err
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, channel, agentServiceURL); err != nil {
		logger.Error(err, "Failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, channel); err != nil {
		logger.Error(err, "Failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Reconcile Ingress (if webhook is configured)
	if channel.Spec.Webhook != nil {
		if err := r.reconcileIngress(ctx, channel); err != nil {
			logger.Error(err, "Failed to reconcile Ingress")
			return ctrl.Result{}, err
		}
	} else {
		// Delete ingress if webhook config is removed
		if err := r.deleteIngressIfExists(ctx, channel); err != nil {
			logger.Error(err, "Failed to delete Ingress")
			return ctrl.Result{}, err
		}
	}

	// Update status
	if err := r.updateStatus(ctx, channel); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ChannelReconciler) reconcileDeployment(ctx context.Context, channel *agentsv1alpha1.Channel, agentServiceURL string) error {
	desired := resources.ChannelDeployment(channel, agentServiceURL)
	if err := controllerutil.SetControllerReference(channel, desired, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Compare desired spec hash (same approach as AgentReconciler)
	desiredHash := desired.Annotations[resources.DesiredSpecHashAnnotation]
	existingHash := existing.Annotations[resources.DesiredSpecHashAnnotation]

	if desiredHash != existingHash {
		existing.Spec = desired.Spec
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		existing.Annotations[resources.DesiredSpecHashAnnotation] = desiredHash
		return r.Update(ctx, existing)
	}
	return nil
}

func (r *ChannelReconciler) reconcileService(ctx context.Context, channel *agentsv1alpha1.Channel) error {
	desired := resources.ChannelService(channel)
	if err := controllerutil.SetControllerReference(channel, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Check if operator-managed fields changed (ports, selector, labels)
	needsUpdate := false
	if !servicePortsEqual(existing.Spec.Ports, desired.Spec.Ports) {
		existing.Spec.Ports = desired.Spec.Ports
		needsUpdate = true
	}
	if !mapsEqual(existing.Spec.Selector, desired.Spec.Selector) {
		existing.Spec.Selector = desired.Spec.Selector
		needsUpdate = true
	}
	if !mapsEqual(existing.Labels, desired.Labels) {
		existing.Labels = desired.Labels
		needsUpdate = true
	}
	if needsUpdate {
		return r.Update(ctx, existing)
	}
	return nil
}

func (r *ChannelReconciler) reconcileIngress(ctx context.Context, channel *agentsv1alpha1.Channel) error {
	desired := resources.ChannelIngress(channel)
	if err := controllerutil.SetControllerReference(channel, desired, r.Scheme); err != nil {
		return err
	}

	existing := &networkingv1.Ingress{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Only update if spec or annotations changed
	if hashJSON(existing.Spec) != hashJSON(desired.Spec) || !mapsEqual(existing.Annotations, desired.Annotations) {
		existing.Spec = desired.Spec
		existing.Annotations = desired.Annotations
		return r.Update(ctx, existing)
	}
	return nil
}

func (r *ChannelReconciler) deleteIngressIfExists(ctx context.Context, channel *agentsv1alpha1.Channel) error {
	existing := &networkingv1.Ingress{}
	err := r.Get(ctx, types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}, existing)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, existing)
}

// resolveAgentServiceURL looks up the Agent referenced by the Channel and returns its service URL
func (r *ChannelReconciler) resolveAgentServiceURL(ctx context.Context, channel *agentsv1alpha1.Channel) (string, error) {
	logger := log.FromContext(ctx)

	// Lookup the Agent by name in the same namespace
	agent := &agentsv1alpha1.Agent{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      channel.Spec.AgentRef,
		Namespace: channel.Namespace,
	}, agent)
	if err != nil {
		if errors.IsNotFound(err) {
			return "", fmt.Errorf("agent %q not found in namespace %q", channel.Spec.AgentRef, channel.Namespace)
		}
		return "", err
	}

	// Construct the Agent's OpenCode service URL
	// OpenCode API runs on port 4096 (resources.OpencodePort)
	agentServiceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		agent.Name, agent.Namespace, resources.OpencodePort)

	logger.V(1).Info("Resolved Agent service URL", "agent", agent.Name, "url", agentServiceURL)

	return agentServiceURL, nil
}

func (r *ChannelReconciler) updateStatus(ctx context.Context, channel *agentsv1alpha1.Channel) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.Channel{}
	if err := r.Get(ctx, types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}, current); err != nil {
		return err
	}

	// Calculate desired status
	newPhase := agentsv1alpha1.ChannelPhasePending
	var replicas int32

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}, deployment)
	if err == nil {
		replicas = deployment.Status.ReadyReplicas
		if deployment.Status.ReadyReplicas > 0 {
			newPhase = agentsv1alpha1.ChannelPhaseReady
		}
	}

	// Calculate service URL (internal)
	serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		channel.Name, channel.Namespace, resources.ChannelPort)

	// Calculate webhook URL (external)
	var webhookURL string
	if channel.Spec.Webhook != nil {
		scheme := "http"
		if channel.Spec.Webhook.TLS != nil {
			scheme = "https"
		}
		path := channel.Spec.Webhook.Path
		if path == "" {
			path = "/webhook"
		}
		webhookURL = fmt.Sprintf("%s://%s%s", scheme, channel.Spec.Webhook.Host, path)
	}

	// Only update if status actually changed
	if current.Status.Phase != newPhase ||
		current.Status.ServiceURL != serviceURL ||
		current.Status.WebhookURL != webhookURL ||
		current.Status.Replicas != replicas {

		current.Status.Phase = newPhase
		current.Status.ServiceURL = serviceURL
		current.Status.WebhookURL = webhookURL
		current.Status.Replicas = replicas
		return r.Status().Update(ctx, current)
	}

	return nil
}

func (r *ChannelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.Channel{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		// Watch Agents - when an Agent changes, re-reconcile Channels that reference it
		Watches(
			&agentsv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.findChannelsForAgent),
		).
		Complete(r)
}

// findChannelsForAgent returns reconcile requests for Channels that reference the given Agent
func (r *ChannelReconciler) findChannelsForAgent(ctx context.Context, obj client.Object) []ctrl.Request {
	agent, ok := obj.(*agentsv1alpha1.Agent)
	if !ok {
		return nil
	}

	// List all channels in the same namespace
	channelList := &agentsv1alpha1.ChannelList{}
	if err := r.List(ctx, channelList, client.InNamespace(agent.Namespace)); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, channel := range channelList.Items {
		// If this channel references the changed agent, queue it for reconciliation
		if channel.Spec.AgentRef == agent.Name {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      channel.Name,
					Namespace: channel.Namespace,
				},
			})
		}
	}

	return requests
}
