---
title: Backups and restore
weight: 1
---

## Automatic backups

`serve` backs up the database at start and then whenever the newest backup is 24 h old or more (checked hourly). It keeps the newest 7 in `/data/backups` as `coachd-<UTC YYYYMMDDThhmmssZ>.db` plus a `.json` manifest (SHA-256 of the database and of every file under `/data/fit`).

The `BackupStale` [alert](../monitoring/#alerts) fires when the newest backup is older than 48 h or no backup metric exists.

{{< callout type="warning" >}}
A newest backup named more than 1 h in the future (the clock was wrong when it was written) reports no age, so the alert fires, and `serve` backs up at every check until you fix the clock and delete that backup. Meanwhile those hourly backups push the older daily ones out of the 7 kept (the off-site restic copy still has them), so fix it soon.
{{< /callout >}}

## Manual backup

```bash
docker compose exec coachd /coachd backup
```

It prints `backup written: <path> (database <N> bytes, <M> FIT files, <K> old backups removed)`.

## Not included

- `nonce.key` is recreated on start. Outstanding approval and login links stop working.
- `garmin-key.json`, a key [renewed on the page](/docs/install/garmin-key/#renew-the-key).
- `deploy/secrets/` and `.env`. Keep them in a password manager.

## Restore

```bash
docker compose stop coachd
docker compose run --rm --no-deps coachd restore /data/backups/coachd-<ts>.db
docker compose start coachd
```

`restore` refuses, before touching anything:

- while `serve` runs on the data directory;
- a corrupt or non-coachd snapshot, a snapshot from a newer schema, one whose manifest hash does not match, or one with a non-empty `-wal` file next to it.

It also refuses, leaving `coachd.db` in place, when it cannot prepare the snapshot's copy. The previous `coachd.db` (with `-wal` and `-shm`) moves to `/data/pre-restore-<ts>/`; delete it once satisfied. (If the previous database is corrupt, SQLite may discard its invalid `-wal` while coachd tries to read it.) It warns when FIT files listed in the manifest are missing or differ.

Do not run other coachd commands (`token`, `device`, `backup`, `enroll`) while a restore runs: they open the database and could recreate `-wal`/`-shm` files during the swap.

### What a restore keeps

Before the swap it carries forward, from the previous `coachd.db`, every revocation of an agent token, passkey or watch and every used approval nonce, so nothing revoked after the snapshot works again and no approval link works twice. It prints `carried forward <n> revocations and <m> used approval nonces from the previous database`.

It also clears runner sign-in sessions and pending enrolments (sign in again) and, in built-in mode, every app connection (reconnect your apps), and records a `backup_restored` audit row.

If the previous database cannot be read (it may be corrupt), the restore still happens with a warning: check `docker compose exec coachd /coachd token list` and `device list`, and revoke again anything revoked after the snapshot.

### What rolls back

To the snapshot's time: proposals and their states (a workout approved after the snapshot is pending again), notes, the goal, and tokens, passkeys and watches created after the snapshot (create them again; re-pair such a watch). Audit rows written after the snapshot are not in the restored database; they stay in `pre-restore-<ts>/coachd.db`.

### Undo a restore

Stop coachd, then move all three files back from `pre-restore-<ts>/`, removing the restored ones first so no `-wal` is left beside the other database. The coachd image has no shell, so use a throwaway root container (any image with a shell works):

```bash
docker compose stop coachd
T=tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d
docker run --rm --network none -v coachd_coachd-data:/data --entrypoint sh "$T" -c \
  'cd /data && rm -f coachd.db coachd.db-wal coachd.db-shm && mv pre-restore-<ts>/coachd.db* .'
docker compose start coachd
```

## Off-site with restic

`restic/restic.sh` wraps a pinned `restic/restic` image, mounting the volume read-only. It copies `/data/backups` and `/data/fit` only, with the backups lock (`/data/backups/.lock`) and `*.tmp` excluded.

{{% steps %}}

### Choose a repository

S3, B2, SFTP and so on. Create `restic.env` in `deploy/` (git-ignored): `RESTIC_REPOSITORY=...` plus the provider credentials, mode `0600` (`restic.sh` warns when group or others can read it).

### Create the repository password

Without echoing it, owned by root, mode `0400`:

```bash
read -rs P; printf '%s' "$P" | sudo tee secrets/restic_password >/dev/null
sudo chown 0:0 secrets/restic_password; sudo chmod 0400 secrets/restic_password
```

Or use the [throwaway-container recipe](/docs/install/secrets/#writing-a-secret). **Losing this password means losing the backups**; keep a copy in your password manager.

### Initialise and back up

```bash
./restic/restic.sh init
./restic/restic.sh backup      # also forgets: keep 7 daily, 4 weekly, 12 monthly
./restic/restic.sh snapshots
```

### Schedule it

For example in root's crontab (the script needs docker and its working directory to be `deploy/`):

```text
30 3 * * *  cd /home/coachd/coachd/deploy && ./restic/restic.sh backup  >> /var/log/coachd-restic.log 2>&1
30 4 * * 0  cd /home/coachd/coachd/deploy && ./restic/restic.sh check   >> /var/log/coachd-restic.log 2>&1
```

or a systemd timer with `OnCalendar=*-*-* 03:30:00` running the same command.

{{% /steps %}}

## Disaster recovery

On a new or wiped host. `serve` must not start before the last step: on an empty volume it would write a backup of an empty database, newer than every real one.

{{% steps %}}

### Create the volume without starting coachd

Deploy the stack files (checkout, `secrets/`, `.env`) and run

```bash
docker compose run --rm --no-deps coachd version
```

It creates and initialises the `coachd_coachd-data` volume (owned by 65532) without starting `serve`.

### Fetch the latest restic snapshot

```bash
./restic/restic.sh restore-latest /srv/coachd-restore
```

`/srv/coachd-restore` must be an existing host directory. The files land under `/srv/coachd-restore/data/`.

### Copy it into the volume

With a throwaway root container (`T` as in [Undo a restore](#undo-a-restore)):

```bash
docker run --rm --network none -v coachd_coachd-data:/data \
  -v /srv/coachd-restore/data:/restore:ro --entrypoint sh "$T" -c \
  'cp -a /restore/fit /restore/backups /data/ && chown -R 65532:65532 /data/fit /data/backups'
```

### Check the backups

```bash
docker run --rm --network none -v coachd_coachd-data:/data:ro --entrypoint ls "$T" -l /data/backups
```

If any `coachd-<ts>` there was written after the disaster (for example because `serve` started on the empty volume), delete its `.db` and `.json` the same way (`--entrypoint rm "$T" /data/backups/coachd-<ts>.db /data/backups/coachd-<ts>.json`, without `:ro`) before starting `serve`.

### Restore and start

Restore the newest snapshot that came from restic, then start the stack:

```bash
docker compose run --rm --no-deps coachd restore /data/backups/coachd-<ts>.db
docker compose up -d
```

{{% /steps %}}
