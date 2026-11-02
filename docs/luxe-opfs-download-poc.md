# Luxe OPFS streaming download POC

This is a client-side proof of concept only. It does not call the Recording Manager API, connect to a camera, or change the current native-download recording flow.

## Purpose

Verify the browser-side building blocks required by the next Recording Manager design:

1. Three independent Dedicated Workers can write separate files to the current device's Origin Private File System (OPFS).
2. A Service Worker can return an OPFS file as a chunked HTTP attachment response, while a Dedicated Worker performs the actual OPFS reads.
3. The browser can receive the response as a native download without constructing a Blob, creating an object URL, or loading the complete file into JavaScript memory.
4. The page can recover temporary OPFS files after reload and let the user retry the download or delete them.

## Files

- www/recording-opfs-poc.html: isolated test UI.
- www/recording-opfs-poc.js: capability detection, Worker orchestration, download trigger and cleanup.
- www/recording-opfs-worker.js: writes deterministic test data using FileSystemSyncAccessHandle in a Dedicated Worker.
- www/recording-opfs-sw.js: exposes a streaming attachment response and coordinates the read worker through MessageChannel.

The Service Worker does not call getFile() or read OPFS itself. It sends a MessagePort to the initiating page; that page starts a Dedicated Worker, which reads OPFS through FileSystemSyncAccessHandle and transfers one bounded chunk at a time.

The existing recording-poc.html links to this page. The add-on's Ingress entry remains unchanged.

## Test procedure

1. Open the existing Recording POC Ingress page.
2. Open the independent OPFS streaming download test.
3. Confirm the page reports HTTPS secure context, OPFS, Dedicated Worker and Service Worker support.
4. Confirm Service Worker registration and page control succeeded. If registration succeeds but the page is not controlled, reload this page and run detection again.
5. Select 8, 16, 32 or 64 MiB per file.
6. Click the three-way concurrent write button. Three independent workers write separate deterministic .bin files. The UI reports progress, final size and CRC32.
7. Once a row is ready, click its download button. The Service Worker returns the OPFS file as a chunked attachment stream. Test one download first, then try all three.
8. Confirm the browser's download manager receives the file and the downloaded file size matches the size shown in the POC.
9. Only after confirming the download is saved, click the cleanup button. A cancelled or failed download leaves the OPFS file available for retry.
10. Reload the page once to verify that leftover OPFS files are detected and can be recovered or deleted.

## Important semantics

- The test data is synthetic .bin content, not a playable MP4. This POC validates local storage and browser download transport only; actual camera MP4 compatibility is a later integration test.
- OPFS reads use FileSystemSyncAccessHandle inside a Dedicated Worker. Chunks cross to the Service Worker through a transferred MessagePort; neither File nor Blob objects are created, and the complete file is never buffered in JavaScript memory.
- A Service Worker stream reaching EOF means the browser consumed the response body. It is not a guaranteed operating-system file-saved callback. For that reason this POC deliberately requires manual cleanup after the user verifies the download.
- OPFS consumes storage on the current browser/device. It does not write video data to the NAS, Home Assistant, or go2rtc server.
- Browser storage quotas, eviction, service-worker behavior and downloads inside Home Assistant's iOS WKWebView must be tested on real devices. Desktop CI cannot prove these behaviors.
- The POC does not guarantee recording continues while iOS is locked or the app is backgrounded.

## Acceptance matrix

| Environment | OPFS write | Service Worker control | Attachment download | Three concurrent downloads |
|---|---|---|---|---|
| Mac Chrome | Device test | Device test | Device test | Device test |
| Mac Safari | Device test | Device test | Device test | Device test |
| iPhone Safari | Device test | Device test | Device test | Device test |
| Home Assistant iOS App | Device test | Device test | Device test | Device test |
| iPad Safari | Device test | Device test | Device test | Device test |

No production Recording Manager behavior is changed by this POC.
