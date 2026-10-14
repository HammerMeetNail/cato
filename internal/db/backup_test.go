package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupBeforeMigrateSnapshotsOnlyPendingUpgrades(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backups")

	fresh := openAt(t, 0)
	if path, err := BackupBeforeMigrate(ctx, fresh, dir, 3); err != nil || path != "" {
		t.Fatalf("new database: path=%q err=%v, want no snapshot", path, err)
	}

	old := openAt(t, 17)
	mustExec(t, old, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com')`)
	path, err := BackupBeforeMigrate(ctx, old, dir, 3)
	if err != nil || path == "" {
		t.Fatalf("pending upgrade: path=%q err=%v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode %v, want 0600", info.Mode().Perm())
	}
	snap, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if v, _ := SchemaVersion(snap); v != 17 {
		t.Errorf("snapshot schema version %d, want 17 (taken before migrating)", v)
	}
	if n := scalar(t, snap, `SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("snapshot has %d users", n)
	}

	if err := Migrate(old); err != nil {
		t.Fatal(err)
	}
	if path, err := BackupBeforeMigrate(ctx, old, dir, 3); err != nil || path != "" {
		t.Errorf("up-to-date database: path=%q err=%v, want no snapshot", path, err)
	}
}

func TestBackupRotationKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i, name := range []string{"cato-pre-v9-a.db", "cato-pre-v18-b.db", "cato-pre-v10-c.db", "unrelated.db"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	if err := rotateSnapshots(dir, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cato-pre-v9-a.db")); !os.IsNotExist(err) {
		t.Errorf("oldest snapshot kept")
	}
	for _, keep := range []string{"cato-pre-v18-b.db", "cato-pre-v10-c.db", "unrelated.db"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if newest, _ := LatestBackup(dir); filepath.Base(newest) != "cato-pre-v10-c.db" {
		t.Errorf("LatestBackup = %q", newest)
	}
}
