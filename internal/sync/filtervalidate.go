package sync

import "github.com/enimatek/lattice/internal/eventfilter"

// ValidateFilter checks a filter document against the shape the specification gives it. The
// checks live in eventfilter, where the filter's shape and the filter's effect are defined
// together, so the two doors a filter arrives at — stored through POST /user/{user}/filter and
// given inline to /sync — cannot drift apart from the filter this server then applies.
func ValidateFilter(body []byte) error {
	return eventfilter.ValidateDocument(body)
}
