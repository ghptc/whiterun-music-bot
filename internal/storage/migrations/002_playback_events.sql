CREATE TABLE playback_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 guild_id TEXT NOT NULL REFERENCES guilds(guild_id) ON DELETE CASCADE,
 started_at INTEGER NOT NULL CHECK(started_at>0),
 metadata TEXT NOT NULL CHECK(json_valid(metadata))
);
CREATE INDEX playback_events_guild ON playback_events(guild_id,id);
