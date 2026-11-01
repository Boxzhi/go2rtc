# Luxe Recording Manager

Status: development implementation. The existing HLC7 / CS2 / HVC1 fixes remain the baseline.

## Architecture

The Home Assistant installation uses one **Luxe go2rtc Add-on**. No GPAC sidecar and no `custom_components/luxe_xiaomi_camera` recording integration are required. The dashboard continues to use the standalone `luxe-camera-wall.js` frontend resource.

The Go recording manager owns the **session lifecycle only**. It does not store video files on the add-on filesystem, write to Home Assistant `/media`, or retain a complete recording in server memory.

1. The frontend creates a recording session with `POST /api/luxe/recordings/start`.
2. The Go manager validates the Home Assistant user's Bearer token against Core's `/api/auth/current` endpoint, validates the configured go2rtc stream name, and allocates a random session ID.
3. The frontend reads the returned `download_url` as a streaming HTTP response.
4. The Go manager attaches a native go2rtc MP4 Consumer using `mp4=all`, preserving supported camera audio independently of live-preview mute state.
5. The browser writes incoming chunks to OPFS when available, with the existing frontend fallback behavior, then downloads the completed file to the current device.
6. `POST /api/luxe/recordings/{session_id}/stop` cancels only that session's MP4 consumer. A disconnected browser request also releases the consumer.
7. Session metadata remains in memory briefly for status polling and is discarded. It is not a recording history.

## Fixed limits

- Maximum 3 simultaneous camera sessions.
- At most one active recording per configured stream.
- Maximum 30 minutes per session, enforced by the Go server.
- Camera audio is requested with `mp4=all`; it does not follow the live player mute state.
- Frontend download queue remains sequential.
- Session metadata expires after completion; pending sessions expire if no stream request starts.
- Recording bytes are streamed to the browser; no server-side recording directory or /media write permission is required.

## API

All endpoints require `Authorization: Bearer <Home Assistant access token>`. The token is validated against Home Assistant Core. The browser must access the API through the Add-on's authenticated Ingress URL.

- `GET /api/luxe/recordings` — list the current user's recent in-memory sessions.
- `POST /api/luxe/recordings/start` — JSON body: `{"source":"configured_stream_name","filename":"recording.mp4"}`.
- `GET /api/luxe/recordings/{id}` — session status.
- `GET /api/luxe/recordings/{id}/stream` — native fragmented-MP4 stream.
- `POST /api/luxe/recordings/{id}/stop` — stop the session.

Only existing go2rtc stream names are accepted; arbitrary source URLs are rejected. A session belongs to the Home Assistant user who created it.

## Home Assistant connectivity

The default token-validation URL is `http://homeassistant:8123/api/auth/current`. It can be changed in go2rtc configuration:

```yaml
luxe_recording:
  enabled: true
  auth_url: http://homeassistant:8123/api/auth/current
```

Authentication fails closed if Home Assistant cannot validate the token. The Add-on already enables `homeassistant_api: true`; the implementation uses the user's own token for identity validation rather than granting recording callers Supervisor-level access.

## Acceptance checks before any release

1. CI tests and `go build ./...` pass.
2. HAOS Ingress forwards `X-Ingress-Path` and the user's Authorization header correctly.
3. A recording can be started and stopped without affecting the live preview or other recording sessions.
4. Three different cameras can record simultaneously; a fourth is rejected.
5. A duplicate active session for the same stream is rejected.
6. Manual stop and the 30-minute server limit both produce playable files.
7. Camera audio remains present while the live preview is muted.
8. iPhone, Mac QuickTime and browser playback are tested with actual HLC7 files.
9. Add-on restart, client disconnect and Home Assistant auth timeout release sessions cleanly.
10. No GPAC service, custom component, server-side recording file or /media write access is needed.

**This branch is development only. Do not deploy or publish a release until CI and HAOS real-device tests pass.**
