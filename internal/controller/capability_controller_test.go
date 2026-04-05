package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// newTestScheme creates a scheme with all required types registered
func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = agentsv1alpha1.AddToScheme(s)
	return s
}

// =============================================================================
// VALIDATION TESTS
// =============================================================================

func TestValidateCapability_MissingDescription(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "test:latest",
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected validation error for missing description")
	}
	if err.Error() != "spec.description is required" {
		t.Fatalf("unexpected error: %s", err.Error())
	}
}

func TestValidateCapability_UnknownType(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityType("Invalid"),
			Description: "test",
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected validation error for unknown type")
	}
}

// --- Container type validation ---

func TestValidateContainerCapability_Valid(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test container capability",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image:         "bitnami/kubectl:1.30",
				CommandPrefix: "kubectl ",
				ContainerType: "kubernetes",
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateContainerCapability_MissingSpec(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for missing container spec")
	}
	if err.Error() != "spec.container is required when type is Container" {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestValidateContainerCapability_MissingImage(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
	if err.Error() != "spec.container.image is required" {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestValidateContainerCapability_EmptyAllowPattern(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "test:latest",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Allow: []string{"get *", ""},
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for empty allow pattern")
	}
}

func TestValidateContainerCapability_EmptyDenyPattern(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "test:latest",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Deny: []string{""},
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for empty deny pattern")
	}
}

func TestValidateContainerCapability_EmptyApprovePattern(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "test:latest",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Approve: []agentsv1alpha1.ApprovalRule{
					{Pattern: ""},
				},
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for empty approve pattern")
	}
}

func TestValidateContainerCapability_ValidPermissions(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "test:latest",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Allow: []string{"get *", "describe *"},
				Approve: []agentsv1alpha1.ApprovalRule{
					{Pattern: "apply *", Message: "This modifies resources", Severity: "warning"},
				},
				Deny: []string{"delete *"},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error for valid permissions, got: %s", err)
	}
}

// --- MCP type validation ---

func TestValidateMCPCapability_ValidLocal(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Filesystem MCP server",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateMCPCapability_ValidRemote(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Remote MCP server",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "remote",
				URL:  "https://mcp.example.com/sse",
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateMCPCapability_MissingSpec(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test",
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for missing MCP spec")
	}
	if err.Error() != "spec.mcp is required when type is MCP" {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestValidateMCPCapability_LocalMissingCommand(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "local",
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for local MCP missing command")
	}
}

func TestValidateMCPCapability_RemoteMissingURL(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "remote",
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for remote MCP missing URL")
	}
}

func TestValidateMCPCapability_InvalidMode(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Test",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "invalid",
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for invalid MCP mode")
	}
}

// --- Skill type validation ---

func TestValidateSkillCapability_ValidInlineContent(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Incident responder skill",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				Content: "---\nname: incident-responder\n---\n# Incident Response",
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateSkillCapability_ValidConfigMapRef(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Code review skill",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				ConfigMapRef: &agentsv1alpha1.ConfigMapKeyRef{
					Name: "code-review-skill",
					Key:  "SKILL.md",
				},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateSkillCapability_MissingSpec(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Test",
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for missing skill spec")
	}
}

func TestValidateSkillCapability_NoContentOrRef(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Test",
			Skill:       &agentsv1alpha1.SkillCapabilitySpec{},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for skill with no content or configMapRef")
	}
}

// --- Plugin type validation ---

func TestValidatePluginCapability_ValidInlineCode(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Audit logging plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				Code: `const auditLog = (api) => { api.hook("tool.execute.before", async () => {}) }; export default auditLog`,
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidatePluginCapability_ValidConfigMapRef(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Custom plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				ConfigMapRef: &agentsv1alpha1.ConfigMapKeyRef{
					Name: "my-plugin",
					Key:  "plugin.ts",
				},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidatePluginCapability_ValidPackage(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "npm plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				Package: "@company/opencode-plugin-audit",
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidatePluginCapability_MissingSpec(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Test",
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for missing plugin spec")
	}
}

func TestValidatePluginCapability_NoCodeRefOrPackage(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Test",
			Plugin:      &agentsv1alpha1.PluginCapabilitySpec{},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for plugin with no code, configMapRef, or package")
	}
}

// --- OCI Ref validation tests ---

func TestValidateSkillCapability_ValidOCIRef(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Skill from OCI artifact",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				OCIRef: &agentsv1alpha1.OCIArtifactRef{
					Ref: "ghcr.io/org/skills/incident-response:1.0.0",
				},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateSkillCapability_ValidOCIRefWithDigest(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Skill pinned by digest",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				OCIRef: &agentsv1alpha1.OCIArtifactRef{
					Ref:    "ghcr.io/org/skills/incident-response:1.0.0",
					Digest: "sha256:abc123def456",
				},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateSkillCapability_OCIRefInvalidFormat(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Bad ref",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				OCIRef: &agentsv1alpha1.OCIArtifactRef{
					Ref: "no-slash-in-ref",
				},
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for invalid OCI ref format")
	}
}

func TestValidateSkillCapability_OCIRefInvalidDigest(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Bad digest",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				OCIRef: &agentsv1alpha1.OCIArtifactRef{
					Ref:    "ghcr.io/org/skills/test:1.0",
					Digest: "invalid-digest-no-colon",
				},
			},
		},
	}

	err := r.validateCapability(cap)
	if err == nil {
		t.Fatal("expected error for invalid digest format")
	}
}

func TestValidatePluginCapability_ValidOCIRef(t *testing.T) {
	r := &CapabilityReconciler{}
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Plugin from OCI artifact",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				OCIRef: &agentsv1alpha1.OCIArtifactRef{
					Ref: "ghcr.io/org/plugins/audit:2.0.0",
				},
			},
		},
	}

	if err := r.validateCapability(cap); err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

func TestValidateOCIRef_EmptyRef(t *testing.T) {
	err := validateOCIRef(&agentsv1alpha1.OCIArtifactRef{Ref: ""})
	if err == nil {
		t.Fatal("expected error for empty ref")
	}
}

func TestValidateOCIRef_NoSlash(t *testing.T) {
	err := validateOCIRef(&agentsv1alpha1.OCIArtifactRef{Ref: "invalid"})
	if err == nil {
		t.Fatal("expected error for ref without slash")
	}
}

func TestValidateOCIRef_InvalidDigest(t *testing.T) {
	err := validateOCIRef(&agentsv1alpha1.OCIArtifactRef{
		Ref:    "ghcr.io/org/test:1.0",
		Digest: "nocolon",
	})
	if err == nil {
		t.Fatal("expected error for digest without colon")
	}
}

func TestValidateOCIRef_Valid(t *testing.T) {
	err := validateOCIRef(&agentsv1alpha1.OCIArtifactRef{
		Ref:    "ghcr.io/org/test:1.0",
		Digest: "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
}

// =============================================================================
// RECONCILIATION TESTS (using fake client)
// =============================================================================

func TestCapabilityReconcile_ValidContainer_BecomesReady(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-container",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Test container capability",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "bitnami/kubectl:1.30",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-container", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %s", err)
	}
	if result.Requeue {
		t.Fatal("unexpected requeue")
	}

	// Verify status was updated to Ready
	updated := &agentsv1alpha1.Capability{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "test-container", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated capability: %s", err)
	}
	if updated.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
		t.Fatalf("expected phase Ready, got %s", updated.Status.Phase)
	}
}

func TestCapabilityReconcile_ValidMCP_BecomesReady(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeMCP,
			Description: "Filesystem MCP server",
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-mcp", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %s", err)
	}

	updated := &agentsv1alpha1.Capability{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "test-mcp", Namespace: "default"}, updated)
	if updated.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
		t.Fatalf("expected phase Ready, got %s", updated.Status.Phase)
	}
}

func TestCapabilityReconcile_ValidSkill_BecomesReady(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-skill",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeSkill,
			Description: "Incident responder",
			Skill: &agentsv1alpha1.SkillCapabilitySpec{
				Content: "# Incident Response Skill\n...",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-skill", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %s", err)
	}

	updated := &agentsv1alpha1.Capability{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "test-skill", Namespace: "default"}, updated)
	if updated.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
		t.Fatalf("expected phase Ready, got %s", updated.Status.Phase)
	}
}

func TestCapabilityReconcile_ValidPlugin_BecomesReady(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-plugin",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypePlugin,
			Description: "Audit plugin",
			Plugin: &agentsv1alpha1.PluginCapabilitySpec{
				Package: "@company/opencode-plugin-audit",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-plugin", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %s", err)
	}

	updated := &agentsv1alpha1.Capability{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "test-plugin", Namespace: "default"}, updated)
	if updated.Status.Phase != agentsv1alpha1.CapabilityPhaseReady {
		t.Fatalf("expected phase Ready, got %s", updated.Status.Phase)
	}
}

func TestCapabilityReconcile_InvalidSpec_BecomesFailed(t *testing.T) {
	scheme := newTestScheme()

	// Container capability with missing image
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-invalid",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Invalid capability",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-invalid", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile should not return error (validation failures are recorded in status): %s", err)
	}
	if result.Requeue {
		t.Fatal("should not requeue on validation failure")
	}

	updated := &agentsv1alpha1.Capability{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "test-invalid", Namespace: "default"}, updated)
	if updated.Status.Phase != agentsv1alpha1.CapabilityPhaseFailed {
		t.Fatalf("expected phase Failed, got %s", updated.Status.Phase)
	}
}

func TestCapabilityReconcile_NotFound_NoError(t *testing.T) {
	scheme := newTestScheme()

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error for not found, got: %s", err)
	}
	if result.Requeue {
		t.Fatal("should not requeue for not found")
	}
}

func TestCapabilityReconcile_UsedByTracking(t *testing.T) {
	scheme := newTestScheme()

	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubectl-readonly",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Read-only kubectl",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "bitnami/kubectl:1.30",
			},
		},
	}

	agent1 := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-1",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "anthropic/claude-sonnet-4-20250514",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "kubectl-readonly"},
			},
		},
	}

	agent2 := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-2",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "anthropic/claude-sonnet-4-20250514",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "kubectl-readonly"},
				{Name: "some-other-cap"},
			},
		},
	}

	agentOther := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-other",
			Namespace: "default",
		},
		Spec: agentsv1alpha1.AgentSpec{
			Model: "anthropic/claude-sonnet-4-20250514",
			CapabilityRefs: []agentsv1alpha1.CapabilityRef{
				{Name: "some-other-cap"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cap, agent1, agent2, agentOther).
		WithStatusSubresource(cap).
		Build()

	r := &CapabilityReconciler{Client: client, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kubectl-readonly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %s", err)
	}

	updated := &agentsv1alpha1.Capability{}
	_ = client.Get(context.Background(), types.NamespacedName{Name: "kubectl-readonly", Namespace: "default"}, updated)

	if len(updated.Status.UsedBy) != 2 {
		t.Fatalf("expected 2 agents in usedBy, got %d: %v", len(updated.Status.UsedBy), updated.Status.UsedBy)
	}
	// Check that both agent-1 and agent-2 are in the list
	found := map[string]bool{}
	for _, name := range updated.Status.UsedBy {
		found[name] = true
	}
	if !found["agent-1"] || !found["agent-2"] {
		t.Fatalf("expected usedBy to contain agent-1 and agent-2, got: %v", updated.Status.UsedBy)
	}
}

// =============================================================================
// HELPER TESTS
// =============================================================================

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		a, b  []string
		equal bool
	}{
		{nil, nil, true},
		{[]string{}, []string{}, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{nil, []string{}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"b", "a"}, false},
	}

	for _, tc := range tests {
		result := stringSlicesEqual(tc.a, tc.b)
		if result != tc.equal {
			t.Errorf("stringSlicesEqual(%v, %v) = %v, want %v", tc.a, tc.b, result, tc.equal)
		}
	}
}
