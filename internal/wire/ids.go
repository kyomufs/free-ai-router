package wire

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"sync"
	"time"
)

// Контракт идентификаторов выведен из
// packages/schema/src/identifier.ts (opencode 2.0.30):
//
//	const length = 26
//	const chars  = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
//
//	export function create(descending, timestamp = Date.now()) {
//	  if (timestamp !== lastTimestamp) { lastTimestamp = timestamp; counter = 0 }
//	  counter++
//	  const current = BigInt(timestamp) * 0x1000n + BigInt(counter)
//	  const value   = descending ? ~current : current
//	  const time = 6 байт value, big-endian, как 12 hex-символов
//	  const bytes = crypto.getRandomValues(new Uint8Array(length - 12))
//	  return time + массив bytes, каждый chars[byte % 62]
//	}
//
// Две вещи здесь не косметика, а часть формулы:
//
//   - value инвертируется побитово (descending), поэтому идентификаторы
//     УБЫВАЮТ во времени;
//   - счётчик сбрасывается при смене миллисекунды, а не ведёт себя как
//     глобальный счётчик процесса.
//
// SessionID дополнительно получает префикс "ses_" (session-id.ts), message id —
// "msg_" (session-message.ts).
const (
	// idTotalLength — общая длина части, следующей за префиксом: 12 hex
	// времени плюс 14 base62.
	idTotalLength = 26

	// idTimeLength — длина временной части в hex-символах (6 байт).
	idTimeLength = 12

	// base62Alphabet — алфавит хвостовой части, дословно из identifier.ts.
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// idCounterBits — сколько младших бит value занимает счётчик
	// (BigInt(timestamp) * 0x1000n оставляет 12 бит).
	idCounterBits = 12

	// idTimeMask — маска 48 бит, которые попадают в шестибайтовое поле.
	idTimeMask = 0xFFFFFFFFFFFF
)

// SessionIDPrefix is the prefix every session id carries.
const SessionIDPrefix = "ses_"

// MessageIDPrefix is the prefix every message id carries.
const MessageIDPrefix = "msg_"

// canonicalSessionPattern распознаёт уже канонический session id, чтобы не
// пересобирать его. Совпадает по форме с session-id.ts: "ses_" + 12 hex + 14
// base62.
var canonicalSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// idState счётчик, эквивалентный module-level lastTimestamp/counter из
// identifier.ts. Клиенты mintят идентификаторы редко, поэтому одного мьютекса
// достаточно и он честно воспроизводит исходную семантику.
var idState struct {
	mu        sync.Mutex
	lastMilli int64
	counter   int64
}

// NewSessionID возвращает новый session id, построенный по формуле клиента.
func NewSessionID() string { return NewSessionIDAt(time.Now().UnixMilli()) }

// NewSessionIDAt is NewSessionID with an injected clock, so tests can pin the
// arithmetic without freezing time process-wide.
func NewSessionIDAt(nowMillis int64) string {
	return SessionIDPrefix + newIDAt(nowMillis)
}

// NewMessageID возвращает новый message id (префикс "msg_").
func NewMessageID() string { return NewMessageIDAt(time.Now().UnixMilli()) }

// NewMessageIDAt is NewMessageID with an injected clock.
func NewMessageIDAt(nowMillis int64) string {
	return MessageIDPrefix + newIDAt(nowMillis)
}

// CanonicalSessionID принимает уже канонический id без изменений, а иначе
// минтит новый.
//
// Отдельная функция нужна потому, что клиент может прислать готовый id: его
// нельзя пересобирать, он часть истории сессии.
func CanonicalSessionID(id string) string {
	if canonicalSessionPattern.MatchString(id) {
		return id
	}
	return NewSessionID()
}

// IsCanonicalSessionID reports whether id already has the admitted form.
func IsCanonicalSessionID(id string) bool { return canonicalSessionPattern.MatchString(id) }

// newIDAt — детерминированное ядро формулы, возвращающее часть без префикса.
func newIDAt(nowMillis int64) string {
	counter := nextCounter(nowMillis)

	// current = timestamp * 4096 + counter, затем дополнение и маска до 48
	// бит, которые несёт шестибайтовое поле.
	current := uint64(nowMillis)<<idCounterBits + uint64(counter)
	value := (^current) & idTimeMask

	timePart := hex.EncodeToString([]byte{
		byte(value >> 40), byte(value >> 32), byte(value >> 24),
		byte(value >> 16), byte(value >> 8), byte(value),
	})

	return timePart + randomBase62(idTotalLength-idTimeLength)
}

// nextCounter воспроизводит сброс счётчика при смене миллисекунды.
func nextCounter(nowMillis int64) int64 {
	idState.mu.Lock()
	defer idState.mu.Unlock()
	if nowMillis != idState.lastMilli {
		idState.lastMilli = nowMillis
		idState.counter = 0
	}
	idState.counter++
	return idState.counter
}

// randomBase62 возвращает width символов алфавита, каждый выбран из
// случайного байта через остаток — дословно как Array.from(bytes, byte => chars[byte % 62]).
//
// Остаток от 256 по 62 даёт небольшое смещение (байты 0..247 распределены
// ровно, 248..255 дают лишние попадания в первые 8 символов). Для
// идентификатора это безразлично: он не обязан быть равномерным, он обязан
// иметь ту же форму, что у клиента.
func randomBase62(width int) string {
	buf := make([]byte, width)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand на практике не отказывает; панику заменяем на
		// math/rand-подобный запасной путь не будем — это скрыло бы
		// серьёзную проблему окружения.
		panic(fmt.Sprintf("wire: crypto/rand unavailable: %v", err))
	}
	out := make([]byte, width)
	for i, b := range buf {
		out[i] = base62Alphabet[int(b)%len(base62Alphabet)]
	}
	return string(out)
}

// DecodeSessionTimestamp извлекает момент времени, закодированный в session id.
//
// ВНИМАНИЕ О ТОЧНОСТИ. Шестибайтовое поле хранит 48 бит, а `timestamp * 4096`
// занимает 53 бита, поэтому старшие пять бит значения теряются при записи.
// Так ведёт себя и сам клиент: identifier.ts сдвигает BigInt вправо и берёт по
// байту, отбрасывая всё, что не помещается. Значит восстановленный момент
// времени — это timestamp, усечённый до 36 бит, и он НЕ равен исходному у
// значений выше примерно 2^36 (около 68.7 млрд мс, то есть после 1972 года).
//
// Функция поэтому годится для диагностики формы и для тестов, но не как
// утверждение «здесь лежит время запроса». Каноничность id определяется
// формой (§IsCanonicalSessionID), а не тем, что время внутри читается.
func DecodeSessionTimestamp(id string) (time.Time, error) {
	at, _, err := DecodeSessionTimestampCheck(id)
	return at, err
}

// DecodeSessionTimestampCheck дополнительно возвращает счётчик из младших бит
// — он нужен тестам, проверяющим сброс счётчика при смене миллисекунды.
func DecodeSessionTimestampCheck(id string) (time.Time, int64, error) {
	if !canonicalSessionPattern.MatchString(id) {
		return time.Time{}, 0, fmt.Errorf("wire: %q is not a canonical session id", id)
	}
	timePart := id[len(SessionIDPrefix) : len(SessionIDPrefix)+idTimeLength]
	raw, err := hex.DecodeString(timePart)
	if err != nil || len(raw) != 6 {
		return time.Time{}, 0, fmt.Errorf("wire: session id %q has an undecodable time part", id)
	}

	var value uint64
	for _, b := range raw {
		value = value<<8 | uint64(b)
	}
	current := ^value & idTimeMask
	millis := int64(current >> idCounterBits)
	counter := int64(current & (1<<idCounterBits - 1))
	return time.UnixMilli(millis), counter, nil
}

// base62Value используется только тестами: возвращает индекс символа в
// алфавите, чтобы утверждения могли говорить о форме хвоста.
func base62Value(c byte) (int, bool) {
	for i := 0; i < len(base62Alphabet); i++ {
		if base62Alphabet[i] == c {
			return i, true
		}
	}
	return 0, false
}

// timeFieldOf воспроизводит ровно то, что делает identifier.ts с временным
// полем, — включая потерю старших бит. Существует, чтобы эталонное значение
// для тестов получалось тем же кодом, которым пользуется шлюз, и расхождение
// с реализацией клиента было видно сразу.
func timeFieldOf(millis, counter uint64) string {
	value := (^(millis<<idCounterBits + counter)) & idTimeMask
	return hex.EncodeToString([]byte{
		byte(value >> 40), byte(value >> 32), byte(value >> 24),
		byte(value >> 16), byte(value >> 8), byte(value),
	})
}

// bigIntFromBase62 собирает число из строки алфавита — вспомогательное для
// тестов, которым нужен порядок, а не значение.
func bigIntFromBase62(s string) *big.Int {
	out := new(big.Int)
	base := big.NewInt(int64(len(base62Alphabet)))
	for i := 0; i < len(s); i++ {
		v, ok := base62Value(s[i])
		if !ok {
			return nil
		}
		out.Mul(out, base)
		out.Add(out, big.NewInt(int64(v)))
	}
	return out
}
