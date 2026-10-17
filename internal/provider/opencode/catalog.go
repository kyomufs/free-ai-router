package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/modelcat"
)

// Источник свойств моделей.
//
// ЗАЧЕМ ОТДЕЛЬНО ОТ СПИСКА. Список моделей у сервиса отвечает на вопрос «что
// доступно сейчас», но сообщает про модель только идентификатор. Свойства —
// размер окна, приём изображений, умение думать, поле для размышлений в
// ответе — лежат в другом месте, и оттуда их берут клиенты сервиса.
//
// ПОЧЕМУ ЭТИ ДВА ИСТОЧНИКА НЕ СЛИТЬ. Список точнее: он не может пережить
// вывод модели из оборота. Каталог свойств полнее, но отстаёт: в нынешнем его
// состоянии бесплатных моделей объявлено заметно больше, чем сервис реально
// обслуживает. Слить их в один значило бы либо вернуть в селектор мёртвые
// модели, либо потерять точные пределы у живых. Поэтому состав определяет
// список, а свойства лишь дополняют названное им.

const (
	// metaURL — каталог свойств.
	//
	// Адрес выведен из исходников клиента: это тот же снапшот каталога
	// models.dev, на который клиент опирается при отображении моделей и их
	// возможностей.
	metaURL = "https://models.opencode.ai/api.json"

	// metaTimeout — предел ожидания каталога свойств.
	//
	// Заметно больше, чем у списка: этот каталог описывает все сервисы сразу и
	// потому велик. Список ждёт на пути живого запроса, а свойства — нет: без
	// них модель просто опишется умолчаниями, и задерживать из-за них запрос
	// незачем.
	metaTimeout = 30 * time.Second

	// maxMetaBytes — предел чтения тела.
	//
	// Каталог свойств сейчас около шести мегабайт и растёт вместе с числом
	// сервисов. Предел взят с запасом, но конечный: неожиданно большой ответ
	// не должен съесть память.
	maxMetaBytes = 64 << 20
)

// metaSource читает свойства моделей из каталога сервиса.
type metaSource struct {
	// http — клиент. Общий с остальным шлюзом.
	http *http.Client
}

// Name реализует modelcat.MetaSource.
func (s *metaSource) Name() string { return "каталог свойств opencode" }

// metaModel — свойства одной модели.
//
// Разбирается только то, что использует шлюз: пределы, размышления, приём
// изображений и поле для размышлений в ответе. Остальные поля каталога шлюзу
// не нужны, и разбирать их значило бы тащить в код чужую схему целиком.
type metaModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Attachment — принимает ли модель вложения (изображения).
	Attachment bool `json:"attachment"`
	// Reasoning — умеет ли модель думать.
	Reasoning bool `json:"reasoning"`
	// ReasoningOptions — лестница управления усилием.
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
		Max    int      `json:"max"`
	} `json:"reasoning_options"`
	// Interleaved — куда ответ несёт размышления.
	//
	// Поле объявлено как raw: в каталоге оно встречается в трёх видах —
	// объектом с именем поля, отсутствующим и ЛОЖЬЮ. Строгий разбор на
	// ложь падает, и одно такое значение роняло бы весь каталог свойств,
	// оставляя все модели без пределов. Разбор поэтому терпимый.
	Interleaved json.RawMessage `json:"interleaved"`
	// Modalities — модальности входа и выхода.
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	// Limit — пределы в токенах.
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// Meta реализует modelcat.MetaSource.
//
// Ошибка означает, что свойства останутся неизвестными: каталог опишет модели
// умолчаниями. Это не отказ сервиса, а отсутствие уточнения, поэтому ошибка не
// прерывает сбор каталога.
func (s *metaSource) Meta(ctx context.Context) (map[string]provider.Model, error) {
	reqCtx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &modelcat.Error{Op: "каталог свойств", Status: resp.StatusCode, Reason: "сервис не отдал каталог"}
	}

	// Каталог описывает все сервисы сразу. Читается целиком: разбирать его
	// потоково ради одной секции значило бы повторить разбор руками.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaBytes))
	if err != nil {
		return nil, err
	}

	var catalog map[string]struct {
		Models map[string]metaModel `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}

	section, ok := catalog[string(ID)]
	if !ok {
		return nil, &modelcat.Error{Op: "каталог свойств", Reason: "секция сервиса отсутствует"}
	}

	out := make(map[string]provider.Model, len(section.Models))
	for id, raw := range section.Models {
		out[id] = raw.toModel(id)
	}
	return out, nil
}

// toModel переводит свойства в форму шлюза.
func (m metaModel) toModel(id string) provider.Model {
	out := provider.Model{ID: id}

	if m.Name != "" {
		out.Name = m.Name
	}
	if m.Description != "" {
		out.Description = m.Description
	}
	if m.Limit.Context > 0 {
		out.Context = m.Limit.Context
	}
	if m.Limit.Output > 0 {
		out.MaxOutput = m.Limit.Output
	}

	// Приём изображений объявлен двумя способами: отдельным признаком и
	// списком модальностей входа. Достаточно любого — каталог заполняет то
	// одно, то другое.
	out.Vision = m.Attachment || containsString(m.Modalities.Input, "image")

	// Поле размышлений берётся из свойств: у разных моделей ответ несёт их в
	// разных местах, и запись не туда сломала бы продолжение по своей же
	// прошлой мысли.
	if field := interleavedField(m.Interleaved); field != "" {
		out.ReasoningField = field
	}

	if m.Reasoning {
		out.Reasoning = true
		out.ReasoningOptions = reasoningOptions(m.ReasoningOptions)
	}

	return out
}

// reasoningOptions переводит лестницу усилий в форму шлюза.
//
// Формы три: переключатель, лестница значений и бюджет в токенах. Каталог
// может объявить несколько сразу, и все они сохраняются: клиент выбирает ту,
// которую понимает. Пустой список — норма: часть моделей объявляет умение
// думать, не описывая, как именно им управлять.
func reasoningOptions(in []struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Max    int      `json:"max"`
}) []provider.ReasoningOption {
	out := make([]provider.ReasoningOption, 0, len(in))
	for _, raw := range in {
		switch raw.Type {
		case "toggle":
			out = append(out, provider.ReasoningOption{Kind: provider.ReasoningToggle})
		case "effort":
			if len(raw.Values) > 0 {
				out = append(out, provider.ReasoningOption{
					Kind:   provider.ReasoningEffort,
					Values: append([]string(nil), raw.Values...),
				})
			}
		case "budget_tokens":
			out = append(out, provider.ReasoningOption{
				Kind: provider.ReasoningBudget,
				Max:  raw.Max,
			})
		}
	}
	return out
}

// containsString сообщает, есть ли значение в списке.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// interleavedField достаёт имя поля размышлений.
//
// Поле в каталоге встречается в трёх видах: объектом с именем, отсутствующим
// и ложью. Ложь означает «размышления не чередуются» — свойства нет, и
// возвращается пустая строка. Разбор молчаливый намеренно: непонятное
// значение здесь не повод терять остальные свойства модели.
func interleavedField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return parsed.Field
}
