# Luxe Recording Manager

Status: development implementation. The existing HLC7 / CS2 / HVC1 fixes remain the baseline.

## Architecture

The Home Assistant installation uses one **Luxe go2rtc Add-on**. No GPAC sidecar and no `custom_components/luxe_xiaomi_camera` recording integration are required. The official `luxe-camera-wall.js` is not changed in this POC.

The Go recording manager owns the **session lifecycle only**. It does not store video files on the add-on filesystem, write to Home Assistant `/media`, or retain a complete recording in server memory.

1. The frontend creates a recording session with `POST /api/luxe/recordings/start`, authenticated by the current Home Assistant user's Bearer token.
2. The Go manager validates that token against Core's `/api/auth/current` endpoint, validates the configured go2rtc stream name, and allocates a random session ID.
3. The response sets a short-lived, HttpOnly, SameSite=Strict cookie scoped to that session's exact download URL. The cookie is not exposed to JavaScript or placed in the URL.
4. The browser performs a normal same-origin navigation to the returned `download_url`. The Go manager attaches a native go2rtc MP4 Consumer and sends a streaming HTTP response with `Content-Disposition: attachment`.
5. The browser's native download manager receives MP4 bytes directly. JavaScript does not fetch or buffer the media body and does not use Blob, OPFS, or a server-side file.
6. `POST /api/luxe/recordings/{session_id}/stop` cancels only that session's MP4 consumer. A disconnected browser request also releases the consumer.
7. Session metadata remains in memory briefly for status polling and is discarded. It is not a recording history.

## Fixed limits

- Maximum **3 simultaneous recording sessions and 3 simultaneous native browser downloads**.
- At most one active recording per configured stream.
- Maximum 30 minutes per session, enforced by the Go server.
- Camera audio is requested with `mp4=all`; it does not follow the live player mute state.
- No frontend download queue: each camera has an independent download request.
- Session metadata expires after completion; pending sessions expire if no stream request starts.
- Recording bytes are streamed to the browser; no server-side recording directory, OPFS, Blob buffering, or `/media` write permission is required.

## API

All control endpoints require `Authorization: Bearer <Home Assistant access token>`. The token is validated against Home Assistant Core. The browser must access the API through the Add-on's authenticated Ingress URL.

- `GET /api/luxe/recordings` — list the current user's recent in-memory sessions.
- `POST /api/luxe/recordings/start` — JSON body: `{"source":"configured_stream_name","filename":"recording.mp4"}`; returns a session ID and download URL, and sets the scoped download cookie.
- `GET /api/luxe/recordings/{id}` — session status.
- `GET /api/luxe/recordings/{id}/stream` — native browser attachment download; authenticated by the short-lived scoped cookie.
- `POST /api/luxe/recordings/{id}/stop` — stop the session.

Only existing go2rtc stream names are accepted; arbitrary source URLs are rejected. A session belongs to the Home Assistant user who created it. The download cookie is a high-entropy, session-specific capability, HttpOnly and path-scoped to the single stream URL.

## Browser download POC

Open `/recording-poc.html` through the Add-on Ingress panel. It exposes three independent camera rows for:

- `jinghuafu_keting_ptz_3k`
- `jinghuafu_keting_fixed_3k`
- `jinghuafu_canting_2k`

For each row, create a session, then click **Start download**. Repeat for the other cameras; all three download responses can remain active simultaneously. Stop each session independently. This page is a separate POC and does not modify the production camera-wall card.

The POC intentionally uses normal browser navigation for the MP4 response. It does not use `fetch(response.body)`, Blob, OPFS, or a JavaScript download queue. The POC must be verified in Safari, Home Assistant iOS App, and desktop browsers before the official UI is migrated.

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
3. The scoped HttpOnly cookie is accepted by the browser and sent to the exact download URL.
4. A native browser download starts without JavaScript fetching or buffering MP4 bytes.
5. Three different cameras can record and download simultaneously; a fourth is rejected.
6. Stopping one camera does not stop the other two.
7. A duplicate active session for the same stream is rejected.
8. Manual stop and the 30-minute server limit both produce playable files.
9. Camera audio remains present while the live preview is muted.
10. iPhone Safari, Home Assistant iOS App, Mac QuickTime and desktop browser playback are tested with actual HLC7 files.
11. Add-on restart, client disconnect and Home Assistant auth timeout release sessions cleanly.
12. No GPAC service, custom component, server-side recording file, Blob/OPFS buffer or `/media` write access is needed.

**This branch is development only. Do not deploy or publish a production release until CI and HAOS real-device tests pass.**
