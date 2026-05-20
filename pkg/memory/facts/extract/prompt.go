package extract

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

var thoughtBlockRe = regexp.MustCompile(`(?s)<think(?:ing)?>.*?</think(?:ing)?>|<thought>.*?</thought>`)

// BuildPrompt renders the extraction system+user prompt for the given window
// of messages. The model is asked to reply with JSON only.
func BuildPrompt(window []protocoltypes.Message) (system, user string) {
	system = `Ты — извлекатель атомарных фактов из переписки в чате.

Цель: вытащить факты о людях, чате и боте, которые полезно будет вспомнить через дни и недели.

Что извлекать (примеры):
- Предпочтения и вкусы: "я люблю арбузный сок" → {"entity":"я","attribute":"likes","value":"арбузный сок","confidence":0.9}
- Личные данные: "я живу в Минске" → {"entity":"я","attribute":"lives_in","value":"Минск","confidence":0.9}
- Профессия/занятие: "я работаю программистом" → {"entity":"я","attribute":"works_as","value":"программист","confidence":0.9}
- Идентичность: "меня зовут Андрей" → {"entity":"я","attribute":"name","value":"Андрей","confidence":1.0}
- Прямые просьбы запомнить: "запомни что …" — извлекай уверенно (confidence 0.9+)

Что НЕ извлекать:
- Сиюминутные настроения, мнения о погоде/новостях
- Действия которые уже произошли ("я сходил в магазин")
- Содержимое прочитанных файлов, переписок про код или сервер
- Размышления ассистента в <thought>/<think> блоках

Формат ответа — ТОЛЬКО валидный JSON, без комментариев и markdown:
{"facts":[{"entity":"...","attribute":"...","value":"...","confidence":0.0}]}
Если устойчивых фактов нет — верни {"facts":[]}.

Сохраняй язык значений как в исходнике (русский остаётся русским).`

	var b strings.Builder
	for _, m := range window {
		content := thoughtBlockRe.ReplaceAllString(m.Content, "")
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, content)
	}
	user = "Переписка:\n" + b.String() + "\nИзвлеки факты в указанном JSON-формате."
	return system, user
}
