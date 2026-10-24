#!/usr/bin/env bash
# Builds the coachd Connect IQ watch app.
#
#   COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh        # bin/coachd.prg
#   COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test   # bin/coachd-test.prg, with the (:test) suite
#
# COACHD_PUBLIC_URL (https, required) is compiled into source/Config.mc.
# CIQ_SDK points at the Connect IQ SDK (default ~/.cache/connectiq/sdk-9.3.0).
# CIQ_DEVICE picks the device (fenix7spro or fenix6pro). A release build
# targets fenix7spro and needs its device file; it builds for fenix6pro (a
# compile check: that .prg does not run on the 7S Pro) only when
# CIQ_DEVICE=fenix6pro is set. A test build falls back to fenix6pro on its own.
# The developer key is <repo>/.secrets/ciq-developer.der; it is created on the
# first build and signs every later build, so keep it.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
sdk="${CIQ_SDK:-$HOME/.cache/connectiq/sdk-9.3.0}"
key="$root/.secrets/ciq-developer.der"
devices="$HOME/Library/Application Support/Garmin/ConnectIQ/Devices"

die() {
	echo "build.sh: $*" >&2
	exit 2
}

case "${1:-}" in
"") mode="release" ;;
test) mode="test" ;;
*) die "usage: build.sh [test]" ;;
esac
[[ $# -le 1 ]] || die "usage: build.sh [test]"

# The URL ends up inside a Monkey C string literal: allow only plain URL
# characters, and drop trailing slashes (paths are appended as "/device/v1/...").
url="${COACHD_PUBLIC_URL:-}"
while [[ "$url" == */ ]]; do url="${url%/}"; done
[[ "$url" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]*)?$ ]] ||
	die "COACHD_PUBLIC_URL must be an https URL such as https://coachd.example.com"

[[ -x "$sdk/bin/monkeyc" ]] || die "no Connect IQ SDK at $sdk (set CIQ_SDK)"

device="${CIQ_DEVICE:-}"
case "$device" in
"" | fenix7spro | fenix6pro) ;;
*) die "CIQ_DEVICE must be fenix7spro or fenix6pro" ;;
esac
if [[ -z "$device" ]]; then
	if [[ -d "$devices/fenix7spro" ]]; then
		device=fenix7spro
	elif [[ $mode == test ]]; then
		device=fenix6pro
	fi
fi
if [[ "$device" == "" || ("$device" == fenix7spro && ! -d "$devices/fenix7spro") ]]; then
	die "fenix7spro device file missing: install it with the Connect IQ SDK Manager (or set CIQ_DEVICE=fenix6pro for a compile check)"
fi

if [[ ! -f "$key" ]]; then
	[[ -d "$root/.secrets" ]] || mkdir -m 700 "$root/.secrets"
	# RSA 4096 as unencrypted PKCS#8 DER, the format monkeyc -y reads. The key
	# only ever goes to the file (mode 0600), never to the terminal.
	(
		umask 077
		trap 'rm -f "$key.tmp"' EXIT
		openssl genrsa 4096 | openssl pkcs8 -topk8 -outform DER -nocrypt -out "$key.tmp"
		chmod 600 "$key.tmp"
		mv "$key.tmp" "$key"
	)
	echo "build.sh: created developer key $key (keep it: it signs every update of the app)" >&2
fi

sed "s#\"https://coachd.example.com\"#\"$url\"#" "$here/source/Config.mc.example" >"$here/source/Config.mc"
grep -qF "\"$url\"" "$here/source/Config.mc" || die "could not write source/Config.mc"

mkdir -p "$here/bin"
args=(-d "$device" -y "$key" -l 3 -w)
if [[ $mode == test ]]; then
	# test.jungle adds test/ to the source path; -t compiles the (:test) suite.
	args+=(-f "$here/monkey.jungle;$here/test.jungle" -t)
	out="$here/bin/coachd-test.prg"
else
	args+=(-f "$here/monkey.jungle")
	out="$here/bin/coachd.prg"
fi

echo "build.sh: $mode build for $device -> ${out#"$root"/}" >&2
"$sdk/bin/monkeyc" "${args[@]}" -o "$out"
