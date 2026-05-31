package logfields

import "testing"

func TestShort(t *testing.T) {
	if Short("abcdef") != "abcdef" {
		t.Fatalf("short string unchanged")
	}
	got := Short("0x0123456789abcdef0123456789")
	if got != "0x0123…6789" {
		t.Fatalf("expected head+tail, got %q", got)
	}
}

func TestTitle(t *testing.T) {
	if Title("hello") != "hello" {
		t.Fatalf("short title unchanged")
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	got := Title(long)
	if len([]rune(got)) != 81 {
		t.Fatalf("expected 80 runes + ellipsis, got %d", len([]rune(got)))
	}
}
