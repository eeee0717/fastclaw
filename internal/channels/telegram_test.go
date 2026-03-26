package channels

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type fakeTelegramBot struct {
	sendCalls       []tgbotapi.Chattable
	mediaGroupCalls []tgbotapi.MediaGroupConfig
	mediaGroupErr   error
}

func (f *fakeTelegramBot) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return nil
}

func (f *fakeTelegramBot) StopReceivingUpdates() {}

func (f *fakeTelegramBot) GetFileDirectURL(string) (string, error) {
	return "", nil
}

func (f *fakeTelegramBot) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (f *fakeTelegramBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.sendCalls = append(f.sendCalls, c)
	return tgbotapi.Message{}, nil
}

func (f *fakeTelegramBot) SendMediaGroup(cfg tgbotapi.MediaGroupConfig) ([]tgbotapi.Message, error) {
	f.mediaGroupCalls = append(f.mediaGroupCalls, cfg)
	if f.mediaGroupErr != nil {
		return nil, f.mediaGroupErr
	}
	return []tgbotapi.Message{}, nil
}

func TestTelegramSendMessageSinglePhotoUsesCaption(t *testing.T) {
	tg, fake := newTestTelegram()
	path := writeTestImage(t, "one.png")

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       "hello image",
		MediaPaths: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(fake.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(fake.sendCalls))
	}

	photo, ok := fake.sendCalls[0].(tgbotapi.PhotoConfig)
	if !ok {
		t.Fatalf("expected PhotoConfig, got %T", fake.sendCalls[0])
	}
	if photo.Caption != "hello image" {
		t.Fatalf("expected caption %q, got %q", "hello image", photo.Caption)
	}
	filePath, ok := photo.File.(tgbotapi.FilePath)
	if !ok {
		t.Fatalf("expected FilePath, got %T", photo.File)
	}
	if string(filePath) != path {
		t.Fatalf("expected file path %q, got %q", path, string(filePath))
	}
}

func TestTelegramSendMessageMultiPhotoUsesMediaGroup(t *testing.T) {
	tg, fake := newTestTelegram()
	first := writeTestImage(t, "one.png")
	second := writeTestImage(t, "two.png")

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       "album caption",
		MediaPaths: []string{first, second},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(fake.mediaGroupCalls) != 1 {
		t.Fatalf("expected 1 media group call, got %d", len(fake.mediaGroupCalls))
	}
	if len(fake.sendCalls) != 0 {
		t.Fatalf("expected no direct send calls, got %d", len(fake.sendCalls))
	}

	cfg := fake.mediaGroupCalls[0]
	if len(cfg.Media) != 2 {
		t.Fatalf("expected 2 media items, got %d", len(cfg.Media))
	}
	firstPhoto, ok := cfg.Media[0].(tgbotapi.InputMediaPhoto)
	if !ok {
		t.Fatalf("expected first media item to be InputMediaPhoto, got %T", cfg.Media[0])
	}
	secondPhoto, ok := cfg.Media[1].(tgbotapi.InputMediaPhoto)
	if !ok {
		t.Fatalf("expected second media item to be InputMediaPhoto, got %T", cfg.Media[1])
	}
	if firstPhoto.Caption != "album caption" {
		t.Fatalf("expected first caption %q, got %q", "album caption", firstPhoto.Caption)
	}
	if secondPhoto.Caption != "" {
		t.Fatalf("expected second caption to be empty, got %q", secondPhoto.Caption)
	}
}

func TestTelegramSendMessageLongTextSendsExtraMessage(t *testing.T) {
	tg, fake := newTestTelegram()
	first := writeTestImage(t, "one.png")
	second := writeTestImage(t, "two.png")
	longText := strings.Repeat("a", telegramCaptionLimit+1)

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       longText,
		MediaPaths: []string{first, second},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(fake.mediaGroupCalls) != 1 {
		t.Fatalf("expected 1 media group call, got %d", len(fake.mediaGroupCalls))
	}
	if len(fake.sendCalls) != 1 {
		t.Fatalf("expected 1 text send call, got %d", len(fake.sendCalls))
	}

	firstPhoto := fake.mediaGroupCalls[0].Media[0].(tgbotapi.InputMediaPhoto)
	if firstPhoto.Caption != "" {
		t.Fatalf("expected empty media caption for long text, got %q", firstPhoto.Caption)
	}

	textMsg, ok := fake.sendCalls[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", fake.sendCalls[0])
	}
	if textMsg.Text != longText {
		t.Fatalf("expected text %q, got %q", longText, textMsg.Text)
	}
}

func TestTelegramSendMessageFallsBackToIndividualPhotos(t *testing.T) {
	tg, fake := newTestTelegram()
	fake.mediaGroupErr = errors.New("media group failed")
	first := writeTestImage(t, "one.png")
	second := writeTestImage(t, "two.png")

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       "fallback caption",
		MediaPaths: []string{first, second},
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(fake.mediaGroupCalls) != 1 {
		t.Fatalf("expected 1 media group attempt, got %d", len(fake.mediaGroupCalls))
	}
	if len(fake.sendCalls) != 2 {
		t.Fatalf("expected 2 fallback photo sends, got %d", len(fake.sendCalls))
	}

	firstPhoto := fake.sendCalls[0].(tgbotapi.PhotoConfig)
	secondPhoto := fake.sendCalls[1].(tgbotapi.PhotoConfig)
	if firstPhoto.Caption != "fallback caption" {
		t.Fatalf("expected first fallback caption %q, got %q", "fallback caption", firstPhoto.Caption)
	}
	if secondPhoto.Caption != "" {
		t.Fatalf("expected second fallback caption to be empty, got %q", secondPhoto.Caption)
	}
}

func TestTelegramSendMessageSplitsAlbumsAboveTelegramLimit(t *testing.T) {
	tg, fake := newTestTelegram()
	var paths []string
	for i := 0; i < telegramMediaGroupMax+1; i++ {
		paths = append(paths, writeTestImage(t, filepath.Join("batch", "img"+strconv.Itoa(i)+".png")))
	}

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       "batch caption",
		MediaPaths: paths,
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(fake.mediaGroupCalls) != 1 {
		t.Fatalf("expected 1 media group call, got %d", len(fake.mediaGroupCalls))
	}
	if len(fake.mediaGroupCalls[0].Media) != telegramMediaGroupMax {
		t.Fatalf("expected first media group to contain %d items, got %d", telegramMediaGroupMax, len(fake.mediaGroupCalls[0].Media))
	}
	if len(fake.sendCalls) != 1 {
		t.Fatalf("expected 1 trailing photo send, got %d", len(fake.sendCalls))
	}

	lastPhoto := fake.sendCalls[0].(tgbotapi.PhotoConfig)
	if lastPhoto.Caption != "" {
		t.Fatalf("expected trailing photo caption to be empty, got %q", lastPhoto.Caption)
	}
}

func TestTelegramSendMessageRejectsButtonsWithMedia(t *testing.T) {
	tg, fake := newTestTelegram()
	path := writeTestImage(t, "one.png")

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		Text:       "hello",
		MediaPaths: []string{path},
		Buttons: [][]bus.OutboundButton{
			{{Text: "Open", URL: "https://example.com"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for media buttons, got nil")
	}
	if len(fake.sendCalls) != 0 || len(fake.mediaGroupCalls) != 0 {
		t.Fatal("expected no Telegram API calls when media buttons are rejected")
	}
}

func TestTelegramSendMessageRejectsEditWithMedia(t *testing.T) {
	tg, fake := newTestTelegram()
	path := writeTestImage(t, "one.png")

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		EditMsgID:  "100",
		MediaPaths: []string{path},
	})
	if err == nil {
		t.Fatal("expected error for media edits, got nil")
	}
	if len(fake.sendCalls) != 0 || len(fake.mediaGroupCalls) != 0 {
		t.Fatal("expected no Telegram API calls when media edit is rejected")
	}
}

func TestTelegramSendMessageRejectsNonImagePath(t *testing.T) {
	tg, _ := newTestTelegram()
	dir := t.TempDir()
	path := filepath.Join(dir, "not-image.txt")
	if err := os.WriteFile(path, []byte("plain text"), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
	}

	err := tg.SendMessage(bus.OutboundMessage{
		ChatID:     "42",
		MediaPaths: []string{path},
	})
	if err == nil {
		t.Fatal("expected error for non-image media path, got nil")
	}
}

func newTestTelegram() (*Telegram, *fakeTelegramBot) {
	fake := &fakeTelegramBot{}
	return &Telegram{bot: fake}, fake
}

func writeTestImage(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}
	data := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	return path
}
