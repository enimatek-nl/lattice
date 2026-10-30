package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// validateStateEvent rejects state updates that the state endpoints may not
// carry, and checks the power level rules of m.room.power_levels.
func (s *Service) validateStateEvent(ctx context.Context, roomID, sender, typ, stateKey string, content matrix.RawMessage) error {
	switch typ {
	case matrix.TypeCreate:
		return matrix.ErrForbidden("the m.room.create event cannot be replaced")
	case matrix.TypeMember:
		if stateKey != sender {
			return matrix.ErrForbidden("you cannot change the membership of another user this way")
		}
		return matrix.ErrUnrecognized("membership changes must go through the membership endpoints")
	case matrix.TypePowerLevels:
		return s.validatePowerLevels(ctx, roomID, sender, matrix.MustParseObject(content))
	}
	return nil
}

// validatePowerLevels enforces the power level restrictions of the
// specification: changing the level of a user requires a level that is at
// least as high as both the current and the requested level of that user.
func (s *Service) validatePowerLevels(ctx context.Context, roomID, sender string, content matrix.Object) error {
	requested := intMap(content["users"])
	if len(requested) == 0 {
		return nil
	}
	levels := s.powerLevels(ctx, roomID)
	have := s.PowerLevel(ctx, roomID, sender)
	for userID, want := range requested {
		current, explicit := levels.Users[userID]
		if !explicit {
			current = s.PowerLevel(ctx, roomID, userID)
		}
		if current == want {
			continue
		}
		needed := current
		if want > needed {
			needed = want
		}
		if have < needed {
			return matrix.ErrForbidden(
				"changing the power level of %s requires a power level of %d, you have %d", userID, needed, have)
		}
	}
	return nil
}

// intMap coerces a decoded JSON value into a map of integers.
func intMap(value any) map[string]int64 {
	out := map[string]int64{}
	switch typed := value.(type) {
	case map[string]any:
		for key, raw := range typed {
			switch number := raw.(type) {
			case float64:
				out[key] = int64(number)
			case int64:
				out[key] = number
			case int:
				out[key] = int64(number)
			}
		}
	case map[string]int64:
		for key, number := range typed {
			out[key] = number
		}
	}
	return out
}
