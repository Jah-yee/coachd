#!/bin/sh
# Off-site backup of coachd's /data/backups and /data/fit with restic (encrypted, deduplicated).
#
# Usage (run from deploy/):
#   restic/restic.sh init                    create the repository (once)
#   restic/restic.sh backup                  back up, then forget old snapshots and prune
#   restic/restic.sh check                   verify the repository, reading 5% of the data
#   restic/restic.sh snapshots               list snapshots
#   restic/restic.sh restore-latest DIR      restore the latest snapshot into the host directory DIR
#
# Needs deploy/restic.env (git-ignored, mode 0600) with RESTIC_REPOSITORY and the provider
# credentials, and secrets/restic_password (the repository password). Losing the password
# loses the backups. The script never prints either file.
#
# COACHD_VOLUME overrides the data volume name (default coachd_coachd-data).
# RESTIC_EXTRA_ARGS is for TESTING ONLY: its words are added to `docker run` unquoted
# (split on whitespace, never eval'd), e.g. RESTIC_EXTRA_ARGS='-v /host/repo:/repo' with
# RESTIC_REPOSITORY=/repo to use a local directory as the repository.
set -eu

IMAGE='restic/restic:0.18.1@sha256:39d9072fb5651c80d75c7a811612eb60b4c06b32ffe87c2e9f3c7222e1797e76'
VOLUME="${COACHD_VOLUME:-coachd_coachd-data}"

die() { echo "restic.sh: $*" >&2; exit 1; }

[ -f restic.env ] || die "restic.env not found (run from deploy/). Create it (chmod 0600) with RESTIC_REPOSITORY=... and the provider credentials, e.g. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY for S3 or B2_ACCOUNT_ID / B2_ACCOUNT_KEY for Backblaze."
[ -f secrets/restic_password ] || die "secrets/restic_password not found. Create it (root, mode 0400); losing it loses the backups."
# restic.env holds the provider credentials: warn when others can read it (never print it).
if [ -n "$(find restic.env \( -perm -040 -o -perm -004 \) 2>/dev/null)" ]; then
  echo "restic.sh: warning: restic.env is readable by group or others; run chmod 0600 restic.env" >&2
fi

cmd="${1:-}"
[ $# -gt 0 ] && shift

# run MOUNT_ARGS... -- RESTIC_ARGS...  (the volume is mounted by the caller when needed)
run() {
  # shellcheck disable=SC2086
  docker run --rm \
    --env-file restic.env \
    -e RESTIC_PASSWORD_FILE=/run/secrets/restic_password \
    -e RESTIC_CACHE_DIR=/tmp/restic-cache \
    --tmpfs /tmp/restic-cache \
    -v "$PWD/secrets/restic_password:/run/secrets/restic_password:ro" \
    ${RESTIC_EXTRA_ARGS:-} \
    "$@"
}

case "$cmd" in
  init)
    run "$IMAGE" init
    ;;
  backup)
    run -v "$VOLUME:/data:ro" "$IMAGE" backup --host coachd --tag coachd \
      --exclude '/data/backups/.lock' --exclude '*.tmp' /data/backups /data/fit
    run "$IMAGE" forget --tag coachd --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --prune
    ;;
  check)
    run "$IMAGE" check --read-data-subset 5%
    ;;
  snapshots)
    run "$IMAGE" snapshots
    ;;
  restore-latest)
    [ $# -eq 1 ] || die "usage: restic.sh restore-latest TARGET_DIR"
    [ -d "$1" ] || die "$1 is not a directory"
    dir=$(cd "$1" && pwd)
    run -v "$dir:/restore" "$IMAGE" restore latest --tag coachd --target /restore
    ;;
  *)
    die "usage: restic.sh <init|backup|check|snapshots|restore-latest TARGET_DIR>"
    ;;
esac
