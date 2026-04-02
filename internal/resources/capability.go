package resources

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// CapabilityConfigMap creates a ConfigMap for a container capability (sidecar) with gateway config.
// Only contains command-prefix and instructions — allow/deny/approve patterns
// are handled by OpenCode's native permission system via opencode.json.
func CapabilityConfigMap(agent *agentsv1alpha1.Agent, capability *agentsv1alpha1.Capability, alias string) *corev1.ConfigMap {
	toolName := capability.Name
	if alias != "" {
		toolName = alias
	}

	name := agent.Name + "-" + toolName
	labels := capabilityLabels(agent, capability, toolName)

	data := map[string]string{
		"instructions": capability.Spec.Instructions,
	}

	// Container capabilities have a command prefix
	if capability.Spec.Container != nil {
		data["command-prefix"] = capability.Spec.Container.CommandPrefix
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-config",
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Data: data,
	}
}

// ContainerCapabilityToSourceInfo converts a Container Capability to SourceInfo for config generation.
// port is the localhost port where this capability's sidecar listens.
func ContainerCapabilityToSourceInfo(agent *agentsv1alpha1.Agent, capability *agentsv1alpha1.Capability, alias string, port int32) SourceInfo {
	toolName := capability.Name
	if alias != "" {
		toolName = alias
	}

	// Build service URL for this capability (sidecar mode, localhost)
	serviceURL := fmt.Sprintf("http://localhost:%d", port)

	// Resolve allow/deny patterns from permissions
	var allow, deny []string
	var approveRules []ApprovalRuleInfo

	if capability.Spec.Permissions != nil {
		allow = capability.Spec.Permissions.Allow
		deny = capability.Spec.Permissions.Deny
		for _, rule := range capability.Spec.Permissions.Approve {
			timeout := rule.Timeout
			if timeout == 0 {
				timeout = 300 // Default 5 minutes
			}
			severity := rule.Severity
			if severity == "" {
				severity = "warning"
			}
			approveRules = append(approveRules, ApprovalRuleInfo{
				Pattern:  rule.Pattern,
				Message:  rule.Message,
				Severity: severity,
				Timeout:  timeout,
			})
		}
	}

	// Get container-specific fields
	var commandPrefix, containerType string
	if capability.Spec.Container != nil {
		commandPrefix = capability.Spec.Container.CommandPrefix
		containerType = capability.Spec.Container.ContainerType
	}

	return SourceInfo{
		Name:          toolName,
		Type:          containerType,
		Description:   capability.Spec.Description,
		ServiceURL:    serviceURL,
		CommandPrefix: commandPrefix,
		Instructions:  capability.Spec.Instructions,
		Allow:         allow,
		Deny:          deny,
		ApproveRules:  approveRules,
	}
}

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

// capabilityLabels returns common labels for capability resources
func capabilityLabels(agent *agentsv1alpha1.Agent, capability *agentsv1alpha1.Capability, toolName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "capability",
		"app.kubernetes.io/instance":   agent.Name + "-" + toolName,
		"app.kubernetes.io/managed-by": "agent-operator",
		"agents.io/agent":              agent.Name,
		"agents.io/capability":         capability.Name,
	}
}
