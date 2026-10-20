package storage

import (
	"context"
	"reflect"
	"testing"
	"whiterun/internal/media"
)

func TestHardeningSettingsReadDistinguishesAbsentAndUnavailable(t *testing.T) {
	db := openTest(t)
	missing, err := db.LoadSettings(context.Background(), "missing")
	if err != nil || missing == nil || len(missing) != 0 {
		t.Fatal("absent settings not defaulted", missing, err)
	}
	if err := db.SaveSettings(context.Background(), "one", map[string]string{"channel": "7"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadSettings(context.Background(), "one")
	if err != nil || got["channel"] != "7" {
		t.Fatal("settings lost", got, err)
	}
	db.Close()
	if _, err := db.LoadSettings(context.Background(), "one"); err == nil {
		t.Fatal("unavailable DB confused with absent settings")
	}
}
func TestHardeningLegacyMappingShapeIsNotCacheHit(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.SaveMapping(ctx, "identity", media.Candidate{ID: "abcdefghijk"}); err != nil {
		t.Fatal(err)
	}
	// Syntactically valid JSON can still have an incompatible shape. Such data must
	// be a read error, never a trusted mapping hit.
	if _, err := db.db.Exec("UPDATE mappings SET candidate='[]' WHERE identity='identity'"); err != nil {
		t.Fatal(err)
	}
	_, hit, err := db.LoadMapping(ctx, "identity")
	if err == nil || hit {
		t.Fatalf("corrupt metadata trusted: hit=%v err=%v", hit, err)
	}
}

func TestHardeningMappingConstraintPreservesPreviousRecord(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	previous := media.Candidate{ID: "abcdefghijk", Title: "original", Artists: []string{"artist"}}
	if err := db.SaveMapping(ctx, "identity", previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("UPDATE mappings SET candidate='not-json' WHERE identity='identity'"); err == nil {
		t.Fatal("SQLite accepted malformed JSON")
	}
	got, hit, err := db.LoadMapping(ctx, "identity")
	if err != nil || !hit || !reflect.DeepEqual(got, previous) {
		t.Fatalf("constraint rejection lost previous mapping: %+v hit=%v err=%v", got, hit, err)
	}
}
