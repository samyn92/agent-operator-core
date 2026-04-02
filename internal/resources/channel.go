package resources

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

const (
	// ChannelPort is the default port for channel containers
	ChannelPort = 8080
)

// channelLabels returns standard labels for a Channel resource
func channelLabels(channel *agentsv1alpha1.Channel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       channel.Name,
		"app.kubernetes.io/component":  "channel",
		"app.kubernetes.io/managed-by": "agent-operator",
		"agents.io/channel-type":       channel.Spec.Type,
	}
}

// ChannelDeployment creates a Deployment for the channel
// agentServiceURL is the URL to the Agent's OpenCode API (e.g., "http://agent-name.namespace.svc.cluster.local:8080")
func ChannelDeployment(channel *agentsv1alpha1.Channel, agentServiceURL string) *appsv1.Deployment {
	labels := channelLabels(channel)

	replicas := int32(1)
	if channel.Spec.Replicas != nil {
		replicas = *channel.Spec.Replicas
	}

	pullPolicy := corev1.PullIfNotPresent
	if channel.Spec.ImagePullPolicy != "" {
		pullPolicy = channel.Spec.ImagePullPolicy
	}

	// Build container based on channel type
	container := buildChannelContainer(channel, pullPolicy, agentServiceURL)

	// Resource requirements
	if channel.Spec.Resources != nil {
		container.Resources = *channel.Spec.Resources
	} else {
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("32Mi"),
				corev1.ResourceCPU:    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      channel.Name,
			Namespace: channel.Namespace,
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
					ServiceAccountName: channel.Spec.ServiceAccountName,
					Containers:         []corev1.Container{container},
				},
			},
		},
	}

	// Compute and store a hash of the desired spec (see AgentDeployment for rationale)
	specHash := HashDeploymentSpec(dep)
	dep.Annotations = map[string]string{
		DesiredSpecHashAnnotation: specHash,
	}

	return dep
}

// buildChannelContainer builds the container for a channel based on its type
func buildChannelContainer(channel *agentsv1alpha1.Channel, pullPolicy corev1.PullPolicy, agentServiceURL string) corev1.Container {
	envVars := []corev1.EnvVar{
		{Name: "PORT", Value: "8080"},
		{Name: "OPENCODE_URL", Value: agentServiceURL},
	}

	switch channel.Spec.Type {
	case "telegram":
		envVars = append(envVars, buildTelegramEnvVars(channel)...)
	case "slack":
		envVars = append(envVars, buildSlackEnvVars(channel)...)
	}

	return corev1.Container{
		Name:            "channel",
		Image:           channel.Spec.Image,
		ImagePullPolicy: pullPolicy,
		Ports: []corev1.ContainerPort{
			{Name: "http", ContainerPort: ChannelPort, Protocol: corev1.ProtocolTCP},
		},
		Env: envVars,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstr.FromInt(ChannelPort),
				},
			},
			InitialDelaySeconds: 2,
			PeriodSeconds:       5,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstr.FromInt(ChannelPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
		},
	}
}

// buildTelegramEnvVars builds environment variables for a Telegram channel
func buildTelegramEnvVars(channel *agentsv1alpha1.Channel) []corev1.EnvVar {
	if channel.Spec.Telegram == nil {
		return nil
	}

	envVars := []corev1.EnvVar{
		{
			Name: "TELEGRAM_BOT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: channel.Spec.Telegram.BotTokenSecret.Name,
					},
					Key: channel.Spec.Telegram.BotTokenSecret.Key,
				},
			},
		},
	}

	// Add allowed users
	if len(channel.Spec.Telegram.AllowedUsers) > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "ALLOWED_USERS",
			Value: strings.Join(channel.Spec.Telegram.AllowedUsers, ","),
		})
	}

	// Add allowed chats
	if len(channel.Spec.Telegram.AllowedChats) > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "ALLOWED_CHATS",
			Value: strings.Join(channel.Spec.Telegram.AllowedChats, ","),
		})
	}

	return envVars
}

// buildSlackEnvVars builds environment variables for a Slack channel
func buildSlackEnvVars(channel *agentsv1alpha1.Channel) []corev1.EnvVar {
	if channel.Spec.Slack == nil {
		return nil
	}

	envVars := []corev1.EnvVar{
		{
			Name: "SLACK_BOT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: channel.Spec.Slack.BotTokenSecret.Name,
					},
					Key: channel.Spec.Slack.BotTokenSecret.Key,
				},
			},
		},
	}

	// Add signing secret
	if channel.Spec.Slack.SigningSecret != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "SLACK_SIGNING_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: channel.Spec.Slack.SigningSecret.Name,
					},
					Key: channel.Spec.Slack.SigningSecret.Key,
				},
			},
		})
	}

	// Add allowed channels
	if len(channel.Spec.Slack.AllowedChannels) > 0 {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "ALLOWED_CHANNELS",
			Value: strings.Join(channel.Spec.Slack.AllowedChannels, ","),
		})
	}

	return envVars
}

// ChannelService creates a Service for the channel
func ChannelService(channel *agentsv1alpha1.Channel) *corev1.Service {
	labels := channelLabels(channel)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      channel.Name,
			Namespace: channel.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       ChannelPort,
					TargetPort: intstr.FromInt(ChannelPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

// ChannelIngress creates an Ingress for the channel's webhook
func ChannelIngress(channel *agentsv1alpha1.Channel) *networkingv1.Ingress {
	labels := channelLabels(channel)
	annotations := map[string]string{}

	// Merge user-defined annotations
	if channel.Spec.Webhook.Annotations != nil {
		for k, v := range channel.Spec.Webhook.Annotations {
			annotations[k] = v
		}
	}

	// Add cert-manager annotation if TLS is configured
	if channel.Spec.Webhook.TLS != nil && channel.Spec.Webhook.TLS.ClusterIssuer != "" {
		annotations["cert-manager.io/cluster-issuer"] = channel.Spec.Webhook.TLS.ClusterIssuer
	}

	pathType := networkingv1.PathTypePrefix
	host := channel.Spec.Webhook.Host
	path := channel.Spec.Webhook.Path
	if path == "" {
		path = "/webhook"
	}

	// Build TLS config
	var tls []networkingv1.IngressTLS
	if channel.Spec.Webhook.TLS != nil {
		secretName := channel.Spec.Webhook.TLS.SecretName
		if secretName == "" {
			secretName = channel.Name + "-tls"
		}
		tls = []networkingv1.IngressTLS{
			{
				Hosts:      []string{host},
				SecretName: secretName,
			},
		}
	}

	// Set ingress class if specified
	var ingressClassName *string
	if channel.Spec.Webhook.IngressClassName != nil {
		ingressClassName = channel.Spec.Webhook.IngressClassName
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        channel.Name,
			Namespace:   channel.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ingressClassName,
			TLS:              tls,
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     path,
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: channel.Name,
											Port: networkingv1.ServiceBackendPort{
												Number: ChannelPort,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
