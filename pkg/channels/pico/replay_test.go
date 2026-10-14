package pico

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestReconnectRetainsFinalWhileAnotherRequestWaits(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	channel, err := NewPicoChannel(config.PicoConfig{Token: "replay-token"}, mb)
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer channel.Stop(t.Context())
	server := httptest.NewServer(channel)
	defer server.Close()
	dial := func() *websocket.Conn {
		conn, response, err := websocket.DefaultDialer.Dial(
			"ws"+strings.TrimPrefix(server.URL, "http")+"/pico/ws?session_id=queued",
			http.Header{"Authorization": []string{"Bearer replay-token"}},
		)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return conn
	}
	conn := dial()
	defer conn.Close()
	for _, id := range []string{"request-1", "request-2"} {
		if err := conn.WriteJSON(
			PicoMessage{Type: TypeMessageSend, ID: id, Payload: map[string]any{"content": "request " + id}},
		); err != nil {
			t.Fatal(err)
		}
		select {
		case <-mb.InboundChan():
		case <-time.After(time.Second):
			t.Fatal("request not admitted")
		}
	}
	if err := channel.Send(
		t.Context(),
		bus.OutboundMessage{
			ChatID:           "pico:queued",
			DeliveryID:       "final-1",
			ReplyToMessageID: "request-1",
			Content:          "first final",
		},
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	conn = dial()
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var event PicoMessage
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Payload["message_id"] != "final-1" || event.Payload["content"] != "first final" {
		t.Fatalf("pending request lost final: %+v", event)
	}
	if err := channel.Send(
		t.Context(),
		bus.OutboundMessage{
			ChatID:           "pico:queued",
			DeliveryID:       "final-2",
			ReplyToMessageID: "request-2",
			Content:          "second final",
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Payload["message_id"] != "final-2" {
		t.Fatalf("second request missing final: %+v", event)
	}
	conn.Close()
	conn = dial()
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Payload["message_id"] != "final-2" {
		t.Fatalf("reconnect replayed obsolete request: %+v", event)
	}
}
