# Luxe Event-Driven Download Queue v0.4.1

Independent Home Assistant OS App to verify that a native WKDownload modal is never overwritten by a later camera's recording completion.

## One integrated test

1. Three existing go2rtc RTSP restreams record independently for 10, 12 and 14 seconds.
2. As soon as the first recording reaches `ready`, the UI automatically triggers its HTTP MP4 WKDownload. It does **not** wait for every camera to finish.
3. If more recordings reach `ready` while the first native sheet remains open, they are **enqueued** on the independent POC server, not presented as overlapping native sheets.
4. After saving the first MP4 and returning, the user can confirm the save and continue one by one, or if the remaining two are ready and small, share them as two independent MP4 files through one explicit `navigator.share({files})` button.
5. Saving is still a user-controlled iOS operation; the official HA App does not expose a JS-native callback certifying an MP4 was saved to Files.
6. The separate Ingress test page re-checks statuses on return (`focus`, `visibilitychange`, `pageshow`) and during regular polling.

## Installation (do not replace production go2rtc or the successful POC.3)

Download the latest **successful** GitHub Actions artifact from branch `feat/luxe-event-driven-download-queue` and extract the inner `luxe-event-driven-queue-local-app-amd64.tar.gz` to `/config/www/` on Home Assistant OS.

Run in the HA Terminal & SSH App:

```bash
tar -xzf /config/www/luxe-event-driven-queue-local-app-amd64.tar.gz -C /addons/local/
ls -lah /addons/local/luxe_event_queue_poc/
```

Refresh Home Assistant Apps and install the **new** local App `Luxe Event-Driven Download Queue POC` (v0.4.1), with separate slug `luxe_event_queue_poc`.
Leave the prefilled RTSP source URLs pointing to the existing Luxe go2rtc HLC7 service; configure a unique test key of at least 24 characters. Never publish that key.

From the **official Home Assistant iOS App**, open the new App's Ingress page. Enter the test key; tap **同时开始三路录像** once. Keep the first native download/saving sheet visible while other streams finish, then save and return. Observe that ready videos are queued, not forced into another simultaneous WKDownload sheet. If the other two are small and prepared, click the one-share option and save both; otherwise continue one-by-one.

Confirm all three MP4 files exist and play correctly. The short-run Share path is capped at 16 MiB per file and 32 MiB total. Backend retains temporary MP4 data for 30 minutes, and files need not be retained permanently on NAS.

## Scope boundaries

- This is one 10/12/14-second test, not 1/5/30-minute stress testing.
- Official HA iOS App is unmodified; no Safari, ZIP of recordings or changes to production go2rtc.
- If an isolated RTSP recording fails, successful completed videos remain individually saveable.
- A full iOS WebView restart may lose in-page queue state; a production version should support server-side batch recovery. A native download/share sheet being dismissed normally is the scenario under test.
- The POC does **not** automatically save video to iOS Files without a user gesture.
