package main

import (
	"errors"
	"os"
	"strings"
)

// Config holds the channel configuration
type Config struct {
	// Port to listen on
	Port string

	// OpenCode server URL
	OpenCodeURL string

	// Telegram bot token
	BotToken string

	// Allowed user IDs (empty = all allowed)
	AllowedUsers map[string]bool
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	opencodeURL := os.Getenv("OPENCODE_URL")
	if opencodeURL == "" {
		opencodeURL = "http://localhost:4096"
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	allowedUsers := make(map[string]bool)
	if usersStr := os.Getenv("ALLOWED_USERS"); usersStr != "" {
		for _, user := range strings.Split(usersStr, ",") {
			user = strings.TrimSpace(user)
			if user != "" {
				allowedUsers[user] = true
			}
		}
	}

	return &Config{
		Port:         port,
		OpenCodeURL:  opencodeURL,
		BotToken:     botToken,
		AllowedUsers: allowedUsers,
	}, nil
}
