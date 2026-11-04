# Luxe POC.3 — three simultaneous recordings, serial native MP4 downloads

**Status:** experimental. Branch: `feat/luxe-three-record-queue`. POC.2 remains frozen on `feat/luxe-record-after-stop`; production remains on `luxe/hlc7`.

## Purpose and confirmed predecessor

POC.2 was confirmed in the official Home Assistant iOS App: real 10-second H.265/FLAC MP4 recording finalized on the server, WKDownload launched automatically after recording completion, and the saved iPhone movie played with correct image, audio, duration and seek bar.

POC.3 extends the same approach to **three independently saved MP4 files**, with a conservative native download queue.

## Initial scope (0.3.0 short-run gate)

- Three named RTSP restreams: `poc_camera_1` (living room PTZ), `poc_camera_2` (fixed camera), `poc_camera_3` (dining room).
- Starts are dispatched concurrently by the test page; the backend allows up to 3 active sessions on *different* streams. Connection startup latency can create actual start-time skew, to be measured in real tests.
- 5 / 10 / 20 / 30 seconds per session for this gate. **30-minute recording is deliberately not activated yet.**
- Each recording writes a distinct `.mp4.part` temporary server file, renames to `.mp4` after the recording consumer closes, and serves it as a `video/mp4` HTTP attachment with its own short-lived download token.
- Max 256 MiB per file, max 6 retained completed files (two batches); completed recordings are eligible for timed removal after 30 minutes; inactive recordings do not start downloading early.
- All three must finish successfully before the page **automatically starts the first WKDownload**. No ZIP and no Safari.
- **Crucial native App limitation**: website JavaScript does not receive reliable native WKDownload/Save-to-Files completion callbacks. The user must return to the queue, explicitly confirm the current file was saved and proceed to the next file. The UI never starts two WKDownload sheets together, never treats HTTP completion/fixed delays as save confirmation and offers manual retry.
- Camera livestream and production screenshot buttons remain unaffected.
- The separate HAOS local test app installs with its own slug and isolated Docker container, with `boot: manual`, no host port publishing, and the test RTSP *listener* disabled. RTSP restream URLs route to the **existing** production go2rtc, not the Xiaomi private camera protocol.

## Test app installation (not the production go2rtc app)

1. In GitHub Actions on `feat/luxe-three-record-queue`, download the latest **successful** `luxe-three-record-queue-poc3-…` artifact ZIP and extract `luxe-after-stop-poc3-local-app-amd64.tar.gz`.
2. Upload that inner tarball to HA `/config/www/` using your existing upload method.
3. In Terminal & SSH on HAOS:

   ```bash
   tar -xzf /config/www/luxe-after-stop-poc3-local-app-amd64.tar.gz -C /addons/local/
   ls -lah /addons/local/luxe_after_stop_poc3/
   ```

4. In HA Settings → Apps → Install App, refresh app listings and install the **new** `Luxe After-Stop Recording POC.3` app. It is separate from both production `Luxe go2rtc (HLC7)` and the already installed POC.2 app.
5. Configure the three RTSP sources. Defaults assume the confirmed production add-on DNS:
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_keting_ptz_3k`
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_keting_fixed_3k`
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_canting_2k`
   These are environment-specific test defaults. Verify the production feeds before relying on them.
6. Set a dedicated test-only secret of at least 24 characters. Do not share it publicly. Save, start **only** the POC.3 app and confirm its logs report `POC.3 enabled`.
7. In the official HA iOS App (not Safari), open POC.3 via Ingress. Input the same test secret, choose **10 seconds**, and click **同时开始三路录像**.
8. Confirm three sessions become `ready` and show independent MP4 sizes and codecs. The first native download should open automatically. Save it to iPhone, dismiss the share/download sheet, then tap **我已保存，下载下一路** to initiate the second; repeat for the third.
9. Verify three **separate** MP4 files play correctly with audio, timestamps and seek controls. Retry must not overwrite other recordings.

## Next gates before any formal release

- **0.3.1:** independently verify actual concurrent startup timing and 3x10-second complete playable MP4, failure/cancellation/retry/no interfering download sheets.
- **0.3.2:** large-file and longer-duration stress, filesystem free-space/reservation limits, failure-safe cleanup, expiration semantics, POC app restart behavior.
- **0.3.3:** only after space and codec testing, raise per-recording maximum towards 30 minutes; verify total three-stream resource impact on HAOS, QuickTime compatibility and iOS download reliability.
- **Production:** regression test all HLC7 fixes and screens, then implement appropriate frontend integration. **No** automatic third-party iOS App modifications, ZIP download, or direct merge to `luxe/hlc7` without acceptance.

## Caveats

- This is a preliminary functional POC and does not yet measure startup timestamp skew or total disk resource reservation.
- Go unit and build success alone do not prove real three-camera source reliability.
- The server does temporarily write MP4 files to container disk (on HAOS/NAS-backed VM storage), then expires them. This is not a permanent recording library.
- A user-declared save acknowledgment is **not** a verifiable native filesystem write result. Never release server files solely on that claim until the intended retry behavior is verified.

## POC.3 v0.3.1 — partial success and RTSP timeout diagnostics

The first real three-camera test on the official HA iOS App showed:
- Living room PTZ: a recording session was created with H.265 / Opus.
- Dining room: a recording session was created with H.265 / FLAC.
- Living room fixed: RTSP TCP read timed out against the **confirmed** production add-on DNS/port `fee2ccf4-luxe-go2rtc:8554`.
- The 0.3.0 page refused to download any recording whenever one failed to start, even if others finished successfully.

The original screenshots confirm two sessions were established, **not** that the two MP4 files were ready and playable. Do not claim POC.3 success until all three files have been separately validated.

Changes in 0.3.1:

1. Start mode selector: `三路同时启动` (baseline), `仅录制客厅固定` (isolates failing stream), `三路错峰启动` (start requests 3 seconds apart to probe concurrent connection pressure).
2. Successful, finalized recordings can enter a **partial** native download queue, with explicit counts such as `2/3`; a failed camera is never silently considered successful.
3. Queue labels use the actual media source, not an assumed list index, to avoid mislabeling files when one is missing.
4. The first successful file is requested automatically once this batch has finalized. For the next file, native save confirmation still requires a **real user action**. There is no arbitrary timer or fabricated iOS download callback.
5. CI additionally checks inline test-page JavaScript syntax and the new diagnostic controls.

### Validation order (do not change the production add-on)

- Update only the separate `Luxe After-Stop Recording POC.3` local app to **0.3.1** using the new isolated Actions artifact. POC.2 and production `Luxe go2rtc (HLC7)` remain unchanged.
- First test **only `客厅固定`**, 10 seconds. If this times out even alone, diagnose the upstream fixed camera RTSP source and production go2rtc logs. Do not attribute it to simultaneous starts.
- If it works alone, select **three cameras, staggered by 3 seconds**. Observe whether all three reach `ready` and are playable.
- Finally rerun **all three simultaneously** to compare. The test page must still let users save any independently successful recordings if the third fails again.
- Record each source's startup result and MP4 codec/size. Do not mistake a `recording` state for `ready`.
- If the fixed camera fails even when alone, inspect the production add-on's **specific** RTSP/server log and whether the fixed video preview works; no production changes are justified yet.

The earlier 0.3.0 build should remain archived as a reference, not be installed over the production go2rtc.
