#!/command/with-contenv bashio
set -euo pipefail

OPTIONS="/data/options.json"
if [[ ! -r "${OPTIONS}" ]]; then
  bashio::log.error "POC options are unavailable"
  exit 1
fi

SOURCE_VALUE="$(jq -r '.source // empty' "${OPTIONS}")"
SECRET_VALUE="$(jq -r '.secret // empty' "${OPTIONS}")"

if [[ -z "${SOURCE_VALUE}" ]]; then
  bashio::log.error "Set the production RTSP restream URL in the test app's source option"
  exit 1
fi
if [[ ${#SECRET_VALUE} -lt 24 ]]; then
  bashio::log.error "Choose a test-only secret of at least 24 characters"
  exit 1
fi

# Values are JSON-escaped by jq and can be safely embedded as YAML strings.
SOURCE_JSON="$(jq -c '.source' "${OPTIONS}")"
SECRET_JSON="$(jq -c '.secret' "${OPTIONS}")"

# The test container has its own disposable /tmp. Remove leftovers on restart
# before the Go recording manager creates its own per-boot temporary directory.
find /tmp -maxdepth 1 -type d -name 'luxe-after-stop-poc2-*' -exec rm -rf -- {} +

cat >/tmp/go2rtc-poc2.yaml <<EOF
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
  poc_camera: ${SOURCE_JSON}
luxe_after_stop_poc:
  enabled: true
  secret: ${SECRET_JSON}
EOF
chmod 0600 /tmp/go2rtc-poc2.yaml
bashio::log.info "Starting isolated POC.2; production go2rtc is not modified"
/usr/local/bin/go2rtc -v || true
exec /usr/local/bin/go2rtc -c /tmp/go2rtc-poc2.yaml
