package resources

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// =============================================================================
// HELPERS
// =============================================================================

func newTestAgent(name, namespace string) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "anthropic/claude-sonnet-4-20250514",
			Providers: []agentsv1alpha1.ProviderConfig{
				{
					Name: "anthropic",
					APIKeySecret: &agentsv1alpha1.SecretKeySelector{
						Name: "anthropic-secret",
						Key:  "api-key",
					},
				},
			},
		},
	}
}

func newTestSidecar(name string, port int32, image string) CapabilitySidecarInfo {
	return CapabilitySidecarInfo{
		Name: name,
		Capability: &agentsv1alpha1.Capability{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: agentsv1alpha1.CapabilitySpec{
				Type:        agentsv1alpha1.CapabilityTypeContainer,
				Description: fmt.Sprintf("Test capability %s", name),
				Container: &agentsv1alpha1.ContainerCapabilitySpec{
					Image:         image,
					CommandPrefix: name + " ",
				},
			},
		},
		Port:          port,
		ConfigMapName: fmt.Sprintf("my-agent-%s-config", name),
	}
}

// =============================================================================
// getImageConfig TESTS
// =============================================================================

func TestGetImageConfig_Defaults(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	image, initImg, gwImg, policy := getImageConfig(agent)

	if image != DefaultOpencodeImage {
		t.Fatalf("expected default image %q, got %q", DefaultOpencodeImage, image)
	}
	if initImg != DefaultOpencodeImage {
		t.Fatalf("expected default init image to match opencode image %q, got %q", DefaultOpencodeImage, initImg)
	}
	if gwImg != DefaultGatewayImage {
		t.Fatalf("expected default gateway image %q, got %q", DefaultGatewayImage, gwImg)
	}
	if policy != corev1.PullIfNotPresent {
		t.Fatalf("expected PullIfNotPresent, got %v", policy)
	}
}

func TestGetImageConfig_CustomImage(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Images = &agentsv1alpha1.ImagesConfig{
		OpenCode: "my-registry/opencode:v1.0",
	}

	image, initImg, _, policy := getImageConfig(agent)

	if image != "my-registry/opencode:v1.0" {
		t.Fatalf("expected custom image, got %q", image)
	}
	// Init image should follow the opencode image when not explicitly set
	if initImg != "my-registry/opencode:v1.0" {
		t.Fatalf("expected init image to match opencode image %q, got %q", image, initImg)
	}
	if policy != corev1.PullIfNotPresent {
		t.Fatalf("expected default PullIfNotPresent when not set, got %v", policy)
	}
}

func TestGetImageConfig_CustomInitImage(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Images = &agentsv1alpha1.ImagesConfig{
		OpenCode: "my-registry/opencode:v1.0",
		Init:     "my-registry/busybox:1.36",
	}

	_, initImg, _, _ := getImageConfig(agent)

	// Explicit init image override should be respected
	if initImg != "my-registry/busybox:1.36" {
		t.Fatalf("expected explicit init image, got %q", initImg)
	}
}

func TestGetImageConfig_CustomPullPolicy(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Images = &agentsv1alpha1.ImagesConfig{
		OpenCode:   "my-registry/opencode:latest",
		PullPolicy: corev1.PullAlways,
	}

	image, _, _, policy := getImageConfig(agent)

	if image != "my-registry/opencode:latest" {
		t.Fatalf("expected custom image, got %q", image)
	}
	if policy != corev1.PullAlways {
		t.Fatalf("expected PullAlways, got %v", policy)
	}
}

// =============================================================================
// getServiceAccountName TESTS
// =============================================================================

func TestGetServiceAccountName_NoSidecars(t *testing.T) {
	sa := getServiceAccountName(nil)
	if sa != "" {
		t.Fatalf("expected empty SA for nil sidecars, got %q", sa)
	}
}

func TestGetServiceAccountName_NoSASpecified(t *testing.T) {
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
	}

	sa := getServiceAccountName(sidecars)
	if sa != "" {
		t.Fatalf("expected empty SA when none specified, got %q", sa)
	}
}

func TestGetServiceAccountName_FirstSidecarWithSA(t *testing.T) {
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
		newTestSidecar("helm", 8082, "alpine/helm:3.14"),
	}
	sidecars[0].Capability.Spec.Container.ServiceAccountName = "kubectl-sa"
	sidecars[1].Capability.Spec.Container.ServiceAccountName = "helm-sa"

	sa := getServiceAccountName(sidecars)
	if sa != "kubectl-sa" {
		t.Fatalf("expected first SA 'kubectl-sa', got %q", sa)
	}
}

func TestGetServiceAccountName_SkipsEmptySA(t *testing.T) {
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("gh", 8081, "ghcr.io/cli/cli:latest"),
		newTestSidecar("kubectl", 8082, "bitnami/kubectl:1.30"),
	}
	// gh has no SA, kubectl does
	sidecars[1].Capability.Spec.Container.ServiceAccountName = "kubectl-sa"

	sa := getServiceAccountName(sidecars)
	if sa != "kubectl-sa" {
		t.Fatalf("expected 'kubectl-sa', got %q", sa)
	}
}

func TestGetServiceAccountName_NilContainerSpec(t *testing.T) {
	sidecars := []CapabilitySidecarInfo{
		{
			Name: "mcp-cap",
			Capability: &agentsv1alpha1.Capability{
				Spec: agentsv1alpha1.CapabilitySpec{
					Type: agentsv1alpha1.CapabilityTypeMCP,
					// No Container spec
				},
			},
			Port: 8081,
		},
	}

	sa := getServiceAccountName(sidecars)
	if sa != "" {
		t.Fatalf("expected empty SA for non-container capability, got %q", sa)
	}
}

// =============================================================================
// AgentDeployment TESTS
// =============================================================================

func TestAgentDeployment_BasicStructure(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	if dep.Name != "my-agent" {
		t.Fatalf("expected name 'my-agent', got %q", dep.Name)
	}
	if dep.Namespace != "default" {
		t.Fatalf("expected namespace 'default', got %q", dep.Namespace)
	}
	if *dep.Spec.Replicas != 1 {
		t.Fatalf("expected 1 replica, got %d", *dep.Spec.Replicas)
	}
}

func TestAgentDeployment_Labels(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	labels := dep.Labels
	if labels["app.kubernetes.io/name"] != "agent" {
		t.Fatalf("expected label name 'agent', got %v", labels)
	}
	if labels["app.kubernetes.io/instance"] != "my-agent" {
		t.Fatalf("expected label instance 'my-agent', got %v", labels)
	}
	if labels["app.kubernetes.io/managed-by"] != "agent-operator" {
		t.Fatalf("expected managed-by label, got %v", labels)
	}
	// Template labels should match
	if dep.Spec.Template.Labels["app.kubernetes.io/instance"] != "my-agent" {
		t.Fatal("template labels should match deployment labels")
	}
}

func TestAgentDeployment_NoConfigMapHashAnnotation(t *testing.T) {
	// ConfigMap hash annotation is no longer used — config changes propagate via
	// Kubernetes ConfigMap volume updates and symlinks, not rolling restarts.
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	podAnnotations := dep.Spec.Template.Annotations
	if _, ok := podAnnotations[ConfigMapHashAnnotation]; ok {
		t.Fatal("configmap hash annotation should not be present — config reload is handled via volume updates")
	}
}

func TestAgentDeployment_DesiredSpecHashAnnotation(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	if dep.Annotations == nil {
		t.Fatal("expected deployment annotations")
	}
	hash := dep.Annotations[DesiredSpecHashAnnotation]
	if hash == "" {
		t.Fatal("expected desired-spec-hash annotation to be set")
	}
	if len(hash) != 64 { // SHA256 hex length
		t.Fatalf("expected 64-char SHA256 hash, got %d chars", len(hash))
	}
}

func TestAgentDeployment_MainContainerBasics(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) < 1 {
		t.Fatal("expected at least one container")
	}
	main := containers[0]
	if main.Name != "opencode" {
		t.Fatalf("expected main container name 'opencode', got %q", main.Name)
	}
	if main.Image != DefaultOpencodeImage {
		t.Fatalf("expected default image, got %q", main.Image)
	}
	if main.WorkingDir != "/data/workspace" {
		t.Fatalf("expected working dir '/data/workspace', got %q", main.WorkingDir)
	}
}

func TestAgentDeployment_MainContainerArgs(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	args := strings.Join(main.Args, " ")
	if !strings.Contains(args, "serve") {
		t.Fatalf("expected 'serve' arg, got %v", main.Args)
	}
	if !strings.Contains(args, "--port") || !strings.Contains(args, "4096") {
		t.Fatalf("expected port 4096 arg, got %v", main.Args)
	}
	if !strings.Contains(args, "--hostname") || !strings.Contains(args, "0.0.0.0") {
		t.Fatalf("expected hostname 0.0.0.0 arg, got %v", main.Args)
	}
}

func TestAgentDeployment_LoggingArgs(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	enabled := true
	agent.Spec.Logging = &agentsv1alpha1.LoggingConfig{
		Level:   "DEBUG",
		Enabled: &enabled,
	}

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	args := strings.Join(main.Args, " ")
	if !strings.Contains(args, "--print-logs") {
		t.Fatalf("expected --print-logs when logging enabled, got %v", main.Args)
	}
	if !strings.Contains(args, "--log-level") || !strings.Contains(args, "DEBUG") {
		t.Fatalf("expected --log-level DEBUG, got %v", main.Args)
	}
}

func TestAgentDeployment_LoggingDisabled(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	disabled := false
	agent.Spec.Logging = &agentsv1alpha1.LoggingConfig{
		Enabled: &disabled,
	}

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	args := strings.Join(main.Args, " ")
	if strings.Contains(args, "--print-logs") {
		t.Fatalf("expected no --print-logs when logging disabled, got %v", main.Args)
	}
}

func TestAgentDeployment_MainContainerEnvVars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	envMap := make(map[string]corev1.EnvVar)
	for _, e := range main.Env {
		envMap[e.Name] = e
	}

	// Check HOME
	if envMap["HOME"].Value != "/data" {
		t.Fatalf("expected HOME=/data, got %q", envMap["HOME"].Value)
	}
	// Check XDG_CONFIG_HOME
	if envMap["XDG_CONFIG_HOME"].Value != "/data/.config" {
		t.Fatalf("expected XDG_CONFIG_HOME=/data/.config, got %q", envMap["XDG_CONFIG_HOME"].Value)
	}
	// Check BUN_CONFIG_REGISTRY for airgap fast-fail
	if envMap["BUN_CONFIG_REGISTRY"].Value != "http://localhost:1" {
		t.Fatalf("expected BUN_CONFIG_REGISTRY=http://localhost:1, got %q", envMap["BUN_CONFIG_REGISTRY"].Value)
	}
	// Check API key env
	apiKeyEnv := envMap["ANTHROPIC_API_KEY"]
	if apiKeyEnv.ValueFrom == nil || apiKeyEnv.ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected ANTHROPIC_API_KEY from secret")
	}
	if apiKeyEnv.ValueFrom.SecretKeyRef.Name != "anthropic-secret" {
		t.Fatalf("expected secret name 'anthropic-secret', got %q", apiKeyEnv.ValueFrom.SecretKeyRef.Name)
	}
}

func TestAgentDeployment_AdditionalProviderEnvVars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// Add a second provider alongside the default anthropic
	agent.Spec.Providers = append(agent.Spec.Providers, agentsv1alpha1.ProviderConfig{
		Name: "openai",
		APIKeySecret: &agentsv1alpha1.SecretKeySelector{
			Name: "openai-secret",
			Key:  "key",
		},
	})

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	var found bool
	for _, e := range main.Env {
		if e.Name == "OPENAI_API_KEY" {
			found = true
			if e.ValueFrom.SecretKeyRef.Name != "openai-secret" {
				t.Fatalf("expected openai-secret, got %q", e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	if !found {
		t.Fatal("expected OPENAI_API_KEY env var from additional provider")
	}
}

func TestAgentDeployment_ProviderWithNoAPIKey(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// Ollama-style provider with no API key
	agent.Spec.Providers = append(agent.Spec.Providers, agentsv1alpha1.ProviderConfig{
		Name:         "ollama",
		APIKeySecret: nil,
	})

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	for _, e := range main.Env {
		if e.Name == "OLLAMA_API_KEY" {
			t.Fatal("should not inject API key env for provider without apiKeySecret")
		}
	}
}

func TestAgentDeployment_DefaultResources(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	memReq := main.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "256Mi" {
		t.Fatalf("expected default memory request 256Mi, got %s", memReq.String())
	}
	cpuReq := main.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "100m" {
		t.Fatalf("expected default CPU request 100m, got %s", cpuReq.String())
	}
	memLimit := main.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "1Gi" {
		t.Fatalf("expected default memory limit 1Gi, got %s", memLimit.String())
	}
}

func TestAgentDeployment_CustomResources(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
			corev1.ResourceCPU:    resource.MustParse("250m"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	memReq := main.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "512Mi" {
		t.Fatalf("expected custom memory request 512Mi, got %s", memReq.String())
	}
}

func TestAgentDeployment_MainContainerPorts(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	if len(main.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(main.Ports))
	}
	if main.Ports[0].Name != "http" {
		t.Fatalf("expected port name 'http', got %q", main.Ports[0].Name)
	}
	if main.Ports[0].ContainerPort != OpencodePort {
		t.Fatalf("expected port %d, got %d", OpencodePort, main.Ports[0].ContainerPort)
	}
}

func TestAgentDeployment_MainContainerProbes(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	if main.ReadinessProbe == nil {
		t.Fatal("expected readiness probe")
	}
	// Probes use exec+wget to localhost to avoid IPv6 issues on dual-stack clusters
	if main.ReadinessProbe.Exec == nil {
		t.Fatal("expected readiness probe to use exec (not HTTPGet)")
	}
	readinessCmd := strings.Join(main.ReadinessProbe.Exec.Command, " ")
	if !strings.Contains(readinessCmd, "wget") || !strings.Contains(readinessCmd, "/global/health") {
		t.Fatalf("expected readiness exec command to use wget with /global/health, got %q", readinessCmd)
	}
	if !strings.Contains(readinessCmd, "127.0.0.1") {
		t.Fatalf("expected readiness probe to use 127.0.0.1 (localhost), got %q", readinessCmd)
	}
	if main.LivenessProbe == nil {
		t.Fatal("expected liveness probe")
	}
	if main.LivenessProbe.Exec == nil {
		t.Fatal("expected liveness probe to use exec (not HTTPGet)")
	}
	livenessCmd := strings.Join(main.LivenessProbe.Exec.Command, " ")
	if !strings.Contains(livenessCmd, "wget") || !strings.Contains(livenessCmd, "/global/health") {
		t.Fatalf("expected liveness exec command to use wget with /global/health, got %q", livenessCmd)
	}
}

func TestAgentDeployment_WithSidecars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
		newTestSidecar("gh", 8082, "ghcr.io/cli/cli:latest"),
	}

	dep := AgentDeployment(agent, sidecars)

	// 1 main + 2 sidecars = 3 containers
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(containers))
	}
	if containers[0].Name != "opencode" {
		t.Fatalf("first container should be opencode, got %q", containers[0].Name)
	}
	if containers[1].Name != "kubectl" {
		t.Fatalf("second container should be kubectl, got %q", containers[1].Name)
	}
	if containers[2].Name != "gh" {
		t.Fatalf("third container should be gh, got %q", containers[2].Name)
	}
}

func TestAgentDeployment_SidecarConfigMapVolumes(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
	}

	dep := AgentDeployment(agent, sidecars)

	volumes := dep.Spec.Template.Spec.Volumes
	var found bool
	for _, v := range volumes {
		if v.Name == "config-kubectl" {
			found = true
			if v.ConfigMap.Name != "my-agent-kubectl-config" {
				t.Fatalf("expected configmap name 'my-agent-kubectl-config', got %q", v.ConfigMap.Name)
			}
		}
	}
	if !found {
		t.Fatal("expected config-kubectl volume")
	}
}

func TestAgentDeployment_ServiceAccountFromSidecars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
	}
	sidecars[0].Capability.Spec.Container.ServiceAccountName = "kubectl-sa"

	dep := AgentDeployment(agent, sidecars)

	if dep.Spec.Template.Spec.ServiceAccountName != "kubectl-sa" {
		t.Fatalf("expected SA 'kubectl-sa', got %q", dep.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestAgentDeployment_NoServiceAccountWithoutSidecars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	if dep.Spec.Template.Spec.ServiceAccountName != "" {
		t.Fatalf("expected empty SA without sidecars, got %q", dep.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestAgentDeployment_InitContainer(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, nil)

	initContainers := dep.Spec.Template.Spec.InitContainers
	if len(initContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(initContainers))
	}
	init := initContainers[0]
	if init.Name != "init-config" {
		t.Fatalf("expected init container name 'init-config', got %q", init.Name)
	}
	if init.Image != DefaultOpencodeImage {
		t.Fatalf("expected init image %q, got %q", DefaultOpencodeImage, init.Image)
	}
}

func TestAgentDeployment_CustomImage(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Images = &agentsv1alpha1.ImagesConfig{
		OpenCode:   "custom/opencode:v2",
		PullPolicy: corev1.PullAlways,
	}

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	if main.Image != "custom/opencode:v2" {
		t.Fatalf("expected custom image, got %q", main.Image)
	}
	if main.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("expected PullAlways, got %v", main.ImagePullPolicy)
	}
}

func TestAgentDeployment_AdditionalVolumeMounts(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.AdditionalVolumeMounts = []corev1.VolumeMount{
		{Name: "extra", MountPath: "/extra"},
	}

	dep := AgentDeployment(agent, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	var found bool
	for _, vm := range main.VolumeMounts {
		if vm.Name == "extra" && vm.MountPath == "/extra" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected additional volume mount '/extra'")
	}
}

// =============================================================================
// buildCapabilitySidecarContainer TESTS
// =============================================================================

func TestSidecarContainer_BasicEnvVars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	sidecar.Capability.Spec.Container.ContainerType = "kubernetes"

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	envMap := make(map[string]string)
	for _, e := range c.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["TOOL_PORT"] != "8081" {
		t.Fatalf("expected TOOL_PORT=8081, got %q", envMap["TOOL_PORT"])
	}
	if envMap["TOOL_NAME"] != "kubectl" {
		t.Fatalf("expected TOOL_NAME=kubectl, got %q", envMap["TOOL_NAME"])
	}
	if envMap["SOURCE_TYPE"] != "kubernetes" {
		t.Fatalf("expected SOURCE_TYPE=kubernetes, got %q", envMap["SOURCE_TYPE"])
	}
	if envMap["WORKSPACE_PATH"] != "/data/workspace" {
		t.Fatalf("expected WORKSPACE_PATH=/data/workspace, got %q", envMap["WORKSPACE_PATH"])
	}
}

func TestSidecarContainer_NoSourceType(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("custom", 8081, "custom:latest")
	sidecar.Capability.Spec.Container.ContainerType = "" // no type

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	for _, e := range c.Env {
		if e.Name == "SOURCE_TYPE" {
			t.Fatal("should not have SOURCE_TYPE when containerType is empty")
		}
	}
}

func TestSidecarContainer_AuditEnabled(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	sidecar.Capability.Spec.Audit = true

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	envMap := make(map[string]string)
	for _, e := range c.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["AUDIT_ENABLED"] != "true" {
		t.Fatal("expected AUDIT_ENABLED=true")
	}
	if envMap["AUDIT_LOG_COMMANDS"] != "true" {
		t.Fatal("expected AUDIT_LOG_COMMANDS=true")
	}
}

func TestSidecarContainer_AuditDisabled(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	sidecar.Capability.Spec.Audit = false

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	for _, e := range c.Env {
		if e.Name == "AUDIT_ENABLED" {
			t.Fatal("should not have AUDIT_ENABLED when audit is false")
		}
	}
}

func TestSidecarContainer_RateLimit(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	sidecar.Capability.Spec.RateLimit = &agentsv1alpha1.CapabilityRateLimit{
		RequestsPerMinute: 60,
	}

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	envMap := make(map[string]string)
	for _, e := range c.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["RATE_LIMIT_RPM"] != "60" {
		t.Fatalf("expected RATE_LIMIT_RPM=60, got %q", envMap["RATE_LIMIT_RPM"])
	}
}

func TestSidecarContainer_NoRateLimit(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	// No rate limit

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	for _, e := range c.Env {
		if e.Name == "RATE_LIMIT_RPM" {
			t.Fatal("should not have RATE_LIMIT_RPM when no rate limit")
		}
	}
}

func TestSidecarContainer_GitAuthorConfig(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("gh", 8081, "ghcr.io/cli/cli:latest")
	sidecar.Capability.Spec.Container.Config = &agentsv1alpha1.CapabilityConfig{
		Git: &agentsv1alpha1.GitConfig{
			Author: &agentsv1alpha1.GitAuthor{
				Name:  "Agent Bot",
				Email: "bot@example.com",
			},
		},
	}

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	envMap := make(map[string]string)
	for _, e := range c.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["GIT_AUTHOR_NAME"] != "Agent Bot" {
		t.Fatalf("expected GIT_AUTHOR_NAME='Agent Bot', got %q", envMap["GIT_AUTHOR_NAME"])
	}
	if envMap["GIT_AUTHOR_EMAIL"] != "bot@example.com" {
		t.Fatalf("expected GIT_AUTHOR_EMAIL='bot@example.com', got %q", envMap["GIT_AUTHOR_EMAIL"])
	}
	if envMap["GIT_COMMITTER_NAME"] != "Agent Bot" {
		t.Fatalf("expected GIT_COMMITTER_NAME='Agent Bot', got %q", envMap["GIT_COMMITTER_NAME"])
	}
	if envMap["GIT_COMMITTER_EMAIL"] != "bot@example.com" {
		t.Fatalf("expected GIT_COMMITTER_EMAIL='bot@example.com', got %q", envMap["GIT_COMMITTER_EMAIL"])
	}
}

func TestSidecarContainer_GitLabDomain(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("glab", 8081, "registry.gitlab.com/glab:latest")
	sidecar.Capability.Spec.Container.Config = &agentsv1alpha1.CapabilityConfig{
		GitLab: &agentsv1alpha1.GitLabConfig{
			Domain: "gitlab.company.com",
		},
	}

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	envMap := make(map[string]string)
	for _, e := range c.Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	if envMap["GITLAB_HOST"] != "gitlab.company.com" {
		t.Fatalf("expected GITLAB_HOST='gitlab.company.com', got %q", envMap["GITLAB_HOST"])
	}
}

func TestSidecarContainer_Secrets(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("gh", 8081, "ghcr.io/cli/cli:latest")
	sidecar.Capability.Spec.Secrets = []agentsv1alpha1.SecretEnvVar{
		{
			Name: "GITHUB_TOKEN",
			ValueFrom: agentsv1alpha1.SecretKeySelector{
				Name: "github-secret",
				Key:  "token",
			},
		},
		{
			Name: "EXTRA_SECRET",
			ValueFrom: agentsv1alpha1.SecretKeySelector{
				Name: "extra-secret",
				Key:  "value",
			},
		},
	}

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	secretEnvs := make(map[string]*corev1.SecretKeySelector)
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			secretEnvs[e.Name] = e.ValueFrom.SecretKeyRef
		}
	}

	if secretEnvs["GITHUB_TOKEN"] == nil {
		t.Fatal("expected GITHUB_TOKEN secret env")
	}
	if secretEnvs["GITHUB_TOKEN"].Name != "github-secret" {
		t.Fatalf("expected secret name 'github-secret', got %q", secretEnvs["GITHUB_TOKEN"].Name)
	}
	if secretEnvs["EXTRA_SECRET"] == nil {
		t.Fatal("expected EXTRA_SECRET secret env")
	}
}

func TestSidecarContainer_VolumeMounts(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	mountMap := make(map[string]corev1.VolumeMount)
	for _, vm := range c.VolumeMounts {
		mountMap[vm.Name] = vm
	}

	// Config mount
	configMount := mountMap["config-kubectl"]
	if configMount.MountPath != "/etc/tool" {
		t.Fatalf("expected config mount at /etc/tool, got %q", configMount.MountPath)
	}
	if !configMount.ReadOnly {
		t.Fatal("config mount should be read-only")
	}

	// Data mount
	dataMount := mountMap["data"]
	if dataMount.MountPath != "/data" {
		t.Fatalf("expected data mount at /data, got %q", dataMount.MountPath)
	}
}

func TestSidecarContainer_DefaultResources(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	memReq := c.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "64Mi" {
		t.Fatalf("expected default sidecar memory request 64Mi, got %s", memReq.String())
	}
	cpuReq := c.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "50m" {
		t.Fatalf("expected default sidecar CPU request 50m, got %s", cpuReq.String())
	}
	memLimit := c.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "256Mi" {
		t.Fatalf("expected default sidecar memory limit 256Mi, got %s", memLimit.String())
	}
}

func TestSidecarContainer_CustomResources(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")
	sidecar.Capability.Spec.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	memReq := c.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "128Mi" {
		t.Fatalf("expected custom memory request 128Mi, got %s", memReq.String())
	}
	memLimit := c.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "512Mi" {
		t.Fatalf("expected custom memory limit 512Mi, got %s", memLimit.String())
	}
}

func TestSidecarContainer_PortName(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	if c.Ports[0].Name != "kubectl" {
		t.Fatalf("expected port name 'kubectl', got %q", c.Ports[0].Name)
	}
	if c.Ports[0].ContainerPort != 8081 {
		t.Fatalf("expected port 8081, got %d", c.Ports[0].ContainerPort)
	}
}

func TestSidecarContainer_PortNameTruncation(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// Name > 15 chars
	sidecar := newTestSidecar("very-long-capability-name", 8083, "image:latest")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	portName := c.Ports[0].Name
	if len(portName) > 15 {
		t.Fatalf("port name should be max 15 chars, got %d: %q", len(portName), portName)
	}
	expected := "cap-8083"
	if portName != expected {
		t.Fatalf("expected truncated port name %q, got %q", expected, portName)
	}
}

func TestSidecarContainer_Probes(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	if c.ReadinessProbe == nil {
		t.Fatal("expected readiness probe")
	}
	if c.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Fatalf("expected readiness path '/healthz', got %q", c.ReadinessProbe.HTTPGet.Path)
	}
	if c.ReadinessProbe.HTTPGet.Port.IntValue() != 8081 {
		t.Fatalf("expected readiness port 8081, got %d", c.ReadinessProbe.HTTPGet.Port.IntValue())
	}
	if c.LivenessProbe == nil {
		t.Fatal("expected liveness probe")
	}
	if c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatalf("expected liveness path '/healthz', got %q", c.LivenessProbe.HTTPGet.Path)
	}
}

func TestSidecarContainer_ContainerName(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	sidecar := newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30")

	c := buildCapabilitySidecarContainer(agent, sidecar, corev1.PullIfNotPresent)

	if c.Name != "kubectl" {
		t.Fatalf("expected container name 'kubectl', got %q", c.Name)
	}
	if c.Image != "bitnami/kubectl:1.30" {
		t.Fatalf("expected image 'bitnami/kubectl:1.30', got %q", c.Image)
	}
}

// =============================================================================
// buildInitContainer TESTS
// =============================================================================

func TestInitContainer_Structure(t *testing.T) {
	c := buildInitContainer(DefaultOpencodeImage, corev1.PullIfNotPresent)

	if c.Name != "init-config" {
		t.Fatalf("expected name 'init-config', got %q", c.Name)
	}
	if c.Image != DefaultOpencodeImage {
		t.Fatalf("expected image %q, got %q", DefaultOpencodeImage, c.Image)
	}
	if c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("expected PullIfNotPresent, got %v", c.ImagePullPolicy)
	}
}

func TestInitContainer_WorkingDir(t *testing.T) {
	c := buildInitContainer(DefaultOpencodeImage, corev1.PullIfNotPresent)

	// WorkingDir must be /data (NOT /data/workspace) to prevent the CRI from
	// pre-creating /data/workspace as root on PVC-backed volumes. The image's
	// WORKDIR /data/workspace would be used if not overridden, causing the CRI
	// to create that dir with root ownership (mode 0755), making it unwritable
	// by UID 1000 even with fsGroup.
	if c.WorkingDir != "/data" {
		t.Fatalf("expected WorkingDir '/data' to prevent CRI creating /data/workspace as root, got %q", c.WorkingDir)
	}
}

func TestInitContainer_Command(t *testing.T) {
	c := buildInitContainer(DefaultOpencodeImage, corev1.PullIfNotPresent)

	if len(c.Command) != 2 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" {
		t.Fatalf("expected [/bin/sh -c], got %v", c.Command)
	}
	if len(c.Args) != 1 {
		t.Fatalf("expected 1 script arg, got %d", len(c.Args))
	}
}

func TestInitContainer_ScriptContent(t *testing.T) {
	c := buildInitContainer(DefaultOpencodeImage, corev1.PullIfNotPresent)

	script := c.Args[0]
	// Verify rmdir to clean up root-owned workspace dir from CRI WORKDIR creation
	if !strings.Contains(script, "rmdir /data/workspace") {
		t.Fatal("expected rmdir /data/workspace to fix CRI WORKDIR ownership")
	}
	// Verify all important mkdir + cp operations
	if !strings.Contains(script, "mkdir -p /data/.config/opencode /data/.cache/opencode /data/.local/share/opencode") {
		t.Fatal("expected mkdir for config, cache, and local share dirs")
	}
	if !strings.Contains(script, "mkdir -p /data/workspace") {
		t.Fatal("expected explicit mkdir for workspace dir")
	}
	if !strings.Contains(script, "mkdir -p /data/.config/opencode/.opencode/plugins") {
		t.Fatal("expected mkdir for plugins dir")
	}
	if !strings.Contains(script, "/data/workspace/.opencode/tools") {
		t.Fatal("expected mkdir for tools dir")
	}
	if !strings.Contains(script, "/data/workspace/.opencode/skills") {
		t.Fatal("expected mkdir for skills dir")
	}
	// opencode.json and AGENTS.md are symlinked (not copied) for hot-reload support
	if !strings.Contains(script, "ln -sf /config/opencode.json /data/.config/opencode/opencode.json") {
		t.Fatal("expected symlink for opencode.json")
	}
	if !strings.Contains(script, "ln -sf /config/AGENTS.md /data/workspace/AGENTS.md") {
		t.Fatal("expected symlink for AGENTS.md")
	}
	if !strings.Contains(script, "cp /config/telemetry.ts") {
		t.Fatal("expected cp for telemetry plugin")
	}
	// Verify tool/plugin/skill copy loops
	if !strings.Contains(script, "tool-*.ts") {
		t.Fatal("expected tool file copy loop")
	}
	if !strings.Contains(script, "plugin-*.ts") {
		t.Fatal("expected plugin file copy loop")
	}
	if !strings.Contains(script, "skill-*-SKILL.md") {
		t.Fatal("expected skill file copy loop")
	}
	// Verify pre-cached node_modules copy for airgap support
	if !strings.Contains(script, "/opt/opencode/node_modules") {
		t.Fatal("expected node_modules copy from /opt/opencode/node_modules")
	}
	if !strings.Contains(script, "/data/.config/opencode") {
		t.Fatal("expected copy destination /data/.config/opencode")
	}
	if !strings.Contains(script, "/data/workspace/.opencode") {
		t.Fatal("expected copy destination /data/workspace/.opencode")
	}
}

func TestInitContainer_VolumeMounts(t *testing.T) {
	c := buildInitContainer(DefaultOpencodeImage, corev1.PullIfNotPresent)

	mountMap := make(map[string]string)
	for _, vm := range c.VolumeMounts {
		mountMap[vm.Name] = vm.MountPath
	}

	if mountMap["data"] != "/data" {
		t.Fatalf("expected data mount at /data, got %q", mountMap["data"])
	}
	if mountMap["config"] != "/config" {
		t.Fatalf("expected config mount at /config, got %q", mountMap["config"])
	}
}

// =============================================================================
// buildVolumes TESTS
// =============================================================================

func TestBuildVolumes_EmptyDir(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// No storage configured = emptyDir

	volumes := buildVolumes(agent)

	volumeMap := make(map[string]corev1.Volume)
	for _, v := range volumes {
		volumeMap[v.Name] = v
	}

	// Config volume
	config := volumeMap["config"]
	if config.ConfigMap == nil {
		t.Fatal("expected config volume to use ConfigMap")
	}
	if config.ConfigMap.Name != "my-agent-config" {
		t.Fatalf("expected ConfigMap name 'my-agent-config', got %q", config.ConfigMap.Name)
	}

	// Data volume — should be emptyDir
	data := volumeMap["data"]
	if data.EmptyDir == nil {
		t.Fatal("expected data volume to use emptyDir when no storage configured")
	}
	if data.PersistentVolumeClaim != nil {
		t.Fatal("should not have PVC when no storage configured")
	}
}

func TestBuildVolumes_PVC(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Storage = &agentsv1alpha1.StorageConfig{
		Size: resource.MustParse("10Gi"),
	}

	volumes := buildVolumes(agent)

	volumeMap := make(map[string]corev1.Volume)
	for _, v := range volumes {
		volumeMap[v.Name] = v
	}

	data := volumeMap["data"]
	if data.PersistentVolumeClaim == nil {
		t.Fatal("expected data volume to use PVC when storage configured")
	}
	if data.PersistentVolumeClaim.ClaimName != "my-agent-data" {
		t.Fatalf("expected PVC name 'my-agent-data', got %q", data.PersistentVolumeClaim.ClaimName)
	}
	if data.EmptyDir != nil {
		t.Fatal("should not have emptyDir when storage configured")
	}
}

func TestBuildVolumes_AdditionalVolumes(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.AdditionalVolumes = []corev1.Volume{
		{
			Name: "extra-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "extra"},
				},
			},
		},
		{
			Name: "extra-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "my-secret"},
			},
		},
	}

	volumes := buildVolumes(agent)

	// Should have config + data + 2 additional = 4
	if len(volumes) != 4 {
		t.Fatalf("expected 4 volumes, got %d", len(volumes))
	}

	volumeNames := make(map[string]bool)
	for _, v := range volumes {
		volumeNames[v.Name] = true
	}
	if !volumeNames["extra-config"] {
		t.Fatal("expected extra-config volume")
	}
	if !volumeNames["extra-secret"] {
		t.Fatal("expected extra-secret volume")
	}
}

// =============================================================================
// DEPLOYMENT STRATEGY TESTS
// =============================================================================

func TestAgentDeployment_Strategy_RecreateWithPVC(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	agent.Spec.Storage = &agentsv1alpha1.StorageConfig{
		Size: resource.MustParse("10Gi"),
	}

	dep := AgentDeployment(agent, nil)

	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("expected Recreate strategy with PVC, got %v", dep.Spec.Strategy.Type)
	}
}

func TestAgentDeployment_Strategy_RollingUpdateWithoutPVC(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// No storage = emptyDir

	dep := AgentDeployment(agent, nil)

	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("expected RollingUpdate strategy without PVC, got %v", dep.Spec.Strategy.Type)
	}
}

// =============================================================================
// HashDeploymentSpec TESTS
// =============================================================================

func TestHashDeploymentSpec_Deterministic(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep1 := AgentDeployment(agent, nil)
	dep2 := AgentDeployment(agent, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 != hash2 {
		t.Fatalf("same deployment spec should produce same hash, got %q vs %q", hash1, hash2)
	}
}

func TestHashDeploymentSpec_DifferentSpecs(t *testing.T) {
	agent1 := newTestAgent("agent-a", "default")
	agent2 := newTestAgent("agent-b", "default")

	dep1 := AgentDeployment(agent1, nil)
	dep2 := AgentDeployment(agent2, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 == hash2 {
		t.Fatal("different deployment specs should produce different hashes")
	}
}

func TestHashDeploymentSpec_SameInputsSameHash(t *testing.T) {
	// Without configmap hash in annotations, same agent always produces same spec hash
	agent := newTestAgent("my-agent", "default")

	dep1 := AgentDeployment(agent, nil)
	dep2 := AgentDeployment(agent, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 != hash2 {
		t.Fatal("same agent should produce identical spec hashes")
	}
}

func TestHashDeploymentSpec_Length(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	dep := AgentDeployment(agent, nil)

	hash := HashDeploymentSpec(dep)

	// SHA256 hex = 64 characters
	if len(hash) != 64 {
		t.Fatalf("expected 64-char hash, got %d chars: %q", len(hash), hash)
	}
}

// =============================================================================
// INTEGRATION-STYLE TESTS
// =============================================================================

func TestAgentDeployment_FullStack(t *testing.T) {
	// Test a realistic deployment with multiple sidecars, custom resources, storage
	agent := newTestAgent("sre-agent", "production")
	agent.Spec.Storage = &agentsv1alpha1.StorageConfig{
		Size: resource.MustParse("20Gi"),
	}
	agent.Spec.Images = &agentsv1alpha1.ImagesConfig{
		OpenCode:   "ghcr.io/anomalyco/opencode:v1.2.0",
		PullPolicy: corev1.PullAlways,
	}
	enabled := true
	agent.Spec.Logging = &agentsv1alpha1.LoggingConfig{
		Level:   "INFO",
		Enabled: &enabled,
	}

	sidecars := []CapabilitySidecarInfo{
		newTestSidecar("kubectl", 8081, "bitnami/kubectl:1.30"),
		newTestSidecar("helm", 8082, "alpine/helm:3.14"),
		newTestSidecar("gh", 8083, "ghcr.io/cli/cli:2.47"),
	}
	sidecars[0].Capability.Spec.Container.ServiceAccountName = "sre-sa"
	sidecars[2].Capability.Spec.Secrets = []agentsv1alpha1.SecretEnvVar{
		{Name: "GITHUB_TOKEN", ValueFrom: agentsv1alpha1.SecretKeySelector{Name: "gh-secret", Key: "token"}},
	}

	dep := AgentDeployment(agent, sidecars)

	// Basic structure
	if dep.Name != "sre-agent" {
		t.Fatalf("expected name 'sre-agent', got %q", dep.Name)
	}

	// Containers: 1 main + 3 sidecars = 4
	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 4 {
		t.Fatalf("expected 4 containers, got %d", len(containers))
	}

	// Main container uses custom image
	if containers[0].Image != "ghcr.io/anomalyco/opencode:v1.2.0" {
		t.Fatalf("expected custom image, got %q", containers[0].Image)
	}

	// All containers use PullAlways
	for _, c := range containers {
		if c.ImagePullPolicy != corev1.PullAlways {
			t.Fatalf("container %q should use PullAlways, got %v", c.Name, c.ImagePullPolicy)
		}
	}

	// ServiceAccount from kubectl sidecar
	if dep.Spec.Template.Spec.ServiceAccountName != "sre-sa" {
		t.Fatalf("expected SA 'sre-sa', got %q", dep.Spec.Template.Spec.ServiceAccountName)
	}

	// PVC volume
	var hasPVC bool
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == "data" && v.PersistentVolumeClaim != nil {
			hasPVC = true
		}
	}
	if !hasPVC {
		t.Fatal("expected PVC data volume")
	}

	// Strategy must be Recreate when PVC is used (RWO block storage)
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("expected Recreate strategy with PVC, got %v", dep.Spec.Strategy.Type)
	}

	// Sidecar config volumes
	volumeNames := make(map[string]bool)
	for _, v := range dep.Spec.Template.Spec.Volumes {
		volumeNames[v.Name] = true
	}
	for _, name := range []string{"config-kubectl", "config-helm", "config-gh"} {
		if !volumeNames[name] {
			t.Fatalf("expected volume %q", name)
		}
	}

	// Configmap hash annotation should NOT be present — config propagates via volume updates
	if _, ok := dep.Spec.Template.Annotations[ConfigMapHashAnnotation]; ok {
		t.Fatal("configmap hash annotation should not be present on pod template")
	}

	// Desired spec hash annotation on deployment
	if dep.Annotations[DesiredSpecHashAnnotation] == "" {
		t.Fatal("expected desired-spec-hash annotation on deployment")
	}

	// Init containers: 1 config init + 1 gateway init (because sidecars are present)
	initContainers := dep.Spec.Template.Spec.InitContainers
	if len(initContainers) != 2 {
		t.Fatalf("expected 2 init containers (config + gateway), got %d", len(initContainers))
	}
	if initContainers[0].Name != "init-config" {
		t.Fatalf("expected first init container 'init-config', got %q", initContainers[0].Name)
	}
	if initContainers[1].Name != "init-gateway" {
		t.Fatalf("expected second init container 'init-gateway', got %q", initContainers[1].Name)
	}
	if initContainers[1].Image != DefaultGatewayImage {
		t.Fatalf("expected gateway init image %q, got %q", DefaultGatewayImage, initContainers[1].Image)
	}

	// gateway-bin volume should be present
	if !volumeNames["gateway-bin"] {
		t.Fatal("expected gateway-bin volume when sidecars are present")
	}

	// All sidecars should use the gateway binary from the init container
	for _, c := range containers[1:] {
		if len(c.Command) == 0 || c.Command[0] != "/gateway/capability-gateway" {
			t.Fatalf("sidecar %q should use /gateway/capability-gateway command, got %v", c.Name, c.Command)
		}
	}

	// Logging args
	mainArgs := strings.Join(containers[0].Args, " ")
	if !strings.Contains(mainArgs, "--print-logs") {
		t.Fatal("expected --print-logs in args")
	}
	if !strings.Contains(mainArgs, "--log-level") || !strings.Contains(mainArgs, "INFO") {
		t.Fatal("expected --log-level INFO in args")
	}

	// gh sidecar has GITHUB_TOKEN secret
	ghContainer := containers[3]
	var hasGHToken bool
	for _, e := range ghContainer.Env {
		if e.Name == "GITHUB_TOKEN" && e.ValueFrom != nil {
			hasGHToken = true
		}
	}
	if !hasGHToken {
		t.Fatal("expected GITHUB_TOKEN env in gh sidecar")
	}
}
