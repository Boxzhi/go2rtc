FROM ghcr.io/home-assistant/base:3.24

ARG BUILD_VERSION
LABEL \
  io.hass.version="0.4.0" \
  io.hass.type="app" \
  io.hass.arch="amd64"

RUN apk add --no-cache jq
COPY go2rtc /usr/local/bin/go2rtc
COPY run.sh /run.sh
RUN chmod 0755 /usr/local/bin/go2rtc /run.sh

CMD ["/run.sh"]
