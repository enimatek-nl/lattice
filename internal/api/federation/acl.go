package federation

import "context"

// RoomACLs decides whether a room has excluded a homeserver. The room service implements it: a
// room's exclusion of servers is part of the room's state, so the endpoints that answer a room and
// the path that accepts its events have to read the same one entry rather than each keeping a
// notion of who is out.
type RoomACLs interface {
	DeniesServer(ctx context.Context, roomID, serverName string) (bool, error)
}
