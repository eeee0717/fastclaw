package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestMessageToolUsesCurrentConversationDefaults(t *testing.T) {
	mb := bus.New()
	tool := makeMessageTool(mb)

	ctx := WithMessageDefaults(context.Background(), bus.InboundMessage{
		Channel:   "telegram",
		AccountID: "bot-1",
		ChatID:    "chat-123",
	})

	args, err := json.Marshal(messageArgs{
		Text:       "caption text",
		MediaPaths: []string{"/tmp/tg-1.png"},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	result, err := tool(ctx, args)
	if err != nil {
		t.Fatalf("tool returned error: %v", err)
	}
	if result != "Message sent" {
		t.Fatalf("expected success result, got %q", result)
	}

	select {
	case msg := <-mb.Outbound:
		if msg.Channel != "telegram" {
			t.Fatalf("expected channel telegram, got %q", msg.Channel)
		}
		if msg.AccountID != "bot-1" {
			t.Fatalf("expected account bot-1, got %q", msg.AccountID)
		}
		if msg.ChatID != "chat-123" {
			t.Fatalf("expected chat chat-123, got %q", msg.ChatID)
		}
		if msg.Text != "caption text" {
			t.Fatalf("expected text %q, got %q", "caption text", msg.Text)
		}
		if len(msg.MediaPaths) != 1 || msg.MediaPaths[0] != "/tmp/tg-1.png" {
			t.Fatalf("unexpected media paths: %#v", msg.MediaPaths)
		}
	default:
		t.Fatal("expected outbound message")
	}
}

func TestMessageToolAllowsExplicitRoutingOverrides(t *testing.T) {
	mb := bus.New()
	tool := makeMessageTool(mb)

	ctx := WithMessageDefaults(context.Background(), bus.InboundMessage{
		Channel:   "telegram",
		AccountID: "bot-1",
		ChatID:    "chat-123",
	})

	args, err := json.Marshal(messageArgs{
		Channel:   "discord",
		AccountID: "bot-2",
		ChatID:    "chan-456",
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	if _, err := tool(ctx, args); err != nil {
		t.Fatalf("tool returned error: %v", err)
	}

	select {
	case msg := <-mb.Outbound:
		if msg.Channel != "discord" || msg.AccountID != "bot-2" || msg.ChatID != "chan-456" {
			t.Fatalf("unexpected routing fields: %#v", msg)
		}
	default:
		t.Fatal("expected outbound message")
	}
}

func TestMessageToolRequiresPayload(t *testing.T) {
	mb := bus.New()
	tool := makeMessageTool(mb)

	ctx := WithMessageDefaults(context.Background(), bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-123",
	})

	args, err := json.Marshal(messageArgs{})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	if _, err := tool(ctx, args); err == nil {
		t.Fatal("expected error when both text and media_paths are empty")
	}
}
