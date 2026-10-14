package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupBeforeMigrate snapshots the database with VACUUM INTO when the
// schema is about to change (a non-empty database below LatestVersion), so a
// bad migration can be undone with the stopped-service restore procedure.
// It keeps the newest keep snapshots in dir and returns the new file's path
// ("" when no snapshot was needed).
func BackupBeforeMigrate(ctx context.Context, database *DB, dir string, keep int) (string, error) {
	current, err := SchemaVersion(database)
	if err != nil {
		return "", err
	}
	latest := LatestVersion()
	if current == 0 || current >= latest {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("cato-pre-v%d-from-v%d-%s.db", latest, current, time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	if _, err := database.Write.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("snapshot before migration: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	if err := rotateSnapshots(dir, keep); err != nil {
		return path, err
	}
	return path, nil
}

// LatestBackup returns the newest pre-migration snapshot in dir and its
// modification time (zero values when there is none).
func LatestBackup(dir string) (string, time.Time) {
	snaps, _ := snapshots(dir)
	if len(snaps) == 0 {
		return "", time.Time{}
	}
	newest := snaps[len(snaps)-1]
	info, err := os.Stat(newest)
	if err != nil {
		return "", time.Time{}
	}
	return newest, info.ModTime()
}

func snapshots(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "cato-pre-v") && strings.HasSuffix(e.Name(), ".db") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	// Names end in a UTC timestamp, but versions sort as text ("v9" > "v18"),
	// so order by modification time.
	sort.Slice(out, func(i, j int) bool {
		a, _ := os.Stat(out[i])
		b, _ := os.Stat(out[j])
		if a == nil || b == nil {
			return out[i] < out[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	return out, nil
}

func rotateSnapshots(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	snaps, err := snapshots(dir)
	if err != nil {
		return err
	}
	for len(snaps) > keep {
		if err := os.Remove(snaps[0]); err != nil {
			return err
		}
		snaps = snaps[1:]
	}
	return nil
}
