package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/pkg/oci"
)

// PiAgentReconciler reconciles a PiAgent object.
//
// Unlike AgentReconciler, this controller does NOT create Deployments, Services,
// PVCs, or ConfigMaps. PiAgent is a definition-only CRD — it becomes a Job only
// when invoked by a WorkflowRun. The controller's sole responsibility is to
// validate the PiAgent spec and set the status to Ready/Failed.
type PiAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agents.io,resources=piagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=piagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=piagents/finalizers,verbs=update

func (r *PiAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the PiAgent instance
	piAgent := &agentsv1alpha1.PiAgent{}
	if err := r.Get(ctx, req.NamespacedName, piAgent); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Reconciling PiAgent", "name", req.Name, "namespace", req.Namespace)

	// Validate source: exactly one of oci, inline, or configMapRef must be set
	if err := r.validateSource(ctx, piAgent); err != nil {
		logger.Error(err, "Source validation failed")
		if statusErr := r.setStatus(ctx, piAgent, agentsv1alpha1.PiAgentPhaseFailed, "SourceInvalid", err.Error()); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil // Don't requeue — user must fix the spec
	}

	// Validate provider references: secrets must exist
	if err := r.validateProviders(ctx, piAgent); err != nil {
		logger.Error(err, "Provider validation failed")
		if statusErr := r.setStatus(ctx, piAgent, agentsv1alpha1.PiAgentPhaseFailed, "ProviderInvalid", err.Error()); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}

	// Validate model references a configured provider
	if err := r.validateModel(piAgent); err != nil {
		if statusErr := r.setStatus(ctx, piAgent, agentsv1alpha1.PiAgentPhaseFailed, "ModelInvalid", err.Error()); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}

	// Validate toolRefs: OCI refs must be non-empty, signature verification if configured
	if err := r.validateToolRefs(ctx, piAgent); err != nil {
		logger.Error(err, "ToolRef validation failed")
		if statusErr := r.setStatus(ctx, piAgent, agentsv1alpha1.PiAgentPhaseFailed, "ToolRefInvalid", err.Error()); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}

	// All validations passed — set Ready
	if err := r.setStatus(ctx, piAgent, agentsv1alpha1.PiAgentPhaseReady, "Validated", "Source resolved, provider secrets verified"); err != nil {
		logger.Error(err, "Failed to update status to Ready")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateSource ensures exactly one source is set and the referenced source is resolvable.
func (r *PiAgentReconciler) validateSource(ctx context.Context, piAgent *agentsv1alpha1.PiAgent) error {
	source := piAgent.Spec.Source
	setCount := 0

	if source.OCI != nil {
		setCount++
	}
	if source.Inline != "" {
		setCount++
	}
	if source.ConfigMapRef != nil {
		setCount++
	}

	if setCount == 0 {
		return fmt.Errorf("exactly one of source.oci, source.inline, or source.configMapRef must be set")
	}
	if setCount > 1 {
		return fmt.Errorf("only one of source.oci, source.inline, or source.configMapRef may be set, got %d", setCount)
	}

	// Validate OCI artifact is reachable (if set)
	if source.OCI != nil {
		if source.OCI.Ref == "" {
			return fmt.Errorf("source.oci.ref must not be empty")
		}
		// Verify signature if verification is configured
		if source.OCI.Verify != nil {
			if err := r.verifyOCIArtifact(ctx, piAgent.Namespace, source.OCI); err != nil {
				return fmt.Errorf("OCI artifact signature verification failed for %s: %w", source.OCI.Ref, err)
			}
		}
	}

	// Validate ConfigMap reference exists (if set)
	if source.ConfigMapRef != nil {
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      source.ConfigMapRef.Name,
			Namespace: piAgent.Namespace,
		}, cm); err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("source ConfigMap %q not found in namespace %s", source.ConfigMapRef.Name, piAgent.Namespace)
			}
			return fmt.Errorf("failed to get source ConfigMap %q: %w", source.ConfigMapRef.Name, err)
		}
		if _, ok := cm.Data[source.ConfigMapRef.Key]; !ok {
			return fmt.Errorf("key %q not found in ConfigMap %q", source.ConfigMapRef.Key, source.ConfigMapRef.Name)
		}
	}

	return nil
}

// validateProviders ensures all provider API key secrets exist.
func (r *PiAgentReconciler) validateProviders(ctx context.Context, piAgent *agentsv1alpha1.PiAgent) error {
	for _, provider := range piAgent.Spec.Providers {
		if provider.APIKeySecret == nil {
			continue // API key is optional for local providers (e.g., ollama)
		}
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      provider.APIKeySecret.Name,
			Namespace: piAgent.Namespace,
		}, secret); err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("provider %q: API key secret %q not found in namespace %s",
					provider.Name, provider.APIKeySecret.Name, piAgent.Namespace)
			}
			return fmt.Errorf("provider %q: failed to get API key secret %q: %w",
				provider.Name, provider.APIKeySecret.Name, err)
		}
		if _, ok := secret.Data[provider.APIKeySecret.Key]; !ok {
			return fmt.Errorf("provider %q: key %q not found in secret %q",
				provider.Name, provider.APIKeySecret.Key, provider.APIKeySecret.Name)
		}
	}
	return nil
}

// validateModel ensures the model string references a provider in the providers list.
func (r *PiAgentReconciler) validateModel(piAgent *agentsv1alpha1.PiAgent) error {
	model := piAgent.Spec.Model
	// Model format is "provider/model" — extract the provider portion
	slashIdx := -1
	for i, c := range model {
		if c == '/' {
			slashIdx = i
			break
		}
	}
	if slashIdx <= 0 {
		return fmt.Errorf("model %q must be in 'provider/model' format", model)
	}
	providerName := model[:slashIdx]

	for _, p := range piAgent.Spec.Providers {
		if p.Name == providerName {
			return nil
		}
	}
	return fmt.Errorf("model references provider %q which is not in the providers list", providerName)
}

// validateToolRefs ensures all toolRef OCI references are valid and optionally verifies signatures.
func (r *PiAgentReconciler) validateToolRefs(ctx context.Context, piAgent *agentsv1alpha1.PiAgent) error {
	for i, toolRef := range piAgent.Spec.ToolRefs {
		if toolRef.Ref == "" {
			return fmt.Errorf("toolRefs[%d].ref must not be empty", i)
		}
		if toolRef.Verify != nil {
			if err := r.verifyOCIArtifact(ctx, piAgent.Namespace, &piAgent.Spec.ToolRefs[i]); err != nil {
				return fmt.Errorf("toolRefs[%d] (%s) signature verification failed: %w", i, toolRef.Ref, err)
			}
		}
	}
	return nil
}

// verifyOCIArtifact performs Cosign signature verification if configured.
// Reuses the same verification patterns as AgentReconciler.
func (r *PiAgentReconciler) verifyOCIArtifact(ctx context.Context, namespace string, ociRef *agentsv1alpha1.OCIArtifactRef) error {
	if ociRef.Verify == nil {
		return nil
	}

	verifier, err := oci.NewVerifier()
	if err != nil {
		return fmt.Errorf("cosign not available: %w", err)
	}

	verifyOpts := oci.VerifyOptions{
		Ref: ociRef.Ref,
	}

	if ociRef.Verify.PublicKey != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ociRef.Verify.PublicKey.Name,
			Namespace: namespace,
		}, secret); err != nil {
			return fmt.Errorf("failed to get cosign public key secret %s: %w", ociRef.Verify.PublicKey.Name, err)
		}
		key := ociRef.Verify.PublicKey.Key
		if key == "" {
			key = "cosign.pub"
		}
		pubKeyData, ok := secret.Data[key]
		if !ok {
			return fmt.Errorf("key %q not found in cosign public key secret %s", key, ociRef.Verify.PublicKey.Name)
		}
		verifyOpts.PublicKey = string(pubKeyData)
	}

	if ociRef.Verify.Keyless != nil {
		verifyOpts.Keyless = &oci.KeylessVerifyOptions{
			Issuer:   ociRef.Verify.Keyless.Issuer,
			Identity: ociRef.Verify.Keyless.Identity,
		}
	}

	return verifier.Verify(ctx, verifyOpts)
}

// setStatus updates the PiAgent status with the given phase and condition.
func (r *PiAgentReconciler) setStatus(ctx context.Context, piAgent *agentsv1alpha1.PiAgent, phase agentsv1alpha1.PiAgentPhase, reason, message string) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.PiAgent{}
	if err := r.Get(ctx, types.NamespacedName{Name: piAgent.Name, Namespace: piAgent.Namespace}, current); err != nil {
		return err
	}

	conditionStatus := metav1.ConditionTrue
	if phase != agentsv1alpha1.PiAgentPhaseReady {
		conditionStatus = metav1.ConditionFalse
	}

	// Check if update is needed
	existingCondition := meta.FindStatusCondition(current.Status.Conditions, "Ready")
	if current.Status.Phase == phase &&
		existingCondition != nil &&
		existingCondition.Status == conditionStatus &&
		existingCondition.Reason == reason &&
		existingCondition.Message == message {
		return nil // No change needed
	}

	current.Status.Phase = phase
	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	return r.Status().Update(ctx, current)
}

func (r *PiAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.PiAgent{}).
		Complete(r)
}
