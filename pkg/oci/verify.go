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
