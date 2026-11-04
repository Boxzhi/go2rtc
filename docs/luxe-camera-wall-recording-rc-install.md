# Luxe Xiaomi Camera Recording RC2 — test integration installation

**Test only.** This candidate is based on production branch `luxe/hlc7` but does not replace the working production go2rtc or original `luxe-camera-wall.js`.

## Contents of the successful GitHub Actions artifact

- `luxe-xiaomi-recording-rc-local-app-amd64.tar.gz`: standalone HAOS App with go2rtc 1.9.15 HLC7 patches plus opt-in server recorder.
- `luxe-camera-wall-recording-rc.js`: small subclass patch that **requires the existing original** `luxe-camera-wall.js` already registered. Adds a **new** card type; does not change the original card's methods.
- `README-luxe-camera-wall-recording-rc.md`: this document.
- `SHA256SUMS.txt`: checksums.

## 1. Install or upgrade the isolated test App

The add-on slug remains `luxe_xiaomi_record_rc`. Stop the old **RC test App only** (not production go2rtc) before replacing it.

Upload the inner tar.gz to `/config/www/` and use Home Assistant Terminal & SSH:

```bash
tar -xzf /config/www/luxe-xiaomi-recording-rc-local-app-amd64.tar.gz -C /addons/local/
ls -l /addons/local/luxe_xiaomi_record_rc/
```

Refresh Home Assistant Apps. Install/upgrade **Luxe Xiaomi Camera Recording RC** to `1.9.15-luxe.record-rc2`. Keep the three existing RTSP restream source defaults. Keep or enter the existing dedicated test secret (24+ characters). Start RC App. Production go2rtc is not modified.

## 2. Add the new camera-wall test resource

Upload the artifact's `luxe-camera-wall-recording-rc.js` to `/config/www/`.

Settings > Dashboards > Resources: ensure the **original** `luxe-camera-wall.js` resource remains loaded. Add this second JS module:

```yaml
url: /local/luxe-camera-wall-recording-rc.js?v=rc2
type: module
```

**Do not remove the original resource**. Original production cards with `type: custom:luxe-camera-wall` remain unchanged. The new resource only creates a second card type.

Create a new separate test dashboard view/card (do not overwrite the existing monitoring wall):

```yaml
type: custom:luxe-camera-wall-recording-rc
recording_addon_slug: luxe_xiaomi_record_rc
cameras:
  - url: jinghuafu_keting_ptz_3k
    muted: true
  - url: jinghuafu_keting_fixed_3k
    muted: true
  - url: jinghuafu_canting_2k
    muted: true
```

The new card inherits the original responsive layout, screenshot tools, native WebRTC controls, audio exclusivity, playing logic and iPad/iPhone sizing. Only its recording implementation is replaced.

## 3. One integrated iPhone acceptance test

1. Open the separate card in the **official** Home Assistant iOS App as HA admin.
2. Start one of the camera previews, click the camera record icon, and enter the separate RC test secret in the card's password dialog. The value is kept in memory, never written into Lovelace YAML. If the RC Add-on cannot be located using its short slug, look up its **actual full slug** under the HA App's Info page and set `recording_addon_slug` to that value.
3. Start the other two cameras' recordings (they can be within ~8 s of the first to be treated as one recording group).
4. Stop all three after a short period. If the MP4 readiness timestamps are within 2 seconds, wait for the small **保存 3 份录像** group control and click once. Otherwise the first ready recording automatically opens WKDownload and subsequent completed cameras each show a blue **Save** icon in the usual record-control position.
5. After saving a file, tap its **已保存** confirmation control to remove that individual server cache and reset its recording icon. **Do not tap the confirmation when a native download is canceled**. Confirming a Web Share Promise is not proof of writing to Files.
6. Navigate away or close/reopen the test view while recording: the server should continue recording. The new card refreshes **ready** and **recording** states when reopened, after entering the test key again.
7. Confirm a ready MP4 expires **15 minutes after its individual ready timestamp**. Its Save action must disappear and its file endpoint must reject any *new* download. Janitor removes the stale MP4 within 10 s after expiry; first API request may trigger earlier cleanup.
8. For large files, combined sharing is disabled unless each is <=16 MiB and combined <=32 MiB. Use per-camera native WKDownload instead. The server limit is 30 minutes/1 GiB per camera subject to disk budget.

### Known constraints

- Official Home Assistant App provides no reliable native download saved/closed event. The UI never blindly auto-opens a second native sheet and requires user acknowledgement to release ready files.
- One Web Share action requires an actual user click. Camera-wall RC will prepare small MP4 files in advance but cannot call the native share sheet automatically after an asynchronous recording completion.
- The source wall is the Nov 2 verified 3-camera implementation (`luxe-camera-wall`). The RC patch is a **subclass loaded after the original JS**; review actual deployed card version if newer before a production merge.
- This candidate has automated tests but **has not yet been verified in a physical HA iOS camera-wall session**. The production HLC7 App and original wall must remain unmodified.
- Temporary files physically occupy the HAOS App disk until user acknowledgement or 15-minute expiry; the backup setting for the test App is separate from a permanent NAS video library.

## Rollback

Remove only the test card and RC2 resource from your test dashboard, and stop or uninstall the separate RC Add-on. Do not remove the original camera-wall resource or the production `fee2ccf4_luxe_go2rtc` App.
