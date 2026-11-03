# Luxe POC.2 — after-stop MP4 download (isolated HAOS local app)

**POC ONLY. Do not replace the official go2rtc installation or merge this branch into `luxe/hlc7`.**

## Goal

Verify, in the official App Store Home Assistant iOS app, that a short **real** go2rtc recording can finish on the server before the browser starts downloading its MP4. This follows the successful POC.1 HTTP async download test.

Features implemented on branch `feat/luxe-record-after-stop`:

- One test recording at a time. Duration 3–30 seconds; default 10 seconds.
- File-based MP4 output using the existing go2rtc MP4 consumer, with a 256 MiB per-file cap.
- Status endpoint, manual stop endpoint, short-lived download capability and `Content-Disposition: attachment` for native WKDownload.
- Temporary media files on the **test app container writable filesystem**, cleaned after 15 minutes; leftover POC directories are cleared when the test app starts. No `/media` mount, permanent library, ZIP of recordings or Safari dependency.
- Dedicated secret for API start/stop/status. The front-end does not place the secret into a URL. The download link contains a random per-session capability that expires with the recording.
- Polling-based `ready` detection; once ready, the page automatically executes the same-origin HTTP anchor navigation used by POC.1.
- Live screenshot buttons and the production camera dashboard are untouched.

**Not implemented:** Three-camera recording, thirty-minute duration, automatic saving to Files, an iOS WKDownload completion callback, or large-file reliability. A successful Go compilation is not a live playback test.

## Artifact contents

The GitHub Actions run on this branch generates an artifact ZIP with:

- `luxe-after-stop-poc2-local-app-amd64.tar.gz`: a **separate** Home Assistant OS Local app package
- `go2rtc-record-after-stop-poc-linux-amd64`: standalone static Linux AMD64 test binary
- `SHA256SUMS.txt`

No formal GitHub Release is created. Do not install the standalone binary over the current go2rtc add-on.

## Install as a separate local app on Home Assistant OS

1. Download the newest **successful** Actions artifact from `feat/luxe-record-after-stop`. Extract its outer ZIP on your Mac. Keep the `luxe-after-stop-poc2-local-app-amd64.tar.gz` file intact.
2. Copy the `.tar.gz` to Home Assistant `/share/`, for example through the Samba share app.
3. Open the **Terminal & SSH** app of Home Assistant OS and run:

   ```sh
   tar -xzf /config/www/luxe-after-stop-poc2-local-app-amd64.tar.gz -C /addons/local
   ls -la /addons/local/luxe_after_stop_poc2/
   ```

4. In Home Assistant go to **Settings → Apps → Install app** and choose **Check for updates**. The new app should appear under **Local apps** as `Luxe After-Stop Recording POC.2` (slug `local_luxe_after_stop_poc2`).
5. Install the new test app. In its configuration set:
   - `source`: the **RTSP URL of an existing production go2rtc restream**, e.g. `rtsp://<reachable-production-go2rtc-host>:8554/jinghuafu_canting_2k`. The hostname/IP must be reachable *from the HAOS test app*. Use an authenticated RTSP URL if production requires it. The example is a placeholder, not an asserted current endpoint.
   - `secret`: a unique test-only string at least **24 characters** long. Do not use your Home Assistant access token.
6. Save and start the test app. Confirm the Logs show `POC.2 enabled` and no RTSP errors. Then tap **Open Web UI** from **the official Home Assistant iOS app**, not Safari.
7. On the POC page, input the same test secret, verify source name `poc_camera`, choose 10 seconds and tap **Start recording**. **Do not click the download button.** After the recording completes, look for the HA App native download UI.
8. If WKDownload completes, use the native share button to save the MP4 to the current iPhone, then inspect its video, audio, duration and compatibility. A native `download completed` screen alone is not proof of permanent user storage.

### Why a separate HAOS Local app?

Supervisor installs the POC in a new container with its own slug, network and writable layer. The existing go2rtc add-on stays installed, running and unmodified. The POC does not expose host ports; it uses Home Assistant Ingress (`ingress_port: 1984`) to display the page inside the official app. No new external HTTPS endpoint is needed. Its own RTSP *listener* is disabled; it connects as an RTSP client to the existing production restream instead of opening another Xiaomi connection.

If the current production add-on does not expose a reachable RTSP restream, this must be diagnosed before the real-camera POC can work. Do **not** change the production add-on on the assumption that the example URL is already valid.

The package is specifically Linux AMD64 for the current Intel-based HAOS VM. Other CPU architectures need another build.

### What to report

- The test app's start log and whether it reports `POC.2 enabled`.
- Whether the recording reaches `ready`, with nonzero MP4 bytes.
- Whether the WKDownload Sheet opens **automatically** after about 10 seconds, without another tap.
- Whether MP4 downloading finishes, and whether the saved file plays with sound on iPhone / Mac QuickTime.
- If playback fails: `ffprobe` codec names and codec tags, plus the error/screenshot.

### Uninstall and cleanup

Stop/uninstall only `Luxe After-Stop Recording POC.2` in Settings → Apps. For the local source files, remove only `/local_apps/luxe_after_stop_poc2/`. The existing production go2rtc app, its options, and `luxe/hlc7` source stay untouched. Temporary POC recording files expire automatically within 15 minutes while the app is running; stopping and uninstalling its container removes its writable layer. Files in the HAOS app's container writable layer still occupy temporary VM/NAS disk, **not** an entirely storage-free path.

## Important engineering limitations

- POC.2 uses the existing go2rtc fragmented-MP4 output. HEVC `hvc1` depends on valid stable VPS/SPS/PPS. Native playback is not guaranteed until tested with real HLC7 media.
- If a source selects Opus audio, native QuickTime compatibility requires empirical verification; POC.2 does not silently transcode to AAC.
- No JS API exists for reliable native WKDownload completion / Save-to-Files confirmation. It deliberately launches at most one download; no fixed-delay multi-download queue is implemented.
- This POC's download link uses a random query capability, rather than exposing Home Assistant tokens; treat the link as sensitive and do not share it.
- The HAOS app source config may contain RTSP credentials. Treat configuration and logs as sensitive; do not post real URLs or passwords publicly.

## POC.2 v0.2.1 — 404 ingress fix

The 0.2.0 local app used an ingress_entry with a leading slash. The Supervisor ingress URL constructor already emits a trailing slash after the token, causing a doubled slash. 0.2.1 changes `ingress_entry` to `luxe-record-after-stop-poc2.html` (no leading slash) and tests the embedded HTTP page in CI.

To update an **installed** 0.2.0 local POC app, stop the POC app (not the production go2rtc), unpack the new 0.2.1 package over `/addons/local/luxe_after_stop_poc2` and use the app store **Check for updates** / update route to rebuild the isolated app. The new image version should say **0.2.1**. Do not assume restarting the installed 0.2.0 container refreshes its baked configuration or binary.
