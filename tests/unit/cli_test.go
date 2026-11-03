package unit

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/cli"
)

// The binary decides what it is from its first word, and the two answers have very
// different consequences: an operator command opens a database file, while anything else
// starts a homeserver on a port. A word that means neither - `lattice adduser` typed
// without the dash - must not quietly become a server listening on :8008 with a fresh
// database in the current directory.

func TestCommandWords(t *testing.T) {
	cases := []struct {
		word string
		want bool
	}{
		{word: cli.AddUser, want: true},
		{word: cli.RemoveUser, want: true},
		{word: cli.Users, want: true},
		{word: cli.Help, want: true},
		{
			// Every existing way of starting the server begins with a flag, and each of
			// these has to stay a flag rather than be read as a word.
			word: "-addr", want: false,
		},
		{word: "-server-name", want: false},
		{word: "-version", want: false},
		{word: "-disable-registration", want: false},
		{word: "-federation", want: false},
		{word: "", want: false},
		{word: "adduser", want: false},
		{word: "add_user", want: false},
		{word: "AddUser", want: false},
		{word: "user", want: false},
		{word: "register", want: false},
		{word: "serve", want: false},
	}
	for _, tc := range cases {
		t.Run("["+tc.word+"]", func(t *testing.T) {
			if got := cli.IsCommand(tc.word); got != tc.want {
				t.Errorf("IsCommand(%q) = %v, want %v", tc.word, got, tc.want)
			}
		})
	}
}

// An unknown command is answered with a sentence that names where the list is, because
// the alternative is an operator guessing which of the four words they meant.
func TestUnknownCommandPointsAtTheList(t *testing.T) {
	var out bytes.Buffer
	err := cli.RunWithEnv("user", []string{"alice"}, strings.NewReader(""), &out, os.LookupEnv)
	if err == nil {
		t.Fatalf("`lattice user` was expected to fail, printed: %s", out.String())
	}
	for _, want := range []string{`is not a command`, cli.Help} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the answer %q does not mention %q", err, want)
		}
	}
}
