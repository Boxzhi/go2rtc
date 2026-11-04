#!/command/with-contenv bashio
set -euo pipefail
OPTIONS=/data/options.json
test -r "$OPTIONS" || { bashio::log.error "Missing App options";exit 1; }
for index in 1 2 3; do
  v="$(jq -r --arg key "source_$index" '.[$key] // empty' "$OPTIONS")"
  test -n "$v" || { bashio::log.error "Recording RC requires source_$index";exit 1; }
done
SECRET_VALUE="$(jq -r '.secret // empty' "$OPTIONS")"
if [[ ${#SECRET_VALUE} -lt 24 ]]; then
  bashio::log.error "A dedicated recording secret with at least 24 characters is required"
  exit 1
fi
SOURCE1="$(jq -c '.source_1' "$OPTIONS")"
SOURCE2="$(jq -c '.source_2' "$OPTIONS")"
SOURCE3="$(jq -c '.source_3' "$OPTIONS")"
SECRET_JSON="$(jq -c '.secret' "$OPTIONS")"
mkdir -p /data/luxe-recorder
chmod 0700 /data/luxe-recorder
cat > /tmp/go2rtc-luxe-recording-rc.yaml <<EOF
app:
  modules:
    - api
    - rtsp
    - mp4
    - luxe_recording
api:
  listen: ":1984"
rtsp:
  listen: ""
streams:
  jinghuafu_keting_ptz_3k: ${SOURCE1}
  jinghuafu_keting_fixed_3k: ${SOURCE2}
  jinghuafu_canting_2k: ${SOURCE3}
luxe_recording:
  enabled: true
  secret: ${SECRET_JSON}
  directory: "/data/luxe-recorder"
EOF
chmod 0600 /tmp/go2rtc-luxe-recording-rc.yaml
bashio::log.info "Running isolated Luxe Xiaomi Camera Recording RC; production luxe/hlc7 unaffected"
exec /usr/local/bin/go2rtc -c /tmp/go2rtc-luxe-recording-rc.yaml
