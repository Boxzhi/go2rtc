# Luxe Xiaomi Camera Recording RC4 — test integration installation

**Test only.** This candidate is based on production branch `luxe/hlc7` but does not replace the working production go2rtc or original `luxe-camera-wall.js`.

## Contents of the successful GitHub Actions artifact

- `luxe-xiaomi-recording-rc-local-app-amd64.tar.gz`: standalone HAOS App with go2rtc 1.9.15 HLC7 patches plus opt-in server recorder.
- `luxe-camera-wall-recording-rc.js`: small subclass patch that **requires the existing original** `luxe-camera-wall.js` already registered. Adds a **new** card type; does not change the original card's methods.
- `README-luxe-camera-wall-recording-rc.md`: this document.
- `SHA256SUMS.txt`: checksums.

## 1. Install or upgrade the isolated test App

The add-on's local folder / config slug is `luxe_xiaomi_record_rc`, but its **full Supervisor slug** is `local_luxe_xiaomi_record_rc`. Stop the old **RC test App only** (not production go2rtc) before replacing it.

Upload the inner tar.gz to `/config/www/` and use Home Assistant Terminal & SSH:

```bash
tar -xzf /config/www/luxe-xiaomi-recording-rc-local-app-amd64.tar.gz -C /addons/local/
ls -l /addons/local/luxe_xiaomi_record_rc/
```

Refresh Home Assistant Apps. Install/upgrade **Luxe Xiaomi Camera Recording RC** to `1.9.15-luxe.record-rc4`. Keep the three existing RTSP restream source defaults. Keep or enter the existing dedicated test secret (24+ characters). Start RC App. Production go2rtc is not modified.

## 2. Add the new camera-wall test resource

Upload the artifact's `luxe-camera-wall-recording-rc.js` to `/config/www/`.

Settings > Dashboards > Resources: ensure the **original** `luxe-camera-wall.js` resource remains loaded. Add this second JS module:

```yaml
url: /local/luxe-camera-wall-recording-rc.js?v=rc4
type: module
```

**Do not remove the original resource**. Original production cards with `type: custom:luxe-camera-wall` remain unchanged. The new resource only creates a second card type.

Create a new separate test dashboard view/card (do not overwrite the existing monitoring wall):

```yaml
type: custom:luxe-camera-wall-recording-rc
recording_addon_slug: local_luxe_xiaomi_record_rc
cameras:
  - url: jinghuafu_keting_ptz_3k
    muted: true
  - url: jinghuafu_keting_fixed_3k
    muted: true
  - url: jinghuafu_canting_2k
    muted: true
```

The new card inherits the original responsive layout, screenshot tools, native WebRTC controls, audio exclusivity, playing logic and iPad/iPhone sizing. Only its recording implementation is replaced.

## RC4: iPhone background = abort and discard (new rule)

**Requires both RC4 test backend and RC4 camera-wall JS.** Do not use the RC4 JS with RC3 backend: RC3 does not implement `heartbeat` / `abort` endpoints.

### Expected mobile interaction

- Temporary feedback now reads **录像已开始**, **正在生成录像**, **已打开保存**, or **录像已取消**. No permanent pending-save countdown and no save-confirmation controls are reintroduced.
- When the official HA iOS App transitions to the background **while a recording is still in progress**, the test wall immediately restores the original Record icon, stops foreground heartbeat, and sends a best-effort \`keepalive\` abort request. The server **discards** the partially recorded MP4 and never exposes a Save/download action.
- If iOS suspends the WebView before its cancellation request is delivered, the recorder's **server-side foreground lease expires after 12 seconds of missed heartbeats**; a janitor runs every 2 seconds and forces abort. This is a fallback, not an immediate iOS-native background callback; **do not claim zero-delay stop** until actual HA iOS verification.
- An already completed \`ready\` MP4 (or previously \`presented\` MP4) does not count as **actively recording** and is not aborted merely because the user enters background. If explicit abort races with MP4 finalization, the newly finalized MP4 is still discarded. Existing 15-minute \`ready\`/\`presented\` TTL remains in effect.
- iOS native save/share dialogs may affect focus without actually backgrounding the app. RC4 deliberately **does not abort on \`blur\`** alone.
- Leaving/destroying the whole test WebView can also stop its heartbeat; after the lease deadline, the backend may abort unfinished recordings. This is intentional fail-safe behavior for the official iOS App scenario and also applies to other browsers when the JS stops executing.

### Single-device acceptance, before stable release

1. In official Home Assistant iOS App, start a recording and observe REC. Press Home or switch apps *during* recording.
2. Return to HA: the original record icon should be shown; no Save icon; no MP4 file should be made available for that aborted session.
3. Repeat one time with a short recording kept in the foreground and manually stopped: normal MP4 download/share must still work, including video, audio and seeking. The completed MP4 retains its 15-minute TTL (or is cleared when the same camera begins a new recording).
4. If the app's WebView does not receive a background notification, watch server state: no heartbeat should remain; abort must occur automatically after lease expiration and janitor sweep (~12–14 seconds after last renewal in normal conditions).
5. If background was entered while multiple cameras were recording, **all unfinished sessions** belonging to the active card should abort; completed recordings are not treated as unfinished.
6. Only after this real-device regression passes is stable `luxe/hlc7` merge/release appropriate. Do not upgrade the currently stable production `1.9.15-luxe.1` based on simulated CI alone.

## RC3 behavior and upgrade (requires updated backend)

RC3 changes both the **test card JS** and the **isolated recording backend**. Update the test App from RC2 to RC3 before using the RC3 JS; RC2 does not understand the `{action:"presented"}` acknowledgement.

- **Native download launched:** Restore original recording button at once. Do not display a save-confirmation button or an upper-right pending-save countdown.
- **Batch share launched:** Restore the record buttons for every included camera as soon as iOS share is invoked. No confirmation even if the user cancels.
- **Server cache:** Mark each MP4 as `presented`, retain it without extending its original 15-minute expiry, including across test App restarts.
- **Next recording of the same camera:** When a new stream is successfully admitted, delete that camera's previous presented MP4 and session manifest. Other cameras and never-presented ready files are unaffected.
- **Failure to update server:** Restore the camera's Save icon with an error message rather than silently leave a hidden ready MP4.
- Only the **test** Add-on and the **test** camera-wall overlay change. Production HLC7 Add-on and original `luxe-camera-wall.js` remain unchanged.

## RC2 Ingress API path hotfix (no new backend install)

The former RC2 overlay incorrectly treated the add-on `ingress_url` as its API root. Supervisor's `ingress_url` **includes** the configured `ingress_entry` file, so it was generating `.../luxe-xiaomi-recorder-rc.html/api/luxe/recordings/start` and receiving HTML/other content instead of JSON.

The repaired overlay extracts just `/api/hassio_ingress/<token>/` before appending `api/luxe/recordings/start`. It keeps the known full Supervisor slug `local_luxe_xiaomi_record_rc`.

If RC2 is already installed and running, **only replace** `/config/www/luxe-camera-wall-recording-rc.js` with this branch's updated JS file, change the resource query version to `?v=rc2-ingressfix`, and reload the official HA iOS App test view. The recording add-on does **not** need to be reinstalled; do not touch production. The corrected test reports the HTTP status and content type if an Ingress proxy problem remains, without printing credentials or tokenized paths.

## 3. One integrated iPhone acceptance test

1. Open the separate card in the **official** Home Assistant iOS App as HA admin.
2. Start one of the camera previews, click the camera record icon, and enter the separate RC test secret in the card's password dialog. The value is kept in memory, never written into Lovelace YAML. The Supervisor lookup uses the **full local Add-on slug** `local_luxe_xiaomi_record_rc`. Earlier RC2 test cards mistakenly used the short `luxe_xiaomi_record_rc`; correct the YAML to the full slug. The new test JS normalizes the legacy short value for compatibility. This lookup fix changes **only the JS**: you do not need to update the recording back-end App again.
3. Start the other two cameras' recordings (they can be within ~8 s of the first to be treated as one recording group).
4. Stop all three after a short period. If the MP4 readiness timestamps are within 2 seconds, wait for the small **保存 3 份录像** group control and click once. Otherwise the first ready recording automatically opens WKDownload and subsequent completed cameras each show a blue **Save** icon in the usual record-control position.
5. **Immediately upon opening** the native WKDownload or Web Share interface, the corresponding Save button **returns to the original recording icon**, even if the user later cancels the operation. No **已保存**, **确认已保存**, or **待保存 xx:xx** overlay is shown. The App cannot determine whether the MP4 really reached iOS Files.
6. Navigate away or close/reopen the test view while recording: the server should continue recording. The new card refreshes **ready** and **recording** states when reopened, after entering the test key again.
7. Confirm a ready or `presented` MP4 expires **15 minutes after its individual ready timestamp**, even if a share/download dialog was already launched. At expiry its file capability is revoked and the physical MP4 is removed (janitor runs every 10 s). **When the same camera successfully begins a new recording, its previous `presented` MP4 and manifest are removed immediately**; the other cameras' caches are left intact. A `ready` MP4 that has never been offered for download still requires its Save step before new recording can start.
8. For large files, combined sharing is disabled unless each is <=16 MiB and combined <=32 MiB. Use per-camera native WKDownload instead. The server limit is 30 minutes/1 GiB per camera subject to disk budget.

### Known constraints

- Official Home Assistant App provides no reliable native download saved/closed event. **RC3 never requests confirmation**. Issuing a download/share triggers a `presented` state and immediately restores the recording icon. This does **not** claim the file was saved. The actual file is kept on the test App until its 15-minute TTL or a new recording for the same camera, whichever is earlier. Previously saved files in the iPhone Files app are **never** deleted by this server-side cleanup.
- One Web Share action requires an actual user click. Camera-wall RC will prepare small MP4 files in advance but cannot call the native share sheet automatically after an asynchronous recording completion.
- The source wall is the Nov 2 verified 3-camera implementation (`luxe-camera-wall`). The RC patch is a **subclass loaded after the original JS**; review actual deployed card version if newer before a production merge.
- This candidate has automated tests but **has not yet been verified in a physical HA iOS camera-wall session**. The production HLC7 App and original wall must remain unmodified.
- Temporary files physically occupy the HAOS App disk until the independent 15-minute deadline or successful subsequent same-camera recording. Removing an MP4 from the test server cannot delete an already saved video in the iPhone Files app.

## Rollback

Remove only the test card and RC4 resource from your test dashboard, and stop or uninstall the separate RC Add-on. Do not remove the original camera-wall resource or the production `fee2ccf4_luxe_go2rtc` App.
