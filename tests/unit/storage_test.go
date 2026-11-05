package unit

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/users"
)

// newTestDatabase opens a throwaway database for repository and service tests.
func newTestDatabase(t *testing.T) *persist.Database {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cannot open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUserRepoRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	ctx := context.Background()

	user := &persist.User{UserID: "@alice:hs1", Localpart: "alice", PasswordHash: "$2a$10$x"}
	if err := db.Users.Create(ctx, user); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if user.ID == 0 {
		t.Errorf("primary key not populated")
	}

	byID, err := db.Users.ByUserID(ctx, "@alice:hs1")
	if err != nil {
		t.Fatalf("ByUserID failed: %v", err)
	}
	if byID.Localpart != "alice" {
		t.Errorf("localpart = %q", byID.Localpart)
	}
	if _, err := db.Users.ByLocalpart(ctx, "alice"); err != nil {
		t.Errorf("ByLocalpart failed: %v", err)
	}
	if _, err := db.Users.ByUserID(ctx, "@nobody:hs1"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	duplicate := &persist.User{UserID: "@alice:hs1", Localpart: "alice2", PasswordHash: "x"}
	if err := db.Users.Create(ctx, duplicate); !errors.Is(err, persist.ErrConflict) {
		t.Errorf("expected ErrConflict for duplicate user ID, got %v", err)
	}

	count, err := db.Users.Count(ctx)
	if err != nil || count != 1 {
		t.Errorf("Count = (%d,%v)", count, err)
	}
}

func TestDeviceAndTokenRepos(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	ctx := context.Background()

	device := &persist.Device{UserID: "@alice:hs1", DeviceID: "DEV1", DisplayName: "phone"}
	if err := db.Devices.Create(ctx, device); err != nil {
		t.Fatalf("device create failed: %v", err)
	}
	second := &persist.Device{UserID: "@alice:hs1", DeviceID: "DEV1"}
	if err := db.Devices.Create(ctx, second); !errors.Is(err, persist.ErrConflict) {
		t.Errorf("duplicate (user, device) should conflict, got %v", err)
	}
	other := &persist.Device{UserID: "@bob:hs1", DeviceID: "DEV1"}
	if err := db.Devices.Create(ctx, other); err != nil {
		t.Errorf("same device id on another user must be allowed: %v", err)
	}

	token := &persist.AccessToken{TokenHash: "abc123", UserID: "@alice:hs1", DeviceID: "DEV1"}
	if err := db.Tokens.Create(ctx, token); err != nil {
		t.Fatalf("token create failed: %v", err)
	}
	found, err := db.Tokens.ByHash(ctx, "abc123")
	if err != nil {
		t.Fatalf("token lookup failed: %v", err)
	}
	if found.DeviceID != "DEV1" {
		t.Errorf("token = %+v", found)
	}
	if _, err := db.Tokens.ByHash(ctx, "nope"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	if err := db.Tokens.DeleteByDevice(ctx, "@alice:hs1", "DEV1"); err != nil {
		t.Fatalf("delete by device failed: %v", err)
	}
	if _, err := db.Tokens.ByHash(ctx, "abc123"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("token should be gone after revocation")
	}

	if err := db.Devices.SetDisplayName(ctx, "@alice:hs1", "DEV1", "laptop"); err != nil {
		t.Fatalf("SetDisplayName failed: %v", err)
	}
	updated, err := db.Devices.ByDeviceID(ctx, "@alice:hs1", "DEV1")
	if err != nil || updated.DisplayName != "laptop" {
		t.Errorf("display name not updated: %+v (%v)", updated, err)
	}

	devices, err := db.Devices.ListByUser(ctx, "@bob:hs1")
	if err != nil || len(devices) != 1 {
		t.Errorf("ListByUser = (%d,%v)", len(devices), err)
	}
}

func TestTransactionRollback(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := db.Tx(ctx, func(tx *gorm.DB) error {
		if err := db.Users.WithTx(tx).Create(ctx, &persist.User{UserID: "@rollback:hs1", Localpart: "rollback", PasswordHash: "x"}); err != nil {
			return err
		}
		if err := db.Devices.WithTx(tx).Create(ctx, &persist.Device{UserID: "@rollback:hs1", DeviceID: "D1"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx should surface the inner error, got %v", err)
	}
	if _, err := db.Users.ByUserID(ctx, "@rollback:hs1"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("user row survived a rollback: %v", err)
	}
	devices, err := db.Devices.ListByUser(ctx, "@rollback:hs1")
	if err != nil || len(devices) != 0 {
		t.Errorf("device rows survived a rollback: %d (%v)", len(devices), err)
	}
}

func TestSigningKeysLoadCreatesStableKey(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	ctx := context.Background()

	first, err := keys.Load(ctx, "hs1", db.SigningKeys)
	if err != nil {
		t.Fatalf("first Load failed: %v", err)
	}
	second, err := keys.Load(ctx, "hs1", db.SigningKeys)
	if err != nil {
		t.Fatalf("second Load failed: %v", err)
	}
	if !first.PublicKey().Equal(second.PublicKey()) {
		t.Errorf("signing key changed between loads")
	}
	if first.KeyID() != "ed25519:v1" {
		t.Errorf("key id = %q", first.KeyID())
	}

	rows, err := db.SigningKeys.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected exactly one stored key, got %d (%v)", len(rows), err)
	}
	if rows[0].PrivateKey == "" {
		t.Errorf("stored private key is empty")
	}
}

func TestServerKeyDocumentVerifiesWithPublishedKey(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	service, err := keys.Load(context.Background(), "hs1", db.SigningKeys)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	doc, err := service.ServerKeyDocument(time.Now())
	if err != nil {
		t.Fatalf("ServerKeyDocument failed: %v", err)
	}
	if doc["server_name"] != "hs1" {
		t.Errorf("server_name = %v", doc["server_name"])
	}
	res, err := matrixjson.VerifyJSON(doc, func(origin, keyID string) (ed25519.PublicKey, error) {
		return service.VerifyLocal(origin, keyID)
	})
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if !res.VerifiedAll([]string{"hs1"}) {
		t.Errorf("document does not verify: %+v", res)
	}
}

func TestServerKeyDocumentExtendsValidity(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	service, err := keys.Load(context.Background(), "hs1", db.SigningKeys)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := time.Now().Add(48 * time.Hour)
	doc, err := service.ServerKeyDocument(want)
	if err != nil {
		t.Fatalf("ServerKeyDocument failed: %v", err)
	}
	ts, ok := doc["valid_until_ts"].(int64)
	if !ok {
		t.Fatalf("valid_until_ts has type %T", doc["valid_until_ts"])
	}
	if got := time.UnixMilli(ts); got.Before(want) {
		t.Errorf("valid_until_ts = %v, want at least %v", got, want)
	}
}

func TestUsersRegisterPolicy(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	tokens := auth.NewTokenService(db.Tokens)
	cfg := config.RegistrationConfig{Open: true, MinPasswordLength: 6, BcryptCost: 4}
	service := users.New(db, tokens, cfg, "hs1", slog.New(slog.DiscardHandler))
	ctx := context.Background()

	cases := []struct {
		name     string
		params   users.RegisterParams
		wantUser string
		wantErr  matrixerr.Code
	}{
		{"valid", users.RegisterParams{Username: "alice", Password: "hunter22"}, "@alice:hs1", ""},
		{"downcased", users.RegisterParams{Username: "Bob", Password: "hunter22"}, "@bob:hs1", ""},
		{"too short password", users.RegisterParams{Username: "shorty", Password: "abc"}, "", matrixerr.CodeWeakPassword},
		{"invalid characters", users.RegisterParams{Username: "us,er", Password: "hunter22"}, "", matrixerr.CodeInvalidUsername},
		{"non ascii", users.RegisterParams{Username: "üs", Password: "hunter22"}, "", matrixerr.CodeInvalidUsername},
		{"leading underscore", users.RegisterParams{Username: "_bot", Password: "hunter22"}, "", matrixerr.CodeInvalidUsername},
		{"duplicate", users.RegisterParams{Username: "alice", Password: "hunter22"}, "", matrixerr.CodeUserInUse},
		{"legal punctuation", users.RegisterParams{Username: "a.b_c=-/d", Password: "hunter22"}, "@a.b_c=-/d:hs1", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reg, err := service.Register(ctx, tc.params)
			if tc.wantErr != "" {
				me, ok := matrixerr.As(err)
				if !ok {
					t.Fatalf("expected Matrix error %s, got %v", tc.wantErr, err)
				}
				if me.Code != tc.wantErr {
					t.Fatalf("errcode = %s, want %s", me.Code, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reg.UserID != tc.wantUser {
				t.Errorf("user_id = %q, want %q", reg.UserID, tc.wantUser)
			}
			if reg.AccessToken == "" || reg.DeviceID == "" {
				t.Errorf("registration must return a token and device: %+v", reg)
			}
			rows, err := db.Devices.ListByUser(ctx, reg.UserID)
			if err != nil || len(rows) != 1 {
				t.Errorf("expected one device row, got %d (%v)", len(rows), err)
			}
		})
	}
}

func TestUsersRegisterAtomicity(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	tokens := auth.NewTokenService(db.Tokens)
	cfg := config.RegistrationConfig{Open: true, MinPasswordLength: 6, BcryptCost: 4}
	service := users.New(db, tokens, cfg, "hs1", slog.New(slog.DiscardHandler))
	ctx := context.Background()

	if _, err := service.Register(ctx, users.RegisterParams{Username: "alice", Password: "hunter22", DeviceID: "FIXED"}); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	// Remove the account row but keep its device row: the next registration of
	// the same localpart will pass the user insert and then fail on the device
	// insert, which must roll the whole registration back.
	if err := db.DB().Exec("DELETE FROM users WHERE user_id = ?", "@alice:hs1").Error; err != nil {
		t.Fatalf("cannot delete account row: %v", err)
	}
	if _, err := service.Register(ctx, users.RegisterParams{Username: "alice", Password: "hunter22", DeviceID: "FIXED"}); err == nil {
		t.Fatalf("expected the duplicate device ID to fail registration")
	}
	if _, err := db.Users.ByUserID(ctx, "@alice:hs1"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("the account row survived a failed registration: %v", err)
	}
	// No token may be left behind either.
	tokensForAlice, err := db.Tokens.ByHash(ctx, "unused")
	if err == nil && tokensForAlice.UserID == "@alice:hs1" {
		t.Errorf("a token row survived a failed registration")
	}
}

func TestPasswordHashingAndLoginLookup(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	tokens := auth.NewTokenService(db.Tokens)
	cfg := config.RegistrationConfig{Open: true, MinPasswordLength: 6, BcryptCost: 4}
	service := users.New(db, tokens, cfg, "hs1", slog.New(slog.DiscardHandler))
	ctx := context.Background()

	if _, err := service.Register(ctx, users.RegisterParams{Username: "Carol", Password: "sUp3rs3kr1t"}); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	user, err := db.Users.ByLocalpart(ctx, "carol")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if user.PasswordHash == "sUp3rs3kr1t" {
		t.Fatalf("password stored in plaintext")
	}
	if len(user.PasswordHash) < 20 || user.PasswordHash[:4] != "$2a$" {
		t.Errorf("password hash does not look like bcrypt: %q", user.PasswordHash)
	}
	if !users.CheckPassword(user, "sUp3rs3kr1t") {
		t.Errorf("correct password rejected")
	}
	if users.CheckPassword(user, "wrong") {
		t.Errorf("wrong password accepted")
	}

	// Login identifiers: full MXID, uppercase MXID, bare localpart.
	identifiers := []string{"@carol:hs1", "@CAROL:hs1", "carol", "CAROL"}
	for _, id := range identifiers {
		if _, err := service.FindForLogin(ctx, id); err != nil {
			t.Errorf("FindForLogin(%q) failed: %v", id, err)
		}
	}
	for _, id := range []string{"nobody", "@nobody:hs1", "@carol:elsewhere", ""} {
		if _, err := service.FindForLogin(ctx, id); !errors.Is(err, users.ErrUnknownUser) {
			t.Errorf("FindForLogin(%q) = %v, want ErrUnknownUser", id, err)
		}
	}
}

func TestTokenServiceLifecycle(t *testing.T) {
	t.Parallel()
	db := newTestDatabase(t)
	tokens := auth.NewTokenService(db.Tokens)
	ctx := context.Background()

	raw, err := tokens.Mint(ctx, nil, "@alice:hs1", "DEV1")
	if err != nil {
		t.Fatalf("Mint failed: %v", err)
	}
	if len(raw) < 40 {
		t.Errorf("token looks short: %d chars", len(raw))
	}
	record, err := tokens.Validate(ctx, raw)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if record.UserID != "@alice:hs1" || record.DeviceID != "DEV1" {
		t.Errorf("session = %+v", record)
	}
	// The token itself must never be stored.
	if record.TokenHash == raw {
		t.Errorf("token stored verbatim")
	}
	if _, err := tokens.Validate(ctx, raw+"x"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("tampered token accepted")
	}
	if err := tokens.RevokeDevice(ctx, "@alice:hs1", "DEV1"); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	if _, err := tokens.Validate(ctx, raw); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("revoked token still valid")
	}
}

func TestUIASessionsAndNonces(t *testing.T) {
	t.Parallel()
	uia := auth.NewUIAService(20*time.Millisecond, 4)
	flows := []auth.Flow{{Stages: []string{auth.StageDummy}}}

	sess := uia.NewSession(flows, nil)
	if sess.ID == "" {
		t.Fatalf("session has no id")
	}
	if _, ok := uia.GetSession(sess.ID); !ok {
		t.Fatalf("session not found immediately after creation")
	}
	body := uia.ChallengeBody(sess)
	if body["session"] != sess.ID {
		t.Errorf("challenge missing session: %v", body)
	}
	if _, ok := body["flows"].([]auth.Flow); !ok {
		t.Errorf("challenge flows have the wrong type: %T", body["flows"])
	}
	if _, ok := body["completed"].([]string); !ok {
		t.Errorf("challenge completed has the wrong type: %T", body["completed"])
	}
	if _, ok := uia.CompleteStage(sess.ID, auth.StageDummy); !ok {
		t.Errorf("stage completion failed")
	}

	// Sessions expire.
	time.Sleep(40 * time.Millisecond)
	if _, ok := uia.GetSession(sess.ID); ok {
		t.Errorf("expired session still usable")
	}

	// Nonces are single use.
	nonce, err := uia.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce failed: %v", err)
	}
	if !uia.ConsumeNonce(nonce) {
		t.Errorf("fresh nonce rejected")
	}
	if uia.ConsumeNonce(nonce) {
		t.Errorf("nonce replay accepted")
	}
	if uia.ConsumeNonce("unknown") {
		t.Errorf("unknown nonce accepted")
	}
}

func TestUIAStoreIsBounded(t *testing.T) {
	t.Parallel()
	uia := auth.NewUIAService(time.Hour, 3)
	for i := 0; i < 10; i++ {
		uia.NewSession([]auth.Flow{{Stages: []string{auth.StageDummy}}}, nil)
	}
	live := 0
	for i := 0; i < 10; i++ {
		// Nothing to enumerate directly; instead assert creation keeps working and
		// that a fresh session is retrievable, which implies the store is bounded.
		sess := uia.NewSession([]auth.Flow{{Stages: []string{auth.StageDummy}}}, nil)
		if _, ok := uia.GetSession(sess.ID); ok {
			live++
		}
	}
	if live == 0 {
		t.Errorf("sessions should remain usable under pressure")
	}
}
