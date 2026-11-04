#!/command/with-contenv bashio
set -euo pipefail
OPTIONS="/data/options.json"
if [[ ! -r "${OPTIONS}" ]]; then
  bashio::log.error "POC.4 configuration is unavailable"
  exit 1
fi
for key in source_1 source_2 source_3; do
  if [[ -z "$(jq -r --arg key "$key" '.[$key] // empty' "$OPTIONS")" ]]; then
    bashio::log.error "POC.4 requires $key"
    exit 1
  fi
done
SECRET_VALUE="$(jq -r '.secret // empty' "$OPTIONS")"
if [[ ${#SECRET_VALUE} -lt 24 ]]; then
  bashio::log.error "POC.4 test-only secret must contain at least 24 characters"
  exit 1
fi
SOURCE1="$(jq -c '.source_1' "$OPTIONS")"
SOURCE2="$(jq -c '.source_2' "$OPTIONS")"
SOURCE3="$(jq -c '.source_3' "$OPTIONS")"
SECRET_JSON="$(jq -c '.secret' "$OPTIONS")"
find /tmp -maxdepth 1 -type d -name 'luxe-after-stop-poc4-*' -exec rm -rf -- {} +
cat >/tmp/go2rtc-poc4.yaml <<EOF
app:
  modules:
    - api
    - rtsp
    - mp4
    - luxe_after_stop_poc
api:
  listen: ":1984"
rtsp:
  listen: ""
streams:
  poc_camera_1: ${SOURCE1}
  poc_camera_2: ${SOURCE2}
  poc_camera_3: ${SOURCE3}
luxe_after_stop_poc:
  enabled: true
  secret: ${SECRET_JSON}
EOF
chmod 0600 /tmp/go2rtc-poc4.yaml
bashio::log.info "Starting isolated POC.4; existing production and POC.2 installations remain untouched"
exec /usr/local/bin/go2rtc -c /tmp/go2rtc-poc4.yaml
