package telegram

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newBatchingTestChannel(t *testing.T, windowMS int) (*TelegramChannel, *bus.MessageBus) {
	t.Helper()

	messageBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Channels.Telegram.Batching.Enabled = true
	cfg.Channels.Telegram.Batching.WindowMS = windowMS
	ch := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, messageBus, nil),
		bot:         newTestTelegramBot(t, "testbot"),
		config:      cfg,
		chatIDs:     make(map[string]int64),
		ctx:         context.Background(),
		batches:     make(map[string]*telegramInboundBatch),
	}
	return ch, messageBus
}

func recvInbound(t *testing.T, ch <-chan bus.InboundMessage, timeout time.Duration) bus.InboundMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for inbound message")
		return bus.InboundMessage{}
	}
}

func TestTelegramChannel_SetChatIDConcurrent(t *testing.T) {
	ch, _ := newBatchingTestChannel(t, 30)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch.setChatID(strconv.Itoa(i%8), int64(1000+i))
		}(i)
	}
	wg.Wait()

	if _, ok := ch.getChatID("0"); !ok {
		t.Fatal("expected chat ID for key 0")
	}
}

func TestHandleMessage_BatchesPlainTextWithinWindow(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	msg1 := &telego.Message{
		Text:      "первая часть",
		MessageID: 101,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}
	msg2 := &telego.Message{
		Text:      "вторая часть",
		MessageID: 102,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("handleMessage(msg2) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if inbound.Content != "первая часть\n\nвторая часть" {
		t.Fatalf("content=%q", inbound.Content)
	}
	if inbound.MessageID != "101" {
		t.Fatalf("messageID=%q want=101", inbound.MessageID)
	}
	if got := inbound.Metadata["batch_count"]; got != "2" {
		t.Fatalf("batch_count=%q want=2", got)
	}
	if got := inbound.Metadata["batch_message_ids"]; got != "101,102" {
		t.Fatalf("batch_message_ids=%q want=101,102", got)
	}
}

func TestHandleMessage_CommandBypassesBatching(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 50)

	msg1 := &telego.Message{
		Text:      "обычный текст",
		MessageID: 201,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}
	cmd := &telego.Message{
		Text:      "/new",
		MessageID: 202,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeBotCommand,
			Offset: 0,
			Length: len("/new"),
		}},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), cmd); err != nil {
		t.Fatalf("handleMessage(cmd) error: %v", err)
	}

	first := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	second := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)

	if first.Content != "обычный текст" {
		t.Fatalf("first content=%q want ordinary text", first.Content)
	}
	if second.Content != "/new" {
		t.Fatalf("second content=%q want /new", second.Content)
	}
}

func TestHandleMessage_ForwardedMessagesAndOwnCommentStayLabeled(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	forward1 := &telego.Message{
		Text:      "первый пересланный",
		MessageID: 301,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ForwardOrigin: &telego.MessageOriginUser{
			SenderUser: telego.User{ID: 7, Username: "bob", FirstName: "Bob"},
		},
	}
	forward2 := &telego.Message{
		Text:      "второй пересланный",
		MessageID: 302,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ForwardOrigin: &telego.MessageOriginHiddenUser{
			SenderUserName: "Charlie",
		},
	}
	own := &telego.Message{
		Text:      "мой комментарий",
		MessageID: 303,
		Chat:      telego.Chat{ID: 123, Type: "private"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
	}

	if err := ch.handleMessage(context.Background(), forward1); err != nil {
		t.Fatalf("handleMessage(forward1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), forward2); err != nil {
		t.Fatalf("handleMessage(forward2) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), own); err != nil {
		t.Fatalf("handleMessage(own) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	wantParts := []string{
		"[forwarded from bob]: первый пересланный",
		"[forwarded from Charlie]: второй пересланный",
		"мой комментарий",
	}
	for _, want := range wantParts {
		if !strings.Contains(inbound.Content, want) {
			t.Fatalf("content=%q missing %q", inbound.Content, want)
		}
	}
	if got := inbound.Metadata["batch_count"]; got != "3" {
		t.Fatalf("batch_count=%q want=3", got)
	}
}

func TestHandleMessage_ReplyBoundaryFlushesBatch(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	msg1 := &telego.Message{
		Text:      "ответ на первое",
		MessageID: 401,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ReplyToMessage: &telego.Message{
			MessageID: 1,
			Text:      "msg1",
			From:      &telego.User{ID: 77, FirstName: "Bob"},
		},
	}
	msg2 := &telego.Message{
		Text:      "ответ на второе",
		MessageID: 402,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 42, FirstName: "Alice"},
		ReplyToMessage: &telego.Message{
			MessageID: 2,
			Text:      "msg2",
			From:      &telego.User{ID: 88, FirstName: "Carol"},
		},
	}

	if err := ch.handleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("handleMessage(msg1) error: %v", err)
	}
	if err := ch.handleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("handleMessage(msg2) error: %v", err)
	}

	first := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	second := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if first.Metadata["reply_to_message_id"] != "1" {
		t.Fatalf("first reply_to_message_id=%q want=1", first.Metadata["reply_to_message_id"])
	}
	if second.Metadata["reply_to_message_id"] != "2" {
		t.Fatalf("second reply_to_message_id=%q want=2", second.Metadata["reply_to_message_id"])
	}
}

func TestDispatchInboundCandidate_BatchesTelegramMediaGroup(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	first := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "501",
		senderID:      "42",
		chatID:        "123",
		content:       "[forwarded from chan]: подпись\n[image: photo]",
		media:         []string{"media://photo-1"},
		metadata:      map[string]string{"media_group_id": "album-1"},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
		mediaGroupID:  "album-1",
	}
	second := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "502",
		senderID:      "42",
		chatID:        "123",
		content:       "[forwarded from chan]: [image: photo]",
		media:         []string{"media://photo-2"},
		metadata:      map[string]string{"media_group_id": "album-1"},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
		mediaGroupID:  "album-1",
	}

	if err := ch.dispatchInboundCandidate(context.Background(), first); err != nil {
		t.Fatalf("dispatchInboundCandidate(first) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), second); err != nil {
		t.Fatalf("dispatchInboundCandidate(second) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if got := inbound.Metadata["media_group_id"]; got != "album-1" {
		t.Fatalf("media_group_id=%q want=album-1", got)
	}
	if got := inbound.Metadata["batch_count"]; got != "2" {
		t.Fatalf("batch_count=%q want=2", got)
	}
	if len(inbound.Media) != 2 {
		t.Fatalf("len(media)=%d want=2", len(inbound.Media))
	}
	if !strings.Contains(inbound.Content, "подпись") {
		t.Fatalf("content=%q missing caption", inbound.Content)
	}
	if strings.Count(inbound.Content, "[image: photo]") != 2 {
		t.Fatalf("content=%q want two image markers", inbound.Content)
	}
}

func TestDispatchInboundCandidate_BatchesCommentWithTelegramMediaGroup(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	comment := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "601",
		senderID:      "42",
		chatID:        "123",
		content:       "что думаешь об этом?",
		metadata:      map[string]string{},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}
	imageOnly := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "602",
		senderID:      "42",
		chatID:        "123",
		content:       "[forwarded from chan]: [image: photo]",
		media:         []string{"media://photo-1"},
		metadata:      map[string]string{"media_group_id": "album-2"},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
		mediaGroupID:  "album-2",
	}
	caption := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "603",
		senderID:      "42",
		chatID:        "123",
		content:       "[forwarded from chan]: полный текст поста\n[image: photo]",
		media:         []string{"media://photo-2"},
		metadata:      map[string]string{"media_group_id": "album-2"},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
		mediaGroupID:  "album-2",
	}

	if err := ch.dispatchInboundCandidate(context.Background(), comment); err != nil {
		t.Fatalf("dispatchInboundCandidate(comment) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), imageOnly); err != nil {
		t.Fatalf("dispatchInboundCandidate(imageOnly) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), caption); err != nil {
		t.Fatalf("dispatchInboundCandidate(caption) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if got := inbound.Metadata["media_group_id"]; got != "album-2" {
		t.Fatalf("media_group_id=%q want=album-2", got)
	}
	if got := inbound.Metadata["batch_count"]; got != "3" {
		t.Fatalf("batch_count=%q want=3", got)
	}
	if !strings.Contains(inbound.Content, "что думаешь об этом?") {
		t.Fatalf("content=%q missing user comment", inbound.Content)
	}
	if !strings.Contains(inbound.Content, "полный текст поста") {
		t.Fatalf("content=%q missing caption text", inbound.Content)
	}
	commentIdx := strings.Index(inbound.Content, "что думаешь об этом?")
	captionIdx := strings.Index(inbound.Content, "полный текст поста")
	imageIdx := strings.Index(inbound.Content, "[forwarded from chan]: [image: photo]")
	if commentIdx == -1 || captionIdx == -1 || imageIdx == -1 {
		t.Fatalf("unexpected content order: %q", inbound.Content)
	}
	if imageIdx < commentIdx || imageIdx < captionIdx {
		t.Fatalf("content=%q want image-only segment after text segments", inbound.Content)
	}
	if len(inbound.Media) != 2 {
		t.Fatalf("len(media)=%d want=2", len(inbound.Media))
	}
}

func TestDispatchInboundCandidate_BatchesStandaloneVoiceMessages(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	first := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "701",
		senderID:      "42",
		chatID:        "123",
		content:       "[voice]",
		media:         []string{"media://voice-1"},
		metadata:      map[string]string{},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}
	second := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "702",
		senderID:      "42",
		chatID:        "123",
		content:       "[forwarded from Bob]: [voice]",
		media:         []string{"media://voice-2"},
		metadata:      map[string]string{"forwarded_from": "Bob"},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}

	if err := ch.dispatchInboundCandidate(context.Background(), first); err != nil {
		t.Fatalf("dispatchInboundCandidate(first) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), second); err != nil {
		t.Fatalf("dispatchInboundCandidate(second) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if got := inbound.Metadata["batch_count"]; got != "2" {
		t.Fatalf("batch_count=%q want=2", got)
	}
	if len(inbound.Media) != 2 {
		t.Fatalf("len(media)=%d want=2", len(inbound.Media))
	}
	if strings.Count(inbound.Content, "[voice]") != 2 {
		t.Fatalf("content=%q want two voice markers", inbound.Content)
	}
	if !strings.Contains(inbound.Content, "[forwarded from Bob]: [voice]") {
		t.Fatalf("content=%q missing forwarded voice label", inbound.Content)
	}
}

func TestDispatchInboundCandidate_BatchesCommentWithStandaloneVoiceMessages(t *testing.T) {
	ch, messageBus := newBatchingTestChannel(t, 30)

	comment := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "801",
		senderID:      "42",
		chatID:        "123",
		content:       "послушай это",
		metadata:      map[string]string{},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}
	voice1 := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "802",
		senderID:      "42",
		chatID:        "123",
		content:       "[voice]",
		media:         []string{"media://voice-1"},
		metadata:      map[string]string{},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}
	voice2 := telegramInboundCandidate{
		peer:          bus.Peer{Kind: "direct", ID: "42"},
		messageID:     "803",
		senderID:      "42",
		chatID:        "123",
		content:       "[voice]",
		media:         []string{"media://voice-2"},
		metadata:      map[string]string{},
		sender:        bus.SenderInfo{Platform: "telegram", PlatformID: "42", CanonicalID: "telegram:42", DisplayName: "Alice"},
		batchKey:      "123|telegram:42",
		batchEligible: true,
	}

	if err := ch.dispatchInboundCandidate(context.Background(), comment); err != nil {
		t.Fatalf("dispatchInboundCandidate(comment) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), voice1); err != nil {
		t.Fatalf("dispatchInboundCandidate(voice1) error: %v", err)
	}
	if err := ch.dispatchInboundCandidate(context.Background(), voice2); err != nil {
		t.Fatalf("dispatchInboundCandidate(voice2) error: %v", err)
	}

	inbound := recvInbound(t, messageBus.InboundChan(), 250*time.Millisecond)
	if got := inbound.Metadata["batch_count"]; got != "3" {
		t.Fatalf("batch_count=%q want=3", got)
	}
	if !strings.Contains(inbound.Content, "послушай это") {
		t.Fatalf("content=%q missing comment", inbound.Content)
	}
	if strings.Count(inbound.Content, "[voice]") != 2 {
		t.Fatalf("content=%q want two voice markers", inbound.Content)
	}
	if len(inbound.Media) != 2 {
		t.Fatalf("len(media)=%d want=2", len(inbound.Media))
	}
}

func TestTelegramStandaloneBatchableMedia(t *testing.T) {
	if !telegramStandaloneBatchableMedia(&telego.Message{Voice: &telego.Voice{FileID: "voice-1"}}) {
		t.Fatal("voice should be standalone-batchable")
	}
	if !telegramStandaloneBatchableMedia(&telego.Message{Audio: &telego.Audio{FileID: "audio-1"}}) {
		t.Fatal("audio should be standalone-batchable")
	}
	if telegramStandaloneBatchableMedia(&telego.Message{Document: &telego.Document{FileID: "doc-1"}}) {
		t.Fatal("document should not be standalone-batchable")
	}
	if telegramStandaloneBatchableMedia(&telego.Message{Photo: []telego.PhotoSize{{FileID: "photo-1"}}}) {
		t.Fatal("photo should not be standalone-batchable without media group")
	}
}
