package transport

import (
	"context"
	"net/http"
	"testing"
)

// TestDirectRoundTripperIsUsable — прямой транспорт обязан отдавать рабочий
// RoundTripper и уметь останавливаться без ошибок.
func TestDirectRoundTripperIsUsable(t *testing.T) {
	v, err := NewDirect(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	tr := v.(Transport)

	if tr.ID() != Direct {
		t.Fatalf("ID = %q, want %q", tr.ID(), Direct)
	}
	rt, err := tr.RoundTripper()
	if err != nil {
		t.Fatalf("RoundTripper: %v", err)
	}
	if rt == nil {
		t.Fatal("RoundTripper returned nil")
	}
	if _, ok := rt.(*http.Transport); !ok {
		t.Fatalf("RoundTripper is %T, want *http.Transport", rt)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestDirectFamilyPinsNetwork — пиннинг семейства обязан менять сеть дозвона,
// иначе настройка ничего не значит.
//
// Проверяется поведение, а не поле: RoundTripper должен оставаться рабочим при
// любом семействе, включая нераспознанное.
func TestDirectFamilyPinsNetwork(t *testing.T) {
	for _, family := range []string{"", "auto", "v4", "v6", "мусор"} {
		v, err := NewDirect(map[string]any{"family": family})
		if err != nil {
			t.Fatalf("family %q: %v", family, err)
		}
		if _, err := v.RoundTripper(); err != nil {
			t.Fatalf("family %q: RoundTripper: %v", family, err)
		}
	}
}

// TestNetworkFor — перевод настройки в имя сети. Нераспознанное значение
// означает «пусть решает система», а не ошибку: это то же самое, что "auto".
func TestNetworkFor(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"auto":   "",
		"v4":     "tcp4",
		"v6":     "tcp6",
		"что-то": "",
	}
	for in, want := range cases {
		if got := networkFor(in); got != want {
			t.Fatalf("networkFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRegistryWithDefaultsHasDirect — реестр по умолчанию содержит прямой
// транспорт, и точка входа не обязана его регистрировать.
func TestRegistryWithDefaultsHasDirect(t *testing.T) {
	r := NewRegistryWithDefaults()

	ids := r.IDs()
	if len(ids) == 0 {
		t.Fatal("the default registry is empty")
	}
	found := false
	for _, id := range ids {
		if id == Direct {
			found = true
		}
	}
	if !found {
		t.Fatalf("the default registry lacks %q: %v", Direct, ids)
	}

	v, err := r.Build(Direct, map[string]any{})
	if err != nil {
		t.Fatalf("Build(direct): %v", err)
	}
	if v.ID() != Direct {
		t.Fatalf("Build(direct) returned %q", v.ID())
	}
}

// TestRegistryRejectsUnknown — сборка незарегистрированного транспорта это
// ошибка, а не молчаливый откат к чему-то другому.
func TestRegistryRejectsUnknown(t *testing.T) {
	r := NewRegistryWithDefaults()
	if _, err := r.Build("нет-такого", nil); err == nil {
		t.Fatal("an unknown transport must fail")
	}
}

// TestRegistryIDsStableOrder — порядок стабилен, иначе списки в CLI и TUI
// прыгали бы между вызовами.
func TestRegistryIDsStableOrder(t *testing.T) {
	r := NewRegistryWithDefaults()
	first := r.IDs()
	second := r.IDs()
	if len(first) != len(second) {
		t.Fatalf("IDs changed length between calls: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("IDs order is unstable: %v vs %v", first, second)
		}
	}
}
