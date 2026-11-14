package cli

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadMenuEvent(t *testing.T) {
	cases := []struct {
		in   string
		want menuEvent
	}{
		{"\x1b[A", menuEvent{kind: menuUp}},
		{"\x1b[B", menuEvent{kind: menuDown}},
		{"\x1b[H", menuEvent{kind: menuHome}},
		{"\x1b[F", menuEvent{kind: menuEnd}},
		{"\r", menuEvent{kind: menuEnter}},
		{"\x03", menuEvent{kind: menuCancel}},
		{"\x7f", menuEvent{kind: menuBackspace}},
		{"a", menuEvent{kind: menuRune, r: 'a'}},
	}
	for _, tc := range cases {
		got, err := readMenuEvent(bufio.NewReader(strings.NewReader(tc.in)))
		if err != nil {
			t.Fatalf("readMenuEvent(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("readMenuEvent(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}
