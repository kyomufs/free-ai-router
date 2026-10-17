package capability

import "testing"

// TestKeyRotationSingleKeyHasNoAlternatives — при одном ключе повтор
// бессмыслен, и механизм обязан это сообщить.
func TestKeyRotationSingleKeyHasNoAlternatives(t *testing.T) {
	v, err := NewKeyRotation(map[string]any{"keys": []any{"только-один"}})
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	if kr.HasAlternatives() {
		t.Fatal("a single key must report no alternatives")
	}
	if _, ok := kr.Take("только-один"); ok {
		t.Fatal("Take must fail when there is no different key")
	}
}

// TestKeyRotationEmptyYieldsNothing — без ключей механизм не выдумывает
// значение, а честно сообщает, что ключа нет: провайдер тогда пойдёт анонимно.
func TestKeyRotationEmptyYieldsNothing(t *testing.T) {
	v, err := NewKeyRotation(nil)
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	if kr.Len() != 0 {
		t.Fatalf("Len = %d, want 0", kr.Len())
	}
	if _, ok := kr.Next(); ok {
		t.Fatal("Next must report no key when the list is empty")
	}
	if kr.HasAlternatives() {
		t.Fatal("an empty list must report no alternatives")
	}
}

// TestKeyRotationEmptyStringsDropped — пустая строка в списке это след лишней
// запятой, а не ключ: выдавать её значило бы гарантировать отказ.
func TestKeyRotationEmptyStringsDropped(t *testing.T) {
	v, err := NewKeyRotation(map[string]any{"keys": []any{"первый", "", "   ", "второй"}})
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	// Пустая строка отбрасывается, а вот строка из пробелов остаётся: это
	// осмысленное (хоть и бесполезное) значение, и решать за оператора
	// шлюз не вправе.
	if kr.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (only the empty string is dropped)", kr.Len())
	}
	for _, k := range kr.KeysSnapshot() {
		if k == "" {
			t.Fatal("an empty key survived the filter")
		}
	}
}

// TestKeyRotationTakeReturnsDifferentKey — смысл механизма: повтор идёт с
// ДРУГИМ ключом.
func TestKeyRotationTakeReturnsDifferentKey(t *testing.T) {
	v, err := NewKeyRotation(map[string]any{"keys": []any{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	for _, current := range []string{"a", "b", "c"} {
		got, ok := kr.Take(current)
		if !ok {
			t.Fatalf("Take(%q) reported no alternative", current)
		}
		if got == current {
			t.Fatalf("Take(%q) returned the same key", current)
		}
	}
}

// TestKeyRotationNextCycles — Next выдаёт разные ключи подряд.
func TestKeyRotationNextCycles(t *testing.T) {
	v, err := NewKeyRotation(map[string]any{"keys": []any{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	first, ok1 := kr.Next()
	second, ok2 := kr.Next()
	if !ok1 || !ok2 {
		t.Fatal("Next reported no key with two keys available")
	}
	if first == second {
		t.Fatalf("two consecutive Next calls returned the same key %q", first)
	}
}

// TestKeyRotationAddAndRemove — правка списка работает и не сбивает порядок.
func TestKeyRotationAddAndRemove(t *testing.T) {
	v, err := NewKeyRotation(map[string]any{"keys": []any{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	kr := v.(*KeyRotation)

	if !kr.Add("b") {
		t.Fatal("Add of a new key reported no change")
	}
	if kr.Add("b") {
		t.Fatal("Add of an existing key must report no change")
	}
	if kr.Add("") {
		t.Fatal("Add of an empty key must be rejected")
	}
	if kr.Len() != 2 {
		t.Fatalf("Len = %d, want 2", kr.Len())
	}

	if !kr.Remove("a") {
		t.Fatal("Remove of an existing key reported no change")
	}
	if kr.Remove("a") {
		t.Fatal("Remove of an absent key must report no change")
	}
	if kr.Len() != 1 {
		t.Fatalf("Len = %d, want 1", kr.Len())
	}
}

// TestKeyRotationRejectsWrongConfigShape — неверная форма настроек это ошибка,
// а не молчаливое игнорирование: иначе опечатка в конфигурации осталась бы
// незамеченной.
func TestKeyRotationRejectsWrongConfigShape(t *testing.T) {
	cases := []map[string]any{
		{"keys": "строка вместо списка"},
		{"keys": []any{42}},
		{"startAt": "не число"},
	}
	for _, cfg := range cases {
		if _, err := NewKeyRotation(cfg); err == nil {
			t.Fatalf("config %v was accepted, want an error", cfg)
		}
	}
}

// TestRegistryReturnsRegistered — реестр отдаёт то, что зарегистрировано, и
// сообщает об отсутствии ясно.
func TestRegistryReturnsRegistered(t *testing.T) {
	r := NewRegistry()
	r.Register(Info{Kind: KindKeyRotation, Name: "pool", Summary: "s"}, NewKeyRotation)

	built, err := r.Build(KindKeyRotation, "pool", map[string]any{"keys": []any{"k"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := built.(*KeyRotation); !ok {
		t.Fatalf("Build returned %T, want *KeyRotation", built)
	}

	if _, err := r.Build(KindKeyRotation, "нет-такого", nil); err == nil {
		t.Fatal("an unknown name must fail")
	}
	if _, err := r.Build(KindRetry, "pool", nil); err == nil {
		t.Fatal("an unknown kind must fail")
	}
}

// TestRegistryNamesAreSorted — порядок стабилен, иначе списки в интерфейсах
// прыгали бы между перерисовками.
func TestRegistryNamesAreSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(Info{Kind: KindKeyRotation, Name: "c"}, NewKeyRotation)
	r.Register(Info{Kind: KindKeyRotation, Name: "a"}, NewKeyRotation)
	r.Register(Info{Kind: KindKeyRotation, Name: "b"}, NewKeyRotation)

	names := r.Names(KindKeyRotation)
	want := []string{"a", "b", "c"}
	if len(names) != len(want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names = %v, want %v", names, want)
		}
	}
}
