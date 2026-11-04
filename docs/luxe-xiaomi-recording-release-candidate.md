# Luxe Xiaomi Camera — Recording Release Candidate

> Design baseline (2026-11-04). **Feature branch only**: `feat/luxe-xiaomi-recording-release-candidate` created from `luxe/hlc7`. This document defines the implementation acceptance contract; it is **not** evidence of implemented behavior.

## Branch and release isolation

- Baseline: `luxe/hlc7` production (go2rtc 1.9.15-luxe.1 and existing HLC7 / CS2 / MP4 fixes).
- Do not fast-forward, replace, or publish a production Release on `luxe/hlc7` during development.
- Keep earlier `internal/afterstop` POC logic as **reference only**; do not merge all POC commits or their test-only HTML, packaging and workflow files.
- Keep official Home Assistant iOS App untouched; all recording, temporary storage and download endpoints are server-side, UI is a Home Assistant camera card.
- Test **separately from the existing production addon** and avoid TCP port / RTSP name collisions. A separate test addon may consume the production go2rtc RTSP restreams without modifying it.
- Test and production binaries must have clear identity. No claim of Release or success until a build artifact and actual HA iOS integration test exist.

## Interaction contract (confirmed)

1. Each camera controls its own recording independently. A recording can run for up to **30 minutes**, with **audio included** regardless of muted live preview. Permit simultaneous recording of three named streams.
2. One camera transitions `idle → recording → finalizing → ready` without blocking another camera. A per-camera button transitions from record to **save** when a completed MP4 exists, and shows its exact expiry countdown.
3. Group starts: a **2-second coalescing window** begins at the first MP4 reaching `ready`. If all expected cameras reach `ready` inside the window, offer **one user-triggered share for all** when files pass the WebView size guard. Do not require all streams to finish when they are significantly staggered.
4. Otherwise the first completed MP4 can immediately start the previously proven native WKDownload after the coalescing interval. Further MP4s completed while an iOS native sheet may be open remain `ready` and expose their own **Save** button. Do not open another native sheet automatically.
5. Never infer that a native sheet has closed or that Files was saved from `blur`, `focus`, `visibilitychange`, `pageshow`, timeout or a Web Share promise. The official HA App does **not** expose such a web callback.
6. When more than one MP4 is pending, offer one explicit Web Share action for small files if `navigator.canShare({files})` allows it; otherwise save each recording with its own native WKDownload. Do not create video ZIPs or open Safari.
7. Following a successful actual save, the user **confirms** the file was saved, then the camera button returns to `idle` and the server may release its pending file. If cancelled or failed, the camera remains `ready` until expiry and can be retried.
8. A page reload / reconnection reconstructs every camera's current state and pending `ready` MP4 from server-side metadata. File links are short-lived capabilities, and no secret is disclosed in logs.
9. **Independent 15-minute retention starts at each MP4's `ready` timestamp** (the end of successful finalization). Expiry must revoke new downloads atomically, remove temporary MP4, and reset the button to `idle`. A download initiated before expiry may receive a bounded grace period to finish; it must never extend the 15-minute expiry for other downloads.
10. Expired, cancelled, corrupt or deleted MP4s may never be presented as `ready`; clear their save buttons. Retention is enforced by the Go backend even if the frontend is closed. Handle temporary partial files after crashes and failed jobs.
11. Protect storage: maximum three concurrent recording consumers, per-stream / total byte budget, HAOS disk-free reserve, partial-file cleanup, recoverable failed / timeout paths, no unbounded client memory allocation. **Do not remove capacity checks to enable 30-minute recording.**
12. Preserve HEVC, HLC7 HD SPS/VPS/PPS improvements, and H.265/Opus and H.265/FLAC playback / MP4 compatibility already tested. Run existing codec tests on every candidate build.

## Proposed stable API (names reserved; implementation pending)

- `POST /api/luxe/recordings` — start one named stream; returns a stable session id.
- `POST /api/luxe/recordings/{id}/stop` — stop and finalize.
- `GET /api/luxe/recordings` — authenticated listing to recover active and ready sessions, camera mapping, sizes, codec metadata, `ready_at`, `expires_at`.
- `GET /api/luxe/recordings/{id}` — recording progress/state.
- `GET /api/luxe/recordings/{id}/file?token=...` — origin-bound/authorized download, with `attachment` and Range support for native HA WKDownload; do not issue a live streaming URL.
- `POST /api/luxe/recordings/{id}/ack` — user-attested saved or discarded; note this is not an iOS native callback. Must be idempotent and avoid releasing a file still needed by another process.
- The server must sanitize filename, reject traversal, redact tokens and never expose host filesystem paths.

## Required automated tests and real-device acceptance

- Three sources recording concurrently without the global session lock covering network negotiation.
- Finished sources with 0s/1s/2s difference: group share offered once; differences beyond 2s: the first file is ready independently, later files remain in per-camera save state.
- Returning while another native download sheet is open does **not** trigger a second native sheet.
- Partial failure: successful MP4s remain downloadable; failed session does not block all.
- User cancels share/download: pending video remains until its own 15-minute deadline.
- 15-minute expiry starts at `ready_at` independently. At/after expiration: new GETs denied, temporary file removed, session no longer appears downloadable, UI returns to record.
- Multiple ready files with large sizes: no massive multi-Blob loading in WKWebView; revert to one-file downloads.
- Verified successful HEVC/Opus and HEVC/FLAC MP4 with audio, duration and seekability on stock Home Assistant iOS App.
- Test-install procedure must have rollback to the prior `1.9.15-luxe.1` production addon and no production mutation.

## Delivery gates

1. New standalone **integrated feature branch** from `luxe/hlc7` (not previous POC branches).
2. Implement Go recording subsystem, tests, secure temporary download/cleanup and an integration interface for the actual `luxe-camera-wall.js` card.
3. Build a separately installable test addon / CI artifact clearly labeled test, with the same HLC7 fixes retained.
4. Only after feature tests + HA App device acceptance, prepare a reviewed stable merge and separate formal release; never call a test artifact a formal release.
