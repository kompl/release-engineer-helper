package models

import (
	"sort"
	"testing"
)

func TestStringSetBasics(t *testing.T) {
	s := NewStringSet("a", "b")
	if s.Len() != 2 {
		t.Errorf("Len() = %d, want 2", s.Len())
	}
	if !s.Contains("a") || !s.Contains("b") {
		t.Error("set must contain initial items")
	}
	if s.Contains("c") {
		t.Error("set must not contain absent item")
	}

	s.Add("c")
	s.Add("c") // idempotent
	if s.Len() != 3 || !s.Contains("c") {
		t.Errorf("after Add: Len() = %d, Contains(c) = %v", s.Len(), s.Contains("c"))
	}

	items := s.ToSlice()
	sort.Strings(items)
	want := []string{"a", "b", "c"}
	if len(items) != len(want) {
		t.Fatalf("ToSlice() = %v, want %v", items, want)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("ToSlice()[%d] = %q, want %q", i, items[i], want[i])
		}
	}
}

func TestStringSetDifference(t *testing.T) {
	a := NewStringSet("a", "b", "c")
	b := NewStringSet("b", "d")

	diff := a.Difference(b)
	if diff.Len() != 2 || !diff.Contains("a") || !diff.Contains("c") {
		t.Errorf("a-b = %v, want {a, c}", diff.ToSlice())
	}

	empty := NewStringSet().Difference(a)
	if empty.Len() != 0 {
		t.Errorf("∅-a = %v, want empty", empty.ToSlice())
	}
}

func TestStringSetUnion(t *testing.T) {
	a := NewStringSet("a", "b")
	b := NewStringSet("b", "c")

	u := a.Union(b)
	if u.Len() != 3 || !u.Contains("a") || !u.Contains("b") || !u.Contains("c") {
		t.Errorf("a∪b = %v, want {a, b, c}", u.ToSlice())
	}

	// Union must not mutate the operands
	if a.Len() != 2 || b.Len() != 2 {
		t.Error("Union must not mutate operands")
	}
}
