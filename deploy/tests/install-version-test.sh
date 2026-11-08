#!/bin/bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

cat > "$TEMP_DIR/xy2api" <<'EOF'
#!/bin/bash
case "$VERSION_CASE" in
    valid) printf 'XY2API 0.5.10 (Sub2API compatibility: 0.2.8)\n' ;;
    empty) : ;;
    no_version) printf 'version unavailable\n' ;;
    failed) exit 7 ;;
esac
EOF
chmod +x "$TEMP_DIR/xy2api"

for mode in plain strict; do
    for version_case in valid empty no_version failed; do
        expected=unknown
        [[ "$version_case" == valid ]] && expected=0.5.10
        rc=0
        output=$(VERSION_CASE="$version_case" bash -c '
            set -e
            if [[ "$1" == strict ]]; then set -uo pipefail; fi
            source <(head -n -1 "$2")
            INSTALL_DIR=$3
            print_info() { printf "%s\n" "$*"; }
            msg() { printf "%s" "$1"; }
            systemctl() { echo reached_systemctl; exit 99; }
            upgrade
        ' bash "$mode" "$ROOT_DIR/deploy/install.sh" "$TEMP_DIR") || rc=$?
        if [[ "$rc" != 99 ]] || ! grep -Fxq "current_version: $expected" <<< "$output" || ! grep -Fxq reached_systemctl <<< "$output"; then
            printf 'FAIL mode=%s case=%s expected=%s rc=%s output=%s\n' "$mode" "$version_case" "$expected" "$rc" "$output" >&2
            exit 1
        fi
    done
done
echo 'install version fallback checks passed'
