package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

// computeDigest computes the OCI sha256 digest for the given data.
func computeDigest(data []byte) digest.Digest {
	h := sha256.Sum256(data)
	return digest.Digest(fmt.Sprintf("sha256:%x", h))
}

// createTarGz creates a tar.gz archive with a single file.
func createTarGz(t *testing.T, filename string, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	if err := tw.WriteHeader(&tar.Header{
		Name:     filename,
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestExtractSkillFromTarGz_SkillMDFound(t *testing.T) {
	skillContent := "---\nname: test-skill\n---\n# Test Skill\nDo things."
	tarGzData := createTarGz(t, "test-skill/SKILL.md", skillContent)

	client := NewClient()
	ctx := context.Background()

	store := memory.New()
	digest := computeDigest(tarGzData)
	layerDesc := ocispec.Descriptor{
		MediaType: MediaTypeSkillContent,
		Digest:    digest,
		Size:      int64(len(tarGzData)),
	}

	if err := store.Push(ctx, layerDesc, bytes.NewReader(tarGzData)); err != nil {
		t.Fatal(err)
	}

	result, err := client.extractSkillFromTarGz(ctx, store, layerDesc)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result != skillContent {
		t.Fatalf("expected %q, got %q", skillContent, result)
	}
}

func TestExtractSkillFromTarGz_BareSkillMD(t *testing.T) {
	// SKILL.md at root of archive (no subdirectory)
	skillContent := "# Bare Skill"
	tarGzData := createTarGz(t, "SKILL.md", skillContent)

	client := NewClient()
	ctx := context.Background()

	store := memory.New()
	digest := computeDigest(tarGzData)
	layerDesc := ocispec.Descriptor{
		MediaType: MediaTypeSkillContent,
		Digest:    digest,
		Size:      int64(len(tarGzData)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(tarGzData)); err != nil {
		t.Fatal(err)
	}

	result, err := client.extractSkillFromTarGz(ctx, store, layerDesc)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result != skillContent {
		t.Fatalf("expected %q, got %q", skillContent, result)
	}
}

func TestExtractSkillFromTarGz_SkillMDNotFound(t *testing.T) {
	tarGzData := createTarGz(t, "README.md", "# Just a readme")

	client := NewClient()
	ctx := context.Background()

	store := memory.New()
	digest := computeDigest(tarGzData)
	layerDesc := ocispec.Descriptor{
		MediaType: MediaTypeSkillContent,
		Digest:    digest,
		Size:      int64(len(tarGzData)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(tarGzData)); err != nil {
		t.Fatal(err)
	}

	_, err := client.extractSkillFromTarGz(ctx, store, layerDesc)
	if err == nil {
		t.Fatal("expected error when SKILL.md not found")
	}
}

func TestExtractFirstFileFromTarGz(t *testing.T) {
	fileContent := `import { tool } from "@opencode-ai/plugin"; export default tool({});`
	tarGzData := createTarGz(t, "health-check.ts", fileContent)

	client := NewClient()
	ctx := context.Background()

	store := memory.New()
	digest := computeDigest(tarGzData)
	layerDesc := ocispec.Descriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    digest,
		Size:      int64(len(tarGzData)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(tarGzData)); err != nil {
		t.Fatal(err)
	}

	result, err := client.extractFirstFileFromTarGz(ctx, store, layerDesc)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result != fileContent {
		t.Fatalf("expected %q, got %q", fileContent, result)
	}
}

func TestFetchLayerContent_PlainText(t *testing.T) {
	content := "# My Skill\nDo stuff."

	client := NewClient()
	ctx := context.Background()

	store := memory.New()
	digest := computeDigest([]byte(content))
	layerDesc := ocispec.Descriptor{
		MediaType: "text/plain",
		Digest:    digest,
		Size:      int64(len(content)),
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader([]byte(content))); err != nil {
		t.Fatal(err)
	}

	result, err := client.fetchLayerContent(ctx, store, layerDesc)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result != content {
		t.Fatalf("expected %q, got %q", content, result)
	}
}

func TestFetchLayerContent_TooLarge(t *testing.T) {
	client := NewClient()
	ctx := context.Background()
	_ = ctx

	store := memory.New()
	_ = store

	// Descriptor claims content is too large — should be rejected before even fetching
	layerDesc := ocispec.Descriptor{
		MediaType: "text/plain",
		Size:      maxContentSize + 1,
	}

	_, err := client.fetchLayerContent(context.Background(), memory.New(), layerDesc)
	if err == nil {
		t.Fatal("expected error for oversized layer")
	}
}

func TestIsTarGzMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		expected  bool
	}{
		{MediaTypeSkillContent, true},
		{"application/vnd.oci.image.layer.v1.tar+gzip", true},
		{"application/tar.gz", true},
		{"text/plain", false},
		{"application/json", false},
		{"application/octet-stream", false},
	}

	for _, tt := range tests {
		if got := isTarGzMediaType(tt.mediaType); got != tt.expected {
			t.Errorf("isTarGzMediaType(%q) = %v, want %v", tt.mediaType, got, tt.expected)
		}
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient()
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.plainHTTP {
		t.Fatal("expected plainHTTP to be false by default")
	}
}

func TestNewClientWithOptions(t *testing.T) {
	client := NewClientWithOptions(true)
	if !client.plainHTTP {
		t.Fatal("expected plainHTTP to be true")
	}
}

func TestReadLimited_Normal(t *testing.T) {
	client := NewClient()
	data := "hello world"
	result, err := client.readLimited(bytes.NewReader([]byte(data)), int64(len(data)))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if result != data {
		t.Fatalf("expected %q, got %q", data, result)
	}
}

func TestReadLimited_DeclaredTooLarge(t *testing.T) {
	client := NewClient()
	_, err := client.readLimited(bytes.NewReader([]byte("small")), maxContentSize+1)
	if err == nil {
		t.Fatal("expected error for oversized declared size")
	}
}

// =============================================================================
// ExtractToolName Tests
// =============================================================================

func TestExtractToolName(t *testing.T) {
	tests := []struct {
		ref      string
		expected string
	}{
		{"ghcr.io/samyn92/agent-tools/git:0.1.0", "git"},
		{"ghcr.io/samyn92/agent-tools/file:0.1.0", "file"},
		{"ghcr.io/org/tools/gitlab@sha256:abc123", "gitlab"},
		{"registry.io/tool:latest", "tool"},
		{"registry:5000/org/my-tool:v1", "my-tool"},
		{"ghcr.io/samyn92/agent-tools/git", "git"},
		{"registry:5000/foo/bar/baz:1.0", "baz"},
		// Edge cases
		{"ghcr.io/samyn92/agent-tools/MY-Tool:0.1.0", "my-tool"},
		{"ghcr.io/samyn92/tools/some_thing:v1", "something"},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := ExtractToolName(tt.ref)
			if got != tt.expected {
				t.Errorf("ExtractToolName(%q) = %q, want %q", tt.ref, got, tt.expected)
			}
		})
	}
}
