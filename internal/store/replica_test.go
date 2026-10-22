package store

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
)

// The replicated tables go through; the local ones (sessions, partition
// catalogue, HA state, alerts) do not; LDAP accounts created locally stay.
func TestMetaReplication(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(t0)
	primary, err := Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	standby, err := Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer standby.Close()

	primary.SaveCluster(ctx, Cluster{ID: "c1", Name: "One", Enabled: true, MachineAttr: "machine"})
	primary.SetSetting(ctx, "retention", Retention{MaxAgeDays: 90})
	primary.SetFamilyRules(ctx, "c1", []string{`^(logs)-`})
	primary.SaveUser(ctx, User{Name: "admin", Role: "admin", Source: "local", Password: "$argon2id$x"})
	primary.AddAck(ctx, Ack{Cluster: "c1", Node: "node-1", FromMs: t0.UnixMilli(), Comment: "retired", Author: "admin"})
	primary.Audit(ctx, "admin", "cluster.save", "c1", "")
	primary.CreateSession(ctx, Session{TokenHash: "p", User: "admin", CSRF: "x", CreatedMs: 1, ExpiresMs: 2})
	primary.SetHAState(ctx, map[string]string{"role": "primary"})

	standby.SaveCluster(ctx, Cluster{ID: "stale", Name: "Stale"})
	standby.SaveUser(ctx, User{Name: "old-local", Role: "admin", Source: "local"})
	standby.SaveUser(ctx, User{Name: "ldap-bob", Role: "reader", Source: "ldap"})
	standby.CreateSession(ctx, Session{TokenHash: "s", User: "ldap-bob", CSRF: "y", CreatedMs: 1, ExpiresMs: 2})
	standby.SetHAState(ctx, map[string]string{"role": "standby", "standby_of": "https://p"})

	snap, err := primary.ExportMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), `"token_hash"`) {
		t.Error("sessions are exported")
	}
	got, err := DecodeMetaSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := standby.ImportMeta(ctx, got); err != nil {
		t.Fatal(err)
	}

	cs, _ := standby.Clusters(ctx, true)
	if len(cs) != 1 || cs[0].ID != "c1" || cs[0].MachineAttr != "machine" {
		t.Errorf("clusters: %+v", cs)
	}
	var ret Retention
	standby.GetSetting(ctx, "retention", &ret)
	if ret.MaxAgeDays != 90 {
		t.Errorf("retention: %+v", ret)
	}
	if rules, _ := standby.FamilyRules(ctx, "c1"); len(rules) != 1 {
		t.Errorf("family rules: %v", rules)
	}
	if acks, _ := standby.Acks(ctx, "c1"); len(acks) != 1 || acks[0].FromMs != t0.UnixMilli() {
		t.Errorf("acks: %+v", acks)
	}
	users, _ := standby.Users(ctx)
	names := []string{}
	for _, u := range users {
		names = append(names, u.Name)
	}
	if strings.Join(names, ",") != "admin,ldap-bob" {
		t.Errorf("users: %v", names)
	}
	if u, _ := standby.User(ctx, "admin"); u.Password != "$argon2id$x" {
		t.Errorf("password hash not replicated: %+v", u)
	}
	if _, err := standby.SessionByHash(ctx, "s"); err != nil {
		t.Errorf("local session dropped: %v", err)
	}
	if _, err := standby.SessionByHash(ctx, "p"); err == nil {
		t.Error("primary session imported")
	}
	if ha, _ := standby.HAState(ctx); ha["role"] != "standby" {
		t.Errorf("HA state overwritten: %v", ha)
	}
	if log, _ := standby.AuditLog(ctx, 10); len(log) != 1 || log[0].Action != "cluster.save" {
		t.Errorf("audit: %+v", log)
	}

	// Same export twice: same bytes (the ETag only changes with the content).
	again, _ := primary.ExportMeta(ctx)
	if b2, _ := json.Marshal(again); string(b2) != string(b) {
		t.Error("export is not deterministic")
	}
	bad := got
	bad.Schema++
	if err := standby.ImportMeta(ctx, bad); err == nil {
		t.Error("schema mismatch accepted")
	}
	got.Tables["setting"] = append(got.Tables["setting"], map[string]any{"key": "x", "value": "1", "evil); DROP TABLE user; --": 1})
	if err := standby.ImportMeta(ctx, got); err == nil {
		t.Error("unknown column accepted")
	}
	if users, _ := standby.Users(ctx); len(users) != 2 {
		t.Errorf("failed import changed the users: %+v", users)
	}
}

// A partition copied with SnapshotPartition and installed elsewhere gives
// the same content, and the ingestion goes on from it.
func TestPartitionCopy(t *testing.T) {
	ctx := context.Background()
	e := newTEnv(t)
	e.full(0)
	e.probe(5 * time.Minute)
	e.full(10 * time.Minute)

	parts, _ := e.st.Partitions(ctx, "small")
	if len(parts) != 1 {
		t.Fatalf("partitions: %+v", parts)
	}
	tmp := filepath.Join(t.TempDir(), "copy.db")
	if err := e.st.SnapshotPartition(ctx, parts[0], tmp); err != nil {
		t.Fatal(err)
	}

	dst, err := Open(ctx, t.TempDir(), clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.InstallPartition(ctx, "other", parts[0].Week, parts[0].FromMs, tmp, false); err == nil {
		t.Error("partition installed under another cluster")
	}
	if err := e.st.SnapshotPartition(ctx, parts[0], tmp); err != nil {
		t.Fatal(err)
	}
	if err := dst.InstallPartition(ctx, "small", parts[0].Week, parts[0].FromMs, tmp, true); err != nil {
		t.Fatal(err)
	}
	got, _ := dst.Partitions(ctx, "small")
	if len(got) != 1 || !got[0].Sealed || got[0].SizeBytes == 0 {
		t.Errorf("catalogue: %+v", got)
	}
	in := NewIngester(dst, Options{MachineAttr: func(string) string { return "machine" }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	last, err := in.LastTs(ctx, "small")
	if err != nil || last != e.at(10*time.Minute) {
		t.Fatalf("last ts after install: %d %v", last, err)
	}
	for _, tbl := range tables {
		var a, b int
		e.db(0).QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&a)
		p, _ := dst.Partition(ctx, "small", e.at(0))
		p.DB().QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&b)
		if a != b {
			t.Errorf("%s: %d rows, %d copied", tbl, a, b)
		}
	}
	// The next capture continues the copied history (nothing reopened).
	res, err := in.Ingest(ctx, e.c.CaptureAt(e.at(15*time.Minute), "small", record.Full, record.TriggerSchedule))
	if err != nil || res.ShardsOpened+res.ShardsClosed != 0 {
		t.Errorf("ingest after install: %+v %v", res, err)
	}
}

// The standby ends its sessions like the primary: those of the accounts
// whose password, role or source changed, or that were disabled or deleted
// (an LDAP account created here too, when the replicated audit shows its
// deletion), and all of them when the authentication mode changes.
func TestMetaReplicationEndsSessions(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(t0)
	primary, err := Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	standby, err := Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer standby.Close()
	sync := func() {
		t.Helper()
		snap, err := primary.ExportMeta(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(snap)
		got, err := DecodeMetaSnapshot(b)
		if err != nil {
			t.Fatal(err)
		}
		if err := standby.ImportMeta(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	primary.SetSetting(ctx, "auth", map[string]any{"mode": "ldap", "session_hours": 12})
	for _, u := range []User{
		{Name: "admin", Role: "admin", Source: "local", Password: "$argon2id$1"},
		{Name: "bob", Role: "reader", Source: "local", Password: "$argon2id$b"},
		{Name: "carol", Role: "reader", Source: "local", Password: "$argon2id$c"},
		{Name: "dan", Role: "admin", Source: "local", Password: "$argon2id$d"},
		{Name: "eve", Role: "reader", Source: "local", Password: "$argon2id$e"},
	} {
		primary.SaveUser(ctx, u)
	}
	sync()
	standby.SaveUser(ctx, User{Name: "ldap-dave", Role: "admin", Source: "ldap"})
	standby.SaveUser(ctx, User{Name: "ldap-erin", Role: "reader", Source: "ldap"})
	open := func(names ...string) {
		for _, n := range names {
			if err := standby.CreateSession(ctx, Session{TokenHash: "s-" + n, User: n, CSRF: "x", CreatedMs: 1, ExpiresMs: t0.Add(time.Hour).UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	alive := func(n string) bool {
		_, err := standby.SessionByHash(ctx, "s-"+n)
		return err == nil
	}
	open("admin", "bob", "carol", "dan", "eve", "ldap-dave", "ldap-erin")

	clk.Advance(time.Minute)
	primary.SetPassword(ctx, "admin", "$argon2id$2")
	primary.DeleteUser(ctx, "carol")
	primary.SaveUser(ctx, User{Name: "dan", Role: "reader", Source: "local"})
	primary.SaveUser(ctx, User{Name: "eve", Role: "reader", Source: "local", Disabled: true})
	primary.Audit(ctx, "admin", "user.delete", "ldap-dave", "")
	primary.SetSetting(ctx, "auth", map[string]any{"mode": "ldap", "session_hours": 24})
	sync()
	for n, want := range map[string]bool{"admin": false, "bob": true, "carol": false, "dan": false, "eve": false, "ldap-dave": false, "ldap-erin": true} {
		if alive(n) != want {
			t.Errorf("session of %s alive %v, want %v", n, !want, want)
		}
	}
	if _, err := standby.User(ctx, "ldap-dave"); err == nil {
		t.Error("LDAP account deleted on the primary kept on the standby")
	}
	if _, err := standby.User(ctx, "ldap-erin"); err != nil {
		t.Errorf("LDAP account of the standby dropped: %v", err)
	}
	// Logging in again after the deletion recreates the account for good.
	clk.Advance(time.Minute)
	standby.SaveUser(ctx, User{Name: "ldap-dave", Role: "admin", Source: "ldap"})
	open("ldap-dave")
	sync()
	if !alive("ldap-dave") {
		t.Error("LDAP account recreated after its deletion dropped again")
	}

	primary.SetSetting(ctx, "auth", map[string]any{"mode": "local"})
	sync()
	for _, n := range []string{"bob", "ldap-erin", "ldap-dave"} {
		if alive(n) {
			t.Errorf("session of %s survives a change of mode", n)
		}
	}
}
