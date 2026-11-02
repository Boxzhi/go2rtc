# Luxe Recording Manager

Status: development implementation. The existing HLC7 / CS2 / HVC1 fixes remain the baseline.

## Architecture

The Home Assistant installation uses one **Luxe go2rtc Add-on**. No GPAC sidecar and no `custom_components/luxe_xiaomi_camera` recording integration are required. The official `luxe-camera-wall.js` is not changed in this POC.

The Go recording manager owns the **session lifecycle only**. It does not store video files on the add-on filesystem, write to Home Assistant `/media`, or retain a complete recording in server memory.

1. The frontend creates a recording session with `POST /api/luxe/recordings/start`, authenticated through the user's Home Assistant Ingress session.
2. The Go manager accepts the Supervisor-provided identity only when the TCP peer is 172.30.32.2 with a valid X-Ingress-Path, and validates direct clients' Bearer tokens against Core's `/api/auth/current` endpoint, validates the configured go2rtc stream name, and allocates a random session ID.
3. The response sets a short-lived, HttpOnly, SameSite=Strict cookie scoped to that session's exact download URL. The cookie is not exposed to JavaScript or placed in the URL.
4. The browser performs a normal same-origin navigation to the returned `download_url`. The Go manager attaches a native go2rtc MP4 Consumer and sends a streaming HTTP response with `Content-Disposition: attachment`.
5. The browser's native download manager receives MP4 bytes directly. JavaScript does not fetch or buffer the media body and does not use Blob, OPFS, or a server-side file.
6. `POST /api/luxe/recordings/{session_id}/stop` gives the consumer a bounded drain period before closing that session's MP4 response. A disconnected browser request also releases the consumer.
7. Session metadata remains in memory briefly for status polling and is discarded. It is not a recording history.

## Fixed limits

- Maximum **3 simultaneous recording sessions and 3 simultaneous native browser downloads**.
- At most one active recording per configured stream.
- Maximum 30 minutes per session, enforced by the Go server.
- Camera audio is requested with `mp4=all`; it does not follow the live player mute state.
- No frontend download queue: each camera has an independent download request.
- Sender queue packet drops are monitored; any detected loss fails only that session instead of silently producing a potentially incomplete MP4.
- Session metadata expires after completion; pending sessions expire if no stream request starts.
- Recording bytes are streamed to the browser; no server-side recording directory, OPFS, Blob buffering, or `/media` write permission is required.

## API

Ingress control requests use the Supervisor-authenticated `X-Remote-User-Id` and require no frontend access token. The go2rtc server trusts that header only from TCP peer `172.30.32.2` with a valid `X-Ingress-Path`. Direct/non-Ingress control requests still require a HA Bearer token verified against Home Assistant Core.

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

## OPFS stop-then-download browser POC

Open `/recording-opfs-poc.html` through the same Add-on Ingress page, or use the link from `/recording-poc.html`.

This is a **client-side storage/download transport test only**. It writes deterministic `.bin` sample data, not camera video, and does not call the Go Recording Manager API.

- A separate Dedicated Worker per sample writes fixed-size chunks into OPFS using `FileSystemSyncAccessHandle`.
- A Dedicated Worker reads OPFS chunks for download; it does not call `FileSystemFileHandle.getFile()`.
- A MessageChannel transfers bounded chunks to the Service Worker. The Service Worker only exposes a streaming `ReadableStream` attachment response and does not access OPFS.
- The transfer uses backpressure: the Service Worker requests one chunk when the browser stream pulls.
- CRC32 and exact byte count are checked before the response is marked complete.
- No `Blob`, `File`, `URL.createObjectURL()`, `arrayBuffer()`, full-file JavaScript buffer, server-side file, or NAS/HA write is used.
- Download stream completion is not proof that the operating system saved the file. The test page therefore requires the user to check the browser download manager before manually cleaning the OPFS file.
- The POC uses three independent sample files to exercise concurrent writes and downloads. It does not yet test actual camera MP4, iOS background execution, or automatic cleanup after a native download.

This POC is the prerequisite for the future stop-then-download architecture. Do not connect it to the live camera stream or migrate the production UI until Safari and Home Assistant iOS App tests pass.

## Home Assistant connectivity

The token-validation URL used for direct/non-Ingress requests is `http://homeassistant:8123/api/auth/current`. It can be changed in go2rtc configuration:

```yaml
luxe_recording:
  enabled: true
  auth_url: http://homeassistant:8123/api/auth/current
```

Authentication fails closed if Home Assistant cannot validate a direct caller's token or an Ingress session has no user ID. The Add-on already enables `homeassistant_api: true`; the implementation uses the user's own token for identity validation rather than granting recording callers Supervisor-level access.

## Acceptance checks before any release

1. CI tests and `go build ./...` pass.
2. HAOS Ingress forwards `X-Ingress-Path` and `X-Remote-User-Id`; direct requests cannot spoof an authenticated Ingress identity.
3. The scoped HttpOnly cookie is accepted by the browser and sent to the exact download URL.
4. A native browser download starts without JavaScript fetching or buffering MP4 bytes.
5. Three different cameras can record and download simultaneously; a fourth is rejected.
6. Stopping one camera does not stop the other two.
7. A duplicate active session for the same stream is rejected.
8. Manual stop and the 30-minute server limit both produce playable files.
9. Camera audio remains present while the live preview is muted.
10. iPhone Safari, Home Assistant iOS App, Mac QuickTime and desktop browser playback are tested with actual HLC7 files.
11. Add-on restart, client disconnect and Home Assistant auth timeout release sessions cleanly.
12. No GPAC service, custom component, server-side recording file, Blob/full-file memory buffer or `/media` write access is needed. The separate OPFS POC may use device-local temporary storage only; it must never write recordings to NAS, HAOS, or go2rtc server storage.

**This branch is development only. Do not deploy or publish a production release until CI and HAOS real-device tests pass.**


## Reliability updates (2026-11-02)

- The separate Recording Manager implementation remains confined to the development branch; the normal `api/stream.mp4` endpoint is unchanged.
- The REC state begins **only after the first actual MP4 media fragment** reaches the HTTP response, not merely when a consumer is attached or the init segment is written.
- User stop and the 30-minute time limit attempt a bounded (five-second) drain of already queued MP4 sender packets before the download response finishes.
- A session that ends without media, or cannot drain its queued fragments, is marked as failed.
- A client disconnect immediately releases the MP4 consumer rather than attempting a graceful download to an absent client.
- The POC uses trusted Ingress identity and does not read HA access tokens from a parent frame.
- No complete recording is stored on NAS, HA /media, or inside go2rtc.

**CI success is not device verification.** Native downloads on iPhone Safari, Home Assistant iOS App, Mac Safari, and actual HLC7 MP4 playback must still be tested through the Add-on's Ingress before any release.
