#!/bin/sh
# Assembles Grafana provisioning in tmpfs: the repo files plus, when the
# alert URL secret is set, the ntfy contact point; when it is empty, a reset
# that turns notifications off. Then runs Grafana.
# The URL never reaches an external command's argv: only shell builtins
# (printf, read, [, case, parameter expansion) see it, and grep reads it
# from stdin.
set -eu
src=/etc/coachd-grafana
dst=/tmp/provisioning
secret=/run/secrets/grafana_alert_url
bad="coachd: secrets/grafana_alert_url must be one https:// URL (see https://stefiix92.github.io/coachd/docs/operations/monitoring/)"
rm -rf "$dst"
mkdir -p "$dst/datasources" "$dst/dashboards" "$dst/alerting" "$dst/plugins"
cp "$src"/provisioning/datasources/*.yaml "$dst/datasources/"
cp "$src"/provisioning/dashboards/*.yaml "$dst/dashboards/"
cp "$src/alerts/coachd-alerts.yaml" "$dst/alerting/"
# One non-empty line at most (grep gets the file path, not the URL).
if [ "$(grep -c '[^[:space:]]' "$secret" || true)" -gt 1 ]; then
	echo "$bad" >&2
	exit 1
fi
# That line, with leading and trailing whitespace (CR too) trimmed.
url=
while IFS= read -r l || [ -n "$l" ]; do
	l=${l#"${l%%[![:space:]]*}"}
	l=${l%"${l##*[![:space:]]}"}
	[ -z "$l" ] || url=$l
done < "$secret"
case $url in
*[[:space:]]*)
	echo "$bad" >&2
	exit 1
	;;
esac
if [ -z "$url" ]; then
	echo "coachd: secrets/grafana_alert_url is empty: alert notifications are off (alerts still show in Grafana)" >&2
	# The policy tree and the ntfy contact point are file-managed: reset the
	# tree and delete the contact point, so emptying the secret turns a
	# previously set URL off. Grafana applies resetPolicies before
	# deleteContactPoints; deleting a missing contact point is a no-op.
	cat > "$dst/alerting/notifications-off.yaml" <<'EOF'
apiVersion: 1
resetPolicies:
  - 1
deleteContactPoints:
  - orgId: 1
    uid: coachd-ntfy
EOF
else
	# No quote, backslash, pipe, space or $: safe inside the single-quoted YAML
	# scalar, and Grafana's $VAR expansion of provisioning values cannot fire.
	if ! printf '%s' "$url" | grep -Eq '^https://[A-Za-z0-9._~:/?#@!&()*+,;=%-]+$'; then
		echo "$bad" >&2
		exit 1
	fi
	(
		umask 077
		while IFS= read -r l || [ -n "$l" ]; do
			case $l in
			*__COACHD_ALERT_URL__*) printf '%s%s%s\n' "${l%%__COACHD_ALERT_URL__*}" "$url" "${l#*__COACHD_ALERT_URL__}" ;;
			*) printf '%s\n' "$l" ;;
			esac
		done < "$src/notifications.yaml.tmpl" > "$dst/alerting/notifications.yaml"
	)
fi
exec /run.sh "$@"
