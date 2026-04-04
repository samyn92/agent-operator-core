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
// TOOL REFS — NIL SERVER
// =============================================================================

// newTestMCPCapabilityWithToolRefs creates a capability with toolRefs but NO explicit server spec.
// This exercises the nil-server guard path in MCPServerDeployment().
func newTestMCPCapabilityWithToolRefs(name, namespace string) *agentsv1alpha1.Capability {
	return &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Git tools via tool-bridge",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "server",
				ToolRefs: []agentsv1alpha1.OCIArtifactRef{
					{Ref: "ghcr.io/samyn92/agent-tools/git:0.1.0"},
					{Ref: "ghcr.io/samyn92/agent-tools/file:0.1.0"},
				},
			},
		},
	}
}

func TestMCPServerHasToolRefs(t *testing.T) {
	t.Run("no toolRefs", func(t *testing.T) {
		cap := newTestMCPCapability("git-mcp", "agents")
		if MCPServerHasToolRefs(cap) {
			t.Fatal("expected false for capability without toolRefs")
		}
	})

	t.Run("with toolRefs", func(t *testing.T) {
		cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
		if !MCPServerHasToolRefs(cap) {
			t.Fatal("expected true for capability with toolRefs")
		}
	})

	t.Run("nil MCP spec", func(t *testing.T) {
		cap := &agentsv1alpha1.Capability{
			Spec: agentsv1alpha1.CapabilitySpec{Type: agentsv1alpha1.CapabilityTypeMCP},
		}
		if MCPServerHasToolRefs(cap) {
			t.Fatal("expected false for nil MCP spec")
		}
	})
}

func TestMCPServerDeployment_ToolRefs_NilServer(t *testing.T) {
	// When toolRefs are set without an explicit server spec, the deployment
	// should not panic and should auto-configure the tool-bridge image.
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	if dep.Name != "mcp-git-tools" {
		t.Fatalf("expected name 'mcp-git-tools', got %q", dep.Name)
	}

	podSpec := dep.Spec.Template.Spec

	// Main container should use the tool-bridge image (auto-derived)
	mainContainer := podSpec.Containers[0]
	if mainContainer.Image != DefaultToolBridgeImage {
		t.Fatalf("expected tool-bridge image %q, got %q", DefaultToolBridgeImage, mainContainer.Image)
	}

	// Should have RollingUpdate strategy (no workspace)
	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("expected RollingUpdate strategy, got %v", dep.Spec.Strategy.Type)
	}

	// Default port should be 8080
	if len(mainContainer.Ports) != 1 || mainContainer.Ports[0].ContainerPort != 8080 {
		t.Fatalf("expected port 8080, got %v", mainContainer.Ports)
	}
}

func TestMCPServerDeployment_ToolRefs_AutoCommand(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	// GATEWAY_COMMAND should be the tool-bridge command
	envMap := envToMap(dep.Spec.Template.Spec.Containers[0].Env)

	if envMap["GATEWAY_COMMAND"] != "node /app/dist/tool-bridge.js" {
		t.Fatalf("expected auto-configured tool-bridge command, got %q", envMap["GATEWAY_COMMAND"])
	}
	if envMap["TOOLS_DIR"] != "/tools" {
		t.Fatalf("expected TOOLS_DIR=/tools, got %q", envMap["TOOLS_DIR"])
	}
	if envMap["SERVER_NAME"] != "git-tools" {
		t.Fatalf("expected SERVER_NAME=git-tools, got %q", envMap["SERVER_NAME"])
	}
}

func TestMCPServerDeployment_ToolRefs_ExplicitCommandOverride(t *testing.T) {
	// When both toolRefs and an explicit Command are set, the explicit command wins.
	cap := newTestMCPCapabilityWithToolRefs("custom-bridge", "agents")
	cap.Spec.MCP.Command = []string{"node", "/custom/server.js"}
	cap.Spec.MCP.Server = &agentsv1alpha1.MCPServerDeploymentSpec{
		Image: "custom-image:latest",
	}
	dep := MCPServerDeployment(cap)

	mainContainer := dep.Spec.Template.Spec.Containers[0]

	// Should use explicit image, not tool-bridge
	if mainContainer.Image != "custom-image:latest" {
		t.Fatalf("expected custom image, got %q", mainContainer.Image)
	}

	envMap := envToMap(mainContainer.Env)
	if envMap["GATEWAY_COMMAND"] != "node /custom/server.js" {
		t.Fatalf("expected explicit command, got %q", envMap["GATEWAY_COMMAND"])
	}
}

// =============================================================================
// TOOL REFS — CRANE INIT CONTAINERS
// =============================================================================

func TestMCPServerDeployment_ToolRefs_CraneInitContainers(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have 3 init containers: init-gateway + 2 crane (one per toolRef)
	if len(podSpec.InitContainers) != 3 {
		names := make([]string, len(podSpec.InitContainers))
		for i, c := range podSpec.InitContainers {
			names[i] = c.Name
		}
		t.Fatalf("expected 3 init containers (init-gateway + 2 crane), got %d: %v", len(podSpec.InitContainers), names)
	}

	// First init container should be init-gateway
	if podSpec.InitContainers[0].Name != "init-gateway" {
		t.Fatalf("expected first init container to be 'init-gateway', got %q", podSpec.InitContainers[0].Name)
	}

	// Second init container: crane for git tool
	gitCrane := podSpec.InitContainers[1]
	if gitCrane.Name != "tool-0-git" {
		t.Fatalf("expected crane init container named 'tool-0-git', got %q", gitCrane.Name)
	}
	if gitCrane.Image != DefaultCraneImage {
		t.Fatalf("expected crane image %q, got %q", DefaultCraneImage, gitCrane.Image)
	}

	// Verify crane command includes the OCI ref and target dir
	cmd := gitCrane.Command[2] // "sh -c <cmd>"
	if !strings.Contains(cmd, "ghcr.io/samyn92/agent-tools/git:0.1.0") {
		t.Fatalf("expected crane command to contain OCI ref, got %q", cmd)
	}
	if !strings.Contains(cmd, "/tools/git") {
		t.Fatalf("expected crane command to target /tools/git, got %q", cmd)
	}

	// Third init container: crane for file tool
	fileCrane := podSpec.InitContainers[2]
	if fileCrane.Name != "tool-1-file" {
		t.Fatalf("expected crane init container named 'tool-1-file', got %q", fileCrane.Name)
	}
	if !strings.Contains(fileCrane.Command[2], "/tools/file") {
		t.Fatalf("expected crane command to target /tools/file, got %q", fileCrane.Command[2])
	}

	// Crane containers should mount the tools volume
	for _, crane := range podSpec.InitContainers[1:] {
		found := false
		for _, vm := range crane.VolumeMounts {
			if vm.Name == "tools" && vm.MountPath == "/tools" {
				found = true
			}
		}
		if !found {
			t.Fatalf("crane container %q should mount 'tools' volume at /tools", crane.Name)
		}
	}
}

func TestMCPServerDeployment_ToolRefs_ToolsVolume(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have a 'tools' emptyDir volume
	var toolsVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == "tools" {
			toolsVol = &podSpec.Volumes[i]
			break
		}
	}
	if toolsVol == nil {
		t.Fatal("expected 'tools' volume")
	}
	if toolsVol.EmptyDir == nil {
		t.Fatal("expected 'tools' volume to be an emptyDir")
	}

	// Main container should mount tools as read-only
	var toolsMount *corev1.VolumeMount
	for i, vm := range podSpec.Containers[0].VolumeMounts {
		if vm.Name == "tools" {
			toolsMount = &podSpec.Containers[0].VolumeMounts[i]
			break
		}
	}
	if toolsMount == nil {
		t.Fatal("expected 'tools' volume mount on main container")
	}
	if toolsMount.MountPath != "/tools" {
		t.Fatalf("expected tools mount at /tools, got %q", toolsMount.MountPath)
	}
	if !toolsMount.ReadOnly {
		t.Fatal("expected tools mount to be read-only on main container")
	}
}

func TestMCPServerDeployment_NoToolRefs_NoToolsVolume(t *testing.T) {
	// Standard MCP capability without toolRefs should NOT have tools volume
	cap := newTestMCPCapability("git-mcp", "agents")
	dep := MCPServerDeployment(cap)

	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "tools" {
			t.Fatal("should NOT have 'tools' volume when no toolRefs are configured")
		}
	}
	for _, vm := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if vm.Name == "tools" {
			t.Fatal("should NOT have 'tools' volume mount when no toolRefs are configured")
		}
	}
}

// =============================================================================
// TOOL REFS — PULL SECRETS
// =============================================================================

func TestMCPServerDeployment_ToolRefs_WithPullSecret(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	cap.Spec.MCP.ToolRefs[0].PullSecret = &agentsv1alpha1.SecretKeySelector{
		Name: "registry-creds",
	}
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have a pull-secret volume
	var secretVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == "pull-secret-registry-creds" {
			secretVol = &podSpec.Volumes[i]
			break
		}
	}
	if secretVol == nil {
		t.Fatal("expected 'pull-secret-registry-creds' volume")
	}
	if secretVol.Secret == nil {
		t.Fatal("expected pull-secret volume to be a Secret volume")
	}
	if secretVol.Secret.SecretName != "registry-creds" {
		t.Fatalf("expected secret name 'registry-creds', got %q", secretVol.Secret.SecretName)
	}

	// The first crane init container (git) should have DOCKER_CONFIG env and mount
	gitCrane := podSpec.InitContainers[1]

	// Check DOCKER_CONFIG env
	envMap := envToMap(gitCrane.Env)
	dockerConfig := envMap["DOCKER_CONFIG"]
	if dockerConfig == "" {
		t.Fatal("expected DOCKER_CONFIG env var on crane init container with pull secret")
	}
	if !strings.Contains(dockerConfig, "registry-creds") {
		t.Fatalf("expected DOCKER_CONFIG path to contain secret name, got %q", dockerConfig)
	}

	// Check volume mount
	var secretMount *corev1.VolumeMount
	for i, vm := range gitCrane.VolumeMounts {
		if vm.Name == "pull-secret-registry-creds" {
			secretMount = &gitCrane.VolumeMounts[i]
			break
		}
	}
	if secretMount == nil {
		t.Fatal("expected pull-secret volume mount on crane container")
	}
	if !secretMount.ReadOnly {
		t.Fatal("expected pull-secret mount to be read-only")
	}

	// Second crane init container (file) should NOT have pull secret
	fileCrane := podSpec.InitContainers[2]
	for _, env := range fileCrane.Env {
		if env.Name == "DOCKER_CONFIG" {
			t.Fatal("second crane container should NOT have DOCKER_CONFIG when no pull secret configured")
		}
	}
}

func TestMCPServerDeployment_ToolRefs_SharedPullSecret(t *testing.T) {
	// When multiple toolRefs use the same pull secret, the volume should be deduplicated
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	cap.Spec.MCP.ToolRefs[0].PullSecret = &agentsv1alpha1.SecretKeySelector{Name: "shared-creds"}
	cap.Spec.MCP.ToolRefs[1].PullSecret = &agentsv1alpha1.SecretKeySelector{Name: "shared-creds"}
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Count how many times the pull-secret volume appears
	secretVolCount := 0
	for _, vol := range podSpec.Volumes {
		if vol.Name == "pull-secret-shared-creds" {
			secretVolCount++
		}
	}
	if secretVolCount != 1 {
		t.Fatalf("expected exactly 1 'pull-secret-shared-creds' volume (deduplicated), got %d", secretVolCount)
	}

	// Both crane init containers should have the mount and env
	for _, idx := range []int{1, 2} {
		crane := podSpec.InitContainers[idx]
		envMap := envToMap(crane.Env)
		if envMap["DOCKER_CONFIG"] == "" {
			t.Fatalf("crane container %q should have DOCKER_CONFIG", crane.Name)
		}
	}
}

func TestMCPServerDeployment_ToolRefs_DifferentPullSecrets(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	cap.Spec.MCP.ToolRefs[0].PullSecret = &agentsv1alpha1.SecretKeySelector{Name: "creds-a"}
	cap.Spec.MCP.ToolRefs[1].PullSecret = &agentsv1alpha1.SecretKeySelector{Name: "creds-b"}
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have 2 distinct pull-secret volumes
	secretVolNames := make(map[string]bool)
	for _, vol := range podSpec.Volumes {
		if strings.HasPrefix(vol.Name, "pull-secret-") {
			secretVolNames[vol.Name] = true
		}
	}
	if len(secretVolNames) != 2 {
		t.Fatalf("expected 2 distinct pull-secret volumes, got %d: %v", len(secretVolNames), secretVolNames)
	}
	if !secretVolNames["pull-secret-creds-a"] || !secretVolNames["pull-secret-creds-b"] {
		t.Fatalf("expected pull-secret-creds-a and pull-secret-creds-b, got %v", secretVolNames)
	}
}

// =============================================================================
// TOOL REFS — ENV VARS
// =============================================================================

func TestMCPServerDeployment_ToolRefs_EnvVars(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	cap.Spec.MCP.Environment = map[string]string{
		"GIT_AUTHOR_NAME":  "AI Agent",
		"GIT_AUTHOR_EMAIL": "agent@example.com",
	}
	dep := MCPServerDeployment(cap)

	envMap := envToMap(dep.Spec.Template.Spec.Containers[0].Env)

	// Tool-bridge specific env vars
	if envMap["TOOLS_DIR"] != "/tools" {
		t.Fatalf("expected TOOLS_DIR=/tools, got %q", envMap["TOOLS_DIR"])
	}
	if envMap["SERVER_NAME"] != "git-tools" {
		t.Fatalf("expected SERVER_NAME=git-tools, got %q", envMap["SERVER_NAME"])
	}

	// User-specified env vars should be present
	if envMap["GIT_AUTHOR_NAME"] != "AI Agent" {
		t.Fatalf("expected GIT_AUTHOR_NAME='AI Agent', got %q", envMap["GIT_AUTHOR_NAME"])
	}
	if envMap["GIT_AUTHOR_EMAIL"] != "agent@example.com" {
		t.Fatalf("expected GIT_AUTHOR_EMAIL='agent@example.com', got %q", envMap["GIT_AUTHOR_EMAIL"])
	}

	// Standard env vars should still be present
	if envMap["HOME"] != "/data" {
		t.Fatalf("expected HOME=/data, got %q", envMap["HOME"])
	}
	if envMap["GATEWAY_MODE"] != "mcp" {
		t.Fatalf("expected GATEWAY_MODE=mcp, got %q", envMap["GATEWAY_MODE"])
	}
}

// =============================================================================
// TOOL REFS — WITH WORKSPACE
// =============================================================================

func TestMCPServerDeployment_ToolRefs_WithWorkspace(t *testing.T) {
	// ToolRefs + workspace: should have tools volume AND workspace PVC
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	cap.Spec.MCP.Server = &agentsv1alpha1.MCPServerDeploymentSpec{
		Workspace: &agentsv1alpha1.MCPServerWorkspace{
			Enabled: true,
			Size:    resource.MustParse("20Gi"),
		},
	}
	dep := MCPServerDeployment(cap)

	podSpec := dep.Spec.Template.Spec

	// Should have Recreate strategy (workspace PVC)
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("expected Recreate strategy, got %v", dep.Spec.Strategy.Type)
	}

	// Count expected volumes: gateway-bin, gateway-config, data-home, tmp, workspace, tools
	volNames := make(map[string]bool)
	for _, vol := range podSpec.Volumes {
		volNames[vol.Name] = true
	}
	if !volNames["tools"] {
		t.Fatal("expected 'tools' volume")
	}
	if !volNames["workspace"] {
		t.Fatal("expected 'workspace' volume")
	}

	// Main container should mount both
	mountMap := make(map[string]string)
	for _, m := range podSpec.Containers[0].VolumeMounts {
		mountMap[m.Name] = m.MountPath
	}
	if mountMap["tools"] != "/tools" {
		t.Fatalf("expected tools at /tools, got %q", mountMap["tools"])
	}
	if mountMap["workspace"] != "/data/workspace" {
		t.Fatalf("expected workspace at /data/workspace, got %q", mountMap["workspace"])
	}

	// Should have WORKSPACE env vars
	envMap := envToMap(podSpec.Containers[0].Env)
	if envMap["WORKSPACE_PATH"] != "/data/workspace" {
		t.Fatalf("expected WORKSPACE_PATH=/data/workspace, got %q", envMap["WORKSPACE_PATH"])
	}
	if envMap["WORKSPACE"] != "/data/workspace" {
		t.Fatalf("expected WORKSPACE=/data/workspace, got %q", envMap["WORKSPACE"])
	}
}

// =============================================================================
// TOOL REFS — SECURITY
// =============================================================================

func TestMCPServerDeployment_ToolRefs_CraneSecurityContext(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	// All crane init containers should have hardened security context
	for _, init := range dep.Spec.Template.Spec.InitContainers[1:] {
		sc := init.SecurityContext
		if sc == nil {
			t.Fatalf("crane container %q should have security context", init.Name)
		}
		if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Fatalf("crane container %q should have ReadOnlyRootFilesystem=true", init.Name)
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Fatalf("crane container %q should have AllowPrivilegeEscalation=false", init.Name)
		}
	}
}

func TestMCPServerDeployment_ToolRefs_CraneResources(t *testing.T) {
	cap := newTestMCPCapabilityWithToolRefs("git-tools", "agents")
	dep := MCPServerDeployment(cap)

	for _, init := range dep.Spec.Template.Spec.InitContainers[1:] {
		if init.Resources.Requests == nil {
			t.Fatalf("crane container %q should have resource requests", init.Name)
		}
		if init.Resources.Limits == nil {
			t.Fatalf("crane container %q should have resource limits", init.Name)
		}
	}
}

// =============================================================================
// HELPERS
// =============================================================================

func envToMap(envVars []corev1.EnvVar) map[string]string {
	m := make(map[string]string)
	for _, e := range envVars {
		if e.ValueFrom == nil {
			m[e.Name] = e.Value
		}
	}
	return m
}

func mountNames(mounts []corev1.VolumeMount) []string {
	names := make([]string, len(mounts))
	for i, m := range mounts {
		names[i] = m.Name
	}
	return names
}
