package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// QueryDirectory asks another server what one of its room aliases names.
//
// This is the call that turns `#room:example.org` into something joinable. It is a question only the
// owning server can answer, which is why the Client-Server API is allowed to hand an alias belonging to
// somebody else straight through to a client's `/join`: the client's server is expected to ask. Answering
// "not for me" with a 404 would be the trap here, because it is the same answer as "this alias does not
// exist", and a client that has been told the alias does not exist stops trying rather than trying the
// server that owns it.
func (c *Client) QueryDirectory(ctx context.Context, destination, alias string) (string, []string, error) {
	if destination == "" {
		return "", nil, fmt.Errorf("federation: an alias query needs a destination")
	}
	target := "/_matrix/federation/v1/query/directory?room_alias=" + url.QueryEscape(alias)
	response, err := c.Do(ctx, destination, http.MethodGet, target, nil)
	if err != nil {
		return "", nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		return "", nil, matrixerr.Newf(matrixerr.CodeNotFound,
			"%s says it knows no room alias %q.", destination, alias)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", nil, fmt.Errorf("federation: %s answered an alias query with HTTP %d", destination, response.StatusCode)
	}
	var answer struct {
		RoomID  string   `json:"room_id"`
		Servers []string `json:"servers"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return "", nil, fmt.Errorf("federation: %s answered an alias query with something unreadable: %w", destination, err)
	}
	if answer.RoomID == "" {
		// Legal per the specification — an alias can exist and name no room — and useless to the
		// caller, so it is reported as the dead end it is rather than returned as an empty room ID.
		return "", nil, matrixerr.Newf(matrixerr.CodeNotFound,
			"%s says the alias %q exists but names no room.", destination, alias)
	}
	return answer.RoomID, answer.Servers, nil
}
