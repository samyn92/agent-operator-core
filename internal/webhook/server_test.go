package webhook

import (
	"encoding/json"
	"testing"
)

func TestNormalizeGitLabEvent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Issue Hook", "issue"},
		{"Push Hook", "push"},
		{"Merge Request Hook", "merge_request"},
		{"Note Hook", "note"},
		{"Pipeline Hook", "pipeline"},
		{"issue", "issue"},
		{"push", "push"},
		{"merge_request", "merge_request"},
		{"ISSUE HOOK", "issue"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeGitLabEvent(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeGitLabEvent(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestContainsNormalized(t *testing.T) {
	tests := []struct {
		name           string
		slice          []string
		normalizedItem string
		expected       bool
	}{
		{
			name:           "raw GitLab event name matches normalized",
			slice:          []string{"Issue Hook"},
			normalizedItem: "issue",
			expected:       true,
		},
		{
			name:           "already normalized event name matches",
			slice:          []string{"issue"},
			normalizedItem: "issue",
			expected:       true,
		},
		{
			name:           "mixed formats",
			slice:          []string{"Push Hook", "merge_request", "Issue Hook"},
			normalizedItem: "issue",
			expected:       true,
		},
		{
			name:           "no match",
			slice:          []string{"Push Hook", "Merge Request Hook"},
			normalizedItem: "issue",
			expected:       false,
		},
		{
			name:           "empty slice",
			slice:          []string{},
			normalizedItem: "issue",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsNormalized(tt.slice, tt.normalizedItem)
			if result != tt.expected {
				t.Errorf("containsNormalized(%v, %q) = %v, want %v", tt.slice, tt.normalizedItem, result, tt.expected)
			}
		})
	}
}

func TestExtractGitLabLabels(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected []string
	}{
		{
			name: "labels in object_attributes",
			payload: `{
				"object_kind": "issue",
				"object_attributes": {
					"title": "Test issue",
					"labels": [
						{"id": 1, "title": "agent-task", "color": "#ff0000"},
						{"id": 2, "title": "bug", "color": "#00ff00"}
					]
				}
			}`,
			expected: []string{"agent-task", "bug"},
		},
		{
			name: "labels at top level",
			payload: `{
				"object_kind": "issue",
				"labels": [
					{"id": 1, "title": "agent-task"}
				]
			}`,
			expected: []string{"agent-task"},
		},
		{
			name:     "no labels",
			payload:  `{"object_kind": "push"}`,
			expected: nil,
		},
		{
			name: "empty labels array",
			payload: `{
				"object_attributes": {
					"labels": []
				}
			}`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(tt.payload), &payload); err != nil {
				t.Fatalf("failed to parse payload: %v", err)
			}
			result := extractGitLabLabels(payload)
			if len(result) != len(tt.expected) {
				t.Errorf("extractGitLabLabels() returned %v, want %v", result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("extractGitLabLabels()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestExtractGitHubLabels(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected []string
	}{
		{
			name: "labels on issue",
			payload: `{
				"action": "opened",
				"issue": {
					"title": "Test",
					"labels": [
						{"id": 1, "name": "agent-task"},
						{"id": 2, "name": "enhancement"}
					]
				}
			}`,
			expected: []string{"agent-task", "enhancement"},
		},
		{
			name: "labels on pull_request",
			payload: `{
				"action": "opened",
				"pull_request": {
					"title": "Test PR",
					"labels": [
						{"id": 1, "name": "review-needed"}
					]
				}
			}`,
			expected: []string{"review-needed"},
		},
		{
			name:     "no labels",
			payload:  `{"action": "push"}`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(tt.payload), &payload); err != nil {
				t.Fatalf("failed to parse payload: %v", err)
			}
			result := extractGitHubLabels(payload)
			if len(result) != len(tt.expected) {
				t.Errorf("extractGitHubLabels() returned %v, want %v", result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("extractGitHubLabels()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestHasAllLabels(t *testing.T) {
	tests := []struct {
		name          string
		payloadLabels []string
		required      []string
		expected      bool
	}{
		{
			name:          "all required present",
			payloadLabels: []string{"agent-task", "bug", "priority-high"},
			required:      []string{"agent-task", "bug"},
			expected:      true,
		},
		{
			name:          "exact match",
			payloadLabels: []string{"agent-task"},
			required:      []string{"agent-task"},
			expected:      true,
		},
		{
			name:          "missing required label",
			payloadLabels: []string{"bug"},
			required:      []string{"agent-task"},
			expected:      false,
		},
		{
			name:          "empty payload labels",
			payloadLabels: []string{},
			required:      []string{"agent-task"},
			expected:      false,
		},
		{
			name:          "no required labels",
			payloadLabels: []string{"agent-task"},
			required:      []string{},
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasAllLabels(tt.payloadLabels, tt.required)
			if result != tt.expected {
				t.Errorf("hasAllLabels(%v, %v) = %v, want %v", tt.payloadLabels, tt.required, result, tt.expected)
			}
		})
	}
}
