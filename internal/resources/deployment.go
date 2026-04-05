package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

const (
	// Default images - these should be overridden via spec.images on managed platforms
	// that enforce image registry restrictions (e.g. via proxy registries)
	DefaultOpencodeImage = "ghcr.io/samyn92/opencode:latest"
	OpencodePort         = 4096
	// DesiredSpecHashAnnotation stores a hash of the operator's desired DeploymentSpec.
	// This avoids spurious updates caused by Kubernetes adding server-side defaults
	// (terminationGracePeriodSeconds, dnsPolicy, etc.) that make reflect.DeepEqual fail.
	DesiredSpecHashAnnotation = "agents.io/desired-spec-hash"
)

// CapabilitySidecarInfo is retained for backwards compatibility.
// Container sidecar deployment has been removed; this type is kept so existing
// references compile but is not used at runtime.
type CapabilitySidecarInfo struct {
	Name          string
	Capability    *agentsv1alpha1.Capability
	Port          int32
	ConfigMapName string
}

// MCPWorkspaceInfo holds workspace PVC information for MCP server capabilities
// that need shared filesystem access with the agent pod.
type MCPWorkspaceInfo struct {
	// PVCName is the name of the shared workspace PVC
	PVCName string
	// MountPath is the path where the workspace is mounted in the agent container.
	// Defaults to "/data/workspace".
	MountPath string
}

// GitWorkspaceInfo holds resolved GitWorkspace information needed for mounting
// workspace PVCs into the agent pod. Populated by the Agent controller after
// resolving workspaceRefs to GitWorkspace CRs.
type GitWorkspaceInfo struct {
	// PVCName is the name of the GitWorkspace PVC
	PVCName string
	// MountPath is where the workspace is mounted (e.g., /workspaces/api)
	MountPath string
	// ReadOnly indicates if the workspace is mounted read-only
	ReadOnly bool
}

// getImageConfig returns image settings from agent spec with defaults.
// When spec.images.init is not set, the opencode image is used for the init
// container. This enables the init container to copy pre-cached npm provider
// packages from the opencode image to the data volume, which is essential for
// airgapped environments.
func getImageConfig(agent *agentsv1alpha1.Agent) (opencodeImage, initImage string, pullPolicy corev1.PullPolicy) {
	opencodeImage = DefaultOpencodeImage
	pullPolicy = corev1.PullIfNotPresent

	if agent.Spec.Images != nil {
		if agent.Spec.Images.OpenCode != "" {
			opencodeImage = agent.Spec.Images.OpenCode
		}
		if agent.Spec.Images.PullPolicy != "" {
			pullPolicy = agent.Spec.Images.PullPolicy
		}
	}

	// Default init image to the opencode image unless explicitly overridden.
	// The opencode image is Alpine-based with sh and includes pre-cached
	// npm provider packages at /opt/opencode/node_modules/.
	initImage = opencodeImage
	if agent.Spec.Images != nil && agent.Spec.Images.Init != "" {
		initImage = agent.Spec.Images.Init
	}

	return
}

// ConfigMapHashAnnotation is the annotation key for the ConfigMap content hash
const ConfigMapHashAnnotation = "agents.io/configmap-hash"

// MCPCapabilityHashAnnotation is the annotation key for the MCP capability spec hash.
// When any MCP capability referenced by the agent changes (command, image, env, workspace,
// permissions, etc.), this hash changes, updating the pod template and triggering a
// rolling restart. This is necessary because MCP servers run as separate pods — unlike
// Container sidecars which modify the pod template directly, MCP capability changes
// are invisible to the agent pod without this annotation.
const MCPCapabilityHashAnnotation = "agents.io/mcp-capability-hash"

// AgentDeployment creates a Deployment for the agent.
// configMapHash is the SHA256 hash of the ConfigMap data; when it changes the pod
// template annotation changes, triggering a rolling restart so init-container
// generated files (plugins, skills) are regenerated from the updated ConfigMap.
//
// mcpCapabilityHash is a SHA256 hash of all referenced MCP capability specs. When
// any MCP capability changes, this hash changes, triggering a rolling restart so
// the agent reconnects to updated MCP servers (OpenCode only connects at startup).
//
// mcpWorkspaces contains PVC information for MCP server capabilities that need
// shared filesystem access. These PVCs are mounted into the agent pod so that both
// the agent and the MCP server pod have access to the same workspace files.
//
// gitWorkspaces contains PVC information for GitWorkspace CRs referenced by the
// agent's workspaceRefs. These RWX PVCs provide pre-cloned Git repositories with
// bare-clone + worktree architecture. Agents use them for code reading and editing.
func AgentDeployment(agent *agentsv1alpha1.Agent, configMapHash string, mcpCapabilityHash string, mcpWorkspaces []MCPWorkspaceInfo, gitWorkspaces []GitWorkspaceInfo) *appsv1.Deployment {
	labels := commonLabels(agent)
	replicas := int32(1)

	// Get image configuration
	opencodeImage, initImage, pullPolicy := getImageConfig(agent)

	// Build containers - opencode main container only (sidecars removed)
	containers := []corev1.Container{
		buildOpencodeContainer(agent, opencodeImage, pullPolicy),
	}

	// Build init container to set up config
	initContainers := []corev1.Container{
		buildInitContainer(initImage, pullPolicy),
	}

	// Build volumes
	volumes := buildVolumes(agent)

	// Add MCP workspace PVC volumes.
	// When an MCP server capability has workspace.enabled, both the MCP server pod
	// and the agent pod mount the same RWX PVC. The agent uses it as its working
	// directory so that files created by OpenCode are visible to the MCP server
	// (e.g., git MCP server can add/commit files the agent wrote).
	for i, ws := range mcpWorkspaces {
		volName := fmt.Sprintf("mcp-workspace-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: ws.PVCName,
				},
			},
		})
		// Mount in the opencode main container (first container).
		// The mount path is typically /data/workspace — the same path OpenCode uses
		// as its WorkingDir. When an agent has an MCP workspace, this PVC replaces
		// the default emptyDir/agent-PVC for the workspace subdirectory.
		containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: ws.MountPath,
		})
	}

	// Add GitWorkspace PVC volumes.
	// GitWorkspace CRs provide pre-cloned Git repositories via RWX PVCs managed by
	// standalone workspace Deployments. The agent mounts them to access code — reading
	// from the main/ worktree, creating branch worktrees under branches/, and pushing
	// changes. The workspace's sync pod handles fetch/cleanup independently.
	for i, gws := range gitWorkspaces {
		volName := fmt.Sprintf("git-workspace-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: gws.PVCName,
				},
			},
		})
		containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: gws.MountPath,
			ReadOnly:  gws.ReadOnly,
		})
	}

	// Resource requirements
	var resourceReqs corev1.ResourceRequirements
	if agent.Spec.Resources != nil {
		resourceReqs = *agent.Spec.Resources
	} else {
		resourceReqs = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	}
	containers[0].Resources = resourceReqs

	// Pod template annotations - include configmap hash to trigger rollout on config changes.
	// While opencode.json and AGENTS.md are symlinked and auto-update via kubelet
	// ConfigMap volume propagation (~60s), tool files (.ts), plugins, and skills are
	// COPIED by the init container and only refresh on pod restart. The hash annotation
	// ensures any Capability change (permissions, description, instructions) causes a
	// rolling restart so all files are regenerated.
	podAnnotations := map[string]string{}
	if configMapHash != "" {
		podAnnotations[ConfigMapHashAnnotation] = configMapHash
	}
	if mcpCapabilityHash != "" {
		podAnnotations[MCPCapabilityHashAnnotation] = mcpCapabilityHash
	}

	podSpec := corev1.PodSpec{
		InitContainers:               initContainers,
		Containers:                   containers,
		Volumes:                      volumes,
		AutomountServiceAccountToken: boolPtr(true),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: boolPtr(true),
			RunAsUser:    int64Ptr(1000),
			RunAsGroup:   int64Ptr(1000),
			FSGroup:      int64Ptr(1000),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
	}

	// When a PVC is attached (RWO block storage), the Deployment must use Recreate
	// strategy. RollingUpdate would keep the old pod running while the new one starts,
	// but RWO volumes can only be mounted by a single node — causing Multi-Attach errors
	// if the scheduler places the new pod on a different node.
	strategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
	}
	if agent.Spec.Storage != nil {
		strategy.Type = appsv1.RecreateDeploymentStrategyType
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: strategy,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: podAnnotations,
				},
				Spec: podSpec,
			},
		},
	}

	// Compute and store a hash of the desired spec as a Deployment annotation.
	// The reconciler compares this hash instead of using reflect.DeepEqual on the
	// PodSpec, which is fragile because Kubernetes mutates the existing object with
	// server-side defaults (terminationGracePeriodSeconds, dnsPolicy, etc.).
	specHash := HashDeploymentSpec(dep)
	dep.Annotations = map[string]string{
		DesiredSpecHashAnnotation: specHash,
	}

	return dep
}

func buildOpencodeContainer(agent *agentsv1alpha1.Agent, image string, pullPolicy corev1.PullPolicy) corev1.Container {
	envVars := []corev1.EnvVar{
		{Name: "HOME", Value: "/data"},
		{Name: "XDG_CONFIG_HOME", Value: "/data/.config"},
		// Telemetry plugin configuration
		// The plugin sends traces to the console backend
		{Name: "CONSOLE_TELEMETRY_URL", Value: "http://agent-console.agent-system.svc/api/v1/telemetry/spans"},
		{Name: "TELEMETRY_ENABLED", Value: "true"},
		// Force bun to fail fast instead of hanging when it can't reach the npm
		// registry. OpenCode runs `bun install` lazily on first API call; in
		// airgapped environments the registry is unreachable and bun hangs
		// indefinitely. Pointing to localhost:1 makes the connection refuse
		// instantly (~1ms). OpenCode continues normally after the failure because
		// the required packages are already pre-cached in node_modules by the
		// init container.
		{Name: "BUN_CONFIG_REGISTRY", Value: "http://localhost:1"},
	}

	// Add API keys for all providers that have an apiKeySecret.
	// Cloud providers (anthropic, openai, google) auto-enable when their key is present.
	// Custom providers reference the env var via {env:VAR} in their opencode.json config.
	// Local providers (ollama, lm-studio) without apiKeySecret are skipped.
	for _, p := range agent.Spec.Providers {
		if p.APIKeySecret == nil {
			continue
		}
		envName := strings.ToUpper(strings.ReplaceAll(p.Name, "-", "_")) + "_API_KEY"
		envVars = append(envVars, corev1.EnvVar{
			Name: envName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: p.APIKeySecret.Name,
					},
					Key: p.APIKeySecret.Key,
				},
			},
		})
	}

	// Note: GitHub/GitLab tokens are NOT injected here
	// Credentials belong in Tool CRDs, not in the Agent
	// This is a key security design principle of the operator

	// Build args - always include serve command and port/hostname.
	args := []string{"serve", "--port", "4096", "--hostname", "0.0.0.0"}

	// Add logging flags if configured
	if agent.Spec.Logging != nil {
		// Check if logging is enabled (default: true)
		if agent.Spec.Logging.Enabled == nil || *agent.Spec.Logging.Enabled {
			args = append(args, "--print-logs")
		}
		// Add log level if specified
		if agent.Spec.Logging.Level != "" {
			args = append(args, "--log-level", agent.Spec.Logging.Level)
		}
	}

	// Build volume mounts - start with default /data mount
	volumeMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: "/data"},
	}

	// Add any additional volume mounts from the spec
	if len(agent.Spec.AdditionalVolumeMounts) > 0 {
		volumeMounts = append(volumeMounts, agent.Spec.AdditionalVolumeMounts...)
	}

	container := corev1.Container{
		Name:            "opencode",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Args:            args,
		Ports: []corev1.ContainerPort{
			{Name: "http", ContainerPort: OpencodePort, Protocol: corev1.ProtocolTCP},
		},
		Env:             envVars,
		WorkingDir:      "/data/workspace",
		VolumeMounts:    volumeMounts,
		SecurityContext: hardenedSecurityContext(),
		// Health probes use exec+wget instead of HTTPGet because Kubernetes sends
		// HTTP probes to the pod IP, which on dual-stack/IPv6 clusters is an IPv6
		// address. OpenCode binds to 0.0.0.0 (IPv4 only), so HTTPGet probes to
		// [ipv6]:4096 get "connection refused". Exec probes run inside the container
		// and use 127.0.0.1 (localhost), bypassing the IPv6 routing issue.
		// wget is available in the Alpine-based OpenCode image.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"wget", "-q", "--spider", fmt.Sprintf("http://127.0.0.1:%d/global/health", OpencodePort)},
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"wget", "-q", "--spider", fmt.Sprintf("http://127.0.0.1:%d/global/health", OpencodePort)},
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
		},
	}

	// Inject envFrom sources (Secrets/ConfigMaps) into the main container.
	// Used by Plugin capabilities that read config from process.env.
	if len(agent.Spec.EnvFrom) > 0 {
		container.EnvFrom = agent.Spec.EnvFrom
	}

	return container
}

func buildInitContainer(image string, pullPolicy corev1.PullPolicy) corev1.Container {
	// Init container copies config from ConfigMap to the data volume with correct permissions
	// OpenCode resolves plugin paths relative to the config directory (/data/.config/opencode/)
	// So plugins go in /data/.config/opencode/.opencode/plugins/
	//
	// Custom tools are auto-discovered from {tool,tools}/*.ts relative to Config.directories()
	// which includes .opencode directories from the workspace up to worktree
	// So tools go in /data/workspace/.opencode/tools/ for auto-discovery
	//
	// Skills are loaded from .opencode/skills/<name>/SKILL.md
	// So skills go in /data/workspace/.opencode/skills/<name>/SKILL.md
	//
	// If the image contains pre-cached npm packages at /opt/opencode/
	// (as in the airgap-friendly opencode image), node_modules are copied to
	// both bun install directories on the data volume. The operator also sets
	// BUN_CONFIG_REGISTRY=http://localhost:1 on the main container so that
	// bun's runtime `bun install` fails fast (~1ms connection refused) instead
	// of hanging indefinitely when the npm registry is unreachable. OpenCode
	// continues normally after the failure because the packages are already
	// present in node_modules.
	//
	// Directory creation is split into separate mkdir calls so that a failure on
	// one path (e.g., workspace subdirs on some CSI drivers) doesn't prevent
	// creation of others. The workspace .opencode/ dirs use "|| true" because
	// OpenCode auto-creates them on first use if needed.
	return corev1.Container{
		Name:            "init-config",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		// Override the image's WORKDIR (/data/workspace) to prevent the CRI from
		// creating /data/workspace as root before the container starts. The PVC is
		// mounted at /data with fsGroup, so /data itself is group-writable, but
		// directories the CRI pre-creates for WORKDIR are owned by root with 0755.
		// By setting WorkingDir to /data, the init script's own mkdir creates
		// /data/workspace as UID 1000 with correct ownership.
		WorkingDir: "/data",
		Command:    []string{"/bin/sh", "-c"},
		Args: []string{
			// If /data/workspace exists but is owned by root (e.g., from CRI WORKDIR
			// creation on a previous pod revision), remove and recreate it. rmdir only
			// removes empty dirs, so existing workspace data is preserved safely.
			`rmdir /data/workspace 2>/dev/null; ` +
				`mkdir -p /data/.config/opencode /data/.cache/opencode /data/.local/share/opencode && ` +
				`mkdir -p /data/workspace && ` +
				`mkdir -p /data/.config/opencode/.opencode/plugins && ` +
				`mkdir -p /data/workspace/.opencode/tools /data/workspace/.opencode/skills || true; ` +
				// Copy opencode.json and AGENTS.md from the ConfigMap volume into
				// their expected locations. The /config volume is only mounted on
				// this init container, so symlinks would be broken in the main
				// container. Using cp ensures the files are available at runtime.
				`cp /config/opencode.json /data/.config/opencode/opencode.json && ` +
				`cp /config/AGENTS.md /data/workspace/AGENTS.md && ` +
				`cp /config/telemetry.ts /data/.config/opencode/.opencode/plugins/telemetry.ts; ` +
				`for f in /config/tool-*.ts; do [ -f "$f" ] && cp "$f" "/data/workspace/.opencode/tools/$(basename "$f" | sed 's/^tool-//')"; done; ` +
				`for f in /config/plugin-*.ts; do [ -f "$f" ] && cp "$f" "/data/.config/opencode/.opencode/plugins/$(basename "$f" | sed 's/^plugin-//')"; done; ` +
				`for f in /config/skill-*-SKILL.md; do [ -f "$f" ] && name="$(basename "$f" | sed 's/^skill-//;s/-SKILL\.md$//')" && mkdir -p "/data/workspace/.opencode/skills/$name" && cp "$f" "/data/workspace/.opencode/skills/$name/SKILL.md"; done; ` +
				`if [ -d /opt/opencode/node_modules ]; then ` +
				`for d in /data/.config/opencode /data/workspace/.opencode; do ` +
				`cp -r /opt/opencode/node_modules "$d/node_modules"; ` +
				`done; fi; ` +
				`echo "init-config complete"`,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: "/data"},
			{Name: "config", MountPath: "/config"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: hardenedSecurityContext(),
	}
}

func buildVolumes(agent *agentsv1alpha1.Agent) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agent.Name + "-config",
					},
				},
			},
		},
	}

	// Add PVC or emptyDir for data
	if agent.Spec.Storage != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: agent.Name + "-data",
				},
			},
		})
	} else {
		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Add any additional volumes from the spec
	if len(agent.Spec.AdditionalVolumes) > 0 {
		volumes = append(volumes, agent.Spec.AdditionalVolumes...)
	}

	return volumes
}

// HashDeploymentSpec computes a deterministic SHA256 hash of a Deployment's spec.
// The hash covers the full DeploymentSpec (template, replicas, selector, strategy).
// This is used to detect actual changes without relying on reflect.DeepEqual,
// which fails because Kubernetes adds server-side defaults to the existing object.
func HashDeploymentSpec(dep *appsv1.Deployment) string {
	data, err := json.Marshal(dep.Spec)
	if err != nil {
		// This should never happen with well-formed specs
		return "error"
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// int64Ptr returns a pointer to an int64 value.
func int64Ptr(i int64) *int64 { return &i }

// hardenedSecurityContext returns a container-level SecurityContext that
// satisfies common Kyverno/PSS policies on managed platforms:
//   - allowPrivilegeEscalation: false
//   - readOnlyRootFilesystem: true
//   - capabilities.drop: [ALL]
//   - runAsNonRoot: true
//   - seccompProfile: RuntimeDefault
func hardenedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		RunAsNonRoot:             boolPtr(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}
