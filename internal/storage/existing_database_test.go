package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Optional acceptance against a copied production DB; never writes the source.
func TestExistingDatabaseUpgrade(t *testing.T) {
	source := os.Getenv("BARD_TEST_EXISTING_DB")
	if source == "" {
		t.Skip("set BARD_TEST_EXISTING_DB to a consistent SQLite backup")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "existing.db")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var integrity string
	if err = db.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	rows, err := db.db.Query("SELECT guild_id FROM guilds")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, id := range ids {
		state, err := db.LoadGuild(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.SaveGuild(context.Background(), id, state); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("existing DB: integrity ok; %d guild snapshots upgraded and round-tripped", len(ids))
}
