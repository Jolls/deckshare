package auth

import (
	"context"
	"log/slog"
	"time"
)

// Run deletes expired sessions and sweeps the rate limiters until ctx is cancelled.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.q.DeleteExpiredSessions(ctx); err != nil {
				slog.Error("session cleanup failed", "error", err)
			}
			s.loginIP.Sweep()
			s.loginEmail.Sweep()
			s.signupIP.Sweep()
			s.changePassword.Sweep()
		}
	}
}
