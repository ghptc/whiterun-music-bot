# Original synthetic audio for playback validation

`validation.flac` is an original mathematical tone, generated for this project;
it contains no recordings, samples or commercial music. It may be used and
redistributed as a project test fixture. It is audible: start at a low volume.

- Duration: exactly 30 seconds.
- Format: FLAC, 48,000 Hz, stereo.
- SHA-256: `0dc3540d8f2c19d09f30fabadbec072259e4f8378b378e2018a8e0691950f3f3`.
- Generator used: FFmpeg `n9.0.2` on Arch Linux. Encoder/container metadata may
  differ across versions; validate a regenerated file before updating its hash.

Regenerate from the repository root (overwrites only this generated fixture):

```sh
ffmpeg -hide_banner -loglevel error -f lavfi \
  -i 'aevalsrc=0.12*sin(2*PI*(440+110*sin(2*PI*t/6))*t)+0.06*sin(2*PI*660*t):s=48000:d=30' \
  -af 'afade=t=in:d=0.3,afade=t=out:st=29:d=1' \
  -ac 2 -c:a flac -map_metadata -1 -y \
  internal/player/testdata/local-audio/validation.flac
sha256sum internal/player/testdata/local-audio/validation.flac
```

The real Voice test is opt-in and uses a separate test bot. See
[the EC2 validation procedure](../../../../docs/investigations/local-audio-ec2-validation.md).
