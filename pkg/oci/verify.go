package oci

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// VerifyOptions configures Cosign signature verification for an OCI artifact.
type VerifyOptions struct {
	// Ref is the OCI artifact reference to verify.
	Ref string

	// PublicKey is the PEM-encoded Cosign public key (for key-based verification).
	// Mutually exclusive with Keyless.
	PublicKey string

	// Keyless configures keyless (OIDC-based) verification.
	// Mutually exclusive with PublicKey.
	Keyless *KeylessVerifyOptions
}

// KeylessVerifyOptions configures Cosign keyless verification.
type KeylessVerifyOptions struct {
	// Issuer is the expected OIDC issuer URL.
	Issuer string
	// Identity is the expected signing identity (email or URI).
	Identity string
}

// Verifier performs Cosign signature verification on OCI artifacts.
// It shells out to the `cosign` CLI binary, which must be available on the PATH.
//
// This approach avoids pulling the massive sigstore/cosign Go dependency tree into
// the operator binary, while still providing full verification capability.
// It follows the same pattern used by Flux (source-controller) and other operators.
type Verifier struct {
	// cosignPath is the path to the cosign binary. Defaults to "cosign".
	cosignPath string
}

// NewVerifier creates a new Cosign verifier.
// Returns an error if the cosign binary is not found on PATH.
func NewVerifier() (*Verifier, error) {
	path, err := exec.LookPath("cosign")
	if err != nil {
		return nil, fmt.Errorf("cosign binary not found on PATH: %w (install from https://github.com/sigstore/cosign)", err)
	}
	return &Verifier{cosignPath: path}, nil
}

// NewVerifierWithPath creates a new Cosign verifier with a custom binary path.
func NewVerifierWithPath(cosignPath string) *Verifier {
	return &Verifier{cosignPath: cosignPath}
}

// Verify verifies the Cosign signature of an OCI artifact.
// Returns nil if verification succeeds, or an error with details if it fails.
func (v *Verifier) Verify(ctx context.Context, opts VerifyOptions) error {
	if opts.PublicKey != "" {
		return v.verifyWithKey(ctx, opts.Ref, opts.PublicKey)
	}
	if opts.Keyless != nil {
		return v.verifyKeyless(ctx, opts.Ref, opts.Keyless)
	}
	return fmt.Errorf("no verification method specified: provide either publicKey or keyless configuration")
}

// verifyWithKey performs key-based Cosign verification.
func (v *Verifier) verifyWithKey(ctx context.Context, ref string, publicKeyPEM string) error {
	// Write the public key to a temp file (cosign requires a file path)
	tmpFile, err := os.CreateTemp("", "cosign-key-*.pem")
	if err != nil {
		return fmt.Errorf("creating temp file for public key: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(publicKeyPEM); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing public key to temp file: %w", err)
	}
	tmpFile.Close()

	// Run cosign verify
	args := []string{"verify", "--key", tmpFile.Name(), ref}
	return v.runCosign(ctx, args)
}

// verifyKeyless performs keyless (OIDC-based) Cosign verification.
func (v *Verifier) verifyKeyless(ctx context.Context, ref string, opts *KeylessVerifyOptions) error {
	args := []string{
		"verify",
		"--certificate-oidc-issuer", opts.Issuer,
		"--certificate-identity", opts.Identity,
		ref,
	}
	return v.runCosign(ctx, args)
}

// runCosign executes the cosign binary with the given arguments.
func (v *Verifier) runCosign(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, v.cosignPath, args...)

	// Capture both stdout and stderr for error reporting
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		if outputStr != "" {
			return fmt.Errorf("cosign verification failed: %s: %w", outputStr, err)
		}
		return fmt.Errorf("cosign verification failed: %w", err)
	}

	return nil
}

// IsCosignAvailable checks if the cosign binary is available on the system.
func IsCosignAvailable() bool {
	_, err := exec.LookPath("cosign")
	return err == nil
}

// SecretReader is a minimal interface for fetching Kubernetes Secrets.
// This is satisfied by any controller-runtime client.Reader (e.g., the reconciler's
// embedded client.Client). Using a minimal interface keeps pkg/oci free of
// controller-runtime dependencies and makes it easy to test with fakes.
type SecretReader interface {
	// GetSecretData returns the Data map of the named Secret in the given namespace.
	// Returns an error if the Secret does not exist or cannot be read.
	GetSecretData(ctx context.Context, namespace, name string) (map[string][]byte, error)
}

// OCIVerification mirrors the CRD type for signature verification config.
// This avoids importing the api/v1alpha1 package into pkg/oci.
type OCIVerification struct {
	// PublicKeySecret is the name of the Secret containing the Cosign public key.
	PublicKeySecret string
	// PublicKeyField is the key within the Secret (defaults to "cosign.pub").
	PublicKeyField string
	// Keyless configures keyless OIDC-based verification.
	Keyless *KeylessVerifyOptions
}

// VerifyArtifact performs Cosign signature verification on an OCI artifact reference.
// This is a shared helper that replaces the duplicated verifyOCIArtifact methods
// in AgentReconciler, PiAgentReconciler, and CapabilityReconciler.
//
// If verify is nil, returns nil (no verification configured).
// Uses the provided SecretReader to fetch public key Secrets from the cluster.
func VerifyArtifact(ctx context.Context, secrets SecretReader, namespace string, ref string, verify *OCIVerification) error {
	if verify == nil {
		return nil
	}

	verifier, err := NewVerifier()
	if err != nil {
		return fmt.Errorf("cosign not available: %w", err)
	}

	verifyOpts := VerifyOptions{
		Ref: ref,
	}

	if verify.PublicKeySecret != "" {
		data, err := secrets.GetSecretData(ctx, namespace, verify.PublicKeySecret)
		if err != nil {
			return fmt.Errorf("failed to get cosign public key secret %s: %w", verify.PublicKeySecret, err)
		}
		key := verify.PublicKeyField
		if key == "" {
			key = "cosign.pub"
		}
		pubKeyData, ok := data[key]
		if !ok {
			return fmt.Errorf("key %q not found in cosign public key secret %s", key, verify.PublicKeySecret)
		}
		verifyOpts.PublicKey = string(pubKeyData)
	}

	if verify.Keyless != nil {
		verifyOpts.Keyless = &KeylessVerifyOptions{
			Issuer:   verify.Keyless.Issuer,
			Identity: verify.Keyless.Identity,
		}
	}

	return verifier.Verify(ctx, verifyOpts)
}
