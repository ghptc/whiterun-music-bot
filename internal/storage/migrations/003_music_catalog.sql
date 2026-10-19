-- Provider metadata is independent of guild/session state. Mapping evidence
-- remains in mappings; no second Spotify -> YouTube lookup table is created.
CREATE TABLE music_catalog (
 spotify_id TEXT PRIMARY KEY CHECK(length(spotify_id)>0),
 metadata TEXT NOT NULL CHECK(json_valid(metadata)),
 updated_at INTEGER NOT NULL CHECK(updated_at>0)
);
CREATE INDEX mappings_expiration ON mappings(expires);
