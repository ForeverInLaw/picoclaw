package agent

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestSeahorseLazilyImportsSummaryOnlyConversationAndReopens(t *testing.T) {
	p := &sourceScenarioProvider{}
	al, cfg := newSeahorseScenarioLoop(t, p)
	agent := al.GetRegistry().GetDefaultAgent()
	key := inlineConversationKey("main", "42")
	agent.Sessions.SetSummary(key, "LEGACY_SUMMARY_ONLY bicycle evidence")
	if err := agent.Sessions.Save(key); err != nil {
		t.Fatal(err)
	}
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "first", "continue")); err != nil {
		t.Fatal(err)
	}
	input := conversationInput(p.lastInput)
	if !strings.Contains(input, `kind="imported"`) || !strings.Contains(input, "LEGACY_SUMMARY_ONLY") {
		t.Fatalf("legacy summary lacks imported provenance: %s", input)
	}
	if agent.Sessions.GetSummary(key) != "LEGACY_SUMMARY_ONLY bicycle evidence" {
		t.Fatal("import changed canonical summary")
	}
	al.Close()
	p = &sourceScenarioProvider{}
	al = NewAgentLoop(cfg, bus.NewMessageBus(), p)
	defer al.Close()
	if _, err := al.processMessage(t.Context(), selectedInlineMessage("42", "reopened", "continue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversationInput(p.lastInput), "LEGACY_SUMMARY_ONLY") {
		t.Fatal("reopen lost imported summary")
	}
	database, err := sql.Open("sqlite", filepath.Join(agent.Workspace, "sessions", "seahorse-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var summaries, complete int
	if err = database.QueryRow("SELECT COUNT(*) FROM summaries WHERE kind='imported'").Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT COUNT(*) FROM context_imports WHERE completed=1").Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if summaries != 1 || complete == 0 {
		t.Fatalf("durable import: summaries=%d complete=%d", summaries, complete)
	}
}
