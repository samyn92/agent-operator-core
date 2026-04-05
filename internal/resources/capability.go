package resources

import (
	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// MCPCapabilityToMCPEntry converts an MCP Capability to an MCPEntry for opencode.json injection.
// For "local" mode: produces a local entry with command + env.
// For "remote" mode: produces a remote entry with URL.
// For "server" mode: produces a remote entry with the auto-generated Service URL.
//
//	The agent sees this as a standard remote MCP server — it doesn't know the operator
//	deployed it. Secrets stay in the MCP server pod (credential isolation).
func MCPCapabilityToMCPEntry(capability *agentsv1alpha1.Capability) *MCPEntry {
	if capability.Spec.MCP == nil {
		return nil
	}

	mcp := capability.Spec.MCP

	switch mcp.Mode {
	case "server":
		// Server mode: the operator deploys the MCP server as a standalone pod.
		// The agent connects to it as a "remote" MCP server via the Service URL.
		// No command/env is passed to the agent — those belong to the server pod.
		return &MCPEntry{
			Type:    "remote",
			URL:     MCPServerServiceURL(capability),
			Enabled: mcp.Enabled,
		}

	case "local", "remote":
		// Local/remote: pass through as-is
		return &MCPEntry{
			Type:    mcp.Mode,
			Command: mcp.Command,
			URL:     mcp.URL,
			Env:     mcp.Environment,
			Enabled: mcp.Enabled,
		}

	default:
		return nil
	}
}
