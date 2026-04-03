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
	"k8s.io/apimachinery/pkg/util/intstr"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

const (
	// Default images - these should be overridden via spec.images on managed platforms
	// that enforce image registry restrictions (e.g. via proxy registries)
	DefaultOpencodeImage = "ghcr.io/samyn92/opencode:latest"
	OpencodePort         = 4096
	// SidecarBasePort is the starting port for capability sidecars
	// Each sidecar gets port 8081, 8082, etc.
	SidecarBasePort = 8081
	// DesiredSpecHashAnnotation stores a hash of the operator's desired DeploymentSpec.
	// This avoids spurious updates caused by Kubernetes adding server-side defaults
	// (terminationGracePeriodSeconds, dnsPolicy, etc.) that make reflect.DeepEqual fail.
	DesiredSpecHashAnnotation = "agents.io/desired-spec-hash"
)

// CapabilitySidecarInfo contains resolved Capability information needed for sidecar generation.
// Only used for Container-type capabilities.
type CapabilitySidecarInfo struct {
	// Name is the tool name (capability name or alias)
	Name string
	// Capability is the Capability CRD
	Capability *agentsv1alpha1.Capability
	// Port is the port this sidecar listens on (8081, 8082, etc.)
	Port int32
	// ConfigMapName is the name of the ConfigMap with allow/deny patterns
	ConfigMapName string
}

// getImageConfig returns image settings from agent spec with defaults.
// When spec.images.init is not set, the opencode image is used for the init
// container. This enables the init container to copy pre-cached npm provider
// packages from the opencode image to the data volume, which is essential for
// airgapped environments.
func getImageConfig(agent *agentsv1alpha1.Agent) (opencodeImage, initImage, gatewayImage string, pullPolicy corev1.PullPolicy) {
	opencodeImage = DefaultOpencodeImage
	gatewayImage = DefaultGatewayImage
	pullPolicy = corev1.PullIfNotPresent

	if agent.Spec.Images != nil {
		if agent.Spec.Images.OpenCode != "" {
			opencodeImage = agent.Spec.Images.OpenCode
		}
		if agent.Spec.Images.Gateway != "" {
			gatewayImage = agent.Spec.Images.Gateway
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

// getServiceAccountName determines the ServiceAccount for the agent pod.
// Since all sidecars share the same pod, they share the same ServiceAccount.
// We use the first sidecar's ServiceAccount that specifies one (typically kubectl).
func getServiceAccountName(sidecars []CapabilitySidecarInfo) string {
	for _, sidecar := range sidecars {
		if sidecar.Capability != nil && sidecar.Capability.Spec.Container != nil && sidecar.Capability.Spec.Container.ServiceAccountName != "" {
			return sidecar.Capability.Spec.Container.ServiceAccountName
		}
	}
	return "" // Use default if no capability specifies a ServiceAccount
}

// AgentDeployment creates a Deployment for the agent.
// sidecars contains resolved capability information for sidecar containers.
// ConfigMap changes are propagated via Kubernetes volume updates and symlinks,
// so no configmap hash is needed to trigger rollouts.
func AgentDeployment(agent *agentsv1alpha1.Agent, sidecars []CapabilitySidecarInfo) *appsv1.Deployment {
	labels := commonLabels(agent)
	replicas := int32(1)

	// Get image configuration
	opencodeImage, initImage, gatewayImage, pullPolicy := getImageConfig(agent)

	// Build containers - opencode main container + capability sidecars
	containers := []corev1.Container{
		buildOpencodeContainer(agent, opencodeImage, pullPolicy),
	}

	// Add capability sidecar containers
	for _, sidecar := range sidecars {
		containers = append(containers, buildCapabilitySidecarContainer(agent, sidecar, pullPolicy))
	}

	// Build init container to set up config
	initContainers := []corev1.Container{
		buildInitContainer(initImage, pullPolicy),
	}

	// If there are capability sidecars, add the gateway init container.
	// This copies the capability-gateway binary into a shared emptyDir volume
	// that all sidecar containers mount, so the gateway binary is decoupled
	// from individual tool images and can be updated centrally.
	if len(sidecars) > 0 {
		initContainers = append(initContainers, buildGatewayInitContainer(gatewayImage, pullPolicy))
	}

	// Build volumes (includes sidecar config volumes)
	volumes := buildVolumes(agent)

	// Add ConfigMap volumes for each sidecar
	for _, sidecar := range sidecars {
		volumes = append(volumes, corev1.Volume{
			Name: "config-" + sidecar.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: sidecar.ConfigMapName,
					},
				},
			},
		})
	}

	// Add the shared gateway-bin emptyDir volume when sidecars are present.
	// The gateway init container copies the binary here; all sidecars mount it.
	// Also add a writable /tmp volume since the root filesystem is read-only.
	if len(sidecars) > 0 {
		volumes = append(volumes, corev1.Volume{
			Name: "gateway-bin",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		volumes = append(volumes, corev1.Volume{
			Name: "tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
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

	// Pod template annotations
	// Note: We intentionally do NOT include a configmap-hash annotation here.
	// ConfigMap changes (permission rules, system prompt, etc.) are propagated
	// to running pods via Kubernetes' native ConfigMap volume update mechanism
	// (~60s kubelet sync period). The init container symlinks opencode.json and
	// AGENTS.md to the ConfigMap mount, so updates are visible without restart.
	// The capability-gateway sidecars use a ConfigWatcher (fsnotify) to detect
	// and reload config changes from their ConfigMap mounts.
	podAnnotations := map[string]string{}

	// Determine ServiceAccount - sidecars share the pod's SA
	serviceAccountName := getServiceAccountName(sidecars)

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
	if serviceAccountName != "" {
		podSpec.ServiceAccountName = serviceAccountName
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

// buildCapabilitySidecarContainer creates a sidecar container for a Container capability.
// The sidecar runs the capability-gateway in CLI mode and shares the /data volume with
// the main container. The gateway binary is injected via an init container (see
// buildGatewayInitContainer) rather than being baked into each tool image.
func buildCapabilitySidecarContainer(agent *agentsv1alpha1.Agent, sidecar CapabilitySidecarInfo, pullPolicy corev1.PullPolicy) corev1.Container {
	capability := sidecar.Capability
	container := capability.Spec.Container

	// Build environment variables — use GATEWAY_* env vars for capability-gateway.
	// TOOL_PORT and TOOL_NAME are also set as aliases for simpler configuration.
	envVars := []corev1.EnvVar{
		{Name: "GATEWAY_MODE", Value: "cli"},
		{Name: "GATEWAY_PORT", Value: fmt.Sprintf("%d", sidecar.Port)},
		{Name: "TOOL_PORT", Value: fmt.Sprintf("%d", sidecar.Port)},
		{Name: "TOOL_NAME", Value: sidecar.Name},
	}

	// Add source type for tool wrapper awareness
	if container != nil && container.ContainerType != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "SOURCE_TYPE", Value: container.ContainerType})
	}

	// Add audit config
	if capability.Spec.Audit {
		envVars = append(envVars, corev1.EnvVar{Name: "AUDIT_ENABLED", Value: "true"})
		envVars = append(envVars, corev1.EnvVar{Name: "AUDIT_LOG_COMMANDS", Value: "true"})
	}

	// Add rate limit config
	if capability.Spec.RateLimit != nil && capability.Spec.RateLimit.RequestsPerMinute > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "RATE_LIMIT_RPM",
			Value: fmt.Sprintf("%d", capability.Spec.RateLimit.RequestsPerMinute),
		})
	}

	// Add git author config if specified
	if container != nil && container.Config != nil && container.Config.Git != nil && container.Config.Git.Author != nil {
		envVars = append(envVars,
			corev1.EnvVar{Name: "GIT_AUTHOR_NAME", Value: container.Config.Git.Author.Name},
			corev1.EnvVar{Name: "GIT_AUTHOR_EMAIL", Value: container.Config.Git.Author.Email},
			corev1.EnvVar{Name: "GIT_COMMITTER_NAME", Value: container.Config.Git.Author.Name},
			corev1.EnvVar{Name: "GIT_COMMITTER_EMAIL", Value: container.Config.Git.Author.Email},
		)
	}

	// Add GitLab domain config if specified
	// The glab CLI and git credential helper use GITLAB_HOST to target the right instance
	if container != nil && container.Config != nil && container.Config.GitLab != nil && container.Config.GitLab.Domain != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "GITLAB_HOST", Value: container.Config.GitLab.Domain})
	}

	// Sidecars share the /data/workspace with the main container
	// No need for path rewriting!
	envVars = append(envVars, corev1.EnvVar{Name: "WORKSPACE_PATH", Value: "/data/workspace"})

	// Set HOME and XDG_CONFIG_HOME to writable paths on the shared data volume.
	// The root filesystem is read-only (hardened security context), so tools like
	// glab, gh, git, helm etc. that write to $HOME/.config will fail without this.
	envVars = append(envVars,
		corev1.EnvVar{Name: "HOME", Value: "/data"},
		corev1.EnvVar{Name: "XDG_CONFIG_HOME", Value: "/data/.config"},
	)

	// Add secret environment variables
	for _, secret := range capability.Spec.Secrets {
		envVars = append(envVars, corev1.EnvVar{
			Name: secret.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secret.ValueFrom.Name,
					},
					Key: secret.ValueFrom.Key,
				},
			},
		})
	}

	// Volume mounts - gateway binary, config, shared workspace, and writable tmp
	volumeMounts := []corev1.VolumeMount{
		{Name: "gateway-bin", MountPath: "/gateway", ReadOnly: true},
		{Name: "config-" + sidecar.Name, MountPath: "/etc/tool", ReadOnly: true},
		{Name: "data", MountPath: "/data"}, // Share workspace with main container
		{Name: "tmp", MountPath: "/tmp"},   // Writable tmp for tools (git, glab, etc.)
	}

	// Resource requirements
	var resourceReqs corev1.ResourceRequirements
	if capability.Spec.Resources != nil {
		resourceReqs = *capability.Spec.Resources
	} else {
		resourceReqs = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("64Mi"),
				corev1.ResourceCPU:    resource.MustParse("50m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	}

	// Port name must be max 15 chars - use truncated name or port number
	portName := sidecar.Name
	if len(portName) > 15 {
		portName = fmt.Sprintf("cap-%d", sidecar.Port)
	}

	// Get image from container spec
	image := ""
	if container != nil {
		image = container.Image
	}

	return corev1.Container{
		Name:            sidecar.Name,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		// Override the image's ENTRYPOINT to use the capability-gateway binary
		// injected by the init container. This decouples the gateway version from
		// the tool image, allowing centralized updates to the gateway.
		Command: []string{"/gateway/capability-gateway"},
		Ports: []corev1.ContainerPort{
			{Name: portName, ContainerPort: sidecar.Port, Protocol: corev1.ProtocolTCP},
		},
		Env:             envVars,
		VolumeMounts:    volumeMounts,
		Resources:       resourceReqs,
		SecurityContext: hardenedSecurityContext(),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(sidecar.Port),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(sidecar.Port),
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       20,
		},
	}
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
				// Symlink opencode.json and AGENTS.md instead of copying them.
				// Kubernetes auto-updates ConfigMap volume mounts (~60s), and since
				// these are symlinks, the running process sees updated content without
				// a pod restart. This enables hot-reload of permission rules and
				// system prompt changes when Capability CRDs are modified.
				`ln -sf /config/opencode.json /data/.config/opencode/opencode.json && ` +
				`ln -sf /config/AGENTS.md /data/workspace/AGENTS.md && ` +
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

// buildGatewayInitContainer creates an init container that copies the
// capability-gateway binary from the gateway image into a shared emptyDir
// volume (/gateway). This binary is then used by all sidecar containers
// instead of a gateway binary baked into each tool image.
//
// This is the same init container pattern used for MCP server deployments
// (see mcp_deployment.go), ensuring a consistent gateway injection mechanism.
func buildGatewayInitContainer(image string, pullPolicy corev1.PullPolicy) corev1.Container {
	return corev1.Container{
		Name:            "init-gateway",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"cp", "/capability-gateway", "/gateway/capability-gateway"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "gateway-bin", MountPath: "/gateway"},
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
