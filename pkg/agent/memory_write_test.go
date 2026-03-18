package agent

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func TestShouldIndexObservation_FiltersInternalNoise(t *testing.T) {
	cases := []struct {
		name string
		obs  memoryindex.Observation
		want bool
	}{
		{
			name: "normal user message",
			obs: memoryindex.Observation{
				SessionKey: "agent:main:telegram:direct:1",
				Channel:    "telegram",
				ChatID:     "1",
				Role:       "user",
				SenderID:   "telegram:1",
				Content:    "remember we discussed sqlite search",
				CreatedAt:  time.Now(),
			},
			want: true,
		},
		{
			name: "heartbeat content",
			obs: memoryindex.Observation{
				SessionKey: "heartbeat",
				Channel:    "heartbeat",
				Role:       "assistant",
				SenderID:   "heartbeat",
				Content:    "HEARTBEAT_OK",
				CreatedAt:  time.Now(),
			},
			want: false,
		},
		{
			name: "scheduled reminder trigger",
			obs: memoryindex.Observation{
				SessionKey: "agent:main:telegram:group:1",
				Channel:    "telegram",
				ChatID:     "1",
				Role:       "user",
				SenderID:   "cron",
				Content:    "[scheduled_reminder_trigger]\n\nping",
				CreatedAt:  time.Now(),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := memoryindex.ShouldIndexObservation(tc.obs); got != tc.want {
				t.Fatalf("ShouldIndexObservation() = %v, want %v", got, tc.want)
			}
		})
	}
}
