CREATE TABLE guilds (
 guild_id TEXT PRIMARY KEY CHECK(length(guild_id)>0),
 settings TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(settings)),
 state TEXT NOT NULL CHECK(json_valid(state))
);
CREATE TABLE tracks (
 identity TEXT PRIMARY KEY,
 metadata TEXT NOT NULL CHECK(json_valid(metadata))
);
CREATE TABLE history (
 guild_id TEXT NOT NULL REFERENCES guilds(guild_id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 metadata TEXT NOT NULL CHECK(json_valid(metadata)),
 PRIMARY KEY(guild_id,ordinal)
);
CREATE TABLE mappings (
 identity TEXT PRIMARY KEY,
 candidate TEXT NOT NULL CHECK(json_valid(candidate)),
 expires INTEGER NOT NULL CHECK(expires>0)
);
