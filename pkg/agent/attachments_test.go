package agent

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestImageDocumentWithGenericMIMERetainsVisionInput(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer mb.Close()
	defer al.Close()
	p := &recordingProvider{}
	al.GetRegistry().GetDefaultAgent().Provider = p
	image, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0XQAAAAASUVORK5CYII=",
	)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "telegram-download.bin")
	require.NoError(t, os.WriteFile(path, image, 0o600))
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	ref, err := store.Store(
		path,
		media.MediaMeta{Filename: "screenshot.png", ContentType: "application/octet-stream"},
		"upload",
	)
	require.NoError(t, err)
	msg := directIngressMessage("Describe this image")
	msg.Media = []string{ref}
	_, err = al.processMessage(t.Context(), msg)
	require.NoError(t, err)
	current := p.lastMessages[len(p.lastMessages)-1]
	require.Len(t, current.Media, 1)
	require.True(t, strings.HasPrefix(current.Media[0], "data:image/png;base64,"))
	require.NotContains(t, current.Content, "extractor")
}

func TestUnavailableAttachmentsPreserveTextAndValidFiles(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		steering      bool
	}{
		{"missing-reference", "missing-reference", false},
		{"missing-source", "missing-source", false},
		{"oversized", "oversized", false},
		{"steering", "missing-reference", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			al, cfg, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			defer al.Close()
			p := &recordingProvider{}
			a := al.GetRegistry().GetDefaultAgent()
			a.Provider = p
			cfg.Agents.Defaults.MaxMediaSize = 32
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			goodPath := filepath.Join(t.TempDir(), "good.go")
			require.NoError(t, os.WriteFile(goodPath, []byte("package main\n"), 0o600))
			good, err := store.Store(goodPath, media.MediaMeta{Filename: "good.go"}, "good")
			require.NoError(t, err)
			bad := "media://missing-fixture"
			if tc.failure != "missing-reference" {
				path := filepath.Join(t.TempDir(), "bad.go")
				require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 64)), 0o600))
				bad, err = store.Store(path, media.MediaMeta{Filename: "bad.go"}, "bad")
				require.NoError(t, err)
				if tc.failure == "missing-source" {
					require.NoError(t, os.Remove(path))
				}
			}
			msg := directIngressMessage("What is 2+2? [file]")
			msg.SessionKey = "agent:main:partial-attachments"
			msg.Media = []string{bad, good}
			if tc.steering {
				require.NoError(t, al.Steer(providers.Message{Role: "user", Content: msg.Content, Media: msg.Media}))
				msg.Content, msg.Media = "Initial question", nil
			}
			_, err = al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			current := p.lastMessages[len(p.lastMessages)-1]
			require.Contains(t, current.Content, "What is 2+2?")
			require.Contains(t, current.Content, "Attachment unavailable")
			require.Contains(t, current.Content, "package main")
			require.NotContains(t, current.Content, bad)
			history := a.Sessions.GetHistory(msg.SessionKey)
			user := history[len(history)-2]
			require.Len(t, user.Media, 1)
			require.True(t, strings.HasPrefix(user.Media[0], tools.AttachmentPrefix))
			require.Contains(t, user.Content, "Attachment unavailable")
		})
	}
}

func TestAttachmentStorageFailureStillStopsTheTurn(t *testing.T) {
	for _, failure := range []string{"not-a-directory", "writable-directory"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "writable-directory" && runtime.GOOS == "windows" {
				t.Skip("Unix permission bits do not describe Windows ACLs")
			}
			al, cfg, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			defer al.Close()
			p := &recordingProvider{}
			al.GetRegistry().GetDefaultAgent().Provider = p
			directory := filepath.Join(cfg.Agents.Defaults.Workspace, ".attachments")
			if failure == "writable-directory" {
				require.NoError(t, os.Mkdir(directory, 0o700))
				require.NoError(t, os.Chmod(directory, 0o777))
			} else {
				require.NoError(t, os.WriteFile(directory, []byte("not a directory"), 0o600))
			}
			path := filepath.Join(t.TempDir(), "source.go")
			require.NoError(t, os.WriteFile(path, []byte("package main\n"), 0o600))
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			ref, err := store.Store(path, media.MediaMeta{Filename: "source.go"}, "upload")
			require.NoError(t, err)
			msg := directIngressMessage("Analyze [file]")
			msg.Media = []string{ref}
			_, err = al.processMessage(t.Context(), msg)
			require.Error(t, err)
			require.Empty(t, p.lastMessages)
		})
	}
}

func TestTextAttachmentReachesTextModelAndSurvivesFollowUp(t *testing.T) {
	for _, engine := range []string{"legacy", "seahorse"} {
		t.Run(engine, func(t *testing.T) {
			al, cfg, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			al.Close()
			cfg.Agents.Defaults.ContextManager = engine
			al = NewAgentLoop(cfg, mb, &mockProvider{})
			defer al.Close()
			const source = "package main\n\nfunc main() { println(\"attachment source\") }\n"
			requests := make(chan map[string]any, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				requests <- request
				body, _ := json.Marshal(request["messages"])
				if strings.Contains(string(body), `"type":"file"`) ||
					strings.Contains(string(body), `"type":"image_url"`) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"Unsupported file type; use text or image inputs"}}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(
					w,
					`{"choices":[{"message":{"role":"assistant","content":"Source received"},"finish_reason":"stop"}]}`,
				)
			}))
			defer server.Close()
			agent := al.GetRegistry().GetDefaultAgent()
			agent.Provider = providers.NewHTTPProvider("fixture-key", server.URL, "")
			agent.ImageModel = "image-model"
			agent.ImageProvider = agent.Provider
			agent.ImageCandidates = []providers.FallbackCandidate{{Provider: "openai", Model: "image-model"}}
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			path := filepath.Join(t.TempDir(), "telegram-download.bin")
			require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
			ref, err := store.Store(
				path,
				media.MediaMeta{Filename: "nec-reconnect.go", ContentType: "application/octet-stream"},
				"upload",
			)
			require.NoError(t, err)
			msg := directIngressMessage("Is this file malicious? [file]")
			msg.Media = []string{ref}
			response, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Equal(t, "Source received", response)
			first := <-requests
			require.Equal(t, "test-model", first["model"])
			body, err := json.Marshal(first["messages"])
			require.NoError(t, err)
			var messages []providers.Message
			require.NoError(t, json.Unmarshal(body, &messages))
			require.Contains(t, messages[len(messages)-1].Content, source)
			require.Contains(t, messages[len(messages)-1].Content, "nec-reconnect.go")

			require.NoError(t, store.ReleaseAll("upload"))
			al.SetMediaStore(media.NewFileMediaStore())
			followUp := directIngressMessage("?")
			_, err = al.processMessage(t.Context(), followUp)
			require.NoError(t, err)
			second := <-requests
			body, err = json.Marshal(second["messages"])
			require.NoError(t, err)
			require.Contains(t, string(body), "attachment source")
		})
	}
}

func TestLargeAttachmentReadToolSurvivesAgentRestart(t *testing.T) {
	for _, engine := range []string{"legacy", "seahorse"} {
		t.Run(engine, func(t *testing.T) {
			al, cfg, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			al.Close()
			cfg.Agents.Defaults.ContextManager = engine
			al = NewAgentLoop(cfg, mb, &mockProvider{})
			defer al.Close()
			agent := al.GetRegistry().GetDefaultAgent()
			agent.Provider = &recordingProvider{}
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			const tail = "END-OF-SOURCE\n"
			source := strings.Repeat("// source line\n", 2000) + tail
			path := filepath.Join(t.TempDir(), "source.go")
			require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
			ref, err := store.Store(path, media.MediaMeta{Filename: "source.go"}, "upload")
			require.NoError(t, err)
			msg := directIngressMessage("Read this source [file]")
			msg.SessionKey = "agent:main:attachment-read"
			msg.Media = []string{ref}
			_, err = al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			history := agent.Sessions.GetHistory(msg.SessionKey)
			require.Len(t, history[len(history)-2].Media, 1)
			retained := history[len(history)-2].Media[0]
			require.True(t, strings.HasPrefix(retained, tools.AttachmentPrefix))
			require.NoError(t, store.ReleaseAll("upload"))
			al.Close()

			p := &sourceScenarioProvider{
				tool: "read_file",
				args: map[string]any{"path": retained, "offset": len(source) - len(tail), "length": len(tail)},
			}
			restartedBus := bus.NewMessageBus()
			defer restartedBus.Close()
			cfg.Tools.ReadFile.Enabled = true
			restarted := NewAgentLoop(cfg, restartedBus, p)
			defer restarted.Close()
			_, err = restarted.ProcessDirect(t.Context(), "query sources", msg.SessionKey)
			require.NoError(t, err)
			require.Contains(t, p.expandOutput, tail)
			require.Contains(t, p.expandOutput, "END OF FILE")

			_, err = restarted.ProcessDirect(t.Context(), "query sources", "agent:main:foreign")
			require.NoError(t, err)
			require.Contains(t, p.expandOutput, "access denied")
			require.NotContains(t, p.expandOutput, tail)
		})
	}
}

func TestLargeAndBudgetLimitedAttachmentsRemainReadableAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		budget     int
	}{
		{"large", strings.Repeat("source line\n", 3000), 100000},
		{"small-window", strings.Repeat("source line\n", 400), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := tools.NewAttachmentStore(workspace)
			scope := tools.RequestScope{AgentID: "main", SessionKey: "agent:main:telegram:direct:42"}
			path := filepath.Join(t.TempDir(), "source.go")
			require.NoError(t, os.WriteFile(path, []byte(tc.text), 0o600))
			ref, err := store.Import(scope, path, media.MediaMeta{Filename: "source.go"}, 64*1024)
			require.NoError(t, err)
			require.NoError(t, os.Remove(path))
			store = tools.NewAttachmentStore(workspace)
			files := &attachmentContext{store: store, scope: scope, inlineTokens: tc.budget}
			messages := resolveMediaRefs(
				[]providers.Message{{Role: "user", Content: "read it", Media: []string{ref}}},
				nil,
				64*1024,
				files,
			)
			require.Empty(t, messages[0].Media)
			require.NotContains(t, messages[0].Content, "source line")
			require.Contains(t, messages[0].Content, ref)
			require.Contains(t, messages[0].Content, "read_file")
			read := tools.NewReadFileTool(workspace, true, 16).WithAttachments(store)
			ctx := tools.WithRequestScope(t.Context(), scope)
			page := read.Execute(ctx, map[string]any{"path": ref, "offset": len(tc.text) - 12})
			require.False(t, page.IsError, page.ForLLM)
			require.True(t, strings.HasSuffix(page.ForLLM, tc.text[len(tc.text)-12:]))
		})
	}
}
