package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type telegramActivityRow struct {
	text    string
	command bool
	count   int
}

func toolActivityRow(progress *bus.ProgressMessage) telegramActivityRow {
	preview := ""
	for _, key := range []string{"command", "path", "query", "url", "file_path", "pattern", "task", "action", "name"} {
		if value, ok := progress.Arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			preview = strings.Join(strings.Fields(value), " ")
			if progress.Tool == "exec" {
				preview = strings.TrimSpace(strings.SplitN(value, "\n", 2)[0])
				if strings.Contains(value, "\n") {
					preview += " ..."
				}
			}
			break
		}
	}
	preview = limitActivityText(preview, 40)
	label := "⚙️ " + limitActivityText(progress.Tool, 80)
	switch progress.Tool {
	case "exec":
		if preview == "" {
			return telegramActivityRow{text: "💻 terminal", count: 1}
		}
		return telegramActivityRow{text: preview, command: true, count: 1}
	case "web_search":
		label = "🔍 Searching the web for"
	case "web_fetch":
		label = "📄 Reading page"
	case "read_file":
		label = "📖 Reading"
	case "write_file":
		label = "✍️ Writing"
	case "edit_file", "append_file":
		label = "✏️ Editing"
	case "list_dir":
		label = "📂 Listing"
	case "tool_search_tool_regex", "tool_search_tool_bm25":
		label = "🔎 Finding tools for"
	case "spawn", "subagent":
		label = "🤝 Delegating"
	case "spawn_status":
		label = "🤝 Checking task"
	case "find_skills":
		label = "📚 Finding skills for"
	case "install_skill":
		label = "📚 Installing skill"
	case "chat_memory", "short_grep", "short_describe", "short_expand":
		label = "🧠 Recalling"
	case "cron", "personal_todo":
		label = "🗓️ Updating tasks"
	}
	return telegramActivityRow{text: strings.TrimSpace(label + " " + preview), count: 1}
}

func appendActivityRow(rows []telegramActivityRow, row telegramActivityRow) []telegramActivityRow {
	if len(rows) > 0 && rows[len(rows)-1].text == row.text && rows[len(rows)-1].command == row.command {
		rows[len(rows)-1].count++
		return rows
	}
	return append(rows, row)
}

func renderActivityRows(rows []telegramActivityRow) (telego.InputRichMessage, string) {
	blocks := make([]telego.InputRichBlock, 0, len(rows))
	plain := make([]string, 0, len(rows))
	wasCommand := false
	for _, row := range rows {
		text := row.text
		if row.count > 1 {
			text += fmt.Sprintf(" (×%d)", row.count)
		}
		if row.command {
			if !wasCommand {
				blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextPlain("💻 terminal")))
				plain = append(plain, "💻 terminal")
			}
			blocks = append(blocks, tu.RichBlockPreformatted(tu.RichTextPlain(text)))
		} else {
			blocks = append(blocks, tu.RichBlockParagraph(tu.RichTextPlain(text)))
		}
		plain = append(plain, text)
		wasCommand = row.command
	}
	return tu.RichMessage(blocks...), strings.Join(plain, "\n")
}
