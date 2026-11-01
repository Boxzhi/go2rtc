# Luxe Recording Manager — development design

Status: **prototype only**. The stable HLC7 / HVC1 implementation remains untouched.

## Goal

The Home Assistant installation should need only the **Luxe go2rtc Add-on**
for the recording backend. There will be **no separate GPAC app** and **no
`custom_components/luxe_xiaomi_camera` recording integration**.

The Home Assistant dashboard may still load the standalone
`luxe-camera-wall.js` frontend resource.

## Responsibilities

- go2rtc retains the video/audio sources and native fragmented MP4 packaging.
- A new Go recording manager adds session start, stop, status, list, download,
  and deletion APIs.
- The manager stores complete fMP4 fragments in
  `/media/luxe_camera_recordings`, one file per session.
- Up to three parallel camera sessions; one active session per source.
- 30-minute server-side limit; automatic cleanup and recording finalization.
- Never depend on browser playback volume or muted state for recorded audio.
- Browser initiates downloads sequentially when requested.

## Security requirements

- Validate a Home Assistant Bearer token server-side on every recording API
  request; fail closed on validation timeout.
- Accept only existing named go2rtc streams, not arbitrary user-provided URLs.
- Sanitize file names, use random session IDs, and limit concurrent sessions.
- Do not assume a local Add-on slug (e.g. `luxe_go2rtc`) equals the full
  Supervisor application identifier.
- Validate Ingress routing and HA admin/non-admin access before frontend release.

## Acceptance checks

1. Existing three streams and real-time WebRTC remain unchanged.
2. Start/stop/status are idempotent and safe under concurrent requests.
3. Files remain playable after stop and after the 30-minute timeout.
4. MP4 writes preserve complete `moof`/`mdat` pairs on cancellation.
5. Downloaded HEVC samples use a compliant `hvc1` configuration.
6. Audio tracks are captured even when live playback is muted.
7. Files remain discoverable after Add-on restart; incomplete files are handled.
8. Disk space limits, cleanup policy, and access control are tested.
9. Neither GPAC nor `custom_components` is required.

**No version release or deployment before CI and HAOS real-device testing.**
