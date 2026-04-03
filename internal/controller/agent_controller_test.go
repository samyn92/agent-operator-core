package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/resources"
)

// =============================================================================
// checkCapabilityReadiness TESTS
// =============================================================================

func TestCheckCapabilityReadiness_AllReady(t *testing.T) {
	scheme := newTestScheme()

	cap1 := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "cap-1", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}
	cap2 := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "cap-2", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test",
			MCP:         &agentsv1alpha1.MCPCapabilitySpec{Mode: "local", Command: []string{"test"}},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "cap-1"},
				{Name: "cap-2"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap1, cap2, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 0 {
		t.Fatalf("expected 0 unready, got %d: %v", len(unready), unready)
	}
}

func TestCheckCapabilityReadiness_MissingCapability(t *testing.T) {
	scheme := newTestScheme()

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "nonexistent"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 1 {
		t.Fatalf("expected 1 unready, got %d: %v", len(unready), unready)
	}
	if unready[0] != `Capability "nonexistent" not found` {
		t.Fatalf("unexpected message: %s", unready[0])
	}
}

func TestCheckCapabilityReadiness_PendingCapability(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhasePending},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "pending-cap"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 1 {
		t.Fatalf("expected 1 unready, got %d: %v", len(unready), unready)
	}
}

func TestCheckCapabilityReadiness_FailedCapability(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseFailed},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "failed-cap"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 1 {
		t.Fatalf("expected 1 unready, got %d: %v", len(unready), unready)
	}
}

func TestCheckCapabilityReadiness_MixedStates(t *testing.T) {
	scheme := newTestScheme()

	readyCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "ready-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Ready",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	pendingCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Pending",
			MCP:         &agentsv1alpha1.MCPCapabilitySpec{Mode: "local", Command: []string{"test"}},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhasePending},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "ready-cap"},
				{Name: "pending-cap"},
				{Name: "missing-cap"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(readyCap, pendingCap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 2 {
		t.Fatalf("expected 2 unready (pending + missing), got %d: %v", len(unready), unready)
	}
}

func TestCheckCapabilityReadiness_NoCapabilityRefs(t *testing.T) {
	scheme := newTestScheme()

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model:          "test-model",
			CapabilityRefs: nil,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	unready := r.checkCapabilityReadiness(context.Background(), agent)
	if len(unready) != 0 {
		t.Fatalf("expected 0 unready for agent with no refs, got %d", len(unready))
	}
}

// =============================================================================
// resolveCapabilities TESTS
// =============================================================================

func TestResolveCapabilities_ContainerType(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Read-only kubectl",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image:         "bitnami/kubectl:1.30",
				CommandPrefix: "kubectl ",
				ContainerType: "kubernetes",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Allow: []string{"get *", "describe *"},
				Deny:  []string{"delete *"},
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "kubectl-readonly"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.Sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(resolved.Sidecars))
	}
	if resolved.Sidecars[0].Name != "kubectl-readonly" {
		t.Fatalf("expected sidecar name kubectl-readonly, got %s", resolved.Sidecars[0].Name)
	}
	if resolved.Sidecars[0].Port != int32(resources.SidecarBasePort) {
		t.Fatalf("expected port %d, got %d", resources.SidecarBasePort, resolved.Sidecars[0].Port)
	}

	if len(resolved.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(resolved.Sources))
	}
}

func TestResolveCapabilities_ContainerTypeWithAlias(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Read-only kubectl",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "bitnami/kubectl:1.30",
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "kubectl-readonly", Alias: "k8s"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if resolved.Sidecars[0].Name != "k8s" {
		t.Fatalf("expected alias 'k8s', got %s", resolved.Sidecars[0].Name)
	}
}

func TestResolveCapabilities_MCPType(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-filesystem", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Filesystem MCP",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"},
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "mcp-filesystem", Alias: "filesystem"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	// Should have no sidecars (MCP is not a sidecar)
	if len(resolved.Sidecars) != 0 {
		t.Fatalf("expected 0 sidecars for MCP, got %d", len(resolved.Sidecars))
	}

	// Should have 1 MCP entry
	if len(resolved.MCPEntries) != 1 {
		t.Fatalf("expected 1 MCP entry, got %d", len(resolved.MCPEntries))
	}
	if _, ok := resolved.MCPEntries["filesystem"]; !ok {
		t.Fatal("expected MCP entry keyed by alias 'filesystem'")
	}
}

func TestResolveCapabilities_SkillType_InlineContent(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-incident", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Incident response",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				Content: "# Incident Response\n## Triage\n...",
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "skill-incident"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.SkillFiles) != 1 {
		t.Fatalf("expected 1 skill file, got %d", len(resolved.SkillFiles))
	}
	content, ok := resolved.SkillFiles["skill-incident"]
	if !ok {
		t.Fatal("expected skill file keyed by name")
	}
	if content != "# Incident Response\n## Triage\n..." {
		t.Fatalf("unexpected content: %s", content)
	}
}

func TestResolveCapabilities_SkillType_ConfigMapRef(t *testing.T) {
	scheme := newTestScheme()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-cm", Namespace: "default"},
		Data: map[string]string{
			"SKILL.md": "# Code Review Skill\nReview checklist...",
		},
	}

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-review", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Code review",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				ConfigMapRef: &agentsv1alpha1.ConfigMapKeyRef{
					Name: "skill-cm",
					Key:  "SKILL.md",
				},
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "skill-review"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cm, cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.SkillFiles) != 1 {
		t.Fatalf("expected 1 skill file, got %d", len(resolved.SkillFiles))
	}
	content := resolved.SkillFiles["skill-review"]
	if content != "# Code Review Skill\nReview checklist..." {
		t.Fatalf("unexpected content from configmap: %s", content)
	}
}

func TestResolveCapabilities_ToolType_InlineCode(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "tool-health", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeTool,
			Description: "Health check",
			Tool: &agentsv1alpha1.ToolCapabilitySpec{
				Code: `import { tool } from "@opencode-ai/plugin"; export default tool({...})`,
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "tool-health"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.ToolFiles) != 1 {
		t.Fatalf("expected 1 tool file, got %d", len(resolved.ToolFiles))
	}
}

func TestResolveCapabilities_ToolType_ConfigMapRef(t *testing.T) {
	scheme := newTestScheme()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "tool-cm", Namespace: "default"},
		Data: map[string]string{
			"tool.ts": `import { tool } from "@opencode-ai/plugin"; export default tool({name: "test"})`,
		},
	}

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "tool-from-cm", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeTool,
			Description: "Tool from ConfigMap",
			Tool: &agentsv1alpha1.ToolCapabilitySpec{
				ConfigMapRef: &agentsv1alpha1.ConfigMapKeyRef{
					Name: "tool-cm",
					Key:  "tool.ts",
				},
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "tool-from-cm"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cm, cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.ToolFiles) != 1 {
		t.Fatalf("expected 1 tool file, got %d", len(resolved.ToolFiles))
	}
}

func TestResolveCapabilities_PluginType_InlineCode(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "plugin-audit", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Audit plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				Code: `const plugin = (api) => { api.hook("tool.execute.before", async () => {}) }; export default plugin`,
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "plugin-audit"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.PluginFiles) != 1 {
		t.Fatalf("expected 1 plugin file, got %d", len(resolved.PluginFiles))
	}
	if len(resolved.PluginPackages) != 0 {
		t.Fatalf("expected 0 plugin packages, got %d", len(resolved.PluginPackages))
	}
}

func TestResolveCapabilities_PluginType_Package(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "plugin-npm", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "npm plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				Package: "@company/opencode-plugin-audit",
			},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "plugin-npm"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.PluginFiles) != 0 {
		t.Fatalf("expected 0 plugin files, got %d", len(resolved.PluginFiles))
	}
	if len(resolved.PluginPackages) != 1 {
		t.Fatalf("expected 1 plugin package, got %d", len(resolved.PluginPackages))
	}
	if resolved.PluginPackages[0] != "@company/opencode-plugin-audit" {
		t.Fatalf("unexpected package: %s", resolved.PluginPackages[0])
	}
}

func TestResolveCapabilities_SkipsNotReadyCapabilities(t *testing.T) {
	scheme := newTestScheme()

	readyCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "ready-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Ready",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	pendingCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Pending",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhasePending},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "ready-cap"},
				{Name: "pending-cap"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(readyCap, pendingCap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	// Only ready-cap should be resolved
	if len(resolved.Sidecars) != 1 {
		t.Fatalf("expected 1 sidecar (only ready), got %d", len(resolved.Sidecars))
	}
	if resolved.Sidecars[0].Name != "ready-cap" {
		t.Fatalf("expected ready-cap, got %s", resolved.Sidecars[0].Name)
	}
}

func TestResolveCapabilities_MultipleContainerPorts(t *testing.T) {
	scheme := newTestScheme()

	cap1 := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "cap-1", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeContainer, Description: "Cap 1",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:1"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	cap2 := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "cap-2", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeContainer, Description: "Cap 2",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:2"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	cap3 := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "cap-3", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeContainer, Description: "Cap 3",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:3"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "cap-1"},
				{Name: "cap-2"},
				{Name: "cap-3"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap1, cap2, cap3, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	if len(resolved.Sidecars) != 3 {
		t.Fatalf("expected 3 sidecars, got %d", len(resolved.Sidecars))
	}

	// Verify sequential port assignment
	basePort := int32(resources.SidecarBasePort)
	for i, sidecar := range resolved.Sidecars {
		expectedPort := basePort + int32(i)
		if sidecar.Port != expectedPort {
			t.Fatalf("sidecar %d: expected port %d, got %d", i, expectedPort, sidecar.Port)
		}
	}
}

func TestResolveCapabilities_AllFiveTypes(t *testing.T) {
	scheme := newTestScheme()

	containerCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "container-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeContainer, Description: "Container",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "test:latest"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	mcpCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP, Description: "MCP",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{Mode: "local", Command: []string{"test"}},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	skillCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeSkill, Description: "Skill",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{Content: "# Skill content"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	toolCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "tool-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeTool, Description: "Tool",
			Tool: &agentsv1alpha1.ToolCapabilitySpec{Code: "export default tool({...})"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	pluginCap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "plugin-cap", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypePlugin, Description: "Plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{Package: "@test/plugin"},
		},
		Status: agentsv1alpha1.CapabilityStatus{Phase: agentsv1alpha1.CapabilityPhaseReady},
	}

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "container-cap"},
				{Name: "mcp-cap", Alias: "my-mcp"},
				{Name: "skill-cap", Alias: "my-skill"},
				{Name: "tool-cap", Alias: "my-tool"},
				{Name: "plugin-cap", Alias: "my-plugin"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(containerCap, mcpCap, skillCap, toolCap, pluginCap, agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve failed: %s", err)
	}

	// Verify all 5 types resolved correctly
	if len(resolved.Sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(resolved.Sidecars))
	}
	if len(resolved.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(resolved.Sources))
	}
	if len(resolved.MCPEntries) != 1 {
		t.Fatalf("expected 1 MCP entry, got %d", len(resolved.MCPEntries))
	}
	if _, ok := resolved.MCPEntries["my-mcp"]; !ok {
		t.Fatal("expected MCP entry with alias 'my-mcp'")
	}
	if len(resolved.SkillFiles) != 1 {
		t.Fatalf("expected 1 skill file, got %d", len(resolved.SkillFiles))
	}
	if _, ok := resolved.SkillFiles["my-skill"]; !ok {
		t.Fatal("expected skill file with alias 'my-skill'")
	}
	if len(resolved.ToolFiles) != 1 {
		t.Fatalf("expected 1 tool file, got %d", len(resolved.ToolFiles))
	}
	if _, ok := resolved.ToolFiles["my-tool"]; !ok {
		t.Fatal("expected tool file with alias 'my-tool'")
	}
	if len(resolved.PluginPackages) != 1 {
		t.Fatalf("expected 1 plugin package, got %d", len(resolved.PluginPackages))
	}
}

func TestResolveCapabilities_SkipsMissingCapability(t *testing.T) {
	scheme := newTestScheme()

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "nonexistent"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	resolved, err := r.resolveCapabilities(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve should not error for missing capability: %s", err)
	}

	// Should have empty resolved
	if len(resolved.Sidecars) != 0 || len(resolved.MCPEntries) != 0 || len(resolved.SkillFiles) != 0 || len(resolved.ToolFiles) != 0 || len(resolved.PluginFiles) != 0 || len(resolved.PluginPackages) != 0 {
		t.Fatal("expected empty resolved for missing capability")
	}
}

// =============================================================================
// findAgentsForCapability TESTS
// =============================================================================

func TestFindAgentsForCapability(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cap", Namespace: "default"},
	}

	agent1 := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-1", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model:          "test",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{{Name: "test-cap"}},
		},
	}

	agent2 := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-2", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model:          "test",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{{Name: "other-cap"}},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent1, agent2).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	usedBy, err := r.findAgentsUsingCapability(context.Background(), cap)
	if err != nil {
		t.Fatalf("failed: %s", err)
	}

	if len(usedBy) != 1 {
		t.Fatalf("expected 1 agent using capability, got %d", len(usedBy))
	}
	if usedBy[0] != "agent-1" {
		t.Fatalf("expected agent-1, got %s", usedBy[0])
	}
}

// =============================================================================
// updateStatusWaiting TESTS
// =============================================================================

func TestUpdateStatusWaiting(t *testing.T) {
	scheme := newTestScheme()

	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "test-model",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		WithStatusSubresource(agent).
		Build()

	r := &AgentReconciler{Client: client, Scheme: scheme}

	err := r.updateStatusWaiting(context.Background(), agent, []string{`Capability "missing" not found`})
	if err != nil {
		t.Fatalf("update status failed: %s", err)
	}

	updated := &agentsv1alpha1.Agent{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "test-agent", Namespace: "default"}, updated)

	if updated.Status.Phase != agentsv1alpha1.AgentPhasePending {
		t.Fatalf("expected Pending phase, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// extractRegistryCredentials TESTS
// =============================================================================

func TestExtractRegistryCredentials_SpecificKey(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"token": []byte("my-token-123"),
		},
	}

	creds, err := extractRegistryCredentials(secret, "token")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if creds.Password != "my-token-123" {
		t.Fatalf("expected password 'my-token-123', got %q", creds.Password)
	}
	if creds.Username != "" {
		t.Fatalf("expected empty username, got %q", creds.Username)
	}
}

func TestExtractRegistryCredentials_SpecificKeyNotFound(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"other-key": []byte("value"),
		},
	}

	_, err := extractRegistryCredentials(secret, "token")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestExtractRegistryCredentials_DockerConfigJSON(t *testing.T) {
	dockerConfig := `{"auths":{"ghcr.io":{"username":"user","password":"pass123"}}}`
	secret := &corev1.Secret{
		Data: map[string][]byte{
			".dockerconfigjson": []byte(dockerConfig),
		},
	}

	creds, err := extractRegistryCredentials(secret, "")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if creds.Username != "user" {
		t.Fatalf("expected username 'user', got %q", creds.Username)
	}
	if creds.Password != "pass123" {
		t.Fatalf("expected password 'pass123', got %q", creds.Password)
	}
}

func TestExtractRegistryCredentials_PlainUsernamePassword(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("secret"),
		},
	}

	creds, err := extractRegistryCredentials(secret, "")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if creds.Username != "admin" {
		t.Fatalf("expected username 'admin', got %q", creds.Username)
	}
	if creds.Password != "secret" {
		t.Fatalf("expected password 'secret', got %q", creds.Password)
	}
}

func TestExtractRegistryCredentials_NoRecognizableCredentials(t *testing.T) {
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"something-else": []byte("value"),
		},
	}

	_, err := extractRegistryCredentials(secret, "")
	if err == nil {
		t.Fatal("expected error for unrecognizable credentials")
	}
}

func TestParseDockerConfigJSON_Valid(t *testing.T) {
	data := []byte(`{"auths":{"registry.example.com":{"username":"u","password":"p"}}}`)

	creds, err := parseDockerConfigJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if creds.Username != "u" || creds.Password != "p" {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
}

func TestParseDockerConfigJSON_InvalidJSON(t *testing.T) {
	_, err := parseDockerConfigJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseDockerConfigJSON_EmptyAuths(t *testing.T) {
	_, err := parseDockerConfigJSON([]byte(`{"auths":{}}`))
	if err == nil {
		t.Fatal("expected error for empty auths")
	}
}
