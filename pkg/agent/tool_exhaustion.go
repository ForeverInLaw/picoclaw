package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/utils"
)

func toolExhaustionFallback(language, lastToolResult string, hadToolCalls bool) string {
	if !hadToolCalls {
		return defaultResponse
	}

	lastToolResult = strings.TrimSpace(lastToolResult)
	if lastToolResult == "" {
		if strings.HasPrefix(strings.ToLower(language), "ru") {
			return "Модель исчерпала лимит шагов инструментов и не дала финальный ответ. Увеличь max_tool_iterations или повтори запрос."
		}
		return "The model exhausted its tool-step budget and did not produce a final response. Increase max_tool_iterations or retry the request."
	}

	lastToolResult = utils.Truncate(lastToolResult, 400)
	if strings.HasPrefix(strings.ToLower(language), "ru") {
		return "Модель исчерпала лимит шагов инструментов и не дала финальный ответ.\n\nПоследний результат tool:\n" + lastToolResult
	}
	return "The model exhausted its tool-step budget and did not produce a final response.\n\nLast tool result:\n" + lastToolResult
}
