package resources

import (
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

func newTestMCPCapability(name, namespace string) *agentsv1alpha1.Capability {
	return &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test MCP server",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "server",
				Command: []string{"npx", "@cyanheads/git-mcp-server"},
				Server: &agentsv1alpha1.MCPServerDeploymentSpec{
					Image: "node:22-slim",
				},
			},
		},
	}
}

func newTestMCPCapabilityWithWorkspace(name, namespace string) *agentsv1alpha1.Capability {
	cap := newTestMCPCapability(name, namespace)
	cap.Spec.MCP.Server.Workspace = &agentsv1alpha1.MCPServerWorkspace{
		Enabled: true,
		Size:    resource.MustParse("20Gi"),
	}
	return cap
}

// =============================================================================
// NAME / URL HELPERS
// =============================================================================

func TestMCPServerDeploymentName(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	name := MCPServerDeploymentName(cap)
	if name != "mcp-git-mcp" {
		t.Fatalf("expected 'mcp-git-mcp', got %q", name)
	}
}

func TestMCPServerWorkspacePVCName(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	name := MCPServerWorkspacePVCName(cap)
	if name != "mcp-git-mcp-workspace" {
		t.Fatalf("expected 'mcp-git-mcp-workspace', got %q", name)
	}
}

func TestMCPServerServiceURL(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	url := MCPServerServiceURL(cap)
	if url != "http://mcp-git-mcp.agents.svc:8080/sse" {
		t.Fatalf("expected 'http://mcp-git-mcp.agents.svc:8080/sse', got %q", url)
	}
}

func TestMCPServerServiceURL_CustomPort(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	cap.Spec.MCP.Server.Port = 3015
	url := MCPServerServiceURL(cap)
	if url != "http://mcp-git-mcp.agents.svc:3015/sse" {
		t.Fatalf("expected port 3015 in URL, got %q", url)
	}
}

func TestMCPServerHasWorkspace(t *testing.T) {
	t.Run("no workspace", func(t *testing.T) {
		cap := newTestMCPCapability("git-mcp", "agents")
		if MCPServerHasWorkspace(cap) {
			t.Fatal("expected false for capability without workspace")
		}
	})

	t.Run("workspace disabled", func(t *testing.T) {
		cap := newTestMCPCapability("git-mcp", "agents")
		cap.Spec.MCP.Server.Workspace = &agentsv1alpha1.MCPServerWorkspace{
			Enabled: false,
		}
		if MCPServerHasWorkspace(cap) {
			t.Fatal("expected false for capability with workspace disabled")
		}
	})

	t.Run("workspace enabled", func(t *testing.T) {
		cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
		if !MCPServerHasWorkspace(cap) {
			t.Fatal("expected true for capability with workspace enabled")
		}
	})

	t.Run("nil MCP spec", func(t *testing.T) {
		cap := &agentsv1alpha1.Capability{
			Spec: agentsv1alpha1.CapabilitySpec{Type: agentsv1alpha1.CapabilityTypeMCP},
		}
		if MCPServerHasWorkspace(cap) {
			t.Fatal("expected false for nil MCP spec")
		}
	})
}

// =============================================================================
// WORKSPACE PVC
// =============================================================================

func TestMCPServerWorkspacePVC(t *testing.T) {
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	pvc := MCPServerWorkspacePVC(cap)

	if pvc.Name != "mcp-git-mcp-workspace" {
		t.Fatalf("expected PVC name 'mcp-git-mcp-workspace', got %q", pvc.Name)
	}
	if pvc.Namespace != "agents" {
		t.Fatalf("expected namespace 'agents', got %q", pvc.Namespace)
	}

	// Must be ReadWriteMany for cross-pod sharing
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Fatalf("expected ReadWriteMany access mode, got %v", pvc.Spec.AccessModes)
	}

	// Check size
	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if size.String() != "20Gi" {
		t.Fatalf("expected 20Gi storage, got %s", size.String())
	}

	// No storage class specified — should be nil (cluster default)
	if pvc.Spec.StorageClassName != nil {
		t.Fatalf("expected nil storage class, got %q", *pvc.Spec.StorageClassName)
	}

	// Labels
	if pvc.Labels["agents.io/capability"] != "git-mcp" {
		t.Fatalf("expected capability label 'git-mcp', got %q", pvc.Labels["agents.io/capability"])
	}
}

func TestMCPServerWorkspacePVC_DefaultSize(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	cap.Spec.MCP.Server.Workspace = &agentsv1alpha1.MCPServerWorkspace{
		Enabled: true,
		// Size not set — should default to 10Gi
	}
	pvc := MCPServerWorkspacePVC(cap)

	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if size.String() != "10Gi" {
		t.Fatalf("expected default 10Gi storage, got %s", size.String())
	}
}

func TestMCPServerWorkspacePVC_CustomStorageClass(t *testing.T) {
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	cap.Spec.MCP.Server.Workspace.StorageClass = "nfs-client"
	pvc := MCPServerWorkspacePVC(cap)

	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "nfs-client" {
		t.Fatalf("expected storage class 'nfs-client', got %v", pvc.Spec.StorageClassName)
	}
}

// =============================================================================
// DEPLOYMENT — WITHOUT WORKSPACE
// =============================================================================

func TestMCPServerDeployment_Basic(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	if dep.Name != "mcp-git-mcp" {
		t.Fatalf("expected name 'mcp-git-mcp', got %q", dep.Name)
	}
	if *dep.Spec.Replicas != 1 {
		t.Fatalf("expected 1 replica, got %d", *dep.Spec.Replicas)
	}

	// Should have RollingUpdate strategy (no workspace)
	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("expected RollingUpdate strategy, got %v", dep.Spec.Strategy.Type)
	}

	// Should have 2 containers: init-gateway + mcp-server
	podSpec := dep.Spec.Template.Spec
	if len(podSpec.InitContainers) != 1 || podSpec.InitContainers[0].Name != "init-gateway" {
		t.Fatalf("expected 1 init container 'init-gateway', got %v", podSpec.InitContainers)
	}
	if len(podSpec.Containers) != 1 || podSpec.Containers[0].Name != "mcp-server" {
		t.Fatalf("expected 1 container 'mcp-server', got %v", podSpec.Containers)
	}

	// Main container should have gateway-bin, gateway-config, data-home, and tmp volume mounts (always present)
	mounts := podSpec.Containers[0].VolumeMounts
	if len(mounts) != 4 {
		t.Fatalf("expected 4 volume mounts (gateway-bin, gateway-config, data-home, tmp), got %d: %v", len(mounts), mountNames(mounts))
	}

	// 4 volumes: gateway-bin + gateway-config + data-home + tmp
	if len(podSpec.Volumes) != 4 {
		t.Fatalf("expected 4 volumes, got %d volumes", len(podSpec.Volumes))
	}

	// No WORKSPACE_PATH env var
	for _, env := range podSpec.Containers[0].Env {
		if env.Name == "WORKSPACE_PATH" {
			t.Fatal("should NOT have WORKSPACE_PATH env var without workspace")
		}
	}

	// HOME env var should always be set (writable HOME for npm cache, etc.)
	var home string
	for _, env := range podSpec.Containers[0].Env {
		if env.Name == "HOME" {
			home = env.Value
		}
	}
	if home != "/data" {
		t.Fatalf("expected HOME=/data even without workspace, got %q", home)
	}
}

// =============================================================================
// DEPLOYMENT — WITH WORKSPACE
// =============================================================================

func TestMCPServerDeployment_WithWorkspace(t *testing.T) {
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have Recreate strategy (workspace PVC)
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("expected Recreate strategy with workspace, got %v", dep.Spec.Strategy.Type)
	}

	// Should have 5 volumes: gateway-bin, gateway-config, workspace (PVC), data-home, tmp
	if len(podSpec.Volumes) != 5 {
		t.Fatalf("expected 5 volumes with workspace, got %d", len(podSpec.Volumes))
	}

	// Check workspace volume references the PVC
	var workspaceVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == "workspace" {
			workspaceVol = &podSpec.Volumes[i]
			break
		}
	}
	if workspaceVol == nil {
		t.Fatal("expected 'workspace' volume")
	}
	if workspaceVol.PersistentVolumeClaim == nil {
		t.Fatal("expected workspace to be a PVC volume")
	}
	if workspaceVol.PersistentVolumeClaim.ClaimName != "mcp-git-mcp-workspace" {
		t.Fatalf("expected PVC claim 'mcp-git-mcp-workspace', got %q", workspaceVol.PersistentVolumeClaim.ClaimName)
	}

	// Check main container has gateway-config, workspace, data-home, and tmp volume mounts
	mounts := podSpec.Containers[0].VolumeMounts
	if len(mounts) != 5 {
		t.Fatalf("expected 5 volume mounts with workspace, got %d: %v", len(mounts), mountNames(mounts))
	}

	mountMap := make(map[string]corev1.VolumeMount)
	for _, m := range mounts {
		mountMap[m.Name] = m
	}

	if ws, ok := mountMap["workspace"]; !ok || ws.MountPath != "/data/workspace" {
		t.Fatalf("expected workspace mount at /data/workspace, got %v", mountMap["workspace"])
	}
	if dh, ok := mountMap["data-home"]; !ok || dh.MountPath != "/data" {
		t.Fatalf("expected data-home mount at /data, got %v", mountMap["data-home"])
	}
	if tmp, ok := mountMap["tmp"]; !ok || tmp.MountPath != "/tmp" {
		t.Fatalf("expected tmp mount at /tmp, got %v", mountMap["tmp"])
	}

	// Check WORKSPACE_PATH env var
	var workspacePath string
	for _, env := range podSpec.Containers[0].Env {
		if env.Name == "WORKSPACE_PATH" {
			workspacePath = env.Value
		}
	}
	if workspacePath != "/data/workspace" {
		t.Fatalf("expected WORKSPACE_PATH=/data/workspace, got %q", workspacePath)
	}

	// Check HOME env var
	var home string
	for _, env := range podSpec.Containers[0].Env {
		if env.Name == "HOME" {
			home = env.Value
		}
	}
	if home != "/data" {
		t.Fatalf("expected HOME=/data, got %q", home)
	}
}

func TestMCPServerDeployment_CustomMountPath(t *testing.T) {
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	cap.Spec.MCP.Server.Workspace.MountPath = "/workspace"
	dep := MCPServerDeployment(cap)

	mounts := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	mountMap := make(map[string]corev1.VolumeMount)
	for _, m := range mounts {
		mountMap[m.Name] = m
	}

	if ws, ok := mountMap["workspace"]; !ok || ws.MountPath != "/workspace" {
		t.Fatalf("expected workspace mount at /workspace, got %v", mountMap["workspace"])
	}

	var workspacePath string
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "WORKSPACE_PATH" {
			workspacePath = env.Value
		}
	}
	if workspacePath != "/workspace" {
		t.Fatalf("expected WORKSPACE_PATH=/workspace, got %q", workspacePath)
	}
}

func TestMCPServerDeployment_WithSecrets(t *testing.T) {
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	cap.Spec.Secrets = []agentsv1alpha1.SecretEnvVar{
		{
			Name: "GITLAB_TOKEN",
			ValueFrom: agentsv1alpha1.SecretKeySelector{
				Name: "gitlab-token",
				Key:  "token",
			},
		},
	}
	dep := MCPServerDeployment(cap)

	var found bool
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITLAB_TOKEN" {
			found = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Fatal("expected secret key ref for GITLAB_TOKEN")
			}
			if env.ValueFrom.SecretKeyRef.Name != "gitlab-token" {
				t.Fatalf("expected secret name 'gitlab-token', got %q", env.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	if !found {
		t.Fatal("expected GITLAB_TOKEN env var")
	}
}

func TestMCPServerDeployment_SecurityContext(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	podSec := dep.Spec.Template.Spec.SecurityContext
	if podSec == nil {
		t.Fatal("expected pod security context")
	}
	if !*podSec.RunAsNonRoot {
		t.Fatal("expected RunAsNonRoot=true")
	}
	if *podSec.RunAsUser != 1000 {
		t.Fatalf("expected RunAsUser=1000, got %d", *podSec.RunAsUser)
	}
	if *podSec.FSGroup != 1000 {
		t.Fatalf("expected FSGroup=1000, got %d", *podSec.FSGroup)
	}
}

// =============================================================================
// DEPLOYMENT — SERVICE ACCOUNT
// =============================================================================

func TestMCPServerDeployment_WorkingDir(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	mainContainer := dep.Spec.Template.Spec.Containers[0]
	if mainContainer.WorkingDir != "/data" {
		t.Fatalf("expected WorkingDir '/data', got %q", mainContainer.WorkingDir)
	}
}

func TestMCPServerDeployment_NoServiceAccount(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	if dep.Spec.Template.Spec.ServiceAccountName != "" {
		t.Fatalf("expected empty ServiceAccountName, got %q", dep.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestMCPServerDeployment_WithServiceAccount(t *testing.T) {
	cap := newTestMCPCapability("k8s-mcp", "agents")
	cap.Spec.MCP.Server.ServiceAccountName = "k8s-readonly"
	dep := MCPServerDeployment(cap)

	if dep.Spec.Template.Spec.ServiceAccountName != "k8s-readonly" {
		t.Fatalf("expected ServiceAccountName 'k8s-readonly', got %q", dep.Spec.Template.Spec.ServiceAccountName)
	}
}

// =============================================================================
// AGENT DEPLOYMENT — MCP WORKSPACE VOLUME MOUNTS
// =============================================================================

func TestAgentDeployment_MCPWorkspace(t *testing.T) {
	agent := newTestAgent("my-agent", "agents")

	workspaces := []MCPWorkspaceInfo{
		{PVCName: "mcp-git-mcp-workspace", MountPath: "/data/workspace"},
	}

	dep := AgentDeployment(agent, "", "", nil, workspaces)

	// Should have the MCP workspace PVC volume
	var found bool
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "mcp-workspace-0" && vol.PersistentVolumeClaim != nil {
			if vol.PersistentVolumeClaim.ClaimName == "mcp-git-mcp-workspace" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected mcp-workspace-0 volume with PVC claim 'mcp-git-mcp-workspace'")
	}

	// Main container should have the workspace volume mount
	mainContainer := dep.Spec.Template.Spec.Containers[0]
	var mountFound bool
	for _, m := range mainContainer.VolumeMounts {
		if m.Name == "mcp-workspace-0" && m.MountPath == "/data/workspace" {
			mountFound = true
		}
	}
	if !mountFound {
		t.Fatal("expected mcp-workspace-0 volume mount at /data/workspace in main container")
	}
}

func TestAgentDeployment_MultipleMCPWorkspaces(t *testing.T) {
	agent := newTestAgent("my-agent", "agents")

	workspaces := []MCPWorkspaceInfo{
		{PVCName: "mcp-git-mcp-workspace", MountPath: "/data/workspace"},
		{PVCName: "mcp-terraform-workspace", MountPath: "/data/terraform"},
	}

	dep := AgentDeployment(agent, "", "", nil, workspaces)

	mainContainer := dep.Spec.Template.Spec.Containers[0]
	mountMap := make(map[string]string)
	for _, m := range mainContainer.VolumeMounts {
		mountMap[m.Name] = m.MountPath
	}

	if mountMap["mcp-workspace-0"] != "/data/workspace" {
		t.Fatalf("expected mcp-workspace-0 at /data/workspace, got %q", mountMap["mcp-workspace-0"])
	}
	if mountMap["mcp-workspace-1"] != "/data/terraform" {
		t.Fatalf("expected mcp-workspace-1 at /data/terraform, got %q", mountMap["mcp-workspace-1"])
	}
}

func TestAgentDeployment_NoMCPWorkspace(t *testing.T) {
	agent := newTestAgent("my-agent", "agents")
	dep := AgentDeployment(agent, "", "", nil, nil)

	// Should NOT have any mcp-workspace volumes
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "mcp-workspace-0" {
			t.Fatal("should NOT have mcp-workspace volume when no workspaces are configured")
		}
	}
}

// =============================================================================
// SERVICE
// =============================================================================

func TestMCPServerService(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	svc := MCPServerService(cap)

	if svc.Name != "mcp-git-mcp" {
		t.Fatalf("expected name 'mcp-git-mcp', got %q", svc.Name)
	}
	if svc.Namespace != "agents" {
		t.Fatalf("expected namespace 'agents', got %q", svc.Namespace)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 {
		t.Fatalf("expected port 8080, got %v", svc.Spec.Ports)
	}
	if svc.Spec.Selector["agents.io/capability"] != "git-mcp" {
		t.Fatalf("expected capability selector 'git-mcp', got %q", svc.Spec.Selector["agents.io/capability"])
	}
}

// =============================================================================
// CONFIGMAP
// =============================================================================

func TestMCPServerConfigMapName(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	name := MCPServerConfigMapName(cap)
	if name != "mcp-git-mcp-config" {
		t.Fatalf("expected 'mcp-git-mcp-config', got %q", name)
	}
}

func TestMCPServerConfigMap_WithDenyRules(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	cap.Spec.Permissions = &agentsv1alpha1.CapabilityPermissions{
		Deny: []string{
			"git_push",
			"git_push:remote=upstream",
			"git_force_push",
			"git_reset:*=--hard",
		},
	}

	cm := MCPServerConfigMap(cap)

	if cm.Name != "mcp-git-mcp-config" {
		t.Fatalf("expected ConfigMap name 'mcp-git-mcp-config', got %q", cm.Name)
	}
	if cm.Namespace != "agents" {
		t.Fatalf("expected namespace 'agents', got %q", cm.Namespace)
	}

	// Check labels
	if cm.Labels["agents.io/capability"] != "git-mcp" {
		t.Fatalf("expected capability label 'git-mcp', got %q", cm.Labels["agents.io/capability"])
	}

	// Check deny rules content
	rules, ok := cm.Data["mcp-deny-rules"]
	if !ok {
		t.Fatal("expected 'mcp-deny-rules' key in ConfigMap data")
	}

	expected := "git_push\ngit_push:remote=upstream\ngit_force_push\ngit_reset:*=--hard"
	if rules != expected {
		t.Fatalf("expected deny rules:\n%s\ngot:\n%s", expected, rules)
	}
}

func TestMCPServerConfigMap_NoDenyRules(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	// No permissions set

	cm := MCPServerConfigMap(cap)

	if _, ok := cm.Data["mcp-deny-rules"]; ok {
		t.Fatal("should NOT have 'mcp-deny-rules' key when no deny rules configured")
	}
}

func TestMCPServerConfigMap_EmptyDenyList(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	cap.Spec.Permissions = &agentsv1alpha1.CapabilityPermissions{
		Deny: []string{},
	}

	cm := MCPServerConfigMap(cap)

	if _, ok := cm.Data["mcp-deny-rules"]; ok {
		t.Fatal("should NOT have 'mcp-deny-rules' key when deny list is empty")
	}
}

func TestMCPServerConfigMap_AllowWithoutDeny(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	cap.Spec.Permissions = &agentsv1alpha1.CapabilityPermissions{
		Allow: []string{"git_status", "git_log"},
		// No deny rules — ConfigMap should still be created but without deny rules key
	}

	cm := MCPServerConfigMap(cap)

	if _, ok := cm.Data["mcp-deny-rules"]; ok {
		t.Fatal("should NOT have 'mcp-deny-rules' key when only allow rules are set")
	}
}

// =============================================================================
// DEPLOYMENT — CONFIGMAP VOLUME MOUNT
// =============================================================================

func TestMCPServerDeployment_HasConfigMapVolume(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have gateway-config volume referencing the ConfigMap
	var configVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == "gateway-config" {
			configVol = &podSpec.Volumes[i]
			break
		}
	}
	if configVol == nil {
		t.Fatal("expected 'gateway-config' volume")
	}
	if configVol.ConfigMap == nil {
		t.Fatal("expected gateway-config to be a ConfigMap volume")
	}
	if configVol.ConfigMap.Name != "mcp-git-mcp-config" {
		t.Fatalf("expected ConfigMap name 'mcp-git-mcp-config', got %q", configVol.ConfigMap.Name)
	}
	if configVol.ConfigMap.Optional == nil || !*configVol.ConfigMap.Optional {
		t.Fatal("expected ConfigMap to be optional")
	}
}

func TestMCPServerDeployment_HasConfigMapMount(t *testing.T) {
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	mounts := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	mountMap := make(map[string]corev1.VolumeMount)
	for _, m := range mounts {
		mountMap[m.Name] = m
	}

	configMount, ok := mountMap["gateway-config"]
	if !ok {
		t.Fatal("expected 'gateway-config' volume mount in mcp-server container")
	}
	if configMount.MountPath != "/etc/tool" {
		t.Fatalf("expected config mount at /etc/tool, got %q", configMount.MountPath)
	}
	if !configMount.ReadOnly {
		t.Fatal("expected config mount to be read-only")
	}
}

func TestMCPServerDeployment_ConfigMapWithWorkspace(t *testing.T) {
	// Ensure the ConfigMap volume is present even when workspace is enabled
	cap := newTestMCPCapabilityWithWorkspace("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have both gateway-config and workspace volumes
	volNames := make(map[string]bool)
	for _, vol := range podSpec.Volumes {
		volNames[vol.Name] = true
	}

	if !volNames["gateway-config"] {
		t.Fatal("expected 'gateway-config' volume even with workspace enabled")
	}
	if !volNames["workspace"] {
		t.Fatal("expected 'workspace' volume")
	}
	if !volNames["gateway-bin"] {
		t.Fatal("expected 'gateway-bin' volume")
	}

	// Mounts should include both config and workspace
	mountNames := make(map[string]bool)
	for _, m := range podSpec.Containers[0].VolumeMounts {
		mountNames[m.Name] = true
	}
	if !mountNames["gateway-config"] {
		t.Fatal("expected 'gateway-config' mount even with workspace enabled")
	}
	if !mountNames["workspace"] {
		t.Fatal("expected 'workspace' mount")
	}
}

// =============================================================================
// HELPERS
// =============================================================================

func mountNames(mounts []corev1.VolumeMount) []string {
	names := make([]string, len(mounts))
	for i, m := range mounts {
		names[i] = m.Name
	}
	return names
}
