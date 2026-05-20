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

Что извлекать. Примеры (полные ответы для каждой реплики):

Реплика: "я люблю арбузный сок"
Ответ:   {"facts":[{"entity":"я","attribute":"likes","value":"арбузный сок","confidence":0.9}]}

Реплика: "я живу в Минске"
Ответ:   {"facts":[{"entity":"я","attribute":"lives_in","value":"Минск","confidence":0.9}]}

Реплика: "запомни что я предпочитаю эспрессо без молока"
Ответ:   {"facts":[{"entity":"я","attribute":"prefers","value":"эспрессо без молока","confidence":0.95}]}

Реплика: "меня зовут Андрей"
Ответ:   {"facts":[{"entity":"я","attribute":"name","value":"Андрей","confidence":1.0}]}

Что НЕ извлекать:
- Сиюминутные настроения, мнения о погоде или новостях.
- Уже произошедшие действия ("я сходил в магазин").
- Содержимое прочитанных файлов, переписки про код или сервер.
- Размышления ассистента в <thought>/<think> блоках.

Если устойчивых фактов нет — верни ровно {"facts":[]}.

Правила вывода:
- Никаких placeholder-значений типа "..." или "..." в ответе. Возвращай только реальные извлечённые данные.
- Никаких комментариев, markdown, тегов или текста вокруг JSON. Только сам JSON.
- Язык значений как в исходнике (русский остаётся русским).`

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
