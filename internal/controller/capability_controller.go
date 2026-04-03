package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/resources"
)

// CapabilityReconciler reconciles a Capability object
type CapabilityReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agents.io,resources=capabilities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=capabilities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=capabilities/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *CapabilityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Capability instance
	capability := &agentsv1alpha1.Capability{}
	if err := r.Get(ctx, req.NamespacedName, capability); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Reconciling Capability", "name", req.Name, "namespace", req.Namespace, "type", capability.Spec.Type)

	// Validate the capability spec
	if err := r.validateCapability(capability); err != nil {
		logger.Error(err, "Capability validation failed")
		if updateErr := r.updateStatusFailed(ctx, capability, err.Error()); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		// Don't requeue - user needs to fix the spec
		return ctrl.Result{}, nil
	}

	// For server-mode MCP capabilities, reconcile the MCP server Deployment + Service
	if capability.Spec.Type == agentsv1alpha1.CapabilityTypeMCP &&
		capability.Spec.MCP != nil &&
		capability.Spec.MCP.Mode == "server" {

		ready, err := r.reconcileMCPServer(ctx, capability)
		if err != nil {
			logger.Error(err, "Failed to reconcile MCP server")
			if updateErr := r.updateStatusFailed(ctx, capability, fmt.Sprintf("MCP server reconciliation failed: %v", err)); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, err
		}

		if !ready {
			// MCP server Deployment is not ready yet — stay in Pending phase.
			// The controller will be re-triggered when the owned Deployment changes.
			if err := r.updateStatusPending(ctx, capability, "Waiting for MCP server Deployment to become ready"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	// Find agents using this capability
	usedBy, err := r.findAgentsUsingCapability(ctx, capability)
	if err != nil {
		logger.Error(err, "Failed to find agents using capability")
		return ctrl.Result{}, err
	}

	// Update status to Ready
	if err := r.updateStatusReady(ctx, capability, usedBy); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// =============================================================================
// MCP SERVER RECONCILIATION
// =============================================================================

// reconcileMCPServer ensures the Deployment and Service exist for a server-mode MCP capability.
// Returns true if the Deployment is ready (available replicas > 0), false if still rolling out.
func (r *CapabilityReconciler) reconcileMCPServer(ctx context.Context, capability *agentsv1alpha1.Capability) (bool, error) {
	logger := log.FromContext(ctx)

	// Reconcile the MCP server Deployment
	if err := r.reconcileMCPServerDeployment(ctx, capability); err != nil {
		return false, fmt.Errorf("failed to reconcile MCP server deployment: %w", err)
	}

	// Reconcile the MCP server Service
	if err := r.reconcileMCPServerService(ctx, capability); err != nil {
		return false, fmt.Errorf("failed to reconcile MCP server service: %w", err)
	}

	// Check if the Deployment is ready
	dep := &appsv1.Deployment{}
	depName := resources.MCPServerDeploymentName(capability)
	if err := r.Get(ctx, types.NamespacedName{Name: depName, Namespace: capability.Namespace}, dep); err != nil {
		if errors.IsNotFound(err) {
			return false, nil // Not created yet
		}
		return false, err
	}

	ready := dep.Status.AvailableReplicas > 0
	if !ready {
		logger.V(1).Info("MCP server Deployment not ready yet",
			"capability", capability.Name,
			"deployment", depName,
			"availableReplicas", dep.Status.AvailableReplicas,
		)
	}
	return ready, nil
}

// reconcileMCPServerDeployment creates or updates the Deployment for the MCP server.
func (r *CapabilityReconciler) reconcileMCPServerDeployment(ctx context.Context, capability *agentsv1alpha1.Capability) error {
	desired := resources.MCPServerDeployment(capability)

	// Set owner reference so the Deployment is garbage-collected when the Capability is deleted
	if err := controllerutil.SetControllerReference(capability, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on MCP server deployment: %w", err)
	}

	// Check if the Deployment already exists
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Info("Creating MCP server Deployment", "name", desired.Name)
			return r.Create(ctx, desired)
		}
		return err
	}

	// Compare spec hashes to avoid spurious updates (same pattern as agent_controller.go)
	existingHash := existing.Annotations[resources.DesiredSpecHashAnnotation]
	desiredHash := desired.Annotations[resources.DesiredSpecHashAnnotation]
	if existingHash == desiredHash {
		return nil // No changes needed
	}

	// Update the Deployment
	log.FromContext(ctx).Info("Updating MCP server Deployment", "name", desired.Name)
	existing.Spec = desired.Spec
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	existing.Annotations[resources.DesiredSpecHashAnnotation] = desiredHash
	return r.Update(ctx, existing)
}

// reconcileMCPServerService creates or updates the Service for the MCP server.
func (r *CapabilityReconciler) reconcileMCPServerService(ctx context.Context, capability *agentsv1alpha1.Capability) error {
	desired := resources.MCPServerService(capability)

	// Set owner reference
	if err := controllerutil.SetControllerReference(capability, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on MCP server service: %w", err)
	}

	// Check if the Service already exists
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Info("Creating MCP server Service", "name", desired.Name)
			return r.Create(ctx, desired)
		}
		return err
	}

	// Update the Service spec (preserve ClusterIP)
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, existing)
}

// =============================================================================
// VALIDATION
// =============================================================================

// validateCapability performs validation on the Capability spec
func (r *CapabilityReconciler) validateCapability(capability *agentsv1alpha1.Capability) error {
	// Description is required for all types
	if capability.Spec.Description == "" {
		return fmt.Errorf("spec.description is required")
	}

	// Type-specific validation
	switch capability.Spec.Type {
	case agentsv1alpha1.CapabilityTypeContainer:
		return r.validateContainerCapability(capability)
	case agentsv1alpha1.CapabilityTypeMCP:
		return r.validateMCPCapability(capability)
	case agentsv1alpha1.CapabilityTypeSkill:
		return r.validateSkillCapability(capability)
	case agentsv1alpha1.CapabilityTypeTool:
		return r.validateToolCapability(capability)
	case agentsv1alpha1.CapabilityTypePlugin:
		return r.validatePluginCapability(capability)
	default:
		return fmt.Errorf("unknown capability type: %s", capability.Spec.Type)
	}
}

func (r *CapabilityReconciler) validateContainerCapability(capability *agentsv1alpha1.Capability) error {
	if capability.Spec.Container == nil {
		return fmt.Errorf("spec.container is required when type is Container")
	}
	if capability.Spec.Container.Image == "" {
		return fmt.Errorf("spec.container.image is required")
	}

	// Validate permissions patterns compile (basic syntax check)
	if capability.Spec.Permissions != nil {
		for _, pattern := range capability.Spec.Permissions.Allow {
			if pattern == "" {
				return fmt.Errorf("empty pattern in permissions.allow")
			}
		}
		for _, pattern := range capability.Spec.Permissions.Deny {
			if pattern == "" {
				return fmt.Errorf("empty pattern in permissions.deny")
			}
		}
		for i, rule := range capability.Spec.Permissions.Approve {
			if rule.Pattern == "" {
				return fmt.Errorf("empty pattern in permissions.approve[%d]", i)
			}
		}
	}

	return nil
}

func (r *CapabilityReconciler) validateMCPCapability(capability *agentsv1alpha1.Capability) error {
	if capability.Spec.MCP == nil {
		return fmt.Errorf("spec.mcp is required when type is MCP")
	}
	switch capability.Spec.MCP.Mode {
	case "local":
		if len(capability.Spec.MCP.Command) == 0 {
			return fmt.Errorf("spec.mcp.command is required when mode is local")
		}
	case "remote":
		if capability.Spec.MCP.URL == "" {
			return fmt.Errorf("spec.mcp.url is required when mode is remote")
		}
	case "server":
		if len(capability.Spec.MCP.Command) == 0 {
			return fmt.Errorf("spec.mcp.command is required when mode is server")
		}
		if capability.Spec.MCP.Server == nil {
			return fmt.Errorf("spec.mcp.server is required when mode is server")
		}
		if capability.Spec.MCP.Server.Image == "" {
			return fmt.Errorf("spec.mcp.server.image is required")
		}
	default:
		return fmt.Errorf("spec.mcp.mode must be 'local', 'remote', or 'server', got %q", capability.Spec.MCP.Mode)
	}
	return nil
}

func (r *CapabilityReconciler) validateSkillCapability(capability *agentsv1alpha1.Capability) error {
	if capability.Spec.Skill == nil {
		return fmt.Errorf("spec.skill is required when type is Skill")
	}
	if capability.Spec.Skill.Content == "" && capability.Spec.Skill.ConfigMapRef == nil && capability.Spec.Skill.OCIRef == nil {
		return fmt.Errorf("spec.skill.content, spec.skill.configMapRef, or spec.skill.ociRef is required")
	}
	if capability.Spec.Skill.OCIRef != nil {
		if err := validateOCIRef(capability.Spec.Skill.OCIRef); err != nil {
			return fmt.Errorf("spec.skill.ociRef: %w", err)
		}
	}
	return nil
}

func (r *CapabilityReconciler) validateToolCapability(capability *agentsv1alpha1.Capability) error {
	if capability.Spec.Tool == nil {
		return fmt.Errorf("spec.tool is required when type is Tool")
	}
	if capability.Spec.Tool.Code == "" && capability.Spec.Tool.ConfigMapRef == nil && capability.Spec.Tool.OCIRef == nil {
		return fmt.Errorf("spec.tool.code, spec.tool.configMapRef, or spec.tool.ociRef is required")
	}
	if capability.Spec.Tool.OCIRef != nil {
		if err := validateOCIRef(capability.Spec.Tool.OCIRef); err != nil {
			return fmt.Errorf("spec.tool.ociRef: %w", err)
		}
	}
	return nil
}

func (r *CapabilityReconciler) validatePluginCapability(capability *agentsv1alpha1.Capability) error {
	if capability.Spec.Plugin == nil {
		return fmt.Errorf("spec.plugin is required when type is Plugin")
	}
	if capability.Spec.Plugin.Code == "" && capability.Spec.Plugin.ConfigMapRef == nil && capability.Spec.Plugin.Package == "" && capability.Spec.Plugin.OCIRef == nil {
		return fmt.Errorf("spec.plugin.code, spec.plugin.configMapRef, spec.plugin.package, or spec.plugin.ociRef is required")
	}
	if capability.Spec.Plugin.OCIRef != nil {
		if err := validateOCIRef(capability.Spec.Plugin.OCIRef); err != nil {
			return fmt.Errorf("spec.plugin.ociRef: %w", err)
		}
	}
	return nil
}

// validateOCIRef validates the common OCIArtifactRef fields.
func validateOCIRef(ref *agentsv1alpha1.OCIArtifactRef) error {
	if ref.Ref == "" {
		return fmt.Errorf("ref is required")
	}
	// Basic OCI reference format validation: must contain at least one "/"
	if !strings.Contains(ref.Ref, "/") {
		return fmt.Errorf("ref %q is not a valid OCI reference (expected format: <registry>/<repository>:<tag>)", ref.Ref)
	}
	// Validate digest format if specified
	if ref.Digest != "" && !strings.Contains(ref.Digest, ":") {
		return fmt.Errorf("digest %q is not valid (expected format: <algorithm>:<hex>)", ref.Digest)
	}
	return nil
}

// =============================================================================
// AGENT LOOKUP
// =============================================================================

// findAgentsUsingCapability returns a list of agent names that reference this capability
func (r *CapabilityReconciler) findAgentsUsingCapability(ctx context.Context, capability *agentsv1alpha1.Capability) ([]string, error) {
	// List all agents in the same namespace
	agentList := &agentsv1alpha1.AgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(capability.Namespace)); err != nil {
		return nil, err
	}

	var usedBy []string
	for _, agent := range agentList.Items {
		for _, ref := range agent.Spec.CapabilityRefs {
			if ref.Name == capability.Name {
				usedBy = append(usedBy, agent.Name)
				break
			}
		}
	}

	return usedBy, nil
}

// =============================================================================
// STATUS UPDATES
// =============================================================================

// updateStatusReady updates the capability status to Ready
func (r *CapabilityReconciler) updateStatusReady(ctx context.Context, capability *agentsv1alpha1.Capability, usedBy []string) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.Capability{}
	if err := r.Get(ctx, types.NamespacedName{Name: capability.Name, Namespace: capability.Namespace}, current); err != nil {
		return err
	}

	// Only update if status actually changed
	statusChanged := current.Status.Phase != agentsv1alpha1.CapabilityPhaseReady ||
		!stringSlicesEqual(current.Status.UsedBy, usedBy)

	if !statusChanged {
		// Also check if conditions need updating
		readyCondition := meta.FindStatusCondition(current.Status.Conditions, "Ready")
		if readyCondition != nil && readyCondition.Status == metav1.ConditionTrue {
			return nil
		}
	}

	current.Status.Phase = agentsv1alpha1.CapabilityPhaseReady
	current.Status.UsedBy = usedBy

	// Set Ready condition
	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationPassed",
		Message:            "Capability is ready to be used by agents",
		LastTransitionTime: metav1.Now(),
	})

	return r.Status().Update(ctx, current)
}

// updateStatusPending updates the capability status to Pending with a reason message.
// Used for server-mode MCP capabilities while the Deployment is rolling out.
func (r *CapabilityReconciler) updateStatusPending(ctx context.Context, capability *agentsv1alpha1.Capability, message string) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.Capability{}
	if err := r.Get(ctx, types.NamespacedName{Name: capability.Name, Namespace: capability.Namespace}, current); err != nil {
		return err
	}

	// Skip update if already Pending with same message
	if current.Status.Phase == agentsv1alpha1.CapabilityPhasePending {
		readyCondition := meta.FindStatusCondition(current.Status.Conditions, "Ready")
		if readyCondition != nil && readyCondition.Message == message {
			return nil
		}
	}

	current.Status.Phase = agentsv1alpha1.CapabilityPhasePending

	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "MCPServerNotReady",
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	return r.Status().Update(ctx, current)
}

// updateStatusFailed updates the capability status to Failed
func (r *CapabilityReconciler) updateStatusFailed(ctx context.Context, capability *agentsv1alpha1.Capability, reason string) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.Capability{}
	if err := r.Get(ctx, types.NamespacedName{Name: capability.Name, Namespace: capability.Namespace}, current); err != nil {
		return err
	}

	current.Status.Phase = agentsv1alpha1.CapabilityPhaseFailed

	// Set Ready condition to false
	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "ValidationFailed",
		Message:            reason,
		LastTransitionTime: metav1.Now(),
	})

	return r.Status().Update(ctx, current)
}

// =============================================================================
// CONTROLLER SETUP
// =============================================================================

func (r *CapabilityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.Capability{}).
		// Watch owned Deployments (MCP server pods) — triggers re-reconcile when
		// the Deployment becomes ready or changes status
		Owns(&appsv1.Deployment{}).
		// Watch owned Services (MCP server services)
		Owns(&corev1.Service{}).
		Complete(r)
}

// =============================================================================
// HELPERS
// =============================================================================

// stringSlicesEqual returns true if two string slices are equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
