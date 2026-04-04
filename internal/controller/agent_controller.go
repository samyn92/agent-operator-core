package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/resources"
	"github.com/samyn92/agent-operator-core/pkg/oci"
)

// AgentReconciler reconciles an Agent object
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agents.io,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.io,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.io,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=agents.io,resources=capabilities,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;configmaps;persistentvolumeclaims;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Agent instance
	agent := &agentsv1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Reconciling Agent", "name", req.Name, "namespace", req.Namespace)

	// Check if all referenced capabilities are available and ready.
	// Block deployment until they are — deploying without capabilities produces a broken
	// agent that has no tools and tries to shell out directly.
	// The Capability watch (SetupWithManager) will re-trigger reconciliation when capabilities
	// become available, so we don't need to requeue on a timer.
	if unready := r.checkCapabilityReadiness(ctx, agent); len(unready) > 0 {
		logger.Info("Agent blocked: waiting for capabilities", "unready", unready)
		if err := r.updateStatusWaiting(ctx, agent, unready); err != nil {
			logger.Error(err, "Failed to update status to Waiting")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Reconcile ConfigMap
	if err := r.reconcileConfigMap(ctx, agent); err != nil {
		logger.Error(err, "Failed to reconcile ConfigMap")
		return ctrl.Result{}, err
	}

	// Reconcile PVC (only if storage is configured)
	if agent.Spec.Storage != nil {
		if err := r.reconcilePVC(ctx, agent); err != nil {
			logger.Error(err, "Failed to reconcile PVC")
			return ctrl.Result{}, err
		}
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, agent); err != nil {
		logger.Error(err, "Failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, agent); err != nil {
		logger.Error(err, "Failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Reconcile NetworkPolicy (if enabled)
	if err := r.reconcileNetworkPolicy(ctx, agent); err != nil {
		logger.Error(err, "Failed to reconcile NetworkPolicy")
		return ctrl.Result{}, err
	}

	// Reconcile Capabilities (if any capabilityRefs defined)
	if err := r.reconcileCapabilities(ctx, agent); err != nil {
		logger.Error(err, "Failed to reconcile Capabilities")
		return ctrl.Result{}, err
	}

	// Update status
	if err := r.updateStatus(ctx, agent); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentReconciler) reconcileConfigMap(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	// Resolve capabilities to the formats expected by AgentConfigMap
	resolved, err := r.resolveCapabilities(ctx, agent)
	if err != nil {
		return fmt.Errorf("failed to resolve capabilities: %w", err)
	}

	desired := resources.AgentConfigMap(agent, resolved.Sources, resolved.MCPEntries, resolved.SkillFiles, resolved.ToolFiles, resolved.PluginFiles, resolved.PluginPackages)
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Only update if data changed
	if !mapsEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		return r.Update(ctx, existing)
	}
	return nil
}

// ResolvedCapabilities holds all resolved capability information for config/deployment generation.
type ResolvedCapabilities struct {
	// Sources are Container capabilities converted to SourceInfo (for tool wrappers)
	Sources []resources.SourceInfo
	// Sidecars are Container capabilities resolved for sidecar container generation
	Sidecars []resources.CapabilitySidecarInfo
	// MCPEntries are MCP capabilities converted to opencode.json MCP entries
	MCPEntries map[string]resources.MCPEntry
	// MCPWorkspaces are MCP server capabilities with shared workspace PVCs.
	// These PVCs need to be mounted in the agent pod for shared filesystem access.
	MCPWorkspaces []resources.MCPWorkspaceInfo
	// MCPCapabilityHash is a SHA256 hash of all referenced MCP capability specs.
	// When any MCP capability changes (command, image, env, workspace, permissions, etc.),
	// this hash changes, which updates the agent pod template annotation and triggers
	// a rolling restart. This is necessary because MCP servers run as separate pods —
	// unlike Container sidecars, MCP capability changes don't affect the agent pod template
	// directly, so without this hash the agent pod would keep running with stale MCP
	// connections (OpenCode only connects to MCP servers at startup).
	MCPCapabilityHash string
	// SkillFiles are Skill capabilities with their SKILL.md content (name -> content)
	SkillFiles map[string]string
	// ToolFiles are Tool capabilities with their TypeScript code (name -> code)
	ToolFiles map[string]string
	// PluginFiles are Plugin capabilities with inline code (name -> code)
	PluginFiles map[string]string
	// PluginPackages are Plugin capabilities referencing npm packages
	PluginPackages []string
}

// resolveCapabilities resolves all Agent capabilityRefs into their respective types.
// Returns a ResolvedCapabilities struct with all information needed for config and deployment generation.
func (r *AgentReconciler) resolveCapabilities(ctx context.Context, agent *agentsv1alpha1.Agent) (*ResolvedCapabilities, error) {
	logger := log.FromContext(ctx)
	resolved := &ResolvedCapabilities{
		MCPEntries:  make(map[string]resources.MCPEntry),
		SkillFiles:  make(map[string]string),
		ToolFiles:   make(map[string]string),
		PluginFiles: make(map[string]string),
	}

	// Port counter for Container sidecars - starts at 8081
	nextPort := int32(resources.SidecarBasePort)

	// Collect MCP capability specs for hashing. When any MCP capability changes,
	// the hash changes and triggers an agent pod rollout — necessary because MCP
	// servers are separate pods and OpenCode only connects at startup.
	var mcpSpecs []agentsv1alpha1.MCPCapabilitySpec

	for _, ref := range agent.Spec.CapabilityRefs {
		capability := &agentsv1alpha1.Capability{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: agent.Namespace}, capability)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to get capability %s: %w", ref.Name, err)
		}

		// Check if capability is ready
		if capability.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
			continue
		}

		// Determine name
		name := capability.Name
		if ref.Alias != "" {
			name = ref.Alias
		}

		switch capability.Spec.Type {
		case agentsv1alpha1.CapabilityTypeContainer:
			port := nextPort
			nextPort++

			resolved.Sidecars = append(resolved.Sidecars, resources.CapabilitySidecarInfo{
				Name:          name,
				Capability:    capability,
				Port:          port,
				ConfigMapName: agent.Name + "-" + name + "-config",
			})

			sourceInfo := resources.ContainerCapabilityToSourceInfo(agent, capability, ref.Alias, port)
			resolved.Sources = append(resolved.Sources, sourceInfo)

		case agentsv1alpha1.CapabilityTypeMCP:
			entry := resources.MCPCapabilityToMCPEntry(capability)
			if entry != nil {
				resolved.MCPEntries[name] = *entry
			}
			// Track workspace PVCs for server-mode MCP capabilities.
			// These need to be mounted in the agent pod for shared filesystem access.
			if resources.MCPServerHasWorkspace(capability) {
				mountPath := "/data/workspace"
				if capability.Spec.MCP.Server.Workspace.MountPath != "" {
					mountPath = capability.Spec.MCP.Server.Workspace.MountPath
				}
				resolved.MCPWorkspaces = append(resolved.MCPWorkspaces, resources.MCPWorkspaceInfo{
					PVCName:   resources.MCPServerWorkspacePVCName(capability),
					MountPath: mountPath,
				})
			}
			// Collect the full MCP spec for hash computation.
			if capability.Spec.MCP != nil {
				mcpSpecs = append(mcpSpecs, *capability.Spec.MCP)
			}

		case agentsv1alpha1.CapabilityTypeSkill:
			if capability.Spec.Skill != nil {
				content := capability.Spec.Skill.Content
				if content == "" && capability.Spec.Skill.ConfigMapRef != nil {
					// Resolve from ConfigMap
					cm := &corev1.ConfigMap{}
					cmRef := capability.Spec.Skill.ConfigMapRef
					if err := r.Get(ctx, types.NamespacedName{Name: cmRef.Name, Namespace: agent.Namespace}, cm); err == nil {
						content = cm.Data[cmRef.Key]
					}
				}
				if content == "" && capability.Spec.Skill.OCIRef != nil {
					// Resolve from OCI artifact
					var err error
					content, err = r.resolveOCISkillContent(ctx, agent.Namespace, capability.Spec.Skill.OCIRef)
					if err != nil {
						logger.Error(err, "Failed to pull skill from OCI artifact", "capability", capability.Name, "ref", capability.Spec.Skill.OCIRef.Ref)
						continue
					}
				}
				if content != "" {
					resolved.SkillFiles[name] = content
				}
			}

		case agentsv1alpha1.CapabilityTypeTool:
			if capability.Spec.Tool != nil {
				code := capability.Spec.Tool.Code
				if code == "" && capability.Spec.Tool.ConfigMapRef != nil {
					cm := &corev1.ConfigMap{}
					cmRef := capability.Spec.Tool.ConfigMapRef
					if err := r.Get(ctx, types.NamespacedName{Name: cmRef.Name, Namespace: agent.Namespace}, cm); err == nil {
						code = cm.Data[cmRef.Key]
					}
				}
				if code == "" && capability.Spec.Tool.OCIRef != nil {
					// Resolve from OCI artifact
					var err error
					code, err = r.resolveOCIFileContent(ctx, agent.Namespace, capability.Spec.Tool.OCIRef)
					if err != nil {
						logger.Error(err, "Failed to pull tool from OCI artifact", "capability", capability.Name, "ref", capability.Spec.Tool.OCIRef.Ref)
						continue
					}
				}
				if code != "" {
					resolved.ToolFiles[name] = code
				}
			}

		case agentsv1alpha1.CapabilityTypePlugin:
			if capability.Spec.Plugin != nil {
				if capability.Spec.Plugin.Package != "" {
					resolved.PluginPackages = append(resolved.PluginPackages, capability.Spec.Plugin.Package)
				} else {
					code := capability.Spec.Plugin.Code
					if code == "" && capability.Spec.Plugin.ConfigMapRef != nil {
						cm := &corev1.ConfigMap{}
						cmRef := capability.Spec.Plugin.ConfigMapRef
						if err := r.Get(ctx, types.NamespacedName{Name: cmRef.Name, Namespace: agent.Namespace}, cm); err == nil {
							code = cm.Data[cmRef.Key]
						}
					}
					if code == "" && capability.Spec.Plugin.OCIRef != nil {
						// Resolve from OCI artifact
						var err error
						code, err = r.resolveOCIFileContent(ctx, agent.Namespace, capability.Spec.Plugin.OCIRef)
						if err != nil {
							logger.Error(err, "Failed to pull plugin from OCI artifact", "capability", capability.Name, "ref", capability.Spec.Plugin.OCIRef.Ref)
							continue
						}
					}
					if code != "" {
						resolved.PluginFiles[name] = code
					}
				}
			}
		}
	}

	// Compute a deterministic hash of all MCP capability specs.
	// This hash is added as a pod template annotation so that any change to an MCP
	// capability (command, image, env vars, workspace config, etc.) triggers a rolling
	// restart of the agent pod. Without this, the agent would keep stale MCP connections
	// because OpenCode only connects to MCP servers at startup.
	if len(mcpSpecs) > 0 {
		data, err := json.Marshal(mcpSpecs)
		if err == nil {
			hash := sha256.Sum256(data)
			resolved.MCPCapabilityHash = hex.EncodeToString(hash[:])
		}
	}

	return resolved, nil
}

// resolveOCISkillContent pulls a Skill from an OCI artifact and returns its SKILL.md content.
func (r *AgentReconciler) resolveOCISkillContent(ctx context.Context, namespace string, ociRef *agentsv1alpha1.OCIArtifactRef) (string, error) {
	// Verify signature before pulling content, if verification is configured
	if err := verifyOCIArtifactRef(ctx, r.Client, namespace, ociRef); err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}

	opts, err := r.buildOCIPullOptions(ctx, namespace, ociRef)
	if err != nil {
		return "", err
	}

	client := oci.NewClient()
	return client.PullSkillContent(ctx, opts)
}

// resolveOCIFileContent pulls a Tool or Plugin from an OCI artifact and returns its file content.
func (r *AgentReconciler) resolveOCIFileContent(ctx context.Context, namespace string, ociRef *agentsv1alpha1.OCIArtifactRef) (string, error) {
	// Verify signature before pulling content, if verification is configured
	if err := verifyOCIArtifactRef(ctx, r.Client, namespace, ociRef); err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}

	opts, err := r.buildOCIPullOptions(ctx, namespace, ociRef)
	if err != nil {
		return "", err
	}

	client := oci.NewClient()
	return client.PullFileContent(ctx, opts)
}

// buildOCIPullOptions constructs PullOptions from an OCIArtifactRef, resolving credentials from Kubernetes Secrets.
func (r *AgentReconciler) buildOCIPullOptions(ctx context.Context, namespace string, ociRef *agentsv1alpha1.OCIArtifactRef) (oci.PullOptions, error) {
	opts := oci.PullOptions{
		Ref:    ociRef.Ref,
		Digest: ociRef.Digest,
	}

	// Resolve pull secret credentials if specified
	if ociRef.PullSecret != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ociRef.PullSecret.Name,
			Namespace: namespace,
		}, secret); err != nil {
			return opts, fmt.Errorf("failed to get pull secret %s: %w", ociRef.PullSecret.Name, err)
		}

		// Try to extract credentials from the secret.
		// Support both kubernetes.io/dockerconfigjson format and plain username/password keys.
		creds, err := extractRegistryCredentials(secret, ociRef.PullSecret.Key)
		if err != nil {
			return opts, fmt.Errorf("failed to extract credentials from secret %s: %w", ociRef.PullSecret.Name, err)
		}
		opts.Credentials = creds
	}

	return opts, nil
}

// extractRegistryCredentials extracts registry credentials from a Kubernetes Secret.
// Supports:
//   - kubernetes.io/dockerconfigjson secrets (reads .dockerconfigjson)
//   - Plain secrets with "username" and "password" keys
//   - A specific key (token-based auth: password-only)
func extractRegistryCredentials(secret *corev1.Secret, key string) (*oci.Credentials, error) {
	// If a specific key is provided, use it as a token/password
	if key != "" {
		if val, ok := secret.Data[key]; ok {
			return &oci.Credentials{Password: string(val)}, nil
		}
		return nil, fmt.Errorf("key %q not found in secret", key)
	}

	// Try dockerconfigjson format
	if dockerConfig, ok := secret.Data[".dockerconfigjson"]; ok {
		return parseDockerConfigJSON(dockerConfig)
	}

	// Try plain username/password
	username := string(secret.Data["username"])
	password := string(secret.Data["password"])
	if password != "" {
		return &oci.Credentials{Username: username, Password: password}, nil
	}

	return nil, fmt.Errorf("no recognizable credentials found in secret (expected .dockerconfigjson, or username/password keys)")
}

// dockerConfigJSON represents the structure of a .dockerconfigjson file.
type dockerConfigJSON struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

type dockerAuthEntry struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

// parseDockerConfigJSON extracts credentials from a .dockerconfigjson formatted secret.
// Returns the first set of credentials found (registries are typically single-entry for pull secrets).
func parseDockerConfigJSON(data []byte) (*oci.Credentials, error) {
	var config dockerConfigJSON
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing dockerconfigjson: %w", err)
	}

	for _, entry := range config.Auths {
		if entry.Username != "" || entry.Password != "" {
			return &oci.Credentials{
				Username: entry.Username,
				Password: entry.Password,
			}, nil
		}
	}

	return nil, fmt.Errorf("no credentials found in dockerconfigjson")
}

// checkCapabilityReadiness checks whether all referenced capabilities exist and are Ready.
// Returns a list of human-readable reasons for each capability that is not ready.
// An empty slice means all capabilities are satisfied.
func (r *AgentReconciler) checkCapabilityReadiness(ctx context.Context, agent *agentsv1alpha1.Agent) []string {
	var unready []string

	for _, ref := range agent.Spec.CapabilityRefs {
		capability := &agentsv1alpha1.Capability{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: agent.Namespace}, capability)
		if err != nil {
			if errors.IsNotFound(err) {
				unready = append(unready, fmt.Sprintf("Capability %q not found", ref.Name))
			} else {
				unready = append(unready, fmt.Sprintf("Capability %q: lookup error: %v", ref.Name, err))
			}
			continue
		}
		if capability.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
			phase := string(capability.Status.Phase)
			if phase == "" {
				phase = "unknown"
			}
			unready = append(unready, fmt.Sprintf("Capability %q is %s (not Ready)", ref.Name, phase))
		}
	}

	return unready
}

// updateStatusWaiting sets the agent phase to Waiting and records which capabilities
// are preventing deployment. Called when one or more capabilityRefs cannot be resolved.
func (r *AgentReconciler) updateStatusWaiting(ctx context.Context, agent *agentsv1alpha1.Agent, reasons []string) error {
	current := &agentsv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, current); err != nil {
		return err
	}

	message := fmt.Sprintf("Waiting for capabilities: %s", strings.Join(reasons, "; "))

	// Only update if status actually changed
	if current.Status.Phase == agentsv1alpha1.AgentPhasePending {
		// Check if the message is the same — avoid unnecessary writes
		existing := meta.FindStatusCondition(current.Status.Conditions, "CapabilitiesReady")
		if existing != nil && existing.Message == message {
			return nil
		}
	}

	current.Status.Phase = agentsv1alpha1.AgentPhasePending
	current.Status.ReadyReplicas = 0

	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               "CapabilitiesReady",
		Status:             metav1.ConditionFalse,
		Reason:             "CapabilitiesNotReady",
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	return r.Status().Update(ctx, current)
}

// reconcileCapabilities creates ConfigMaps for each Container capability (for sidecar containers)
// NOTE: Container capabilities run as sidecars in the Agent pod, not separate deployments
func (r *AgentReconciler) reconcileCapabilities(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	for _, ref := range agent.Spec.CapabilityRefs {
		// Fetch the Capability resource
		capability := &agentsv1alpha1.Capability{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: agent.Namespace}, capability)
		if err != nil {
			if errors.IsNotFound(err) {
				log.FromContext(ctx).Info("Capability not found, skipping", "capability", ref.Name)
				continue
			}
			return fmt.Errorf("failed to get capability %s: %w", ref.Name, err)
		}

		// Check if capability is ready
		if capability.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
			log.FromContext(ctx).Info("Capability not ready, skipping", "capability", ref.Name, "phase", capability.Status.Phase)
			continue
		}

		// Only Container capabilities need a ConfigMap for their sidecar
		if capability.Spec.Type == agentsv1alpha1.CapabilityTypeContainer {
			if err := r.reconcileCapabilityConfigMap(ctx, agent, capability, ref.Alias); err != nil {
				return fmt.Errorf("failed to reconcile capability %s configmap: %w", ref.Name, err)
			}
		}
	}

	return nil
}

func (r *AgentReconciler) reconcileCapabilityConfigMap(ctx context.Context, agent *agentsv1alpha1.Agent, capability *agentsv1alpha1.Capability, alias string) error {
	desired := resources.CapabilityConfigMap(agent, capability, alias)
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Only update if data changed
	if !mapsEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		return r.Update(ctx, existing)
	}
	return nil
}

func (r *AgentReconciler) reconcilePVC(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	desired := resources.AgentPVC(agent)
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}

	// PVCs are immutable after creation, so just create if not exists
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	return err
}

func (r *AgentReconciler) reconcileDeployment(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	// Resolve capabilities to get sidecar info and other resolved data
	resolved, err := r.resolveCapabilities(ctx, agent)
	if err != nil {
		return fmt.Errorf("failed to resolve capabilities for deployment: %w", err)
	}

	// Compute ConfigMap hash from the DESIRED data (not from the API server).
	// This avoids a race condition: on the first reconcile, the ConfigMap may not
	// yet exist in the API server, producing an empty hash. On the second reconcile
	// (triggered by the ConfigMap creation event), the hash changes, which changes
	// the pod template annotation, which changes the DeploymentSpec hash, causing
	// a spurious Deployment update and double rollout.
	desiredConfigMap := resources.AgentConfigMap(agent, resolved.Sources, resolved.MCPEntries, resolved.SkillFiles, resolved.ToolFiles, resolved.PluginFiles, resolved.PluginPackages)
	configMapHash := resources.HashConfigMapData(desiredConfigMap.Data)

	desired := resources.AgentDeployment(agent, configMapHash, resolved.MCPCapabilityHash, resolved.Sidecars, resolved.MCPWorkspaces)
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Compare the desired spec hash to detect actual changes.
	// This avoids spurious updates caused by Kubernetes adding server-side defaults
	// (terminationGracePeriodSeconds, dnsPolicy, schedulerName, etc.) to the existing
	// object, which would make reflect.DeepEqual on the full PodSpec always return false.
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

func (r *AgentReconciler) reconcileService(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	desired := resources.AgentService(agent)
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
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

func (r *AgentReconciler) reconcileNetworkPolicy(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	// Get source network info for network policy
	sourceNetwork := r.resolveSourceNetwork(agent)

	desired := resources.AgentNetworkPolicy(agent, sourceNetwork)

	// If NetworkPolicy is not enabled, delete any existing one
	if desired == nil {
		existing := &networkingv1.NetworkPolicy{}
		err := r.Get(ctx, types.NamespacedName{Name: agent.Name + "-netpol", Namespace: agent.Namespace}, existing)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.Delete(ctx, existing)
	}

	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Only update if spec changed (use JSON hash to avoid false positives from server-side defaults)
	if hashJSON(existing.Spec) != hashJSON(desired.Spec) {
		existing.Spec = desired.Spec
		return r.Update(ctx, existing)
	}
	return nil
}

// resolveSourceNetwork returns network info for capabilities (used by network policy)
func (r *AgentReconciler) resolveSourceNetwork(agent *agentsv1alpha1.Agent) []resources.SourceNetworkInfo {
	var sources []resources.SourceNetworkInfo

	for _, ref := range agent.Spec.CapabilityRefs {
		toolName := ref.Name
		if ref.Alias != "" {
			toolName = ref.Alias
		}
		sources = append(sources, resources.SourceNetworkInfo{
			Name:      toolName,
			Namespace: agent.Namespace,
			Port:      8080,
		})
	}

	return sources
}

func (r *AgentReconciler) updateStatus(ctx context.Context, agent *agentsv1alpha1.Agent) error {
	// Get fresh copy to avoid conflicts
	current := &agentsv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, current); err != nil {
		return err
	}

	// Calculate desired status
	newPhase := agentsv1alpha1.AgentPhasePending
	var readyReplicas int32

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, deployment)
	if err == nil {
		readyReplicas = deployment.Status.ReadyReplicas
		if readyReplicas > 0 {
			newPhase = agentsv1alpha1.AgentPhaseRunning
		}
	}

	// Calculate service URL
	serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:4096", agent.Name, agent.Namespace)

	// If we reach updateStatus, all capabilities passed the readiness check.
	// Mark CapabilitiesReady=True (clears any previous Waiting state).
	capabilitiesConditionChanged := false
	existing := meta.FindStatusCondition(current.Status.Conditions, "CapabilitiesReady")
	if existing == nil || existing.Status != metav1.ConditionTrue {
		capabilitiesConditionChanged = true
	}

	// Only update if status actually changed
	if current.Status.Phase != newPhase ||
		current.Status.ServiceURL != serviceURL ||
		current.Status.ReadyReplicas != readyReplicas ||
		capabilitiesConditionChanged {
		current.Status.Phase = newPhase
		current.Status.ServiceURL = serviceURL
		current.Status.ReadyReplicas = readyReplicas

		if capabilitiesConditionChanged {
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
				Type:               "CapabilitiesReady",
				Status:             metav1.ConditionTrue,
				Reason:             "AllCapabilitiesReady",
				Message:            "All referenced capabilities are available and ready",
				LastTransitionTime: metav1.Now(),
			})
		}

		return r.Status().Update(ctx, current)
	}

	return nil
}

// mapsEqual compares two string maps for equality without reflect.DeepEqual.
// Returns true if both maps have exactly the same keys and values.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || v != bv {
			return false
		}
	}
	return true
}

// servicePortsEqual compares two ServicePort slices for equality.
// It compares the operator-controlled fields (Name, Port, TargetPort, Protocol)
// and ignores server-assigned fields like NodePort.
func servicePortsEqual(a, b []corev1.ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Port != b[i].Port ||
			a[i].TargetPort.String() != b[i].TargetPort.String() ||
			a[i].Protocol != b[i].Protocol {
			return false
		}
	}
	return true
}

// hashJSON returns a hex-encoded SHA256 hash of the JSON-serialized value.
// Used to compare Kubernetes objects without reflect.DeepEqual, which can
// produce false positives when server-side defaults differ from the desired spec.
func hashJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "error"
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&networkingv1.NetworkPolicy{}).
		// Watch Capabilities - when a Capability changes, re-reconcile Agents that reference it
		Watches(
			&agentsv1alpha1.Capability{},
			handler.EnqueueRequestsFromMapFunc(r.findAgentsForCapability),
		).
		Complete(r)
}

// findAgentsForCapability returns reconcile requests for Agents that reference the given Capability
func (r *AgentReconciler) findAgentsForCapability(ctx context.Context, obj client.Object) []ctrl.Request {
	capability, ok := obj.(*agentsv1alpha1.Capability)
	if !ok {
		return nil
	}

	// List all agents in the same namespace
	agentList := &agentsv1alpha1.AgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(capability.Namespace)); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, agent := range agentList.Items {
		// If this agent references the changed capability, queue it for reconciliation
		for _, ref := range agent.Spec.CapabilityRefs {
			if ref.Name == capability.Name {
				requests = append(requests, ctrl.Request{
					NamespacedName: types.NamespacedName{
						Name:      agent.Name,
						Namespace: agent.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}
