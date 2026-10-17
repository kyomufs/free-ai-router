package opencode

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyOnRecordedRefusal проверяет разбор отказа на ЗАПИСАННОМ ответе
// сервиса, а не на выдуманном.
//
// Это единственная проверка формы отказа, которую нельзя вывести из кода:
// измерения в плане получены чтением исходников, а здесь — живой обмен. Файл и
// его происхождение описаны в testdata/README.md.
//
// Осторожно: ответ зависит от состояния квоты на момент запроса. Если он
// изменится, тест это покажет — и тогда обновлять надо ФИКСТУРУ, а не
// подгонять тест.
func TestClassifyOnRecordedRefusal(t *testing.T) {
	body := readFixture(t, "refusal-freetier.json")

	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
	}
	resp.Header.Set("content-type", "application/json")

	failure := newTestProvider(t).Classify(resp, body)

	// Тело обязано дойти до клиента БЕЗ ИЗМЕНЕНИЙ: клиент ищет в нём признаки
	// error.type и error.code, по которым принимает решение о повторе.
	if string(failure.Body) != string(body) {
		t.Fatalf("the refusal body was altered:\n got %s\nwant %s", failure.Body, body)
	}
	if failure.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", failure.Status)
	}

	// Отказ в доступе означает, что при наличии ключа осмысленно попробовать
	// другой: сервис отвечает так и на исчерпанное право доступа.
	if !failure.KeyRejected {
		t.Fatal("403 must be treated as a reason to try another credential")
	}
}

// TestRecordedRefusalKeepsClientSignals проверяет, что признаки, по которым
// клиент принимает решение, доживают до него.
//
// Признаки проверяются подстроками: тело передаётся дословно, и разбирать его
// здесь значило бы заводить второго разборщика того же ответа.
func TestRecordedRefusalKeepsClientSignals(t *testing.T) {
	body := string(readFixture(t, "refusal-freetier.json"))

	for _, signal := range []string{`"type":"error"`, `"type":"FreeTierError"`, `"error"`} {
		if !strings.Contains(body, signal) {
			t.Fatalf("the recorded refusal lost the signal %s: %s", signal, body)
		}
	}
}

// TestClassifyLeavesAbsentHeaderAlone: в записанном отказе нет заголовка
// ожидания, и разбор не должен его выдумывать.
func TestClassifyLeavesAbsentHeaderAlone(t *testing.T) {
	body := readFixture(t, "refusal-freetier.json")
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}

	failure := newTestProvider(t).Classify(resp, body)
	if failure.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v, want none: the service sent no retry-after", failure.RetryAfter)
	}
}

// readFixture читает записанный обмен.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
	if len(raw) == 0 {
		t.Fatalf("fixture %s is empty", path)
	}
	return raw
}
