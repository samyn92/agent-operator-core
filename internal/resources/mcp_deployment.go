package resources

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

const (
	// DefaultGatewayImage is the default capability-gateway image.
	// This is the unified gateway binary that handles both CLI and MCP modes.
	// Override via the operator's configuration or environment.
	DefaultGatewayImage = "ghcr.io/samyn92/capability-gateway:latest"
)

// MCPServerDeploymentName returns the Deployment name for an MCP server capability.
func MCPServerDeploymentName(capability *agentsv1alpha1.Capability) string {
	return "mcp-" + capability.Name
}

// MCPServerServiceName returns the Service name for an MCP server capability.
func MCPServerServiceName(capability *agentsv1alpha1.Capability) string {
	return "mcp-" + capability.Name
}

// MCPServerServiceURL returns the in-cluster URL for an MCP server capability's SSE endpoint.
// This is what the agent connects to as a "remote" MCP server.
func MCPServerServiceURL(capability *agentsv1alpha1.Capability) string {
	port := int32(8080)
	if capability.Spec.MCP != nil && capability.Spec.MCP.Server != nil && capability.Spec.MCP.Server.Port != 0 {
		port = capability.Spec.MCP.Server.Port
	}
	return fmt.Sprintf("http://%s.%s.svc:%d/sse",
		MCPServerServiceName(capability),
		capability.Namespace,
		port,
	)
}

// MCPServerDeployment creates a Deployment for an operator-managed MCP server capability.
//
// Architecture:
//   - An init container copies the capability-gateway binary from the gateway image
//     into a shared emptyDir volume (/gateway).
//   - The main container uses the user-specified image (e.g., node:22-slim, mcp/gitlab)
//     which has the MCP server runtime/dependencies.
//   - The main container runs the capability-gateway binary in MCP mode, which spawns
//     the MCP server command as a subprocess and bridges its stdio to SSE/HTTP.
//
// This approach:
//   - Works in air-gapped environments (no npm install at runtime)
//   - Keeps the user's image clean (no gateway baked in)
//   - Uses our own Go binary for stdio-to-SSE bridging
//   - Provides rate limiting, audit logging, and health checks for MCP too
func MCPServerDeployment(capability *agentsv1alpha1.Capability) *appsv1.Deployment {
	labels := mcpServerLabels(capability)
	replicas := int32(1)

	mcp := capability.Spec.MCP
	server := mcp.Server

	port := server.Port
	if port == 0 {
		port = 8080
	}

	// Build the MCP command string from the command array
	mcpCommand := strings.Join(mcp.Command, " ")

	// Environment variables — gateway config + secrets + explicit env vars.
	// These go into the MCP server pod only (credential isolation).
	envVars := []corev1.EnvVar{
		{Name: "GATEWAY_MODE", Value: "mcp"},
		{Name: "GATEWAY_PORT", Value: fmt.Sprintf("%d", port)},
		{Name: "GATEWAY_COMMAND", Value: mcpCommand},
		{Name: "TOOL_NAME", Value: capability.Name},
	}

	// Rate limiting
	if capability.Spec.RateLimit != nil && capability.Spec.RateLimit.RequestsPerMinute > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "RATE_LIMIT_RPM",
			Value: fmt.Sprintf("%d", capability.Spec.RateLimit.RequestsPerMinute),
		})
	}

	// Audit logging
	if capability.Spec.Audit {
		envVars = append(envVars, corev1.EnvVar{Name: "AUDIT_ENABLED", Value: "true"})
	}

	// Add explicit environment variables from the MCP spec
	for k, v := range mcp.Environment {
		envVars = append(envVars, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

	// Add secret environment variables from the shared Capability.spec.secrets
	for _, secret := range capability.Spec.Secrets {
		envVars = append(envVars, corev1.EnvVar{
			Name: secret.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secret.ValueFrom.Name,
					},
					Key: secret.ValueFrom.Key,
				},
			},
		})
	}

	// Resource requirements for the MCP server pod
	var resourceReqs corev1.ResourceRequirements
	if server.Resources != nil {
		resourceReqs = *server.Resources
	} else {
		resourceReqs = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		}
	}

	// Init container: copy the capability-gateway binary from the gateway image
	// into a shared volume that the main container can access.
	initContainer := corev1.Container{
		Name:            "init-gateway",
		Image:           DefaultGatewayImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"cp", "/capability-gateway", "/gateway/capability-gateway"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "gateway-bin", MountPath: "/gateway"},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: hardenedSecurityContext(),
	}

	// Main container: user's MCP server image + capability-gateway binary.
	// The gateway spawns the MCP command as a subprocess and bridges stdio to SSE.
	mainContainer := corev1.Container{
		Name:            "mcp-server",
		Image:           server.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/gateway/capability-gateway"},
		Ports: []corev1.ContainerPort{
			{Name: "sse", ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		Env:       envVars,
		Resources: resourceReqs,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "gateway-bin", MountPath: "/gateway", ReadOnly: true},
		},
		SecurityContext: hardenedSecurityContext(),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       30,
		},
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MCPServerDeploymentName(capability),
			Namespace: capability.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{initContainer},
					Containers:     []corev1.Container{mainContainer},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						RunAsUser:    int64Ptr(1000),
						RunAsGroup:   int64Ptr(1000),
						FSGroup:      int64Ptr(1000),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "gateway-bin",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	// Compute spec hash for change detection (same pattern as AgentDeployment)
	specHash := HashDeploymentSpec(dep)
	dep.Annotations = map[string]string{
		DesiredSpecHashAnnotation: specHash,
	}

	return dep
}

// MCPServerService creates a Service for an operator-managed MCP server capability.
// The agent connects to this Service URL as a "remote" MCP server.
func MCPServerService(capability *agentsv1alpha1.Capability) *corev1.Service {
	labels := mcpServerLabels(capability)

	port := int32(8080)
	if capability.Spec.MCP != nil && capability.Spec.MCP.Server != nil && capability.Spec.MCP.Server.Port != 0 {
		port = capability.Spec.MCP.Server.Port
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MCPServerServiceName(capability),
			Namespace: capability.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "sse",
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

// mcpServerLabels returns labels for MCP server resources.
// These are capability-scoped (not agent-scoped) since the MCP server pod
// is owned by the Capability, not by any specific Agent.
func mcpServerLabels(capability *agentsv1alpha1.Capability) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "mcp-server",
		"app.kubernetes.io/instance":   capability.Name,
		"app.kubernetes.io/managed-by": "agent-operator",
		"agents.io/capability":         capability.Name,
	}
}
