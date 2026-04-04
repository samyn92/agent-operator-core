package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/pkg/oci"
)

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

// clientSecretReader adapts a controller-runtime client.Reader to the
// oci.SecretReader interface used by VerifyArtifact.
type clientSecretReader struct {
	client client.Reader
}

func (r *clientSecretReader) GetSecretData(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		return nil, err
	}
	return secret.Data, nil
}

// verifyOCIArtifactRef performs Cosign signature verification on an OCIArtifactRef
// using the shared oci.VerifyArtifact helper. This replaces the duplicated
// verifyOCIArtifact methods that were previously copied into each reconciler.
//
// Returns nil if no verification is configured or if verification succeeds.
func verifyOCIArtifactRef(ctx context.Context, reader client.Reader, namespace string, ociRef *agentsv1alpha1.OCIArtifactRef) error {
	if ociRef.Verify == nil {
		return nil
	}

	var verify *oci.OCIVerification
	v := ociRef.Verify

	verify = &oci.OCIVerification{}
	if v.PublicKey != nil {
		verify.PublicKeySecret = v.PublicKey.Name
		verify.PublicKeyField = v.PublicKey.Key
	}
	if v.Keyless != nil {
		verify.Keyless = &oci.KeylessVerifyOptions{
			Issuer:   v.Keyless.Issuer,
			Identity: v.Keyless.Identity,
		}
	}

	secrets := &clientSecretReader{client: reader}
	return oci.VerifyArtifact(ctx, secrets, namespace, ociRef.Ref, verify)
}
