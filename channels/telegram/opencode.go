package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenCodeClient handles communication with OpenCode server
type OpenCodeClient struct {
	baseURL    string
	httpClient *http.Client

	// Session management - one session per Telegram user
	sessions   map[string]string // userID -> sessionID
	sessionsMu sync.RWMutex
}

// NewOpenCodeClient creates a new OpenCode client
func NewOpenCodeClient(baseURL string) *OpenCodeClient {
	return &OpenCodeClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // Long timeout for AI inference
		},
		sessions: make(map[string]string),
	}
}

// CreateSessionRequest is the request to create a session
type CreateSessionRequest struct {
	Title string `json:"title,omitempty"`
}

// CreateSessionResponse is the response from creating a session
type CreateSessionResponse struct {
	ID string `json:"id"`
}

// MessageRequest is the request to send a message
type MessageRequest struct {
	Parts []MessagePart `json:"parts"`
}

// MessagePart represents a part of a message
type MessagePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// EventResponse represents a streaming event from OpenCode
type EventResponse struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// SessionStatus represents the status of a session
type SessionStatus struct {
	Type string `json:"type"` // "idle" or "busy"
}

// MessageData represents the data in a message event
type MessageData struct {
	Role  string        `json:"role"`
	Parts []MessagePart `json:"parts"`
}

// GetOrCreateSession gets or creates a session for a user
func (c *OpenCodeClient) GetOrCreateSession(ctx context.Context, userID string) (string, error) {
	c.sessionsMu.RLock()
	if sessionID, ok := c.sessions[userID]; ok {
		c.sessionsMu.RUnlock()
		return sessionID, nil
	}
	c.sessionsMu.RUnlock()

	// Create new session
	sessionID, err := c.createSession(ctx, fmt.Sprintf("telegram-%s", userID))
	if err != nil {
		return "", err
	}

	c.sessionsMu.Lock()
	c.sessions[userID] = sessionID
	c.sessionsMu.Unlock()

	slog.Info("Created new session", "userID", userID, "sessionID", sessionID)
	return sessionID, nil
}

func (c *OpenCodeClient) createSession(ctx context.Context, title string) (string, error) {
	reqBody, _ := json.Marshal(CreateSessionRequest{Title: title})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/session", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create session: status %d, body: %s", resp.StatusCode, string(body))
	}

	var result CreateSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode session response: %w", err)
	}

	return result.ID, nil
}

// GetSessionStatus gets the status of a session (idle or busy)
func (c *OpenCodeClient) GetSessionStatus(ctx context.Context, sessionID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/session/status", nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get session status: status %d", resp.StatusCode)
	}

	var statuses map[string]SessionStatus
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		return "", err
	}

	if status, ok := statuses[sessionID]; ok {
		return status.Type, nil
	}
	return "idle", nil
}

// AbortSession aborts a running session
func (c *OpenCodeClient) AbortSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/session/"+sessionID+"/abort", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to abort session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to abort session: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// SendMessage sends a message to OpenCode and waits for the response
func (c *OpenCodeClient) SendMessage(ctx context.Context, sessionID, text string) (string, error) {
	// Check if session is busy and abort if needed
	status, err := c.GetSessionStatus(ctx, sessionID)
	if err != nil {
		slog.Warn("Failed to get session status", "error", err)
	} else if status == "busy" {
		slog.Info("Session is busy, aborting previous request", "sessionID", sessionID)
		if err := c.AbortSession(ctx, sessionID); err != nil {
			slog.Warn("Failed to abort session", "error", err)
		}
		// Give it a moment to clean up
		time.Sleep(500 * time.Millisecond)
	}

	reqBody, _ := json.Marshal(MessageRequest{
		Parts: []MessagePart{
			{Type: "text", Text: text},
		},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/session/"+sessionID+"/message", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to send message: status %d, body: %s", resp.StatusCode, string(body))
	}

	// Parse SSE stream and collect the response
	return c.parseSSEResponse(resp.Body)
}

func (c *OpenCodeClient) parseSSEResponse(reader io.Reader) (string, error) {
	// Read the body
	body, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	// Try to parse as OpenCode message response: { info: {...}, parts: [...] }
	var messageResponse struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"parts"`
	}

	if err := json.Unmarshal(body, &messageResponse); err == nil {
		// Extract text from parts
		var texts []string
		for _, part := range messageResponse.Parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n"), nil
		}
	}

	// Fallback: try SSE/event stream format
	var responseText string
	lines := bytes.Split(body, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// Handle SSE format
		if bytes.HasPrefix(line, []byte("data: ")) {
			line = bytes.TrimPrefix(line, []byte("data: "))
		}

		var event EventResponse
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		text := c.extractTextFromEvent(event)
		if text != "" {
			responseText = text
		}
	}

	if responseText != "" {
		return responseText, nil
	}

	// Last resort: return raw body (truncated for debugging)
	if len(body) > 500 {
		return string(body[:500]) + "...", nil
	}
	return string(body), nil
}

func (c *OpenCodeClient) extractTextFromEvent(event EventResponse) string {
	if event.Type == "message" || event.Type == "assistant" {
		var msgData MessageData
		if err := json.Unmarshal(event.Data, &msgData); err == nil {
			for _, part := range msgData.Parts {
				if part.Type == "text" && part.Text != "" {
					return part.Text
				}
			}
		}

		// Try simpler format
		var simple struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Data, &simple); err == nil && simple.Text != "" {
			return simple.Text
		}
	}
	return ""
}
