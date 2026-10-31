-- Quota accounts UTF-8 catalog payload, not SQLite pages or WAL.
CREATE TABLE catalog_quota (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 limit_bytes INTEGER NOT NULL CHECK(limit_bytes>0),
 used_bytes INTEGER NOT NULL CHECK(used_bytes>=0 AND used_bytes<=limit_bytes),
 episode INTEGER NOT NULL DEFAULT 0,
 warning INTEGER NOT NULL DEFAULT 0,
 monitoring INTEGER NOT NULL DEFAULT 1 CHECK(monitoring IN(0,1)),
 critical INTEGER NOT NULL DEFAULT 0
);
INSERT INTO catalog_quota(singleton,limit_bytes,used_bytes) SELECT 1,10737418240,
 coalesce((SELECT sum(length(CAST(spotify_id AS BLOB))+length(CAST(metadata AS BLOB))) FROM music_catalog),0)+
 coalesce((SELECT sum(length(CAST(spotify_id AS BLOB))+length(CAST(title AS BLOB))+length(CAST(source_url AS BLOB))+8) FROM catalog_playlists),0)+
 coalesce((SELECT sum(length(CAST(playlist_id AS BLOB))+length(CAST(track_id AS BLOB))+24) FROM catalog_positions),0);
CREATE TABLE catalog_alerts (
 id INTEGER PRIMARY KEY,
 episode INTEGER NOT NULL,
 threshold INTEGER NOT NULL CHECK(threshold IN(90,100)),
 used_bytes INTEGER NOT NULL,
 limit_bytes INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0,
 target_guild TEXT,
 target_channel TEXT,
 delivered INTEGER NOT NULL DEFAULT 0,
 UNIQUE(episode,threshold)
);
CREATE TABLE catalog_activity (
 command_id TEXT PRIMARY KEY NOT NULL,
 guild_id TEXT NOT NULL,
 channel_id TEXT NOT NULL,
 occurred_at INTEGER NOT NULL
);
CREATE INDEX catalog_activity_window ON catalog_activity(occurred_at,guild_id,channel_id);
CREATE TRIGGER catalog_thresholds AFTER UPDATE OF used_bytes,limit_bytes ON catalog_quota WHEN NEW.monitoring=1 BEGIN
 INSERT OR IGNORE INTO catalog_alerts(episode,threshold,used_bytes,limit_bytes)
 SELECT NEW.episode,90,NEW.used_bytes,NEW.limit_bytes WHERE NEW.used_bytes>=NEW.limit_bytes*0.9 AND NEW.warning=0;
 INSERT OR IGNORE INTO catalog_alerts(episode,threshold,used_bytes,limit_bytes)
 SELECT NEW.episode,100,NEW.used_bytes,NEW.limit_bytes WHERE NEW.used_bytes=NEW.limit_bytes AND NEW.critical=0;
 UPDATE catalog_quota SET
 warning=CASE WHEN NEW.used_bytes<NEW.limit_bytes*0.85 THEN 0 WHEN NEW.used_bytes>=NEW.limit_bytes*0.9 THEN 1 ELSE warning END,
 critical=CASE WHEN NEW.used_bytes<NEW.limit_bytes*0.85 THEN 0 WHEN NEW.used_bytes=NEW.limit_bytes THEN 1 ELSE critical END,
 episode=episode+CASE WHEN NEW.used_bytes<NEW.limit_bytes*0.85 AND (NEW.warning=1 OR NEW.critical=1) THEN 1 ELSE 0 END
 WHERE singleton=1;
END;
CREATE TRIGGER music_catalog_quota_insert AFTER INSERT ON music_catalog BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(length(CAST(NEW.spotify_id AS BLOB))+length(CAST(NEW.metadata AS BLOB))) WHERE singleton=1;
END;
CREATE TRIGGER music_catalog_quota_update AFTER UPDATE ON music_catalog BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+((length(CAST(NEW.spotify_id AS BLOB))+length(CAST(NEW.metadata AS BLOB)))-(length(CAST(OLD.spotify_id AS BLOB))+length(CAST(OLD.metadata AS BLOB)))) WHERE singleton=1;
END;
CREATE TRIGGER music_catalog_quota_delete AFTER DELETE ON music_catalog BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(-(length(CAST(OLD.spotify_id AS BLOB))+length(CAST(OLD.metadata AS BLOB)))) WHERE singleton=1;
END;
CREATE TRIGGER catalog_playlists_quota_insert AFTER INSERT ON catalog_playlists BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(length(CAST(NEW.spotify_id AS BLOB))+length(CAST(NEW.title AS BLOB))+length(CAST(NEW.source_url AS BLOB))+8) WHERE singleton=1;
END;
CREATE TRIGGER catalog_playlists_quota_update AFTER UPDATE ON catalog_playlists BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+((length(CAST(NEW.spotify_id AS BLOB))+length(CAST(NEW.title AS BLOB))+length(CAST(NEW.source_url AS BLOB))+8)-(length(CAST(OLD.spotify_id AS BLOB))+length(CAST(OLD.title AS BLOB))+length(CAST(OLD.source_url AS BLOB))+8)) WHERE singleton=1;
END;
CREATE TRIGGER catalog_playlists_quota_delete AFTER DELETE ON catalog_playlists BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(-(length(CAST(OLD.spotify_id AS BLOB))+length(CAST(OLD.title AS BLOB))+length(CAST(OLD.source_url AS BLOB))+8)) WHERE singleton=1;
END;
CREATE TRIGGER catalog_positions_quota_insert AFTER INSERT ON catalog_positions BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(length(CAST(NEW.playlist_id AS BLOB))+length(CAST(NEW.track_id AS BLOB))+24) WHERE singleton=1;
END;
CREATE TRIGGER catalog_positions_quota_update AFTER UPDATE ON catalog_positions BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+((length(CAST(NEW.playlist_id AS BLOB))+length(CAST(NEW.track_id AS BLOB))+24)-(length(CAST(OLD.playlist_id AS BLOB))+length(CAST(OLD.track_id AS BLOB))+24)) WHERE singleton=1;
END;
CREATE TRIGGER catalog_positions_quota_delete AFTER DELETE ON catalog_positions BEGIN
 UPDATE catalog_quota SET used_bytes=used_bytes+(-(length(CAST(OLD.playlist_id AS BLOB))+length(CAST(OLD.track_id AS BLOB))+24)) WHERE singleton=1;
END;
-- Existing catalogs must produce pending alerts during upgrade as well.
UPDATE catalog_quota SET used_bytes=used_bytes WHERE singleton=1;
