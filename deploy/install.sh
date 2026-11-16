#!/bin/sh
# Install the kha CLI: the latest stable release by default, KHA_VERSION=0.X.Y
# for a specific one. KHA_RELEASE_BASE_URL overrides the download site
# (GitHub Releases or a mirror).
set -eu

main() {
    base_url=${KHA_RELEASE_BASE_URL:-"https://ha-static.moonshot.cn/kha"}
    release=${KHA_VERSION:-}
    if [ -z "$release" ]; then
        if ! release=$(curl -fsSL "$base_url/stable.txt"); then
            echo "Could not resolve the latest stable release from $base_url/stable.txt" >&2
            echo 'Set KHA_VERSION=0.X.Y to install a specific release.' >&2
            exit 1
        fi
    fi
    release="v${release#v}"
    case "${TEST_UNAME_S:-$(uname -s)}" in
        Darwin) os=darwin ;;
        Linux) os=linux ;;
        *) echo "Unsupported OS: ${TEST_UNAME_S:-$(uname -s)}" >&2; exit 1 ;;
    esac
    case "${TEST_UNAME_M:-$(uname -m)}" in
        x86_64|amd64) arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) echo "Unsupported architecture: ${TEST_UNAME_M:-$(uname -m)}" >&2; exit 1 ;;
    esac
    if command -v sha256sum >/dev/null 2>&1; then
        checksum=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        checksum=shasum
    else
        echo 'Install sha256sum or shasum before running this installer.' >&2
        exit 1
    fi
    install_dir=${KHA_INSTALL_DIR:-"$HOME/.local/bin"}
    mkdir -p "$install_dir"
    install_dir=$(cd "$install_dir" && pwd)
    binary="kha-$os-$arch"
    base="$base_url/$release"
    workdir=$(mktemp -d)
    trap 'rm -rf "$workdir"' 0
    trap 'exit 1' HUP INT TERM
    cd "$workdir"
    curl -fsSL "$base/$binary" -o "$binary"
    curl -fsSL "$base/checksums.txt" -o checksums.txt
    if ! awk -v name="$binary" '$2 == name { print; found++ } END { if (found != 1) exit 1 }' \
        checksums.txt > selected-checksum.txt; then
        echo "Missing or duplicate checksum for $binary" >&2
        exit 1
    fi
    if [ "$checksum" = sha256sum ]; then
        sha256sum -c selected-checksum.txt
    else
        shasum -a 256 -c selected-checksum.txt
    fi
    install -m 755 "$binary" "$install_dir/kha"
    "$install_dir/kha" version
    printf 'Installed to %s\nAdd this directory to PATH: %s\n' "$install_dir/kha" "$install_dir"
}

main "$@"
