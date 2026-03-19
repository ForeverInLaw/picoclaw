package telegram

import "testing"

func TestTelegramTextHasNameTrigger(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		triggers []string
		want     bool
	}{
		{
			name:     "matches exact token",
			text:     "короб, ответь",
			triggers: []string{"короб"},
			want:     true,
		},
		{
			name:     "matches stem with morphology",
			text:     "что думаешь, коробки?",
			triggers: []string{"короб"},
			want:     true,
		},
		{
			name:     "matches full alias",
			text:     "коробка, посмотри",
			triggers: []string{"коробка"},
			want:     true,
		},
		{
			name:     "case insensitive",
			text:     "КОРОБКА, ответь",
			triggers: []string{"короб"},
			want:     true,
		},
		{
			name:     "does not match in middle of token",
			text:     "подкоробка лежит на столе",
			triggers: []string{"короб"},
			want:     false,
		},
		{
			name:     "does not match without trigger",
			text:     "что думаешь об этом",
			triggers: []string{"короб"},
			want:     false,
		},
		{
			name:     "trims at sign in trigger config",
			text:     "коробка, привет",
			triggers: []string{"@коробка"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := telegramTextHasNameTrigger(tt.text, tt.triggers); got != tt.want {
				t.Fatalf("telegramTextHasNameTrigger(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
