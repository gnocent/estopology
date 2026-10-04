package store

import (
	"context"
	"testing"
	"time"

	"estopo/internal/clock"
)

// Expired sessions and old audit entries are removed; the rest stays.
func TestPruneSessionsAndAudit(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(t0.Add(-400 * 24 * time.Hour))
	st, err := Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Audit(ctx, "admin", "old.action", "", "")
	clk.Set(t0)
	now := t0.UnixMilli()
	st.CreateSession(ctx, Session{TokenHash: "old", User: "a", CSRF: "x", CreatedMs: now - 10, ExpiresMs: now - 1})
	st.CreateSession(ctx, Session{TokenHash: "live", User: "a", CSRF: "x", CreatedMs: now - 10, ExpiresMs: now + 1})
	if err := st.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(ctx, "old"); err == nil {
		t.Error("expired session kept")
	}
	if _, err := st.SessionByHash(ctx, "live"); err != nil {
		t.Errorf("live session removed: %v", err)
	}

	st.Audit(ctx, "admin", "recent.action", "", "")
	n, err := st.PruneAudit(ctx, t0.Add(-365*24*time.Hour).UnixMilli())
	if err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if log, _ := st.AuditLog(ctx, 10); len(log) != 1 || log[0].Action != "recent.action" {
		t.Errorf("audit after prune: %+v", log)
	}
}
