package resources

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
)

// AgentPVC creates a PersistentVolumeClaim for the agent
func AgentPVC(agent *agentsv1alpha1.Agent) *corev1.PersistentVolumeClaim {
	labels := commonLabels(agent)

	// Default storage size
	storageSize := resource.MustParse("5Gi")
	var storageClass *string

	if agent.Spec.Storage != nil {
		if !agent.Spec.Storage.Size.IsZero() {
			storageSize = agent.Spec.Storage.Size
		}
		if agent.Spec.Storage.StorageClass != "" {
			storageClass = &agent.Spec.Storage.StorageClass
		}
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name + "-data",
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			StorageClassName: storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}
}
