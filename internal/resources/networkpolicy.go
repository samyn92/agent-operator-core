package resources

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// SourceNetworkInfo contains network info for sources
type SourceNetworkInfo struct {
	Name      string
	Namespace string
	Port      int32
}

// AgentNetworkPolicy creates a NetworkPolicy for the agent if enabled
// Returns nil if network policy is not enabled
// sources contains resolved Source information for egress rules
func AgentNetworkPolicy(agent *agentsv1alpha1.Agent, sources []SourceNetworkInfo) *networkingv1.NetworkPolicy {
	// Return nil if network policy is not configured or not enabled
	if agent.Spec.NetworkPolicy == nil || !agent.Spec.NetworkPolicy.Enabled {
		return nil
	}

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name + "-netpol",
			Namespace: agent.Namespace,
			Labels:    commonLabels(agent),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": agent.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{},
		},
	}

	// Always allow DNS (required for hostname resolution)
	dnsPort := intstr.FromInt(53)
	np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				// Allow DNS to kube-dns in any namespace
				NamespaceSelector: &metav1.LabelSelector{},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"k8s-app": "kube-dns",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptrTo(corev1.ProtocolUDP),
				Port:     &dnsPort,
			},
		},
	})

	// Always allow egress to the Kubernetes API server.
	// Capabilities like kubectl and helm need to reach the API server (kubernetes.default.svc).
	// The API server runs in the host network (not as a regular pod), so we can't use
	// pod/namespace selectors. We allow TCP 443 to all cluster-internal IPs.
	// This is safe because the NetworkPolicy already blocks all other egress by default,
	// and port 443 within the cluster is almost exclusively the API server.
	apiPort := intstr.FromInt(443)
	np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				// Allow to any pod on port 443 (covers API server fronted by kube-apiserver pods)
				NamespaceSelector: &metav1.LabelSelector{},
			},
			{
				// Allow to cluster IPs (covers k3s/minikube where API server is a host process)
				// 10.0.0.0/8 covers standard cluster service CIDRs (10.43.0.0/16, 10.96.0.0/12, etc.)
				IPBlock: &networkingv1.IPBlock{
					CIDR: "10.0.0.0/8",
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptrTo(corev1.ProtocolTCP),
				Port:     &apiPort,
			},
		},
	})

	// Always allow egress to the agent-console for telemetry
	// Note: NetworkPolicies operate on pod ports, not service ports.
	// The console pod listens on 8080, the service just maps 80->8080.
	consolePort := intstr.FromInt(8080)
	np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				// Allow to agent-console in agent-system namespace
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "agent-system",
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": "agent-console",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptrTo(corev1.ProtocolTCP),
				Port:     &consolePort,
			},
		},
	})

	// Add egress rules for sources
	for _, src := range sources {
		port := intstr.FromInt32(src.Port)
		np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					// Allow egress to the source's pods
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": src.Namespace,
						},
					},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"agents.io/source": src.Name,
						},
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: ptrTo(corev1.ProtocolTCP),
					Port:     &port,
				},
			},
		})
	}

	// Add allowed egress rules from spec
	for _, rule := range agent.Spec.NetworkPolicy.AllowedEgress {
		port := intstr.FromInt32(rule.Port)
		np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					// Allow to any IP (host resolution happens via DNS)
					// The CNI plugin will handle the actual host->IP resolution
					IPBlock: &networkingv1.IPBlock{
						CIDR: "0.0.0.0/0",
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: ptrTo(corev1.ProtocolTCP),
					Port:     &port,
				},
			},
		})
	}

	// If DenyAll is false and no rules specified, allow all egress
	if !agent.Spec.NetworkPolicy.DenyAll && len(agent.Spec.NetworkPolicy.AllowedEgress) == 0 && len(sources) == 0 {
		np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{})
	}

	return np
}

func ptrTo[T any](v T) *T {
	return &v
}
