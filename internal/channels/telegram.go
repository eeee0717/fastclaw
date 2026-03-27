package channels

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

var mentionRe = regexp.MustCompile(`@(\w+)`)

// markdownV2Escaper escapes special characters for Telegram MarkdownV2.
var markdownV2SpecialChars = []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

const (
	telegramCaptionLimit  = 1024
	telegramMediaGroupMax = 10
)

var telegramImageExtensions = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".webp": {},
}

type telegramAPI interface {
	GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
	GetFileDirectURL(string) (string, error)
	Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	Send(tgbotapi.Chattable) (tgbotapi.Message, error)
	SendMediaGroup(tgbotapi.MediaGroupConfig) ([]tgbotapi.Message, error)
}

// Telegram implements the Channel interface for Telegram Bot API.
type Telegram struct {
	bot         telegramAPI
	bus         *bus.MessageBus
	accountID   string
	botUsername string
}

// NewTelegram creates a new Telegram channel instance for the given account.
func NewTelegram(botToken string, accountID string, mb *bus.MessageBus) (*Telegram, error) {
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	slog.Info("telegram bot authorized", "username", bot.Self.UserName, "account", accountID)

	return &Telegram{
		bot:         bot,
		bus:         mb,
		accountID:   accountID,
		botUsername: bot.Self.UserName,
	}, nil
}

func (t *Telegram) Name() string {
	return "telegram"
}

func (t *Telegram) AccountID() string {
	return t.accountID
}

// BotUsername returns the Telegram bot's username (without @).
func (t *Telegram) BotUsername() string {
	return t.botUsername
}

// Start begins long polling for Telegram updates.
func (t *Telegram) Start(ctx context.Context) error {
	// Register bot commands so users see them in the / menu
	t.registerCommands()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := t.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			t.bot.StopReceivingUpdates()
			return nil
		case update := <-updates:
			t.handleUpdate(update)
		}
	}
}

func (t *Telegram) handleUpdate(update tgbotapi.Update) {
	// Handle callback queries (inline keyboard button presses)
	if update.CallbackQuery != nil {
		t.handleCallbackQuery(update.CallbackQuery)
		return
	}

	// Handle edited messages - treat like new messages
	msg := update.Message
	if msg == nil {
		msg = update.EditedMessage
	}
	if msg == nil {
		return
	}

	// Build inbound message
	inbound := t.buildInboundMessage(msg)
	if inbound == nil {
		return
	}

	t.bus.Inbound <- *inbound
}

func (t *Telegram) buildInboundMessage(msg *tgbotapi.Message) *bus.InboundMessage {
	// Handle photos
	var photoURL string
	if msg.Photo != nil && len(msg.Photo) > 0 {
		// Use the largest photo (last in the array)
		largest := msg.Photo[len(msg.Photo)-1]
		fileURL, err := t.bot.GetFileDirectURL(largest.FileID)
		if err != nil {
			slog.Warn("telegram get photo URL", "error", err)
		} else {
			photoURL = fileURL
		}
	}

	// Skip messages with no text and no photo
	text := msg.Text
	if msg.Caption != "" {
		text = msg.Caption
	}
	if text == "" && photoURL == "" {
		// Unsupported message type (sticker, voice, etc.) - skip
		slog.Debug("telegram skipping unsupported message type",
			"chat_id", msg.Chat.ID,
			"from", msg.From.UserName,
		)
		return nil
	}

	peerKind := "dm"
	if msg.Chat.IsGroup() || msg.Chat.IsSuperGroup() {
		peerKind = "group"
	}

	senderName := msg.From.UserName
	if senderName == "" {
		senderName = msg.From.FirstName
	}

	// Parse @mentions from message text
	var mentions []string
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		mentions = append(mentions, m[1])
	}

	isBot := msg.From.IsBot

	// Track reply-to
	var replyToMsgID string
	if msg.ReplyToMessage != nil {
		replyToMsgID = strconv.Itoa(msg.ReplyToMessage.MessageID)
	}

	slog.Info("telegram message received",
		"from", senderName,
		"chat_id", msg.Chat.ID,
		"account", t.accountID,
		"peer_kind", peerKind,
		"is_bot", isBot,
		"mentions", mentions,
		"has_photo", photoURL != "",
	)

	return &bus.InboundMessage{
		Channel:      "telegram",
		AccountID:    t.accountID,
		ChatID:       strconv.FormatInt(msg.Chat.ID, 10),
		UserID:       strconv.FormatInt(msg.From.ID, 10),
		MessageID:    strconv.Itoa(msg.MessageID),
		Text:         text,
		PeerKind:     peerKind,
		SenderName:   senderName,
		Mentions:     mentions,
		IsBotMessage: isBot,
		PhotoURL:     photoURL,
		ReplyToMsgID: replyToMsgID,
	}
}

func (t *Telegram) handleCallbackQuery(cq *tgbotapi.CallbackQuery) {
	// Acknowledge the callback
	callback := tgbotapi.NewCallback(cq.ID, "")
	if _, err := t.bot.Request(callback); err != nil {
		slog.Warn("telegram callback ack failed", "error", err)
	}

	if cq.Message == nil || cq.Data == "" {
		return
	}

	peerKind := "dm"
	if cq.Message.Chat.IsGroup() || cq.Message.Chat.IsSuperGroup() {
		peerKind = "group"
	}

	senderName := cq.From.UserName
	if senderName == "" {
		senderName = cq.From.FirstName
	}

	t.bus.Inbound <- bus.InboundMessage{
		Channel:      "telegram",
		AccountID:    t.accountID,
		ChatID:       strconv.FormatInt(cq.Message.Chat.ID, 10),
		UserID:       strconv.FormatInt(cq.From.ID, 10),
		MessageID:    strconv.Itoa(cq.Message.MessageID),
		Text:         cq.Data,
		PeerKind:     peerKind,
		SenderName:   senderName,
		IsBotMessage: false,
	}
}

// registerCommands sets the bot command menu visible to users.
func (t *Telegram) registerCommands() {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Start the bot"},
		{Command: "new", Description: "Start a new conversation"},
		{Command: "status", Description: "Show agent status"},
		{Command: "compact", Description: "Compress conversation history"},
		{Command: "help", Description: "Show available commands"},
		{Command: "version", Description: "Show version info"},
	}
	cfg := tgbotapi.NewSetMyCommands(commands...)
	if _, err := t.bot.Request(cfg); err != nil {
		slog.Warn("failed to set bot commands", "error", err)
	} else {
		slog.Info("registered bot commands", "account", t.accountID, "count", len(commands))
	}
}

// Send sends a plain text message to a Telegram chat.
func (t *Telegram) Send(chatID string, text string) error {
	return t.SendMessage(bus.OutboundMessage{
		ChatID: chatID,
		Text:   text,
	})
}

// SendMessage sends a rich outbound message with formatting, reply-to, buttons, etc.
func (t *Telegram) SendMessage(msg bus.OutboundMessage) error {
	id, err := strconv.ParseInt(msg.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse chat ID: %w", err)
	}

	if len(msg.MediaPaths) > 0 && len(msg.FilePaths) > 0 {
		return fmt.Errorf("telegram does not support mixing image and file attachments in one message")
	}
	if len(msg.FilePaths) > 0 {
		return t.sendFileMessage(id, msg)
	}
	if len(msg.MediaPaths) > 0 {
		return t.sendMediaMessage(id, msg)
	}

	return t.sendTextMessage(id, msg)
}

func (t *Telegram) sendTextMessage(chatID int64, msg bus.OutboundMessage) error {
	// Edit existing message
	if msg.EditMsgID != "" {
		return t.editMessage(chatID, msg)
	}

	// Split long messages at paragraph boundaries
	chunks := splitTelegramMessage(msg.Text)

	for i, chunk := range chunks {
		if err := t.sendSingleMessage(chatID, chunk, msg, i == 0); err != nil {
			return err
		}
		// Small delay between split messages
		if i < len(chunks)-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

func (t *Telegram) sendMediaMessage(chatID int64, msg bus.OutboundMessage) error {
	if err := validateTelegramAttachmentMessage(msg); err != nil {
		return err
	}

	mediaPaths, err := normalizeTelegramMediaPaths(msg.MediaPaths)
	if err != nil {
		return err
	}

	caption, overflowText := splitTelegramCaption(msg.Text)

	if len(mediaPaths) == 1 {
		if err := t.sendPhoto(chatID, mediaPaths[0], caption, msg.ParseMode, msg.ReplyToMsgID); err != nil {
			return err
		}
	} else {
		if err := t.sendPhotoAlbum(chatID, mediaPaths, caption, msg.ParseMode, msg.ReplyToMsgID); err != nil {
			return err
		}
	}

	if overflowText == "" {
		return nil
	}

	return t.sendTextMessage(chatID, bus.OutboundMessage{
		Text:      overflowText,
		ParseMode: msg.ParseMode,
	})
}

func (t *Telegram) sendFileMessage(chatID int64, msg bus.OutboundMessage) error {
	if err := validateTelegramAttachmentMessage(msg); err != nil {
		return err
	}

	filePaths, err := normalizeTelegramFilePaths(msg.FilePaths)
	if err != nil {
		return err
	}

	caption, overflowText := splitTelegramCaption(msg.Text)

	if len(filePaths) == 1 {
		if err := t.sendDocument(chatID, filePaths[0], caption, msg.ParseMode, msg.ReplyToMsgID); err != nil {
			return err
		}
	} else {
		if err := t.sendDocumentAlbum(chatID, filePaths, caption, msg.ParseMode, msg.ReplyToMsgID); err != nil {
			return err
		}
	}

	if overflowText == "" {
		return nil
	}

	return t.sendTextMessage(chatID, bus.OutboundMessage{
		Text:      overflowText,
		ParseMode: msg.ParseMode,
	})
}

func (t *Telegram) sendSingleMessage(chatID int64, text string, msg bus.OutboundMessage, isFirst bool) error {
	tgMsg := tgbotapi.NewMessage(chatID, text)

	// Set parse mode with fallback
	if msg.ParseMode != "" {
		if msg.ParseMode == "MarkdownV2" {
			tgMsg.Text = escapeMarkdownV2(text)
		}
		tgMsg.ParseMode = msg.ParseMode
	}

	// Reply-to (only on first chunk)
	if isFirst && msg.ReplyToMsgID != "" {
		replyID, err := strconv.Atoi(msg.ReplyToMsgID)
		if err == nil {
			tgMsg.ReplyToMessageID = replyID
		}
	}

	// Inline keyboard (only on last chunk, but we set on first for single messages)
	if isFirst && len(msg.Buttons) > 0 {
		tgMsg.ReplyMarkup = buildInlineKeyboard(msg.Buttons)
	}

	_, err := t.bot.Send(tgMsg)
	if err != nil && msg.ParseMode == "MarkdownV2" {
		// Fallback to HTML
		slog.Warn("telegram MarkdownV2 failed, trying HTML", "error", err)
		tgMsg.ParseMode = "HTML"
		tgMsg.Text = text // use original text for HTML
		_, err = t.bot.Send(tgMsg)
		if err != nil {
			// Fallback to plain text
			slog.Warn("telegram HTML failed, sending plain", "error", err)
			tgMsg.ParseMode = ""
			tgMsg.Text = text
			_, err = t.bot.Send(tgMsg)
		}
	}
	return err
}

func (t *Telegram) sendPhotoAlbum(chatID int64, mediaPaths []string, caption string, parseMode string, replyToMsgID string) error {
	for start := 0; start < len(mediaPaths); start += telegramMediaGroupMax {
		end := start + telegramMediaGroupMax
		if end > len(mediaPaths) {
			end = len(mediaPaths)
		}

		batch := mediaPaths[start:end]
		batchCaption := ""
		batchReplyTo := ""
		if start == 0 {
			batchCaption = caption
			batchReplyTo = replyToMsgID
		}

		if len(batch) == 1 {
			if err := t.sendPhoto(chatID, batch[0], batchCaption, parseMode, batchReplyTo); err != nil {
				return err
			}
			continue
		}

		if err := t.sendMediaGroupBatch(chatID, batch, batchCaption, parseMode, batchReplyTo); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendMediaGroupBatch(chatID int64, mediaPaths []string, caption string, parseMode string, replyToMsgID string) error {
	replyID := parseTelegramReplyToMessageID(replyToMsgID)
	var lastErr error

	for _, mode := range telegramParseModes(parseMode) {
		media := make([]interface{}, 0, len(mediaPaths))
		for i, path := range mediaPaths {
			photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(path))
			if i == 0 && caption != "" {
				photo.Caption = formatTelegramCaption(caption, mode)
				photo.ParseMode = mode
			}
			media = append(media, photo)
		}

		cfg := tgbotapi.NewMediaGroup(chatID, media)
		if replyID > 0 {
			cfg.ReplyToMessageID = replyID
		}

		if _, err := t.bot.SendMediaGroup(cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return lastErr
}

func (t *Telegram) sendDocumentAlbum(chatID int64, filePaths []string, caption string, parseMode string, replyToMsgID string) error {
	for start := 0; start < len(filePaths); start += telegramMediaGroupMax {
		end := start + telegramMediaGroupMax
		if end > len(filePaths) {
			end = len(filePaths)
		}

		batch := filePaths[start:end]
		batchCaption := ""
		batchReplyTo := ""
		if start == 0 {
			batchCaption = caption
			batchReplyTo = replyToMsgID
		}

		if len(batch) == 1 {
			if err := t.sendDocument(chatID, batch[0], batchCaption, parseMode, batchReplyTo); err != nil {
				return err
			}
			continue
		}

		if err := t.sendDocumentGroupBatch(chatID, batch, batchCaption, parseMode, batchReplyTo); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendDocumentGroupBatch(chatID int64, filePaths []string, caption string, parseMode string, replyToMsgID string) error {
	replyID := parseTelegramReplyToMessageID(replyToMsgID)
	var lastErr error

	for _, mode := range telegramParseModes(parseMode) {
		media := make([]interface{}, 0, len(filePaths))
		for i, path := range filePaths {
			doc := tgbotapi.NewInputMediaDocument(tgbotapi.FilePath(path))
			if i == 0 && caption != "" {
				doc.Caption = formatTelegramCaption(caption, mode)
				doc.ParseMode = mode
			}
			media = append(media, doc)
		}

		cfg := tgbotapi.NewMediaGroup(chatID, media)
		if replyID > 0 {
			cfg.ReplyToMessageID = replyID
		}

		if _, err := t.bot.SendMediaGroup(cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return lastErr
}

func (t *Telegram) sendPhoto(chatID int64, mediaPath string, caption string, parseMode string, replyToMsgID string) error {
	replyID := parseTelegramReplyToMessageID(replyToMsgID)
	var lastErr error

	for _, mode := range telegramParseModes(parseMode) {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(mediaPath))
		if caption != "" {
			photo.Caption = formatTelegramCaption(caption, mode)
			photo.ParseMode = mode
		}
		if replyID > 0 {
			photo.ReplyToMessageID = replyID
		}

		if _, err := t.bot.Send(photo); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return lastErr
}

func (t *Telegram) sendDocument(chatID int64, filePath string, caption string, parseMode string, replyToMsgID string) error {
	replyID := parseTelegramReplyToMessageID(replyToMsgID)
	var lastErr error

	for _, mode := range telegramParseModes(parseMode) {
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		if caption != "" {
			doc.Caption = formatTelegramCaption(caption, mode)
			doc.ParseMode = mode
		}
		if replyID > 0 {
			doc.ReplyToMessageID = replyID
		}

		if _, err := t.bot.Send(doc); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return lastErr
}

func (t *Telegram) editMessage(chatID int64, msg bus.OutboundMessage) error {
	editMsgID, err := strconv.Atoi(msg.EditMsgID)
	if err != nil {
		return fmt.Errorf("parse edit message ID: %w", err)
	}

	edit := tgbotapi.NewEditMessageText(chatID, editMsgID, msg.Text)
	if msg.ParseMode != "" {
		if msg.ParseMode == "MarkdownV2" {
			edit.Text = escapeMarkdownV2(msg.Text)
		}
		edit.ParseMode = msg.ParseMode
	}

	if len(msg.Buttons) > 0 {
		kb := buildInlineKeyboard(msg.Buttons)
		edit.ReplyMarkup = &kb
	}

	_, err = t.bot.Send(edit)
	if err != nil && msg.ParseMode == "MarkdownV2" {
		// Fallback to HTML then plain
		edit.ParseMode = "HTML"
		edit.Text = msg.Text
		_, err = t.bot.Send(edit)
		if err != nil {
			edit.ParseMode = ""
			edit.Text = msg.Text
			_, err = t.bot.Send(edit)
		}
	}
	return err
}

// SendTyping sends a typing indicator to the chat.
func (t *Telegram) SendTyping(chatID string) error {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse chat ID: %w", err)
	}
	action := tgbotapi.NewChatAction(id, tgbotapi.ChatTyping)
	_, err = t.bot.Send(action)
	return err
}

func normalizeTelegramMediaPaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("telegram media path %q: %w", path, err)
		}
		if !isTelegramImagePath(path) {
			return nil, fmt.Errorf("telegram media path %q is not a supported image", path)
		}
		normalized = append(normalized, path)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("telegram media message has no valid image paths")
	}
	return normalized, nil
}

func normalizeTelegramFilePaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("telegram file path %q: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("telegram file path %q is a directory", path)
		}
		normalized = append(normalized, path)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("telegram file message has no valid file paths")
	}
	return normalized, nil
}

func isTelegramImagePath(path string) bool {
	if _, ok := telegramImageExtensions[strings.ToLower(filepath.Ext(path))]; ok {
		return true
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return false
	}

	return strings.HasPrefix(http.DetectContentType(header[:n]), "image/")
}

func splitTelegramCaption(text string) (caption string, overflow string) {
	if len(text) <= telegramCaptionLimit {
		return text, ""
	}
	return "", text
}

func validateTelegramAttachmentMessage(msg bus.OutboundMessage) error {
	if msg.EditMsgID != "" {
		return fmt.Errorf("telegram does not support editing attachment messages")
	}
	if len(msg.Buttons) > 0 {
		return fmt.Errorf("telegram does not support inline keyboards on attachment messages")
	}
	return nil
}

func parseTelegramReplyToMessageID(replyToMsgID string) int {
	replyID, err := strconv.Atoi(replyToMsgID)
	if err != nil {
		return 0
	}
	return replyID
}

func telegramParseModes(parseMode string) []string {
	switch parseMode {
	case "MarkdownV2":
		return []string{"MarkdownV2", "HTML", ""}
	case "HTML":
		return []string{"HTML", ""}
	case "":
		return []string{""}
	default:
		return []string{parseMode, ""}
	}
}

func formatTelegramCaption(caption string, parseMode string) string {
	if parseMode == "MarkdownV2" {
		return escapeMarkdownV2(caption)
	}
	return caption
}

// escapeMarkdownV2 escapes special characters for Telegram MarkdownV2 format.
func escapeMarkdownV2(text string) string {
	for _, ch := range markdownV2SpecialChars {
		text = strings.ReplaceAll(text, ch, "\\"+ch)
	}
	return text
}

// splitTelegramMessage splits a message that exceeds Telegram's 4096 char limit
// at paragraph boundaries.
func splitTelegramMessage(text string) []string {
	const maxLen = 4096

	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split at paragraph boundary
		cutAt := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n\n"); idx > 0 {
			cutAt = idx + 2
		} else if idx := strings.LastIndex(text[:maxLen], "\n"); idx > 0 {
			cutAt = idx + 1
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}
	return chunks
}

// buildInlineKeyboard converts OutboundButton rows to a Telegram InlineKeyboardMarkup.
func buildInlineKeyboard(buttons [][]bus.OutboundButton) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, row := range buttons {
		var tgRow []tgbotapi.InlineKeyboardButton
		for _, btn := range row {
			if btn.URL != "" {
				tgRow = append(tgRow, tgbotapi.NewInlineKeyboardButtonURL(btn.Text, btn.URL))
			} else {
				tgRow = append(tgRow, tgbotapi.NewInlineKeyboardButtonData(btn.Text, btn.CallbackData))
			}
		}
		if len(tgRow) > 0 {
			rows = append(rows, tgRow)
		}
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}
