package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/matrixid"
)

// AliasTarget resolves one of this server's own room aliases into the room it names and the servers that
// could take a join of it.
//
// It exists because a room ID on its own does not get a joining server anywhere. The alias is the
// identifier a human types and the one the specification tells a client to hand to another server, and
// only the server that owns the alias can say what it names — so the answer has to carry the doors as
// well as the room: this server's own name, because a room this server publishes an alias for is a room
// this server holds state for, plus the homeservers of the users currently in it, which by definition are
// holding the room too. A joining server treats the list as a set of attempts, so an incomplete list is a
// smaller fault than a refused question.
func (s *Service) AliasTarget(ctx context.Context, alias string) (string, []string, error) {
	roomID, err := s.ResolveAlias(ctx, alias)
	if err != nil {
		return "", nil, err
	}
	servers := []string{s.name}
	members, err := s.state.CurrentMembers(ctx, roomID)
	if err != nil {
		s.log.Warn("rooms: cannot read a room's members to answer an alias query",
			"room_id", roomID, "error", err)
		return roomID, servers, nil
	}
	seen := map[string]bool{s.name: true}
	for _, member := range members {
		domain, err := matrixid.UserDomain(member)
		if err != nil || seen[domain] {
			continue
		}
		seen[domain] = true
		servers = append(servers, domain)
	}
	return roomID, servers, nil
}
