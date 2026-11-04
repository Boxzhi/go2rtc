# Luxe Three MP4 One Share POC — one end-to-end run

## Goal

One real-life three-camera test in the official App Store Home Assistant iOS App:

- Use existing validated go2rtc POC.3 server recording implementation.
- Start three independent recordings of 10 seconds each.
- Allow recording / MP4 finalization to differ by 1–2 seconds (or more). The front-end uses a **per-session completion barrier**: all three sessions must have state `ready` before requesting any MP4 bytes for one-share.
- Once the last file is ready, fetch all three full MP4 files individually with HTTP, verify exact byte count and MP4 header, create three `File` objects and enable one **user-triggered** `navigator.share({files})` button.
- User makes one share-sheet selection to save the three individual MP4 files to iPhone Files. Not a ZIP and no external Safari.
- Native WKDownload remains a manual **per-file fallback** if multi-file preparation or share fails.

This intentionally does **not** silently save files without a user gesture. iOS Web Share API requires a user gesture and its resolved promise is not a filesystem write acknowledgment.

## Important test limits

- POC only: **10 seconds × 3**; no 1-minute/5-minute/30-minute stress run, per the request for a single integrated test.
- Max 24 MiB per file, 48 MiB total in the WebView memory. The client refuses to load larger files into one-share, but may still use the existing per-file WKDownload fallback. This is a guardrail for this short POC, **not** a demonstrated safe RAM limit for 30-minute videos.
- The server still temporarily stores each independently finalized MP4 in the test container for its pre-existing 30-minute TTL, not persistently on a recording library.
- A missing/failed stream is not silently omitted. The page identifies the source; successfully finalized files may be downloaded separately, but it does not claim three-file success or share an incomplete set.
- Each MP4 can differ in codec; the successful POC.3 recorded H.265 / Opus on living-room feeds and H.265 / FLAC on dining room.

## Install as **separate** HAOS local test App

1. Open the latest **successful** Actions run from `feat/luxe-three-mp4-one-share`. Download `luxe-three-mp4-one-share-…` artifact ZIP. Extract `luxe-three-mp4-one-share-local-app-amd64.tar.gz`.
2. Upload this inner tarball to HA `/config/www/`.
3. In Home Assistant Terminal & SSH run:

   ```bash
   tar -xzf /config/www/luxe-three-mp4-one-share-local-app-amd64.tar.gz -C /addons/local/
   ls -lah /addons/local/luxe_three_mp4_share_poc/
   ```

4. In HA **Settings → Apps** refresh the local App list and install the **new** `Luxe Three MP4 One Share POC` (version `0.4.0`). This is not an update to production Luxe go2rtc, POC.2, or POC.3.
5. Verify its prefilled three RTSP source addresses. These were verified for this HAOS environment when using the existing `fee2ccf4-luxe-go2rtc` service:
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_keting_ptz_3k`
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_keting_fixed_3k`
   - `rtsp://fee2ccf4-luxe-go2rtc:8554/jinghuafu_canting_2k`
6. Set a unique test secret of at least 24 characters; it can be the previously used POC.3 test secret but should not be pasted into public logs or screenshots. Start only the independent One Share test App.
7. In the **official** Home Assistant iOS App, open its Ingress Web UI. Enter the secret and click **同时开始三路 10 秒录像** **once**. Keep the page open.
8. Check three different MP4 status rows. For example, `ready` may occur at 10:00:10, 10:00:11, and 10:00:12. No `File` sharing is enabled until the **last** reaches ready. Each file is then fetched from its own secure download capability URL and checked.
9. When the button enables, tap **一次分享 3 个 MP4** and choose **存储到“文件”** from iOS. Confirm exactly three distinct MP4 files in the Files app and verify playback.
10. Send screenshots of the three `ready` statuses and of the share sheet or final summary. The only claim of saved media comes from verification in the Files app, not from a `share()` promise.

## If one recording or share fails

- First open the POC test page and note the exact failed camera. A server-side recording error or RTSP timeout does not mean multi-share is broken.
- The page will **not** share only two files while claiming a set of three. It exposes separate per-camera WKDownload fallback buttons for any successfully finalized MP4.
- A 90-second watchdog avoids waiting forever for a status. There is no fixed 1–2-second delay used as a download trigger.
- If preparing the MP4 objects fails due to fetch/byte count/memory cap/canShare, the successful MP4 files remain on the test server until normal TTL expiry for independent retry. Do not immediately repeat the three-camera recording just because sharing failed.
- During large-file / 30-minute design, do not preload all three full files into JS memory without dedicated memory/resource analysis.

## Source and validation

- Branch: `feat/luxe-three-mp4-one-share`, created from tested POC.3 build SHA `cee418f8d55490b8fdab2e815092abebcee2bd7c`.
- Only a new HTML/JavaScript test page, distinct HAOS local test App and CI checks are added. Existing recording implementation and production go2rtc remain unchanged.
