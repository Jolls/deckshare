package auth

import (
	"context"
	"log/slog"
	"time"
)

// Run deletes expired sessions and password reset tokens and sweeps the rate limiters until ctx
// is cancelled.
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
			// Hygiene, not correctness: both reset-token queries already filter on expires_at.
			if _, err := s.q.DeleteExpiredPasswordResetTokens(ctx); err != nil {
				slog.Error("password reset token cleanup failed", "error", err)
			}
			s.loginIP.Sweep()
			s.loginEmail.Sweep()
			s.signupIP.Sweep()
			s.changePassword.Sweep()
			s.resetPassword.Sweep()
		}
	}
}
