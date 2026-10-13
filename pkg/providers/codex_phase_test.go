package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCodexPreservesAnswerPhaseAndExcludesCommentary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		input := body["input"].([]any)
		if input[0].(map[string]any)["phase"] != "commentary" {
			t.Errorf("assistant history lost phase: %v", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(
			w,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message","role":"assistant","status":"in_progress","phase":"commentary","content":[]}}

data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"Checking things"}

data: {"type":"response.output_item.added","output_index":1,"item":{"id":"m2","type":"message","role":"assistant","status":"in_progress","phase":"final_answer","content":[]}}

data: {"type":"response.output_text.delta","item_id":"m2","output_index":1,"content_index":0,"delta":"Answer"}

data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"Checking things"}]},{"id":"m2","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"Answer"}]}]}}

`,
		)
	}))
	defer server.Close()
	p := NewCodexProvider("test-token", "")
	p.client = createOpenAITestClient(server.URL, "test-token", "")
	var updates []StreamContent
	response, err := p.ChatStreamContent(
		t.Context(),
		[]Message{{Role: "assistant", Content: "Earlier commentary", ContentPhase: ContentPhaseCommentary}},
		nil,
		"gpt-4o",
		nil,
		func(update StreamContent) { updates = append(updates, update) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "Answer" || response.ContentPhase != ContentPhaseAnswer {
		t.Fatalf("response=%+v", response)
	}
	if len(updates) != 2 || updates[0].Phase != ContentPhaseCommentary || updates[1].Phase != ContentPhaseAnswer ||
		updates[1].Content != "Answer" {
		t.Fatalf("updates=%+v", updates)
	}
}
