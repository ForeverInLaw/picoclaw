package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSavedRoutingKeepsTemporaryCommandSelectionUntilEffectiveChainChanges(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprint(inline), func(t *testing.T) {
			calls := []string{}
			var callsMu sync.Mutex
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Model string `json:"model"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				callsMu.Lock()
				calls = append(calls, input.Model+":"+r.Header.Get("Authorization"))
				callsMu.Unlock()
				_, _ = fmt.Fprintf(
					w,
					`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`,
					input.Model,
				)
			}))
			defer endpoint.Close()
			al, _ := configuredProviderLoop(t, []config.ModelConfig{
				{ModelName: "primary", Model: "openai/primary", APIBase: endpoint.URL, APIKey: "primary-key"},
				{ModelName: "duplicate", Model: "openai/selected", APIBase: endpoint.URL, APIKey: "selected-key"},
				{ModelName: "duplicate", Model: "openai/wrong", APIBase: endpoint.URL, APIKey: "wrong-key"},
				{ModelName: "admin", Model: "openai/admin", APIBase: endpoint.URL, APIKey: "admin-key"},
			}, []string{})
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, config.SaveConfig(path, al.cfg))
			al.SetRoutingConfigSource(func() (*config.Config, error) { return config.LoadConfig(path) })
			saved, err := config.LoadConfig(path)
			require.NoError(t, err)
			if inline {
				saved.Agents.List = []config.AgentConfig{
					{ID: "main", Model: &config.AgentModelConfig{Primary: "primary", Fallbacks: []string{}}},
				}
				require.NoError(t, config.SaveConfig(path, saved))
			}
			selectedID := saved.ModelList[1].ID
			command, err := al.ProcessDirect(t.Context(), "/switch model to model-id:"+selectedID, "command-routing")
			require.NoError(t, err)
			require.Contains(t, command, "Switched model")
			request := func(message string) (string, error) {
				if inline {
					return al.processMessage(t.Context(), selectedInlineMessage("42", "command-selection", message))
				}
				return al.ProcessDirect(t.Context(), message, "command-routing")
			}
			answer, err := request("use selection")
			require.NoError(t, err)
			require.Contains(t, answer, "selected")
			saved, err = config.LoadConfig(path)
			require.NoError(t, err)
			require.Equal(t, "primary", saved.Agents.Defaults.GetModelName())
			saved.ModelList[1].ModelName = "renamed"
			saved.ModelList[1].APIKey = "rotated-key"
			saved.ModelList[1].Model = "openai/updated-selected"
			saved.ModelList[1], saved.ModelList[2] = saved.ModelList[2], saved.ModelList[1]
			require.NoError(t, config.SaveConfig(path, saved))
			show, err := al.ProcessDirect(t.Context(), "/show model", "command-routing")
			require.NoError(t, err)
			require.Contains(t, show, "renamed")
			answer, err = request("fresh credentials same configured entry")
			require.NoError(t, err)
			require.Contains(t, answer, "updated-selected")
			saved.Agents.Defaults.ModelName = config.ModelIDPrefix + saved.ModelList[3].ID
			require.NoError(t, config.SaveConfig(path, saved))
			if inline {
				show, err = al.ProcessDirect(t.Context(), "/show model", "command-routing")
				require.NoError(t, err)
				require.Contains(t, show, "renamed", "global order does not replace an explicit agent chain")
				saved.Agents.List[0].Model.Primary = saved.Agents.Defaults.ModelName
				require.NoError(t, config.SaveConfig(path, saved))
			}
			show, err = al.ProcessDirect(t.Context(), "/show model", "command-routing")
			require.NoError(t, err)
			require.Contains(t, show, "admin")
			answer, err = request("new administrator priority")
			require.NoError(t, err)
			require.Contains(t, answer, "admin")
			callsMu.Lock()
			defer callsMu.Unlock()
			require.Equal(
				t,
				[]string{
					"selected:Bearer selected-key",
					"updated-selected:Bearer rotated-key",
					"admin:Bearer admin-key",
				},
				calls,
			)
		})
	}
}
