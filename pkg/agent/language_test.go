package agent

import "testing"

func TestDetectMessageLanguageHint(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "russian", text: "какой у меня список дел", want: "ru"},
		{name: "english", text: "what is in my todo", want: "en"},
		{name: "empty", text: "", want: "en"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMessageLanguageHint(tc.text); got != tc.want {
				t.Fatalf("detectMessageLanguageHint(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
