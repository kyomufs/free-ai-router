package wire

import (
	"strings"
	"testing"
)

// TestSessionIDShape проверяет форму идентификатора: префикс, 12 hex времени
// и 14 символов хвоста. Это первое, что видит гейт, и форма выведена из
// identifier.ts.
func TestSessionIDShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewSessionID()

		if !strings.HasPrefix(id, SessionIDPrefix) {
			t.Fatalf("id %q lacks the %q prefix", id, SessionIDPrefix)
		}
		body := id[len(SessionIDPrefix):]
		if len(body) != idTotalLength {
			t.Fatalf("id %q has body length %d, want %d", id, len(body), idTotalLength)
		}
		if got := body[:idTimeLength]; !isLowerHex(got) {
			t.Fatalf("id %q: time part %q is not lowercase hex", id, got)
		}
		for _, c := range body[idTimeLength:] {
			if _, ok := base62Value(byte(c)); !ok {
				t.Fatalf("id %q: tail char %q is outside the base62 alphabet", id, c)
			}
		}
		if !IsCanonicalSessionID(id) {
			t.Fatalf("id %q is not recognised as canonical", id)
		}
	}
}

func TestMessageIDShape(t *testing.T) {
	id := NewMessageID()
	if !strings.HasPrefix(id, MessageIDPrefix) {
		t.Fatalf("id %q lacks the %q prefix", id, MessageIDPrefix)
	}
	if body := id[len(MessageIDPrefix):]; len(body) != idTotalLength {
		t.Fatalf("id %q has body length %d, want %d", id, len(body), idTotalLength)
	}
}

// TestTimestampRoundTripWithinField — главное проверяемое свойство формулы:
// пока timestamp помещается в 36 бит, кодирование и декодирование взаимно
// обратны.
func TestTimestampRoundTripWithinField(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits) // 2^36 - 1

	for _, ts := range []uint64{0, 1, 1000, 1 << 20, 1 << 30, hi - 1, hi} {
		for _, counter := range []uint64{1, 5, 4095} {
			id := SessionIDPrefix + timeFieldOf(ts, counter) + strings.Repeat("0", 14)
			at, err := DecodeSessionTimestamp(id)
			if err != nil {
				t.Fatalf("ts=%d: %v", ts, err)
			}
			if uint64(at.UnixMilli()) != ts {
				t.Fatalf("ts=%d counter=%d: decoded %d, want %d", ts, counter, at.UnixMilli(), ts)
			}
		}
	}
}

// TestTimestampAboveFieldIsTruncated фиксирует ГРАНИЦУ свойства: timestamp
// реального времени (41 бит) в шестибайтовое поле не помещается, старшие биты
// при записи отбрасываются, и обратимость теряется.
//
// Это не дефект шлюза, а свойство формата клиента. Тест написан, чтобы
// границу не приняли за ошибку и не «исправили» формулу в удобную сторону:
// форма обязана совпадать с клиентом.
func TestTimestampAboveFieldIsTruncated(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits)

	// Проверяем ровно на границе: hi+1 уже не помещается.
	id := SessionIDPrefix + timeFieldOf(hi+1, 1) + strings.Repeat("0", 14)
	at, err := DecodeSessionTimestamp(id)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(at.UnixMilli()) == hi+1 {
		t.Fatalf("expected truncation above the field width, got an exact round-trip")
	}
	if at.UnixMilli() != 0 {
		t.Fatalf("hi+1 truncates to 0, got %d", at.UnixMilli())
	}
}

// TestLiveTimestampIsRepresentable — практическая половина: сегодняшний
// timestamp усекается, но идентификатор остаётся каноническим, а это всё, что
// требуется для прохождения.
func TestLiveTimestampIsRepresentable(t *testing.T) {
	const live = 1_792_000_000_000

	id := NewSessionIDAt(live)
	if !IsCanonicalSessionID(id) {
		t.Fatalf("a live timestamp produced non-canonical id %q", id)
	}
	if _, err := DecodeSessionTimestamp(id); err != nil {
		t.Fatalf("live id %q does not decode: %v", id, err)
	}
}

// TestIDsDescend проверяет следствие дополнения: у более позднего момента
// временное поле МЕНЬШЕ. Это и есть смысл descending() — без инверсии формула
// была бы просто хешем.
func TestIDsDescend(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits)

	// hi-1000 — более поздний момент, чем hi-2000, значит его поле меньше.
	earlier := timeFieldOf(hi-2000, 1)
	later := timeFieldOf(hi-1000, 1)

	if later >= earlier {
		t.Fatalf("expected the later timestamp to sort lower: earlier=%s later=%s", earlier, later)
	}

	// И то же самое на реальных идентификаторах.
	const base int64 = 1_700_000_000_000
	first := NewSessionIDAt(base)
	second := NewSessionIDAt(base + 1)
	field := func(id string) string { return id[len(SessionIDPrefix) : len(SessionIDPrefix)+idTimeLength] }
	if field(second) >= field(first) {
		t.Fatalf("later id must sort lower: first=%s second=%s", field(first), field(second))
	}
}

// TestCounterResetsPerMillisecond воспроизводит сброс счётчика: два
// идентификатора в одну миллисекунду различаются счётчиком, а в разные —
// начинаются заново с единицы.
func TestCounterResetsPerMillisecond(t *testing.T) {
	const milli int64 = 1_750_000_000_000

	_, c1, err := DecodeSessionTimestampCheck(NewSessionIDAt(milli))
	if err != nil {
		t.Fatal(err)
	}
	_, c2, err := DecodeSessionTimestampCheck(NewSessionIDAt(milli))
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1+1 {
		t.Fatalf("same-millisecond counter: got %d after %d, want an increment", c2, c1)
	}
	_, c3, err := DecodeSessionTimestampCheck(NewSessionIDAt(milli + 1))
	if err != nil {
		t.Fatal(err)
	}
	if c3 != 1 {
		t.Fatalf("counter after a millisecond change: got %d, want 1 (reset)", c3)
	}
}

// TestCanonicalSessionIDKeepsValidID: готовый канонический id не пересобирается.
// Он часть истории сессии, и подмена сломала бы её.
func TestCanonicalSessionIDKeepsValidID(t *testing.T) {
	original := NewSessionID()
	if got := CanonicalSessionID(original); got != original {
		t.Fatalf("a canonical id must pass through: got %q, want %q", got, original)
	}
}

// TestCanonicalSessionIDReplacesInvalid: неканонический id заменяется свежим.
func TestCanonicalSessionIDReplacesInvalid(t *testing.T) {
	cases := []string{
		"",
		"ses_short",
		"ses_" + strings.Repeat("Z", 26),
		strings.Repeat("a", 26),
		"ses_" + strings.Repeat("a", 25),
		"ses_" + strings.Repeat("a", 27),
		"msg_" + strings.Repeat("a", 26),
	}
	for _, in := range cases {
		got := CanonicalSessionID(in)
		if !IsCanonicalSessionID(got) {
			t.Fatalf("input %q produced non-canonical %q", in, got)
		}
		if got == in {
			t.Fatalf("input %q was passed through but is not canonical", in)
		}
	}
}

// TestDecodeRejectsForeignID: посторонняя строка не декодируется молча.
func TestDecodeRejectsForeignID(t *testing.T) {
	for _, in := range []string{"", "ses_x", "msg_" + strings.Repeat("a", 26), "random"} {
		if _, err := DecodeSessionTimestamp(in); err == nil {
			t.Fatalf("input %q decoded without error", in)
		}
	}
}

// TestCounterBitsBound: счётчик не выходит за 12 бит, иначе он затирал бы
// временное поле.
func TestCounterBitsBound(t *testing.T) {
	const milli int64 = 1_760_000_000_000
	for i := 0; i < (1<<idCounterBits)+5; i++ {
		_, counter, err := DecodeSessionTimestampCheck(NewSessionIDAt(milli))
		if err != nil {
			t.Fatal(err)
		}
		if counter < 0 || counter >= 1<<idCounterBits {
			t.Fatalf("counter %d is outside the %d-bit field", counter, idCounterBits)
		}
	}
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
