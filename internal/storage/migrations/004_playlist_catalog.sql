-- Occurrences have their own identity: duplicate tracks are valid positions.
CREATE TABLE catalog_playlists (
 spotify_id TEXT PRIMARY KEY NOT NULL CHECK(length(spotify_id)>0),
 title TEXT NOT NULL,
 source_url TEXT NOT NULL,
 skipped INTEGER NOT NULL CHECK(skipped>=0)
);
CREATE TABLE catalog_positions (
 playlist_id TEXT NOT NULL REFERENCES catalog_playlists(spotify_id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 position INTEGER NOT NULL CHECK(position>0),
 total INTEGER NOT NULL CHECK(total>=position),
 track_id TEXT NOT NULL REFERENCES music_catalog(spotify_id) ON DELETE RESTRICT,
 PRIMARY KEY(playlist_id,ordinal),
 UNIQUE(playlist_id,position)
);
CREATE INDEX catalog_positions_track ON catalog_positions(track_id);
CREATE INDEX catalog_artist_title ON music_catalog(lower(json_extract(metadata,'$.Artist')),lower(json_extract(metadata,'$.Title')));
-- SQLite permits NULL in non-integer PRIMARY KEY columns unless explicitly
-- constrained. Preserve the existing table and any operator-created indexes.
CREATE TRIGGER music_catalog_identity_insert BEFORE INSERT ON music_catalog WHEN NEW.spotify_id IS NULL BEGIN
 SELECT RAISE(ABORT,'catalog Spotify identity must not be NULL');
END;
CREATE TRIGGER music_catalog_identity_update BEFORE UPDATE OF spotify_id ON music_catalog WHEN NEW.spotify_id IS NULL BEGIN
 SELECT RAISE(ABORT,'catalog Spotify identity must not be NULL');
END;
