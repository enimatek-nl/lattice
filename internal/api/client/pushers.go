package client

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/pushers"
)

// pusherSetRequest is the body of POST /pushers/set.
//
// `Kind` and `Data` are kept as raw JSON on purpose. A client that writes `"kind": null` is
// asking for a pusher to be removed, and a client that omits `kind` has asked a question this
// server cannot answer; unmarshalling either into a string would make them the same request.
// The same holds for `data`: an http pusher that carries none is a refusal, while an absent
// `data` on a removal is nothing at all.
type pusherSetRequest struct {
	Pushkey         string          `json:"pushkey"`
	Kind            json.RawMessage `json:"kind"`
	AppID           string          `json:"app_id"`
	AppDisplayName  string          `json:"app_display_name"`
	DeviceDisplayNm string          `json:"device_display_name"`
	ProfileTag      string          `json:"profile_tag"`
	Lang            string          `json:"lang"`
	Data            json.RawMessage `json:"data"`
}

// handlePushersSet implements POST /pushers/set: a device saying where to wake it.
func (d *Deps) handlePushersSet(w http.ResponseWriter, r *http.Request) {
	var req pusherSetRequest
	if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &req) {
		return
	}
	set := pushers.SetRequest{
		UserID:         auth.UserID(r),
		TokenHash:      auth.TokenHash(r),
		Pushkey:        req.Pushkey,
		AppID:          req.AppID,
		AppDisplayName: req.AppDisplayName,
		DeviceDisplay:  req.DeviceDisplayNm,
		ProfileTag:     req.ProfileTag,
		Lang:           req.Lang,
	}
	switch {
	case len(req.Kind) == 0:
		// Absent: the service refuses it, because guessing between "add" and "remove" is the
		// client's decision to make.
	case string(req.Kind) == "null":
		// The specification's removal request. The service treats an empty kind as that.
		empty := ""
		set.Kind = &empty
	default:
		var kind string
		if err := json.Unmarshal(req.Kind, &kind); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
				`"kind" is "http" or null.`))
			return
		}
		set.Kind = &kind
	}
	if len(req.Data) > 0 {
		set.HasData = true
		if err := json.Unmarshal(req.Data, &set.Data); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
				`"data" is an object.`))
			return
		}
	}
	if err := d.Pushers.Set(r.Context(), set); err != nil {
		d.pusherError(w, err)
		return
	}
	// The specification's answer is an empty object: the pusher was accepted, and there is
	// nothing this server could usefully tell the client about a gateway it does not call yet.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePushersList implements GET /pushers: what this user's devices have asked for.
func (d *Deps) handlePushersList(w http.ResponseWriter, r *http.Request) {
	list, err := d.Pushers.List(r.Context(), auth.UserID(r))
	if err != nil {
		d.serviceError(w, err)
		return
	}
	pushersOut := make([]map[string]any, 0, len(list))
	for _, pusher := range list {
		var data map[string]any
		if pusher.Data != "" {
			if err := json.Unmarshal([]byte(pusher.Data), &data); err != nil {
				// The row was written by this server from JSON it held as text, so it cannot
				// arrive unreadable unless the store has been edited underneath this process.
				// It is reported rather than answered as a pusher with no data, because a
				// client would then be told to set a pusher it has already set.
				d.writeInternal(w, "pusher data unreadable", err)
				return
			}
		}
		entry := map[string]any{
			"pushkey":             pusher.Pushkey,
			"kind":                pusher.Kind,
			"app_id":              pusher.AppID,
			"device_display_name": pusher.DeviceDisplayName,
			"data":                data,
		}
		if pusher.AppDisplayName != "" {
			entry["app_display_name"] = pusher.AppDisplayName
		}
		if pusher.ProfileTag != "" {
			entry["profile_tag"] = pusher.ProfileTag
		}
		if pusher.Lang != "" {
			entry["lang"] = pusher.Lang
		}
		pushersOut = append(pushersOut, entry)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"pushers": pushersOut})
}

// pusherError answers the refusals a pusher request can earn, each in the room's own words.
func (d *Deps) pusherError(w http.ResponseWriter, err error) {
	if errors.Is(err, pushers.ErrEmailKind) {
		// The specification gives email pushers their own error code, because a client that has
		// one has to know it will never be delivered rather than retry it.
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidEmailParam,
			"This server does not deliver notifications by email."))
		return
	}
	if errors.Is(err, pushers.ErrNoPushkey) || errors.Is(err, pushers.ErrNoAppID) ||
		errors.Is(err, pushers.ErrNoDevice) || errors.Is(err, pushers.ErrNoKind) ||
		errors.Is(err, pushers.ErrUnknownKind) || errors.Is(err, pushers.ErrNoURL) ||
		errors.Is(err, pushers.ErrBadURL) {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeInvalidParam,
			pusherWords(err)))
		return
	}
	d.serviceError(w, err)
}

// pusherWords says each refusal out loud in a sentence a client can act on.
func pusherWords(err error) string {
	switch {
	case errors.Is(err, pushers.ErrNoPushkey):
		return "A push key is required."
	case errors.Is(err, pushers.ErrNoAppID):
		return "An app ID is required."
	case errors.Is(err, pushers.ErrNoDevice):
		return "A device display name is required."
	case errors.Is(err, pushers.ErrNoKind):
		return `"kind" is required: "http", or null to remove a pusher.`
	case errors.Is(err, pushers.ErrUnknownKind):
		return "The only pusher kind this server delivers is http."
	case errors.Is(err, pushers.ErrNoURL):
		return "An http pusher needs a data.url to notify."
	case errors.Is(err, pushers.ErrBadURL):
		return "data.url must be an https URL this server can reach."
	}
	return "The pusher was refused."
}
