// Package userdir answers "who may this person be told about, and what do they go by".
//
// The Matrix user directory is a small endpoint with a rule inside it. The specification gives the
// minimum set a server MUST consider — users who share a room with the searcher, users joined to
// rooms whose join rule is `public`, and users joined to rooms whose history visibility is
// `world_readable` — and the interesting part is what that rule refuses. A display name in Matrix is
// not only a profile: it is also written into each room's member event, so Alice can be "Alice
// Cooper" to the world and "Freddy" to one private room. Those are two facts, and the second one
// belongs to that room. A directory that indexed both and showed both would tell a stranger what
// Alice chose to reveal only to Bob, which is the bug this package exists not to have.
//
// So the search space is computed per searcher, from the state this server already holds: the names
// a searcher may be matched on are their profile names plus the names written in rooms they can see,
// and the name they are answered with is the profile one — the specification's own response shape
// asks for "the display name of the user", not for whatever name the match was made on.
//
// Nothing here is maintained. Room state is the only answer this server gives to "who is in which
// room", and a directory table beside it would have to be kept in step with every join, leave, kick,
// profile change and room this server has never heard of. The cost is one state read per search, and
// it is the honest way to be wrong less often.
package userdir

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/enimatek/lattice/internal/persist"
)

// defaultLimit is what a search without a limit returns. The specification names the number.
const defaultLimit = 10

// maxLimit bounds what one search may ask for. The specification sets no ceiling, and a search this
// server answers by reading state should not be able to ask for the whole account list; an operator
// who wants more can page through with narrower terms. This is our decision, not a rule.
const maxLimit = 100

// Entry is one line of a search result: whom the searcher may be told about, and what the server
// states about them. DisplayName and AvatarURL are the profile, which may be absent — a user from
// another homeserver in a room this server can see is a person the searcher may learn about, and
// this server has no profile for them to state.
type Entry struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// Page is the answer to a search: the results, and whether there were more of them.
type Page struct {
	Results []Entry `json:"results"`
	Limited bool    `json:"limited"`
}

// Service answers user directory searches.
type Service struct {
	dir   persist.UserDirectoryRepo
	users profiles
}

// profiles is the part of the account store a directory search reads. It is declared here rather
// than taken as [persist.UserRepo] because a search performs exactly one read of accounts, and a
// test should be able to hand over that one read instead of an account store.
type profiles interface {
	ByUserIDs(ctx context.Context, userIDs []string) (map[string]*persist.User, error)
}

// New returns a user directory service over db.
func New(db *persist.Database) *Service { return NewWith(db.UserDirectory, db.Users) }

// NewWith returns the same service over its two reads, which is how a test supplies them without a
// database: the rules in this package are the thing worth testing on their own, and they are the
// rules about who may be matched on which name.
func NewWith(dir persist.UserDirectoryRepo, accounts profiles) *Service {
	return &Service{dir: dir, users: accounts}
}

// Search looks for people the searcher may be told about whose user ID or name matches term.
//
// The two lists it builds are the whole design. `shared` is the rooms the searcher is in, where a
// member event's room-specific name may be used to match. `visible` is those rooms plus the open
// ones, whose members may be named to anybody. A person known only from an open room is therefore
// findable by their profile name and their user ID and not by the name they wrote in a room the
// searcher is not in — which is the rule, said once, in which rooms each name is allowed to count.
func (s *Service) Search(ctx context.Context, searcherID, term string, limit int) (Page, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	termTokens := tokenise(term)
	page := Page{Results: []Entry{}}
	if len(termTokens) == 0 {
		// A term with no letters or digits in it asks for nobody in particular. Returning everybody
		// would be the surprising answer, and the expensive one.
		return page, nil
	}

	shared, err := s.dir.JoinedRoomIDs(ctx, searcherID)
	if err != nil {
		return Page{}, err
	}
	open, err := s.dir.OpenRoomIDs(ctx)
	if err != nil {
		return Page{}, err
	}
	sharedHere := make(map[string]bool, len(shared))
	for _, roomID := range shared {
		sharedHere[roomID] = true
	}
	visible := make([]string, 0, len(shared)+len(open))
	seenRoom := make(map[string]bool, len(shared)+len(open))
	for _, roomID := range append(append([]string{}, shared...), open...) {
		if seenRoom[roomID] {
			continue
		}
		seenRoom[roomID] = true
		visible = append(visible, roomID)
	}
	if len(visible) == 0 {
		// A searcher who is in no room and to whose server no open room belongs cannot be told about
		// anybody. This is the specification's minimum, not a shortcut: the set it allows is empty.
		return page, nil
	}

	members, err := s.dir.JoinedNamesIn(ctx, visible)
	if err != nil {
		return Page{}, err
	}

	// Group the member rows by person: whom they are in which rooms, and under which names in the
	// rooms the searcher shares.
	type candidate struct {
		userID     string
		shared     bool // in a room the searcher is in
		sharedName []string
	}
	candidates := make(map[string]*candidate)
	for _, member := range members {
		if member.UserID == searcherID {
			// The searcher is not a person they are looking for. Without this, a term that matches
			// their own user ID answers with themselves and pushes someone else off the page.
			continue
		}
		entry, ok := candidates[member.UserID]
		if !ok {
			entry = &candidate{userID: member.UserID}
			candidates[member.UserID] = entry
		}
		if sharedHere[member.RoomID] {
			entry.shared = true
			if member.DisplayName != "" {
				entry.sharedName = append(entry.sharedName, member.DisplayName)
			}
		}
	}
	if len(candidates) == 0 {
		return page, nil
	}

	ids := make([]string, 0, len(candidates))
	for userID := range candidates {
		ids = append(ids, userID)
	}
	profiles, err := s.users.ByUserIDs(ctx, ids)
	if err != nil {
		return Page{}, err
	}

	type ranked struct {
		entry      Entry
		score      int
		hasProfile bool
	}
	matches := make([]ranked, 0, len(candidates))
	for userID, candidate := range candidates {
		profile := profiles[userID]
		if profile != nil && profile.Deactivated {
			// An account that has been closed is not a person to be found. Its member events stay
			// where they are — history keeps the names it was written under — but they are no longer
			// a way into a directory.
			continue
		}
		displayName, avatarURL := "", ""
		var profileTokens []string
		if profile != nil {
			displayName, avatarURL = profile.Displayname, profile.AvatarURL
			profileTokens = tokenise(displayName)
		}
		// The user ID is searchable because the specification says the search is performed on user
		// IDs and display names. The full ID is used, so a searcher on another server can narrow to
		// that server by typing its name.
		idTokens := tokenise(userID)
		score := 0
		if best, ok := matchTokens(termTokens, profileTokens); ok {
			score = max(score, best+weightProfileName)
		}
		if best, ok := matchTokens(termTokens, idTokens); ok {
			score = max(score, best+weightUserID)
		}
		for _, name := range candidate.sharedName {
			if best, ok := matchTokens(termTokens, tokenise(name)); ok {
				score = max(score, best+weightRoomName)
			}
		}
		if score == 0 {
			continue
		}
		matches = append(matches, ranked{
			entry:      Entry{UserID: userID, DisplayName: displayName, AvatarURL: avatarURL},
			score:      score,
			hasProfile: profile != nil,
		})
	}

	// Rank, then whether profile information is available — the order the specification gives — and
	// then by user ID so that two searches of an unchanged server answer identically.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].hasProfile != matches[j].hasProfile {
			return matches[i].hasProfile
		}
		return matches[i].entry.UserID < matches[j].entry.UserID
	})

	if len(matches) > limit {
		page.Limited = true
		matches = matches[:limit]
	}
	page.Results = make([]Entry, 0, len(matches))
	for _, match := range matches {
		page.Results = append(page.Results, match.entry)
	}
	return page, nil
}

// The weights say which name a match is best explained by, and they are ordered so that a person's
// own profile beats a name they wrote in one room, and a user ID beats that too: what a searcher is
// most likely to be looking for is the name a person goes by everywhere.
const (
	weightProfileName = 30
	weightUserID      = 20
	weightRoomName    = 10
	// exactBonus is added when a term's word is the whole word rather than the start of it, so
	// "Alice Cooper" ranks above "Alice Cooperman" for the term "alice cooper".
	exactBonus = 5
)

// matchTokens asks whether every term word is the start of some word in the field, and how well the
// best such pairing went. All of the words have to land, which is what makes "freddy cooper" match
// nobody while "alice cooper" matches "Alice Cooper" — and, since fields are tokenised the same way,
// makes "@user" a search for the word `user` in a user ID rather than for a string that happens to
// start with `@user`: Matrix user IDs are made of words separated by characters no user ID can carry
// twice, and a search that treated the whole term as one string could not find anyone by an MXID.
//
// Prefix matching on words is what Synapse's directory does, and it is the behaviour clients have:
// typing "ali" finds Alice and typing "coop" finds her too.
func matchTokens(termTokens, fieldTokens []string) (int, bool) {
	if len(termTokens) == 0 || len(fieldTokens) == 0 {
		return 0, false
	}
	best := 0
	for _, term := range termTokens {
		matched := false
		for _, field := range fieldTokens {
			if !strings.HasPrefix(field, term) {
				continue
			}
			matched = true
			if field == term {
				best += exactBonus
				break
			}
		}
		if !matched {
			return 0, false
		}
	}
	return best, true
}

// tokenise lower-cases a string and splits it into the words a person would type. Everything that is
// not a letter or a digit separates: an MXID, a display name, an email address inside a name and a
// term with punctuation in it all become the same kind of thing to match against.
//
// The specification asks for case-insensitive matching "preferably using a collation determined
// based upon the Accept-Language header". Unicode case-folding is done here; locale collation is
// not, because it would be a dependency and because the difference shows up in accents that sort
// differently in two languages, not in whether a search finds a person at all. Recorded as a
// known deviation rather than left as an silence.
func tokenise(value string) []string {
	if value == "" {
		return nil
	}
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		// Keep letters and digits, including the accented ones a display name carries; split on the
		// punctuation that separates words, and on the `@` and `:` that frame a user ID.
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
}
