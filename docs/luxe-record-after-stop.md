# Luxe: record first, download after stop (clean branch)

Baseline: `luxe/hlc7` (go2rtc 1.9.15 with existing HLC7/HEVC fixes). This branch must not merge the earlier `luxe/recording-manager-dev` or `feat/luxe-direct-stream-recording-poc` experiment trees.

## Non-negotiable client requirements

- App Store Home Assistant iOS app is unchanged. No fork, TestFlight, Safari companion or ZIP delivery.
- Existing live viewing, HLC7 fixes and screenshot controls stay intact.
- Three separate cameras may record concurrently, up to 30 minutes each, with audio independent of preview mute.
- New behavior: capture first; after an individual recording has stopped and its MP4 has been finalized, automatically **attempt** native download to the currently open HA app.
- At most one WKDownload is launched at a time. Until a native completion bridge exists, JavaScript must **not** infer that native download or "Save to Files" succeeded.
- A download attempt is not a successful save. A short timer, focus event, HTTP GET completion or `anchor.click()` cannot be used as native-save acknowledgement.
- No permanent recording library. The choice between bounded server-side temporary storage and RAM-only buffering MUST be settled before implementing 30-minute recording. RAM-only full recordings are unsafe on resource-constrained HAOS systems; any temporary files occupy server/NAS disk until cleanup.

## Confirmed baseline characteristics (2026-11-03)

- `internal/mp4/mp4.go`: `/api/stream.mp4` is live progressive streaming from request time; it is **not** an after-stop media archive.
- `pkg/mp4/consumer.go`: contains MP4 writer/muxer for existing live streams; seekable finalized MP4, HEVC `hvc1` signaling, AAC/Opus support and correct end-of-file need validation for the new storage lifecycle.
- `.github/workflows/luxe-ci.yml` is restricted to `luxe/hlc7`; this branch needs an isolated workflow.
- Existing HA App download sheet cancels its `WKDownload` on disappear and does not send web JS a completion notification.
- Luxe screenshot button delegates to the existing `webrtc-camera` screenshot control on a real click; this proves **click-initiated** download only.

## Development gates

### Gate 1: isolated single-file WKDownload trigger test
- Confirm a download created after an asynchronous "recording ready" notification opens native WKDownload in the App Store HA iOS App, without a second tap.
- Test two distinct triggers: manual Stop followed by asynchronous finalization, and the automatic 30-minute time limit (shortened in the test harness).
- Use a known valid short MP4 fixture; verify filename, HTTP attachment headers, origin, authorization/cookies and no repeated trigger.
- Failure must keep a visible manual retry action; do not silently drop a file.

### Gate 2: after-stop recording correctness
- Separate capture from client HTTP transfer. When recording stops, produce a complete, independently retrievable file.
- Validate exact duration, audio, MP4 container and codec identity (`hvc1` for HEVC when parameter sets permit; AAC where native playback requires it).
- Explicit per-session isolation, 30-minute cap, bound for active sessions, cleanup TTL and disk-space limit.
- Must survive HA frontend going into background/closing while recording, provided the service remains running.
- No write to `luxe/hlc7`.

### Gate 3: one-at-a-time download queue
- Queue three independent MP4s, never three concurrent HA App download sheets.
- Native completion/save **cannot** be determined via the current web frontend API. Require safe user confirmation or invent and validate an alternative web-exposed acknowledgement; never auto-advance using only a fixed delay.
- On failure, allow independent retry without losing other files. Keep temporary media until confirmed or TTL expiry.

### Gate 4: regression and release
- `gofmt`, `go test`, `go build`, live HLC7 playback, MP4/HEVC/AAC playback on iPhone + Mac QuickTime, parallel camera recording, cancellation, auth and disk cleanup.
- CI artifacts are **POC / test binaries only**; no release/tag/production replacement until single-file native after-stop download and end-to-end save flow are validated.

## Open blockers to resolve before shipping

1. Where can completed MP4s reside between recording end and a native download? RAM-only, bounded tmpfs, or explicitly accepted ephemeral disk on HAOS/NAS. After-stop download cannot work without keeping completed bytes somewhere.
2. How can the native WKDownload be triggered by asynchronous JavaScript after user activation expires?
3. How should the browser receive a secure, same-origin attachment URL with valid WebKit navigation credentials?
4. Where will queue state persist across webview navigation or reload?
5. Is the stream MP4 format accepted by QuickTime with the actual HLC7 codec parameter sets and audio codec?
