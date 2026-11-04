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
2. One camera transitions `idle → recording → finalizing → ready` without blocking another camera. A per-camera button changes from Record to **Save** when an MP4 is complete. **Do not display an expiry countdown or upper-right pending-save badge** in the camera wall.
3. Group starts: a **2-second coalescing window** begins at the first MP4 reaching `ready`. If all expected cameras reach `ready` inside the window, offer **one user-triggered share for all** when files pass the WebView size guard. Do not require all streams to finish when they are significantly staggered.
4. Otherwise the first completed MP4 can immediately start the previously proven native WKDownload after the coalescing interval. Further MP4s completed while an iOS native sheet may be open remain `ready` and expose their own **Save** button. Do not open another native sheet automatically.
5. Never infer that a native sheet has closed or that Files was saved from `blur`, `focus`, `visibilitychange`, `pageshow`, timeout or a Web Share promise. The official HA App does **not** expose such a web callback.
6. When more than one MP4 is pending, offer one explicit Web Share action for small files if `navigator.canShare({files})` allows it; otherwise save each recording with its own native WKDownload. Do not create video ZIPs or open Safari.
7. As soon as the native WKDownload or user-triggered Web Share dialog is **invoked**, immediately restore the camera button to `idle` (the original Record icon) regardless of whether iOS subsequently saves or cancels. Do **not** show an 已保存/确认已保存 control. The backend records `presented` but does not claim the file was saved or delete the MP4 at this point.
8. A page reload / reconnection reconstructs recording and unpresented `ready` sessions from server metadata. `presented` sessions are retained in the backend but hidden from the Save UI even after a reload. File links are time-bounded capabilities; never disclose secrets in logs.
9. **Independent 15-minute retention starts at each MP4's `ready` timestamp**. Opening a download/share dialog does not extend the TTL or remove the file. If the same camera successfully starts a new recording, immediately delete that camera's prior `presented` MP4 and manifest; do not delete other cameras' caches or local iPhone saved files. Otherwise expiry revokes all new downloads and removes the MP4 automatically. A download initiated before expiry may receive bounded completion grace.
10. Expired, corrupt or deleted MP4s must never be presented as `ready`. A canceled system share after its dialog was invoked still leaves the camera button idle and marks its server file `presented`; no automatic saved-result detection is possible. Retention is enforced by the Go backend even if the frontend is closed. Handle partial files after crashes and failed jobs.
11. Protect storage: maximum three concurrent recording consumers, per-stream / total byte budget, HAOS disk-free reserve, partial-file cleanup, recoverable failed / timeout paths, no unbounded client memory allocation. **Do not remove capacity checks to enable 30-minute recording.**
12. Preserve HEVC, HLC7 HD SPS/VPS/PPS improvements, and H.265/Opus and H.265/FLAC playback / MP4 compatibility already tested. Run existing codec tests on every candidate build.

## Proposed stable API (names reserved; implementation pending)

- `POST /api/luxe/recordings` — start one named stream; returns a stable session id.
- `POST /api/luxe/recordings/{id}/stop` — stop and finalize.
- `GET /api/luxe/recordings` — authenticated listing to recover active and ready sessions, camera mapping, sizes, codec metadata, `ready_at`, `expires_at`.
- `GET /api/luxe/recordings/{id}` — recording progress/state.
- `GET /api/luxe/recordings/{id}/file?token=...` — origin-bound/authorized download, with `attachment` and Range support for native HA WKDownload; do not issue a live streaming URL.
- `POST /api/luxe/recordings/ack/{id}` — `{action:"presented"}` acknowledges opening a download/share dialog only, persists hidden state without deleting bytes or extending TTL; legacy `saved` and `discard` actions remain available for explicit management. It is not an iOS native callback.
- The server must sanitize filename, reject traversal, redact tokens and never expose host filesystem paths.

## Required automated tests and real-device acceptance

- Three sources recording concurrently without the global session lock covering network negotiation.
- Finished sources with 0s/1s/2s difference: group share offered once; differences beyond 2s: the first file is ready independently, later files remain in per-camera save state.
- Returning while another native download sheet is open does **not** trigger a second native sheet.
- Partial failure: successful MP4s remain downloadable; failed session does not block all.
- User cancels share/download after dialog invocation: the record icon is already reset and no confirmation is shown; `presented` MP4 remains on the server until its original 15-minute expiry or a successful new same-camera recording. New recording must not purge a different camera's cache.
- 15-minute expiry starts at `ready_at` independently. At/after expiration: new GETs denied, temporary file removed, session no longer appears downloadable, UI returns to record.
- Multiple ready files with large sizes: no massive multi-Blob loading in WKWebView; revert to one-file downloads.
- Verified successful HEVC/Opus and HEVC/FLAC MP4 with audio, duration and seekability on stock Home Assistant iOS App.
- Test-install procedure must have rollback to the prior `1.9.15-luxe.1` production addon and no production mutation.

## Implemented as the first independent RC build

The backend test module now exists in `internal/luxerecord` and is disabled unless explicitly enabled with `luxe_recording.enabled: true`. Compared with early POC.3 it adds:

- Up to three simultaneous recorded MP4 streams with audio; 3–1800 seconds per recording; per-file size bounded to 1 GiB; total 3 GiB active reservation + completed bytes budget, with a separate 1 GiB HAOS free disk reserve.
- Ready timestamp starts an independent **exact 15-minute capability validity window**. Every API request prunes expired sessions; the background janitor deletes physical files on a 10-second interval.
- Stable per-file MP4 and JSON manifest on the **test App's** persistent /data partition, restored after test App restart if and only if the recording was completely finalized and has not expired.
- Authorization header and expiring per-video download capability, native WKDownload attachment and HTTP Range support, plus a `presented` acknowledgement preserving the MP4 for its original 15-minute TTL. Legacy `saved/discard` remain explicit cleanup actions.
- Small-file Web Share and 2-second per-group coalescing in the **test UI**; a completed first camera is otherwise downloaded natively without waiting for the other cameras, and subsequent cameras expose individual Save buttons.

**Status and boundaries**: CI-Go tests, JS syntax and built-in HTTP page checks are possible without iPhone; they are **not** substitutes for a three-camera official Home Assistant iOS App test. The production camera wall JS has not yet been replaced: the test App supplies an isolated test UI at Ingress. The old `luxe/hlc7` running App must remain unchanged.

### Install isolated HAOS recording candidate

1. Download the most recent **successful** Actions artifact on branch `feat/luxe-xiaomi-recording-release-candidate` and extract the inner file `luxe-xiaomi-recording-rc-local-app-amd64.tar.gz`.
2. Upload the inner tar.gz to Home Assistant's `/config/www/` via the available upload method.
3. In the HA Terminal & SSH App run:

   ```bash
   tar -xzf /config/www/luxe-xiaomi-recording-rc-local-app-amd64.tar.gz -C /addons/local/
   ls -lah /addons/local/luxe_xiaomi_record_rc/
   ```

4. Settings → Apps → Install App → Check updates. Install the **new** `Luxe Xiaomi Camera Recording RC`, slug `luxe_xiaomi_record_rc`. This is a different App from `fee2ccf4_luxe_go2rtc`.
5. Confirm the three prefilled RTSP restream URLs point to the already-running production go2rtc. Set a dedicated 24+ character secret, save and start **only** the new RC App. Do not share the key or its unredacted log.
6. In the stock Home Assistant iOS App, open the new RC App's Ingress UI. Enter the same test key. Select 10 seconds and start three streams concurrently; check independent MP4 readiness, 2-second share coalescing, the original WKDownload sheet, and each per-camera "Save" button.
7. A Save state ends immediately when a native download or share dialog is invoked. The button returns to Record without a confirmation or expiry badge. The server retains the hidden MP4 for up to 15 minutes from its finalization, deleting it early when that same camera successfully starts recording again. The 30-minute recording duration is enabled but still requires real-device endurance acceptance.
8. After the RC test, stop/uninstall **only** the RC App to roll back. The existing production HLC7 App remains running and unmodified.

**Potential storage tradeoff:** the recordings are not saved in a permanent NAS library, but during the up-to-15-minute cache they physically occupy temporary HAOS App storage (the VM is hosted on NAS). Completing a recording requires temporary disk space; one cannot deliver a server-side finalized large MP4 without temporary bytes somewhere.

### Actual test API

- `POST api/luxe/recordings/start` with JSON {source,group,duration_seconds}
- `GET api/luxe/recordings/` restores recordings from server metadata
- `GET api/luxe/recordings/status/{id}` gets individual status and expiration
- `POST api/luxe/recordings/stop/{id}` finalizes the selected camera
- `GET api/luxe/recordings/file/{id}?token=...` native attachment and Range
- `POST api/luxe/recordings/ack/{id}` with {action:"presented"} — mark download/share launched, keep cached video and hide Save UI; legacy {action:"saved"}/{action:"discard"} delete the file on explicit request

## Delivery gates

1. New standalone **integrated feature branch** from `luxe/hlc7` (not previous POC branches).
2. Implement Go recording subsystem, tests, secure temporary download/cleanup and an integration interface for the actual `luxe-camera-wall.js` card.
3. Build a separately installable test addon / CI artifact clearly labeled test, with the same HLC7 fixes retained.
4. Only after feature tests + HA App device acceptance, prepare a reviewed stable merge and separate formal release; never call a test artifact a formal release.
