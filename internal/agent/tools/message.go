package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type messageDefaults struct {
	Channel   string
	AccountID string
	ChatID    string
}

type messageDefaultsKey struct{}

type messageArgs struct {
	Channel    string   `json:"channel"`
	AccountID  string   `json:"account_id"`
	ChatID     string   `json:"chat_id"`
	Text       string   `json:"text"`
	MediaPaths []string `json:"media_paths"`
	FilePaths  []string `json:"file_paths"`
}

// WithMessageDefaults attaches the current conversation routing fields to the tool context.
func WithMessageDefaults(ctx context.Context, msg bus.InboundMessage) context.Context {
	return context.WithValue(ctx, messageDefaultsKey{}, messageDefaults{
		Channel:   msg.Channel,
		AccountID: msg.AccountID,
		ChatID:    msg.ChatID,
	})
}

func messageDefaultsFromContext(ctx context.Context) messageDefaults {
	defaults, _ := ctx.Value(messageDefaultsKey{}).(messageDefaults)
	return defaults
}

// RegisterMessage registers the message tool with the given message bus.
func RegisterMessage(r *Registry, mb *bus.MessageBus) {
	r.tools["message"] = registeredTool{
		def: r.tools["message"].def,
		fn:  makeMessageTool(mb),
	}
}

func registerMessage(r *Registry) {
	// Register with a placeholder; will be re-registered with actual bus later.
	r.Register("message", "Send a message, local image attachments, or local files to a channel. If channel/account_id/chat_id are omitted, the current conversation is used.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Target channel (e.g. 'telegram'). Defaults to the current conversation.",
			},
			"account_id": map[string]interface{}{
				"type":        "string",
				"description": "Target account within the channel. Defaults to the current conversation.",
			},
			"chat_id": map[string]interface{}{
				"type":        "string",
				"description": "Target chat ID. Defaults to the current conversation.",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Message text to send. Optional if media_paths is provided.",
			},
			"media_paths": map[string]interface{}{
				"type":        "array",
				"description": "Optional local file paths to send as image attachments. Currently supported by Telegram.",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"file_paths": map[string]interface{}{
				"type":        "array",
				"description": "Optional local file paths to send as document attachments. Currently supported by Telegram.",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		return "", fmt.Errorf("message bus not initialized")
	})
}

func makeMessageTool(mb *bus.MessageBus) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args messageArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		defaults := messageDefaultsFromContext(ctx)
		channel := firstNonEmpty(args.Channel, defaults.Channel)
		accountID := firstNonEmpty(args.AccountID, defaults.AccountID)
		chatID := firstNonEmpty(args.ChatID, defaults.ChatID)

		if channel == "" {
			return "", fmt.Errorf("channel is required when no current conversation is available")
		}
		if chatID == "" {
			return "", fmt.Errorf("chat_id is required when no current conversation is available")
		}

		mediaPaths := compactStrings(args.MediaPaths)
		filePaths := compactStrings(args.FilePaths)
		if len(mediaPaths) > 0 && len(filePaths) > 0 {
			return "", fmt.Errorf("media_paths and file_paths cannot be used together")
		}
		if args.Text == "" && len(mediaPaths) == 0 && len(filePaths) == 0 {
			return "", fmt.Errorf("text, media_paths, or file_paths is required")
		}

		mb.Outbound <- bus.OutboundMessage{
			Channel:    channel,
			AccountID:  accountID,
			ChatID:     chatID,
			Text:       args.Text,
			MediaPaths: mediaPaths,
			FilePaths:  filePaths,
		}

		return "Message sent", nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func compactStrings(values []string) []string {
	compacted := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			compacted = append(compacted, value)
		}
	}
	return compacted
}
