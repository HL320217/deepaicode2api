#!/bin/sh
set -eu

mode=$(printf '%s' "${DEEPAI_GOOGLE_HEADLESS:-false}" | tr '[:upper:]' '[:lower:]')
case "$mode" in
  false|0|f)
    if [ -z "${DISPLAY:-}" ] && [ -z "${WAYLAND_DISPLAY:-}" ]; then
      exec xvfb-run -a -s '-screen 0 1440x900x24 -nolisten tcp' deepaicode2api "$@"
    fi
    ;;
esac

exec deepaicode2api "$@"
