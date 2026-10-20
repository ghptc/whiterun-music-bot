# Real Auto Pilot captures — 2026-10-20

- `spotify.json`: unmodified Spotify Web API `/v1/tracks/3hWu4Z5IkREyWNT4zuuXM5`
  response, using application credentials. No credentials are stored here.
- `music-search.json`: projection of the first ten entries returned by yt-dlp
  2026.08.19 for `https://music.youtube.com/search?q=Queens+of+the+Stone+Age+Auto+Pilot#songs`.
- `video-search.json`: projection of ten real entries returned for
  `ytsearch10:Queens of the Stone Age Auto Pilot`.
- `mhXmqO7hCfA.json`: projection of the candidate returned by full extraction
  using production `MetadataArgs()`. No metadata was inferred or added.

Projections retain Candidate fields and remove URLs, formats, and unrelated
extractor details. Missing fields remain missing. Discovery order is preserved.
Full extraction and YouTube Music discovery used the production Docker image,
production BaseArgs and `--skip-download --dump-single-json --ignore-errors`.
Video discovery used `--socket-timeout 10 --retries 1` rather than production
15/2; this affects transport retries, not the captured candidate metadata.

The ID was first found in the existing Bard volume's `music_catalog`, then
independently queried from Spotify. The user subsequently supplied playlist `6BL3uZVI7Q41TtpsLbmmLt`. The production
Spotify constructor resolved all 302 entries via API and confirmed this ID at
position 4; the selected metadata is retained in
`docs/auto-pilot-playlist-evidence.json`. Historical resolver logs and the
original runtime binary remain unavailable. These fixtures establish a real
reproducible matcher false negative, not historical deployment parity.

The original full JSON and stderr were inspected locally. Both full metadata
and separate format extraction exited 0 with empty stderr. Public audio formats
139, 249, 250, 140 and 251 were present. Signed URLs are not committed. No audio
was downloaded or played; no mapping was inserted into the production database.
