package extract

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// BuildPrompt renders the extraction system+user prompt for the given window
// of messages. The model is asked to reply with JSON only.
func BuildPrompt(window []protocoltypes.Message) (system, user string) {
	system = `Ты — извлекатель атомарных фактов из чата.

Правила:
- Извлекай только устойчивые факты о людях и чате (не сиюминутные настроения).
- Каждый факт — тройка (entity, attribute, value). Не извлекай предложений целиком.
- Уровень уверенности 0..1: 1 — прямое утверждение пользователем; 0.5 — выведено косвенно.
- Сохраняй язык значений как в исходнике (русский остаётся русским).
- Никаких лишних слов вокруг JSON. Отвечай ТОЛЬКО валидным JSON в формате:
{"facts":[{"entity":"...","attribute":"...","value":"...","confidence":0.0}]}
- Если фактов нет — верни {"facts":[]}.`

	var b strings.Builder
	for _, m := range window {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	user = "Сообщения:\n" + b.String() + "\nИзвлеки факты."
	return system, user
}
