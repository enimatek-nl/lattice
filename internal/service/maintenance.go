package service

import (
	"context"
	"log"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// Retention windows of the short-lived data the specification gives a lifetime
// to. The client-server spec asks servers to keep to-device messages and
// remembered transaction ids for at least a day, so that is what Lattice keeps.
const (
	ToDeviceLifetime  = 24 * time.Hour
	TxnLifetime       = 24 * time.Hour
	TypingGracePeriod = 30 * time.Second
	// KeyRetention is how long one-time keys and device list changes are kept.
	// Claimed keys have to stay listed long enough that a replayed claim cannot
	// hand the same key out twice, and device list changes must outlive the
	// longest reasonable gap between two syncs.
	KeyRetention = 30 * 24 * time.Hour
	// MaintenancePeriod is how often the housekeeping loop runs by default.
	MaintenancePeriod = time.Minute
)

// MaintenanceReport counts what one housekeeping pass removed.
type MaintenanceReport struct {
	ToDeviceRemoved      int64 `json:"to_device_removed"`
	TxnsRemoved          int64 `json:"transactions_removed"`
	TypingRemoved        int64 `json:"typing_removed"`
	AuthSessionsDropped  int64 `json:"auth_sessions_dropped"`
	KeysRemoved          int64 `json:"one_time_keys_removed"`
	DeviceChangesRemoved int64 `json:"device_list_changes_removed"`
}

// empty reports whether the pass had nothing to do.
func (r MaintenanceReport) empty() bool {
	return r.ToDeviceRemoved == 0 && r.TxnsRemoved == 0 && r.TypingRemoved == 0 &&
		r.AuthSessionsDropped == 0 && r.KeysRemoved == 0 && r.DeviceChangesRemoved == 0
}

// RunMaintenance prunes expired to-device messages, forgotten transaction ids
// and stale typing indicators. Typing rows are the tricky ones: a client that
// disappears without saying goodbye leaves a row behind, and sync keeps
// re-sending it to anybody whose sync token predates it.
func (s *Service) RunMaintenance(ctx context.Context) (MaintenanceReport, error) {
	var report MaintenanceReport
	if err := ctx.Err(); err != nil {
		return report, matrix.ErrInternal(err)
	}
	now := s.NowMS()

	expired, err := s.db.ToDeviceRepo.Expire(now - ToDeviceLifetime.Milliseconds())
	if err != nil {
		return report, matrix.ErrInternal(err)
	}
	report.ToDeviceRemoved = expired

	expired, err = s.db.TxnRepo.Expire(now - TxnLifetime.Milliseconds())
	if err != nil {
		return report, matrix.ErrInternal(err)
	}
	report.TxnsRemoved = expired

	expired, err = s.db.TypingRepo.Prune(now, TypingGracePeriod.Milliseconds())
	if err != nil {
		return report, matrix.ErrInternal(err)
	}
	report.TypingRemoved = expired

	report.AuthSessionsDropped = s.expireAuthSessions(now)

	expired, err = s.db.OneTimeKeyRepo.Expire(now - KeyRetention.Milliseconds())
	if err != nil {
		return report, matrix.ErrInternal(err)
	}
	report.KeysRemoved = expired

	expired, err = s.db.DeviceListRepo.Expire(now - KeyRetention.Milliseconds())
	if err != nil {
		return report, matrix.ErrInternal(err)
	}
	report.DeviceChangesRemoved = expired

	return report, nil
}

// StartMaintenance runs RunMaintenance on a ticker until the context is
// cancelled or the returned stop function is called. The stop function waits
// for the loop to finish, which keeps the process free of stray goroutines.
func (s *Service) StartMaintenance(ctx context.Context, period time.Duration) func() {
	if period <= 0 {
		// A non-positive interval switches housekeeping off.
		return func() {}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				report, err := s.RunMaintenance(ctx)
				if err != nil {
					log.Printf("maintenance: %v", err)
					continue
				}
				if !report.empty() {
					log.Printf("maintenance: dropped %d to-device messages, %d transactions, %d typing rows, %d keys, %d device list changes",
						report.ToDeviceRemoved, report.TxnsRemoved, report.TypingRemoved,
						report.KeysRemoved, report.DeviceChangesRemoved)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
