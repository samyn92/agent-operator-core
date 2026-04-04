// Package oci provides an OCI artifact client for pulling agent capability content
// (Skills, Tools, Plugins) from OCI-compliant registries.
//
// This package implements support for the "Agent Skills as OCI Artifacts" specification
// by Thomas Vitale, using ORAS (OCI Registry As Storage) as the underlying client.
//
// Artifact types supported:
//   - Skills: application/vnd.agentskills.skill.v1 (contains SKILL.md in a tar.gz layer)
//   - Tools/Plugins: generic OCI artifacts containing .ts/.js source files
//
// The client resolves content at reconciliation time — the extracted text is then placed
// into the agent's ConfigMap alongside inline and ConfigMap-sourced content. This means
// OCI artifacts are resolved once (per reconciliation) and the agent pod never talks
// to the registry directly.
package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Media types from the Agent Skills OCI Artifacts specification.
const (
	// ArtifactTypeSkill is the OCI artifact type for agent skills.
	ArtifactTypeSkill = "application/vnd.agentskills.skill.v1"

	// MediaTypeSkillConfig is the config media type for skill artifacts.
	MediaTypeSkillConfig = "application/vnd.agentskills.skill.config.v1+json"

	// MediaTypeSkillContent is the layer media type for skill content (tar.gz of SKILL.md + resources).
	MediaTypeSkillContent = "application/vnd.agentskills.skill.content.v1.tar+gzip"

	// MediaTypeCollection is the artifact type for skill collections (OCI Image Index).
	MediaTypeCollection = "application/vnd.agentskills.collection.v1"

	// maxContentSize is the maximum size of extracted content (10 MB).
	// This prevents OOM from maliciously large artifacts.
	maxContentSize = 10 * 1024 * 1024
)

// Credentials holds registry authentication credentials.
type Credentials struct {
	// Username for basic auth.
	Username string
	// Password for basic auth (or token).
	Password string
}

// PullOptions configures an OCI artifact pull operation.
type PullOptions struct {
	// Ref is the full OCI reference (e.g., "ghcr.io/org/skills/my-skill:1.0.0").
	Ref string

	// Digest is an optional digest for content verification.
	// If specified, the pulled manifest is verified against this digest.
	Digest string

	// Credentials for registry authentication. Nil means anonymous.
	Credentials *Credentials

	// PlainHTTP allows insecure HTTP connections (for local dev registries).
	PlainHTTP bool
}

// Client is an OCI artifact client that can pull capability content from registries.
type Client struct {
	// plainHTTP allows insecure HTTP connections (useful for local dev/test registries).
	plainHTTP bool
}

// NewClient creates a new OCI artifact client.
func NewClient() *Client {
	return &Client{}
}

// NewClientWithOptions creates a new OCI artifact client with options.
func NewClientWithOptions(plainHTTP bool) *Client {
	return &Client{plainHTTP: plainHTTP}
}

// PullSkillContent pulls a skill artifact and returns the SKILL.md content.
// It handles the Agent Skills OCI Artifacts spec format:
//   - Fetches the manifest for the given reference
//   - Finds the content layer (tar.gz)
//   - Extracts SKILL.md from the archive
//
// For non-spec artifacts (plain single-layer OCI artifacts), it returns the first
// layer's content directly, which allows backward-compatible usage with simpler
// artifacts that just contain a SKILL.md as a plain text layer.
func (c *Client) PullSkillContent(ctx context.Context, opts PullOptions) (string, error) {
	repo, ref, err := c.resolveRepository(opts)
	if err != nil {
		return "", fmt.Errorf("resolving repository: %w", err)
	}

	// Fetch the manifest
	desc, rc, err := oras.Fetch(ctx, repo, ref, oras.DefaultFetchOptions)
	if err != nil {
		return "", fmt.Errorf("fetching manifest for %s: %w", opts.Ref, err)
	}
	manifestBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return "", fmt.Errorf("reading manifest for %s: %w", opts.Ref, err)
	}

	// Verify digest if specified
	if opts.Digest != "" {
		if desc.Digest.String() != opts.Digest {
			return "", fmt.Errorf("digest mismatch: expected %s, got %s", opts.Digest, desc.Digest.String())
		}
	}

	// Parse the manifest to find content layers
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", fmt.Errorf("parsing manifest: %w", err)
	}

	// Look for the skill content layer
	for _, layer := range manifest.Layers {
		switch layer.MediaType {
		case MediaTypeSkillContent:
			// Agent Skills spec: tar.gz containing <name>/SKILL.md
			return c.extractSkillFromTarGz(ctx, repo, layer)
		}
	}

	// Fallback: if no spec-specific media type found, try to read the first layer as plain text.
	// This supports simpler artifacts where SKILL.md is stored as a plain layer.
	if len(manifest.Layers) > 0 {
		return c.fetchLayerContent(ctx, repo, manifest.Layers[0])
	}

	return "", fmt.Errorf("no content layers found in artifact %s", opts.Ref)
}

// PullFileContent pulls a generic OCI artifact and returns its file content.
// Used for Tool and Plugin capabilities where the artifact contains a single
// .ts/.js source file as a layer.
//
// Strategy:
//  1. If a layer has a tar+gzip media type, extract the first file from the archive
//  2. Otherwise, return the first layer's raw content
func (c *Client) PullFileContent(ctx context.Context, opts PullOptions) (string, error) {
	repo, ref, err := c.resolveRepository(opts)
	if err != nil {
		return "", fmt.Errorf("resolving repository: %w", err)
	}

	// Fetch the manifest
	desc, rc, err := oras.Fetch(ctx, repo, ref, oras.DefaultFetchOptions)
	if err != nil {
		return "", fmt.Errorf("fetching manifest for %s: %w", opts.Ref, err)
	}
	manifestBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return "", fmt.Errorf("reading manifest for %s: %w", opts.Ref, err)
	}

	// Verify digest if specified
	if opts.Digest != "" {
		if desc.Digest.String() != opts.Digest {
			return "", fmt.Errorf("digest mismatch: expected %s, got %s", opts.Digest, desc.Digest.String())
		}
	}

	// Parse the manifest
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", fmt.Errorf("parsing manifest: %w", err)
	}

	if len(manifest.Layers) == 0 {
		return "", fmt.Errorf("no content layers found in artifact %s", opts.Ref)
	}

	// Try each layer for content
	for _, layer := range manifest.Layers {
		if isTarGzMediaType(layer.MediaType) {
			return c.extractFirstFileFromTarGz(ctx, repo, layer)
		}
	}

	// Fallback: return first layer as plain content
	return c.fetchLayerContent(ctx, repo, manifest.Layers[0])
}

// resolveRepository parses the OCI reference and creates an authenticated repository client.
func (c *Client) resolveRepository(opts PullOptions) (*remote.Repository, string, error) {
	// Parse the reference to separate registry/repo from tag/digest
	repo, err := remote.NewRepository(opts.Ref)
	if err != nil {
		return nil, "", fmt.Errorf("parsing OCI reference %q: %w", opts.Ref, err)
	}

	repo.PlainHTTP = c.plainHTTP || opts.PlainHTTP

	// Configure authentication
	if opts.Credentials != nil {
		repo.Client = &auth.Client{
			Credential: auth.StaticCredential(repo.Reference.Registry, auth.Credential{
				Username: opts.Credentials.Username,
				Password: opts.Credentials.Password,
			}),
		}
	}

	// Build the reference string for fetching (tag or digest)
	ref := repo.Reference.Reference
	if opts.Digest != "" {
		ref = opts.Digest
	}

	return repo, ref, nil
}

// extractSkillFromTarGz extracts SKILL.md content from a tar.gz layer.
// Per the spec, the archive structure is: <skill-name>/SKILL.md
func (c *Client) extractSkillFromTarGz(ctx context.Context, target content.Fetcher, desc ocispec.Descriptor) (string, error) {
	rc, err := target.Fetch(ctx, desc)
	if err != nil {
		return "", fmt.Errorf("fetching layer: %w", err)
	}
	defer rc.Close()

	gz, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar archive: %w", err)
		}

		// Look for SKILL.md in the archive (may be at <name>/SKILL.md or just SKILL.md)
		if strings.HasSuffix(header.Name, "SKILL.md") && header.Typeflag == tar.TypeReg {
			return c.readLimited(tr, header.Size)
		}
	}

	return "", fmt.Errorf("SKILL.md not found in artifact archive")
}

// extractFirstFileFromTarGz extracts the first regular file from a tar.gz layer.
func (c *Client) extractFirstFileFromTarGz(ctx context.Context, target content.Fetcher, desc ocispec.Descriptor) (string, error) {
	rc, err := target.Fetch(ctx, desc)
	if err != nil {
		return "", fmt.Errorf("fetching layer: %w", err)
	}
	defer rc.Close()

	gz, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar archive: %w", err)
		}

		if header.Typeflag == tar.TypeReg {
			return c.readLimited(tr, header.Size)
		}
	}

	return "", fmt.Errorf("no regular files found in artifact archive")
}

// fetchLayerContent fetches a layer and returns its content as a string.
func (c *Client) fetchLayerContent(ctx context.Context, target content.Fetcher, desc ocispec.Descriptor) (string, error) {
	if desc.Size > maxContentSize {
		return "", fmt.Errorf("layer size %d exceeds maximum %d bytes", desc.Size, maxContentSize)
	}

	rc, err := target.Fetch(ctx, desc)
	if err != nil {
		return "", fmt.Errorf("fetching layer: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxContentSize+1))
	if err != nil {
		return "", fmt.Errorf("reading layer content: %w", err)
	}
	if int64(len(data)) > maxContentSize {
		return "", fmt.Errorf("layer content exceeds maximum %d bytes", maxContentSize)
	}

	return string(data), nil
}

// readLimited reads up to maxContentSize bytes from a reader.
func (c *Client) readLimited(r io.Reader, declaredSize int64) (string, error) {
	if declaredSize > maxContentSize {
		return "", fmt.Errorf("file size %d exceeds maximum %d bytes", declaredSize, maxContentSize)
	}

	data, err := io.ReadAll(io.LimitReader(r, maxContentSize+1))
	if err != nil {
		return "", fmt.Errorf("reading content: %w", err)
	}
	if int64(len(data)) > maxContentSize {
		return "", fmt.Errorf("content exceeds maximum %d bytes", maxContentSize)
	}

	return string(data), nil
}

// isTarGzMediaType checks if a media type indicates a tar+gzip layer.
func isTarGzMediaType(mediaType string) bool {
	return strings.Contains(mediaType, "tar+gzip") || strings.Contains(mediaType, "tar.gz")
}

// ExtractToolName extracts the tool name from an OCI reference.
// It uses the last path segment before the tag/digest as the name.
//
// This is used by both PiAgent (workflowrun_piagent.go) and MCP tool-bridge
// (mcp_deployment.go) to derive the extraction directory name for OCI tool packages.
//
// Examples:
//
//	"ghcr.io/samyn92/agent-tools/git:0.1.0"    → "git"
//	"ghcr.io/samyn92/agent-tools/file:0.1.0"   → "file"
//	"ghcr.io/org/tools/gitlab@sha256:abc..."    → "gitlab"
//	"registry.io/tool:latest"                    → "tool"
func ExtractToolName(ref string) string {
	// Remove tag (:...) or digest (@sha256:...)
	name := ref
	if idx := strings.LastIndex(name, "@"); idx != -1 {
		name = name[:idx]
	}
	if idx := strings.LastIndex(name, ":"); idx != -1 {
		// Make sure this isn't the port separator (e.g., "registry:5000/foo")
		// by checking if there's a "/" after it
		afterColon := name[idx+1:]
		if !strings.Contains(afterColon, "/") {
			name = name[:idx]
		}
	}

	// Get the last path segment
	if idx := strings.LastIndex(name, "/"); idx != -1 {
		name = name[idx+1:]
	}

	// Sanitize for Kubernetes naming (lowercase, alphanumeric + hyphens)
	name = strings.ToLower(name)
	var sanitized []byte
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sanitized = append(sanitized, c)
		}
	}
	if len(sanitized) == 0 {
		return "tool"
	}
	return string(sanitized)
}
