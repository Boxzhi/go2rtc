# Luxe direct streaming recording POC

Experimental code, based on HLC7 commit `d80b1a584a12dac318baa8316eb6ef975e645649`.
This branch is **not installed** by the Home Assistant add-on and does not change the currently running go2rtc.

## Design

Each recording is a separate GET response with `Content-Disposition: attachment`.
The Go server muxes video/audio directly into the response: **no server recording file, no NAS/HA media directory, no FFmpeg or GPAC**.
A recording has a unique ID and a separately generated random secret used for status/stop requests.
Three different configured source names may record simultaneously, with a maximum of 1800 seconds per recording.
Recordings use MP4 fragments from the existing native muxer. The resulting download and HEVC/Opus compatibility must be tested on the target client.

## API POC

- `GET /api/luxe/recording.mp4?src=<configured-name>&id=<id>&token=<secret>&duration=15&mp4=all&filename=test.mp4` – stream download.
- `GET /api/luxe/recordings/status?id=<id>&token=<secret>` – JSON status.
- `POST /api/luxe/recordings/stop?id=<id>&token=<secret>` – request graceful stream termination.

Generate an unpredictable 16+ character ASCII id and a cryptographically random secret (32+ characters recommended) for each recording. Do not reuse them.
Start by requesting the GET download in the browser, then POST the stop request from an authenticated client before the timeout.
The stream may have no Content-Length: the browser controls its download UI and location. An HTTP 200 alone does not prove a valid, locally saved MP4.

```bash
# Example using a fresh random ID and secret. Requires access to the go2rtc HTTP API.
ID="$(openssl rand -hex 16)"
TOKEN="$(openssl rand -hex 32)"
BASE="http://localhost:1984"
curl --fail --no-buffer -o test.mp4 "$BASE/api/luxe/recording.mp4?src=jinghuafu_keting_ptz_3k&id=$ID&token=$TOKEN&duration=15&mp4=all&filename=test.mp4"
# Run stop from another terminal while the command above is recording:
# curl -X POST "$BASE/api/luxe/recordings/stop?id=$ID&token=$TOKEN"
```

## Acceptance gates before production

1. Go unit tests and build succeed.
2. Confirm MP4 ends cleanly on explicit stop, timeout, request abort and HLC7 source disconnect.
3. Confirm all three downloads run concurrently and can stop independently.
4. Confirm Apple Safari / HA iOS App will start, continue and complete an indefinite-length attachment behind Ingress; **not yet verified**.
5. Confirm resulting HEVC tracks are valid hvc1 where applicable and test source audio compatibility with QuickTime.
6. Verify the URL tokens cannot be exposed in access logs and that go2rtc API is authenticated or restricted by network. Current API security is unchanged; do not expose port 1984 publicly.

Do not switch the add-on's pinned `GO2RTC_REF` until these tests pass.
