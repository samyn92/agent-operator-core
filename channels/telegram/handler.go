package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
)

// Handler handles Telegram webhook requests
type Handler struct {
	config    *Config
	opencode  *OpenCodeClient
	telegramAPI string
}

// NewHandler creates a new handler
func NewHandler(cfg *Config, opencode *OpenCodeClient) *Handler {
	return &Handler{
		config:      cfg,
		opencode:    opencode,
		telegramAPI: "https://api.telegram.org/bot" + cfg.BotToken,
	}
}

// TelegramUpdate represents a Telegram webhook update
type TelegramUpdate struct {
	UpdateID int              `json:"update_id"`
	Message  *TelegramMessage `json:"message,omitempty"`
}

// TelegramMessage represents a Telegram message
type TelegramMessage struct {
	MessageID int           `json:"message_id"`
	From      *TelegramUser `json:"from,omitempty"`
	Chat      TelegramChat  `json:"chat"`
	Text      string        `json:"text,omitempty"`
	Date      int           `json:"date"`
}

// TelegramUser represents a Telegram user
type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// TelegramChat represents a Telegram chat
type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// HandleHealth handles health check requests
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// HandleWebhook handles Telegram webhook requests
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("Failed to read request body", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var update TelegramUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		slog.Error("Failed to parse update", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Process message asynchronously
	go h.processUpdate(context.Background(), update)

	// Respond immediately to Telegram
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) processUpdate(ctx context.Context, update TelegramUpdate) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}

	msg := update.Message
	userID := strconv.FormatInt(msg.From.ID, 10)

	slog.Info("Received message",
		"userID", userID,
		"username", msg.From.Username,
		"text", msg.Text,
	)

	// Check if user is allowed
	if len(h.config.AllowedUsers) > 0 {
		if !h.config.AllowedUsers[userID] {
			slog.Warn("Unauthorized user", "userID", userID)
			h.sendMessage(ctx, msg.Chat.ID, "Sorry, you are not authorized to use this bot.")
			return
		}
	}

	// Send typing indicator
	h.sendChatAction(ctx, msg.Chat.ID, "typing")

	// Get or create session for this user
	sessionID, err := h.opencode.GetOrCreateSession(ctx, userID)
	if err != nil {
		slog.Error("Failed to get/create session", "error", err)
		h.sendMessage(ctx, msg.Chat.ID, "Sorry, I'm having trouble connecting. Please try again.")
		return
	}

	// Send message to OpenCode
	response, err := h.opencode.SendMessage(ctx, sessionID, msg.Text)
	if err != nil {
		slog.Error("Failed to get response from OpenCode", "error", err)
		h.sendMessage(ctx, msg.Chat.ID, "Sorry, I encountered an error processing your request.")
		return
	}

	// Send response back to user
	if response != "" {
		if err := h.sendMessage(ctx, msg.Chat.ID, response); err != nil {
			slog.Error("Failed to send response", "error", err)
		}
	}
}

func (h *Handler) sendMessage(ctx context.Context, chatID int64, text string) error {
	// Telegram message limit is 4096 characters
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}

	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	return h.callTelegramAPI(ctx, "sendMessage", payload)
}

func (h *Handler) sendChatAction(ctx context.Context, chatID int64, action string) error {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	}
	return h.callTelegramAPI(ctx, "sendChatAction", payload)
}

func (h *Handler) callTelegramAPI(ctx context.Context, method string, payload interface{}) error {
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", h.telegramAPI+"/"+method, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(jsonReader(body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func jsonReader(data []byte) io.Reader {
	return &jsonReaderImpl{data: data}
}

type jsonReaderImpl struct {
	data []byte
	pos  int
}

func (r *jsonReaderImpl) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
