package main

import "testing"

func TestNormalizeIndexTitleCollapsesWhitespace(t *testing.T) {
	got := normalizeIndexTitle("  Nước   Anh \n Thiếu  Đầu Tư  ")
	want := "Nước Anh Thiếu Đầu Tư"

	if got != want {
		t.Fatalf("normalizeIndexTitle() = %q, want %q", got, want)
	}
}
