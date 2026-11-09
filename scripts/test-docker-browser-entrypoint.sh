#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

cat > "$test_dir/deepaicode2api" <<'EOF'
#!/bin/sh
printf 'display=%s\n' "${DISPLAY:-}"
printf 'arg=%s\n' "$@"
exit "${TEST_EXIT_CODE:-0}"
EOF
cat > "$test_dir/xvfb-run" <<'EOF'
#!/bin/sh
test "$1" = '-a'
test "$2" = '-s'
test "$3" = '-screen 0 1440x900x24 -nolisten tcp'
shift 3
export DISPLAY=:test-xvfb
exec "$@"
EOF
chmod +x "$test_dir/deepaicode2api" "$test_dir/xvfb-run"
export PATH="$test_dir:$PATH"
unset WAYLAND_DISPLAY DISPLAY

actual=$(DEEPAI_GOOGLE_HEADLESS=false sh "$script_dir/docker-browser-entrypoint.sh" -data '/test path/state.json')
expected=$(printf 'display=:test-xvfb\narg=-data\narg=/test path/state.json')
test "$actual" = "$expected"

actual=$(DEEPAI_GOOGLE_HEADLESS=true sh "$script_dir/docker-browser-entrypoint.sh" -version)
expected=$(printf 'display=\narg=-version')
test "$actual" = "$expected"

actual=$(DISPLAY=:existing DEEPAI_GOOGLE_HEADLESS=false sh "$script_dir/docker-browser-entrypoint.sh" -version)
expected=$(printf 'display=:existing\narg=-version')
test "$actual" = "$expected"

set +e
TEST_EXIT_CODE=17 DEEPAI_GOOGLE_HEADLESS=false sh "$script_dir/docker-browser-entrypoint.sh" >/dev/null
status=$?
set -e
test "$status" -eq 17
printf 'Docker browser entrypoint dispatch, arguments and exit status passed.\n'
