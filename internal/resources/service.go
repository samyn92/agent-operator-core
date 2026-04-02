package resources

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// AgentService creates a Service for the agent
// This exposes the OpenCode HTTP API for Channels and other services to connect
func AgentService(agent *agentsv1alpha1.Agent) *corev1.Service {
	labels := commonLabels(agent)

	ports := []corev1.ServicePort{
		{
			Name:       "opencode",
			Port:       OpencodePort,
			TargetPort: intstr.FromInt(OpencodePort),
			Protocol:   corev1.ProtocolTCP,
		},
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    ports,
			Type:     corev1.ServiceTypeClusterIP,
			// On dual-stack clusters the default ClusterIP may be IPv6, but OpenCode
			// binds to 0.0.0.0 (IPv4). PreferDualStack with IPv4 first ensures the
			// primary ClusterIP is IPv4 so clients can reach the server, while still
			// allocating an IPv6 address for pods that prefer it.
			IPFamilyPolicy: ipFamilyPolicyPtr(corev1.IPFamilyPolicyPreferDualStack),
			IPFamilies:     []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
		},
	}
}
