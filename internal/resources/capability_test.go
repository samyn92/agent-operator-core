package resources

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// =============================================================================
// CapabilityConfigMap TESTS
// =============================================================================

func TestCapabilityConfigMap_Basic(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Read-only kubectl",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image:         "bitnami/kubectl:1.30",
				CommandPrefix: "kubectl ",
			},
			Instructions: "Use kubectl to inspect resources",
		},
	}

	cm := CapabilityConfigMap(agent, cap, "")

	if cm.Name != "my-agent-kubectl-readonly-config" {
		t.Fatalf("expected name 'my-agent-kubectl-readonly-config', got %q", cm.Name)
	}
	if cm.Namespace != "default" {
		t.Fatalf("expected namespace 'default', got %q", cm.Namespace)
	}
	if cm.Data["command-prefix"] != "kubectl " {
		t.Fatalf("expected command-prefix 'kubectl ', got %q", cm.Data["command-prefix"])
	}
	if cm.Data["instructions"] != "Use kubectl to inspect resources" {
		t.Fatalf("expected instructions, got %q", cm.Data["instructions"])
	}
}

func TestCapabilityConfigMap_WithAlias(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "production"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "production"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "K8s",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "bitnami/kubectl:1.30",
			},
		},
	}

	cm := CapabilityConfigMap(agent, cap, "k8s")

	if cm.Name != "my-agent-k8s-config" {
		t.Fatalf("expected name with alias 'my-agent-k8s-config', got %q", cm.Name)
	}
	// Check labels use alias
	if cm.Labels["agents.io/capability"] != "kubectl-readonly" {
		t.Fatalf("expected capability label to be original name, got %q", cm.Labels["agents.io/capability"])
	}
}

func TestCapabilityConfigMap_NoContainerSpec(t *testing.T) {
	// Non-container capability should still produce a ConfigMap (for instructions)
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-server", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:         agentsv1alpha1.CapabilityTypeMCP,
			Description:  "MCP server",
			Instructions: "Connect to MCP",
		},
	}

	cm := CapabilityConfigMap(agent, cap, "")

	if cm.Data["instructions"] != "Connect to MCP" {
		t.Fatalf("expected instructions, got %q", cm.Data["instructions"])
	}
	// No command-prefix key for non-container
	if prefix, ok := cm.Data["command-prefix"]; ok && prefix != "" {
		t.Fatalf("expected no command-prefix for MCP capability, got %q", prefix)
	}
}

func TestCapabilityConfigMap_Labels(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "sre-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "helm-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:      agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "alpine/helm:3.14"},
		},
	}

	cm := CapabilityConfigMap(agent, cap, "helm")

	expected := map[string]string{
		"app.kubernetes.io/name":       "capability",
		"app.kubernetes.io/instance":   "sre-agent-helm",
		"app.kubernetes.io/managed-by": "agent-operator",
		"agents.io/agent":              "sre-agent",
		"agents.io/capability":         "helm-readonly",
	}
	for k, v := range expected {
		if cm.Labels[k] != v {
			t.Errorf("label %q: expected %q, got %q", k, v, cm.Labels[k])
		}
	}
}

func TestCapabilityConfigMap_DenyPatterns(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "git-contributor", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Git contributor",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image:         "alpine/git:latest",
				CommandPrefix: "git -C /workspace ",
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Allow: []string{"status *", "add *"},
				Deny:  []string{"push * main", "push * master", "push --force *"},
			},
		},
	}

	cm := CapabilityConfigMap(agent, cap, "")

	// Should have deny-patterns key with prefixed patterns
	denyPatterns, ok := cm.Data["deny-patterns"]
	if !ok {
		t.Fatal("expected deny-patterns key in ConfigMap data")
	}

	lines := strings.Split(denyPatterns, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 deny patterns, got %d: %v", len(lines), lines)
	}

	// Patterns should be prefixed with command prefix
	if lines[0] != "git -C /workspace push * main" {
		t.Fatalf("expected 'git -C /workspace push * main', got %q", lines[0])
	}
	if lines[1] != "git -C /workspace push * master" {
		t.Fatalf("expected 'git -C /workspace push * master', got %q", lines[1])
	}
	if lines[2] != "git -C /workspace push --force *" {
		t.Fatalf("expected 'git -C /workspace push --force *', got %q", lines[2])
	}
}

func TestCapabilityConfigMap_NoDenyPatterns(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:      agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "bitnami/kubectl:1.30"},
		},
	}

	cm := CapabilityConfigMap(agent, cap, "")

	if _, ok := cm.Data["deny-patterns"]; ok {
		t.Fatal("should NOT have deny-patterns key when no deny patterns configured")
	}
}

func TestCapabilityConfigMap_DenyPatternsNoPrefix(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-tool", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image: "custom:latest",
				// No CommandPrefix
			},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Deny: []string{"delete *"},
			},
		},
	}

	cm := CapabilityConfigMap(agent, cap, "")

	denyPatterns := cm.Data["deny-patterns"]
	if denyPatterns != "delete *" {
		t.Fatalf("expected 'delete *' (no prefix), got %q", denyPatterns)
	}
}

// =============================================================================
// ContainerCapabilityToSourceInfo TESTS
// =============================================================================

func TestContainerCapabilityToSourceInfo_Basic(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly", Namespace: "default"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Read-only kubectl access",
			Container: &agentsv1alpha1.ContainerCapabilitySpec{
				Image:         "bitnami/kubectl:1.30",
				CommandPrefix: "kubectl ",
				ContainerType: "kubernetes",
			},
			Instructions: "Use for read-only K8s access",
		},
	}

	info := ContainerCapabilityToSourceInfo(agent, cap, "", 8081)

	if info.Name != "kubectl-readonly" {
		t.Fatalf("expected name 'kubectl-readonly', got %q", info.Name)
	}
	if info.Type != "kubernetes" {
		t.Fatalf("expected type 'kubernetes', got %q", info.Type)
	}
	if info.Description != "Read-only kubectl access" {
		t.Fatalf("expected description, got %q", info.Description)
	}
	if info.ServiceURL != "http://localhost:8081" {
		t.Fatalf("expected URL 'http://localhost:8081', got %q", info.ServiceURL)
	}
	if info.CommandPrefix != "kubectl " {
		t.Fatalf("expected command prefix 'kubectl ', got %q", info.CommandPrefix)
	}
	if info.Instructions != "Use for read-only K8s access" {
		t.Fatalf("expected instructions, got %q", info.Instructions)
	}
}

func TestContainerCapabilityToSourceInfo_WithAlias(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-readonly"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:      agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "bitnami/kubectl:1.30"},
		},
	}

	info := ContainerCapabilityToSourceInfo(agent, cap, "k8s", 8082)

	if info.Name != "k8s" {
		t.Fatalf("expected alias 'k8s', got %q", info.Name)
	}
	if info.ServiceURL != "http://localhost:8082" {
		t.Fatalf("expected port 8082 in URL, got %q", info.ServiceURL)
	}
}

func TestContainerCapabilityToSourceInfo_WithPermissions(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "kubectl-ops"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:      agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "bitnami/kubectl:1.30"},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Allow: []string{"get *", "describe *"},
				Approve: []agentsv1alpha1.ApprovalRule{
					{Pattern: "apply *", Message: "Will modify resources", Severity: "warning", Timeout: 120},
					{Pattern: "scale *", Message: "Will scale", Severity: "critical"},
				},
				Deny: []string{"delete *", "exec *"},
			},
		},
	}

	info := ContainerCapabilityToSourceInfo(agent, cap, "", 8081)

	if len(info.Allow) != 2 || info.Allow[0] != "get *" || info.Allow[1] != "describe *" {
		t.Fatalf("expected allow patterns [get *, describe *], got %v", info.Allow)
	}
	if len(info.Deny) != 2 || info.Deny[0] != "delete *" || info.Deny[1] != "exec *" {
		t.Fatalf("expected deny patterns [delete *, exec *], got %v", info.Deny)
	}
	if len(info.ApproveRules) != 2 {
		t.Fatalf("expected 2 approve rules, got %d", len(info.ApproveRules))
	}

	// First rule has explicit timeout
	if info.ApproveRules[0].Pattern != "apply *" {
		t.Fatalf("expected pattern 'apply *', got %q", info.ApproveRules[0].Pattern)
	}
	if info.ApproveRules[0].Timeout != 120 {
		t.Fatalf("expected timeout 120, got %d", info.ApproveRules[0].Timeout)
	}
	if info.ApproveRules[0].Severity != "warning" {
		t.Fatalf("expected severity 'warning', got %q", info.ApproveRules[0].Severity)
	}

	// Second rule gets defaults
	if info.ApproveRules[1].Timeout != 300 {
		t.Fatalf("expected default timeout 300, got %d", info.ApproveRules[1].Timeout)
	}
	if info.ApproveRules[1].Severity != "critical" {
		t.Fatalf("expected severity 'critical', got %q", info.ApproveRules[1].Severity)
	}
}

func TestContainerCapabilityToSourceInfo_ApproveDefaults(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cap"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:      agentsv1alpha1.CapabilityTypeContainer,
			Container: &agentsv1alpha1.ContainerCapabilitySpec{Image: "test"},
			Permissions: &agentsv1alpha1.CapabilityPermissions{
				Approve: []agentsv1alpha1.ApprovalRule{
					{Pattern: "deploy *"},
				},
			},
		},
	}

	info := ContainerCapabilityToSourceInfo(agent, cap, "", 8081)

	if len(info.ApproveRules) != 1 {
		t.Fatalf("expected 1 approve rule, got %d", len(info.ApproveRules))
	}
	// Default timeout
	if info.ApproveRules[0].Timeout != 300 {
		t.Fatalf("expected default timeout 300, got %d", info.ApproveRules[0].Timeout)
	}
	// Default severity
	if info.ApproveRules[0].Severity != "warning" {
		t.Fatalf("expected default severity 'warning', got %q", info.ApproveRules[0].Severity)
	}
}

func TestContainerCapabilityToSourceInfo_NoPermissions(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "basic-cap"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "No permissions set",
			Container:   &agentsv1alpha1.ContainerCapabilitySpec{Image: "test"},
		},
	}

	info := ContainerCapabilityToSourceInfo(agent, cap, "", 8081)

	if info.Allow != nil {
		t.Fatalf("expected nil allow, got %v", info.Allow)
	}
	if info.Deny != nil {
		t.Fatalf("expected nil deny, got %v", info.Deny)
	}
	if info.ApproveRules != nil {
		t.Fatalf("expected nil approve rules, got %v", info.ApproveRules)
	}
}

func TestContainerCapabilityToSourceInfo_NilContainerSpec(t *testing.T) {
	agent := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}
	cap := &agentsv1alpha1.Capability{
		ObjectMeta: metav1.ObjectMeta{Name: "broken-cap"},
		Spec: agentsv1alpha1.CapabilitySpec{
			Type:        agentsv1alpha1.CapabilityTypeContainer,
			Description: "Missing container spec",
		},
	}

	// Should not panic with nil container spec
	info := ContainerCapabilityToSourceInfo(agent, cap, "", 8081)

	if info.CommandPrefix != "" {
		t.Fatalf("expected empty command prefix, got %q", info.CommandPrefix)
	}
	if info.Type != "" {
		t.Fatalf("expected empty type, got %q", info.Type)
	}
}

// =============================================================================
// MCPCapabilityToMCPEntry TESTS
// =============================================================================

func TestMCPCapabilityToMCPEntry_LocalMode(t *testing.T) {
	enabled := true
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP,
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"},
				Environment: map[string]string{
					"ALLOW_WRITE": "false",
				},
				Enabled: &enabled,
			},
		},
	}

	entry := MCPCapabilityToMCPEntry(cap)

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Type != "local" {
		t.Fatalf("expected type 'local', got %q", entry.Type)
	}
	if len(entry.Command) != 4 || entry.Command[0] != "npx" {
		t.Fatalf("expected command [npx -y @modelcontextprotocol/server-filesystem /data], got %v", entry.Command)
	}
	if entry.URL != "" {
		t.Fatalf("expected empty URL for local mode, got %q", entry.URL)
	}
	if entry.Env["ALLOW_WRITE"] != "false" {
		t.Fatalf("expected env ALLOW_WRITE=false, got %v", entry.Env)
	}
	if entry.Enabled == nil || *entry.Enabled != true {
		t.Fatal("expected enabled=true")
	}
}

func TestMCPCapabilityToMCPEntry_RemoteMode(t *testing.T) {
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP,
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode: "remote",
				URL:  "https://mcp.slack.com/sse",
			},
		},
	}

	entry := MCPCapabilityToMCPEntry(cap)

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Type != "remote" {
		t.Fatalf("expected type 'remote', got %q", entry.Type)
	}
	if entry.URL != "https://mcp.slack.com/sse" {
		t.Fatalf("expected URL, got %q", entry.URL)
	}
	if len(entry.Command) != 0 {
		t.Fatalf("expected empty command for remote mode, got %v", entry.Command)
	}
}

func TestMCPCapabilityToMCPEntry_NilMCPSpec(t *testing.T) {
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP,
		},
	}

	entry := MCPCapabilityToMCPEntry(cap)

	if entry != nil {
		t.Fatalf("expected nil entry for nil MCP spec, got %+v", entry)
	}
}

func TestMCPCapabilityToMCPEntry_DisabledServer(t *testing.T) {
	disabled := false
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP,
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"test"},
				Enabled: &disabled,
			},
		},
	}

	entry := MCPCapabilityToMCPEntry(cap)

	if entry.Enabled == nil || *entry.Enabled != false {
		t.Fatal("expected enabled=false")
	}
}

func TestMCPCapabilityToMCPEntry_NilEnabled(t *testing.T) {
	cap := &agentsv1alpha1.Capability{
		Spec: agentsv1alpha1.CapabilitySpec{
			Type: agentsv1alpha1.CapabilityTypeMCP,
			MCP: &agentsv1alpha1.MCPCapabilitySpec{
				Mode:    "local",
				Command: []string{"test"},
			},
		},
	}

	entry := MCPCapabilityToMCPEntry(cap)

	if entry.Enabled != nil {
		t.Fatalf("expected nil enabled (use default), got %v", *entry.Enabled)
	}
}
