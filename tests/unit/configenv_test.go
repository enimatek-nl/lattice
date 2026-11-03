package unit

import (
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/config"
)

// A container is configured with variables and a person at a shell with flags, and
// both have to end up running the same homeserver. The mapping between them is
// mechanical, which is exactly how it goes wrong: read one name differently, or let
// an image's default outrank what was typed, and a deployment comes up with a
// generated signing secret and its database in a directory that is not on the
// volume it backs up. These cases pin the names, the precedence and the refusal.

// settings holds one representative of every kind of value the binary registers: a
// string, a bool, a duration and an integer, from both the server's flags and the
// operator commands'.
type settings struct {
	serverName string
	federation bool
	tokenTTL   time.Duration
	writeBurst int
	version    bool
	password   string
	admin      bool
	limit      int
	all        bool
}

// snapshot renders the settings as one comparable string.
func (s settings) snapshot() string {
	return fmt.Sprintf("%s|%v|%s|%d|%v|%s|%v|%d|%v",
		s.serverName, s.federation, s.tokenTTL, s.writeBurst, s.version,
		s.password, s.admin, s.limit, s.all)
}

// flagSet registers the settings under the names the server uses.
func (s *settings) flagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("lattice", flag.ContinueOnError)
	fs.StringVar(&s.serverName, "server-name", "localhost", "")
	fs.BoolVar(&s.federation, "federation", false, "")
	fs.DurationVar(&s.tokenTTL, "token-ttl", 24*time.Hour, "")
	fs.IntVar(&s.writeBurst, "rate-limit-write-burst", 100, "")
	fs.BoolVar(&s.version, "version", false, "")
	fs.StringVar(&s.password, "password", "", "")
	fs.BoolVar(&s.admin, "admin", false, "")
	fs.IntVar(&s.limit, "limit", 0, "")
	fs.BoolVar(&s.all, "all", false, "")
	return fs
}

func TestEnvNames(t *testing.T) {
	cases := []struct {
		flagName string
		want     string
	}{
		{flagName: "addr", want: "LATTICE_ADDR"},
		{flagName: "server-name", want: "LATTICE_SERVER_NAME"},
		{flagName: "media-reservation-window", want: "LATTICE_MEDIA_RESERVATION_WINDOW"},
		{flagName: "federation-advertise", want: "LATTICE_FEDERATION_ADVERTISE"},
		{flagName: "rate-limit-write-burst", want: "LATTICE_RATE_LIMIT_WRITE_BURST"},
	}
	for _, tc := range cases {
		t.Run(tc.flagName, func(t *testing.T) {
			if got := config.EnvName(tc.flagName); got != tc.want {
				t.Errorf("EnvName(%q) = %s, want %s", tc.flagName, got, tc.want)
			}
		})
	}
}

func TestApplyEnvDefaults(t *testing.T) {
	cases := []struct {
		name string
		// args is what the command line carried, already past the parser's name.
		args      []string
		env       map[string]string
		want      string
		wantTaken []string
		wantErr   string
	}{
		{
			name: "nothing says anything, so the defaults stand",
			want: "localhost|false|24h0m0s|100|false||false|0|false",
		},
		{
			name:      "a variable reaches the flag it names",
			env:       map[string]string{"LATTICE_SERVER_NAME": "example.org"},
			want:      "example.org|false|24h0m0s|100|false||false|0|false",
			wantTaken: []string{"server-name"},
		},
		{
			name:      "a dash in a flag is an underscore in a variable",
			env:       map[string]string{"LATTICE_RATE_LIMIT_WRITE_BURST": "5"},
			want:      "localhost|false|24h0m0s|5|false||false|0|false",
			wantTaken: []string{"rate-limit-write-burst"},
		},
		{
			name:      "a duration is read as a duration",
			env:       map[string]string{"LATTICE_TOKEN_TTL": "6h30m"},
			want:      "localhost|false|6h30m0s|100|false||false|0|false",
			wantTaken: []string{"token-ttl"},
		},
		{
			name:      "true turns a switch on",
			env:       map[string]string{"LATTICE_FEDERATION": "true"},
			want:      "localhost|true|24h0m0s|100|false||false|0|false",
			wantTaken: []string{"federation"},
		},
		{
			name:      "the command line outranks the environment",
			args:      []string{"-server-name", "typed.example"},
			env:       map[string]string{"LATTICE_SERVER_NAME": "example.org"},
			want:      "typed.example|false|24h0m0s|100|false||false|0|false",
			wantTaken: nil,
		},
		{
			name:      "a variable left blank in a compose file is no answer",
			env:       map[string]string{"LATTICE_SERVER_NAME": "  ", "LATTICE_FEDERATION": ""},
			want:      "localhost|false|24h0m0s|100|false||false|0|false",
			wantTaken: nil,
		},
		{
			name:      "a variable naming no flag of this server is left alone",
			env:       map[string]string{"LATTICE_NOT_A_FLAG": "anything", "LATTICE_TEST_LOGS": "1"},
			want:      "localhost|false|24h0m0s|100|false||false|0|false",
			wantTaken: nil,
		},
		{
			name:      "a switch is not read from the environment",
			env:       map[string]string{"LATTICE_VERSION": "true"},
			want:      "localhost|false|24h0m0s|100|false||false|0|false",
			wantTaken: nil,
		},
		{
			// The operator commands take -password and -admin. A LATTICE_PASSWORD left in a
			// container's environment would otherwise become the password of whichever
			// account is added next, and a LATTICE_ADMIN would quietly make it an
			// administrator: deployment settings and one command's arguments are not the
			// same kind of thing.
			name: "a credential is not read from the environment",
			env: map[string]string{
				"LATTICE_PASSWORD": "left-over", "LATTICE_ADMIN": "true",
				"LATTICE_LIMIT": "5000", "LATTICE_ALL": "true",
			},
			want:      "localhost|false|24h0m0s|100|false||false|0|false",
			wantTaken: nil,
		},
		{
			name:    "a value the flag cannot hold is refused by name",
			env:     map[string]string{"LATTICE_FEDERATION": "maybe"},
			want:    "localhost|false|24h0m0s|100|false||false|0|false",
			wantErr: "LATTICE_FEDERATION",
		},
		{
			// The flag package leaves a numeric or duration value it failed to parse at
			// zero, so a mistyped variable is a setting quietly turned into nothing. That
			// is why the error is fatal in main rather than logged and carried on.
			name:    "every bad variable is named, not just the first",
			env:     map[string]string{"LATTICE_FEDERATION": "maybe", "LATTICE_TOKEN_TTL": "six hours"},
			want:    "localhost|false|0s|100|false||false|0|false",
			wantErr: "LATTICE_TOKEN_TTL",
		},
		{
			name: "several variables and one flag given by hand",
			args: []string{"-federation"},
			env: map[string]string{
				"LATTICE_SERVER_NAME":             "example.org",
				"LATTICE_RATE_LIMIT_WRITE_BURST":  "250",
				"LATTICE_RATE_LIMIT_WRITE_BURSTX": "ignored",
			},
			want:      "example.org|true|24h0m0s|250|false||false|0|false",
			wantTaken: []string{"rate-limit-write-burst", "server-name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got settings
			fs := got.flagSet()
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parsing %v: %v", tc.args, err)
			}

			lookup := func(name string) (string, bool) {
				value, found := tc.env[name]
				return value, found
			}
			taken, err := config.ApplyEnvDefaults(fs, lookup)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error naming %s, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not name %s", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.snapshot() != tc.want {
				t.Errorf("settings = %s, want %s", got.snapshot(), tc.want)
			}
			slices.Sort(taken)
			if !slices.Equal(taken, tc.wantTaken) {
				t.Errorf("taken from the environment = %v, want %v", taken, tc.wantTaken)
			}
		})
	}
}
