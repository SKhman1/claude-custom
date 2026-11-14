package cli

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadMaskedKey(t *testing.T) {
	var out bytes.Buffer
	got, err := readMaskedKey(bufio.NewReader(strings.NewReader("ab\x7fc\r")), &out)
	if err != nil {
		t.Fatalf("readMaskedKey: %v", err)
	}
	if got != "ac" {
		t.Errorf("key = %q, want %q", got, "ac")
	}
	if want := "**\b \b*\r\n"; out.String() != want {
		t.Errorf("masked output = %q, want %q", out.String(), want)
	}
}

func TestReadMaskedKeyIgnoresEscapeSequences(t *testing.T) {
	var out bytes.Buffer
	got, err := readMaskedKey(bufio.NewReader(strings.NewReader("a\x1b[Db\r")), &out)
	if err != nil {
		t.Fatalf("readMaskedKey: %v", err)
	}
	if got != "ab" {
		t.Errorf("key = %q, want %q", got, "ab")
	}
	if want := "**\r\n"; out.String() != want {
		t.Errorf("masked output = %q, want %q", out.String(), want)
	}
}

func TestReadMaskedKeyCancel(t *testing.T) {
	var out bytes.Buffer
	_, err := readMaskedKey(bufio.NewReader(strings.NewReader("\x03")), &out)
	if !errors.Is(err, errCanceled) {
		t.Fatalf("err = %v, want errCanceled", err)
	}
}
