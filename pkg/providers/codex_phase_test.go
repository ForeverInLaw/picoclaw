package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestCodexIgnoresUnexpectedExplicitPhasesBeforeFinalAnswer(t *testing.T) {
	for _, phased := range []bool{true, false} {
		t.Run(fmt.Sprintf("explicit=%t", phased), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				output := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Legacy answer"}]}`
				if phased {
					output = `{"type":"message","role":"assistant","phase":"unexpected_internal","content":[{"type":"output_text","text":"PRIVATE_UNKNOWN_PHASE"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Final answer"}]}`
				}
				_, _ = fmt.Fprintf(
					w,
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[%s]}}\n\n",
					output,
				)
			}))
			defer server.Close()
			provider := NewCodexProvider("test-token", "")
			provider.client = createOpenAITestClient(server.URL, "test-token", "")
			response, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-4o", nil)
			want := "Legacy answer"
			if phased {
				want = "Final answer"
			}
			if err != nil || response == nil || response.Content != want {
				t.Fatalf("response=%+v error=%v", response, err)
			}
		})
	}
}

func TestCodexStreamsWholePhaseAcrossOutputItemsAndContentParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(
			w,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"comment","type":"message","role":"assistant","phase":"commentary","content":[]}}

data: {"type":"response.output_text.delta","item_id":"comment","output_index":0,"content_index":0,"delta":"PRIVATE"}

data: {"type":"response.output_item.added","output_index":1,"item":{"id":"a","type":"message","role":"assistant","phase":"final_answer","content":[]}}

data: {"type":"response.output_text.delta","item_id":"a","output_index":1,"content_index":1,"delta":"B"}

data: {"type":"response.output_text.delta","item_id":"a","output_index":1,"content_index":0,"delta":"A"}

data: {"type":"response.output_item.added","output_index":2,"item":{"id":"b","type":"message","role":"assistant","phase":"final_answer","content":[]}}

data: {"type":"response.output_text.delta","item_id":"b","output_index":2,"content_index":0,"delta":"C"}

data: {"type":"response.output_text.delta","item_id":"comment","output_index":0,"content_index":0,"delta":" NOTES"}

data: {"type":"response.output_text.delta","item_id":"b","output_index":2,"content_index":0,"delta":"D"}

data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"PRIVATE NOTES"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"A"},{"type":"output_text","text":"B"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"CD"}]}]}}

`,
		)
	}))
	defer server.Close()
	p := NewCodexProvider("test-token", "")
	p.client = createOpenAITestClient(server.URL, "test-token", "")
	var previews []string
	response, err := p.ChatStreamContent(
		t.Context(),
		[]Message{{Role: "user", Content: "answer"}},
		nil,
		"gpt-4o",
		nil,
		func(update StreamContent) {
			if update.Phase == ContentPhaseAnswer {
				previews = append(previews, update.Content)
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(previews) != "[B AB ABC ABCD]" || response.Content != "ABCD" {
		t.Fatalf("phase snapshots=%q final=%q", previews, response.Content)
	}
}

func TestCodexRejectsStreamsWithoutCompletedResponse(t *testing.T) {
	for _, ending := range []string{"eof", "incomplete", "failed"} {
		t.Run(ending, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(
					w,
					"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"output_index\":0,\"content_index\":0,\"delta\":\"Partial answer\"}\n\n",
				)
				if ending != "eof" {
					fmt.Fprintf(
						w,
						"data: {\"type\":\"response.%s\",\"response\":{\"id\":\"r1\",\"status\":\"%s\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial answer\"}]}]}}\n\n",
						ending,
						ending,
					)
				}
			}))
			defer server.Close()
			provider := NewCodexProvider("fixture-token", "")
			provider.client = createOpenAITestClient(server.URL, "fixture-token", "")
			response, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "hello"}},
				nil,
				"gpt-5.3-codex",
				nil,
			)
			if !errors.Is(err, io.ErrUnexpectedEOF) || response != nil {
				t.Fatalf("unfinished stream returned response=%+v error=%v", response, err)
			}
		})
	}
}
