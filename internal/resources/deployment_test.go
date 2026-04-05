package resources

import (
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

// =============================================================================
// getImageConfig TESTS
// =============================================================================

func TestGetImageConfig_Defaults(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	image, initImg, policy := getImageConfig(agent)

	if image != DefaultOpencodeImage {
		t.Fatalf("expected default image %q, got %q", DefaultOpencodeImage, image)
	}
	if initImg != DefaultOpencodeImage {
		t.Fatalf("expected default init image to match opencode image %q, got %q", DefaultOpencodeImage, initImg)
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

	image, initImg, policy := getImageConfig(agent)

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

	_, initImg, _ := getImageConfig(agent)

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

	image, _, policy := getImageConfig(agent)

	if image != "my-registry/opencode:latest" {
		t.Fatalf("expected custom image, got %q", image)
	}
	if policy != corev1.PullAlways {
		t.Fatalf("expected PullAlways, got %v", policy)
	}
}

// =============================================================================
// AgentDeployment TESTS
// =============================================================================

func TestAgentDeployment_BasicStructure(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

	podAnnotations := dep.Spec.Template.Annotations
	if _, ok := podAnnotations[ConfigMapHashAnnotation]; ok {
		t.Fatal("configmap hash annotation should not be present — config reload is handled via volume updates")
	}
}

func TestAgentDeployment_MCPCapabilityHashAnnotation(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	// With an MCP hash, the annotation should be present on the pod template
	dep := AgentDeployment(agent, "", "abc123def456", nil, nil)

	podAnnotations := dep.Spec.Template.Annotations
	hash, ok := podAnnotations[MCPCapabilityHashAnnotation]
	if !ok {
		t.Fatal("expected MCP capability hash annotation on pod template")
	}
	if hash != "abc123def456" {
		t.Fatalf("expected MCP hash 'abc123def456', got %q", hash)
	}
}

func TestAgentDeployment_NoMCPCapabilityHashAnnotation(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	// Without an MCP hash, the annotation should not be present
	dep := AgentDeployment(agent, "", "", nil, nil)

	podAnnotations := dep.Spec.Template.Annotations
	if _, ok := podAnnotations[MCPCapabilityHashAnnotation]; ok {
		t.Fatal("MCP capability hash annotation should not be present when no MCP capabilities")
	}
}

func TestAgentDeployment_MCPCapabilityHashChangesSpecHash(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	// Different MCP hashes should produce different deployment spec hashes,
	// which triggers a rolling restart when MCP capabilities change.
	dep1 := AgentDeployment(agent, "", "hash-v1", nil, nil)
	dep2 := AgentDeployment(agent, "", "hash-v2", nil, nil)

	hash1 := dep1.Annotations[DesiredSpecHashAnnotation]
	hash2 := dep2.Annotations[DesiredSpecHashAnnotation]

	if hash1 == hash2 {
		t.Fatal("different MCP capability hashes should produce different spec hashes (triggers rollout)")
	}
}

func TestAgentDeployment_DesiredSpecHashAnnotation(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	args := strings.Join(main.Args, " ")
	if strings.Contains(args, "--print-logs") {
		t.Fatalf("expected no --print-logs when logging disabled, got %v", main.Args)
	}
}

func TestAgentDeployment_MainContainerEnvVars(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	for _, e := range main.Env {
		if e.Name == "OLLAMA_API_KEY" {
			t.Fatal("should not inject API key env for provider without apiKeySecret")
		}
	}
}

func TestAgentDeployment_DefaultResources(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

	main := dep.Spec.Template.Spec.Containers[0]
	memReq := main.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "512Mi" {
		t.Fatalf("expected custom memory request 512Mi, got %s", memReq.String())
	}
}

func TestAgentDeployment_MainContainerPorts(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

func TestAgentDeployment_InitContainer(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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

	dep := AgentDeployment(agent, "", "", nil, nil)

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
	// opencode.json and AGENTS.md are copied (not symlinked) because the
	// /config volume is only mounted in the init container
	if !strings.Contains(script, "cp /config/opencode.json /data/.config/opencode/opencode.json") {
		t.Fatal("expected cp for opencode.json")
	}
	if !strings.Contains(script, "cp /config/AGENTS.md /data/workspace/AGENTS.md") {
		t.Fatal("expected cp for AGENTS.md")
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

	dep := AgentDeployment(agent, "", "", nil, nil)

	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("expected Recreate strategy with PVC, got %v", dep.Spec.Strategy.Type)
	}
}

func TestAgentDeployment_Strategy_RollingUpdateWithoutPVC(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	// No storage = emptyDir

	dep := AgentDeployment(agent, "", "", nil, nil)

	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("expected RollingUpdate strategy without PVC, got %v", dep.Spec.Strategy.Type)
	}
}

// =============================================================================
// HashDeploymentSpec TESTS
// =============================================================================

func TestHashDeploymentSpec_Deterministic(t *testing.T) {
	agent := newTestAgent("my-agent", "default")

	dep1 := AgentDeployment(agent, "", "", nil, nil)
	dep2 := AgentDeployment(agent, "", "", nil, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 != hash2 {
		t.Fatalf("same deployment spec should produce same hash, got %q vs %q", hash1, hash2)
	}
}

func TestHashDeploymentSpec_DifferentSpecs(t *testing.T) {
	agent1 := newTestAgent("agent-a", "default")
	agent2 := newTestAgent("agent-b", "default")

	dep1 := AgentDeployment(agent1, "", "", nil, nil)
	dep2 := AgentDeployment(agent2, "", "", nil, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 == hash2 {
		t.Fatal("different deployment specs should produce different hashes")
	}
}

func TestHashDeploymentSpec_SameInputsSameHash(t *testing.T) {
	// Without configmap hash in annotations, same agent always produces same spec hash
	agent := newTestAgent("my-agent", "default")

	dep1 := AgentDeployment(agent, "", "", nil, nil)
	dep2 := AgentDeployment(agent, "", "", nil, nil)

	hash1 := HashDeploymentSpec(dep1)
	hash2 := HashDeploymentSpec(dep2)

	if hash1 != hash2 {
		t.Fatal("same agent should produce identical spec hashes")
	}
}

func TestHashDeploymentSpec_Length(t *testing.T) {
	agent := newTestAgent("my-agent", "default")
	dep := AgentDeployment(agent, "", "", nil, nil)

	hash := HashDeploymentSpec(dep)

	// SHA256 hex = 64 characters
	if len(hash) != 64 {
		t.Fatalf("expected 64-char hash, got %d chars: %q", len(hash), hash)
	}
}
