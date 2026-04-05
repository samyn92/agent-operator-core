package resources

import (
	"testing"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

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
