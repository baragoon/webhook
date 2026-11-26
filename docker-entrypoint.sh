#!/bin/sh
set -eu

collect_cli_flags() {
	_cli_flags='|'
	while [ "$#" -gt 0 ]; do
		_cli_arg=$1
		shift
		case "$_cli_arg" in
			--) break ;;
			-?*) ;;
			*) break ;;
		esac
		_cli_name=${_cli_arg#-}
		_cli_name=${_cli_name#-}
		case "$_cli_name" in
			*=*)
				_cli_name=${_cli_name%%=*}
				_cli_has_value=true
				;;
			*)
				_cli_has_value=false
				;;
		esac
		_cli_flags="${_cli_flags}${_cli_name}|"
		case "$_cli_name" in
			verbose|debug|nopanic|hotreload|secure|template|version|list-cipher-suites|x-request-id)
				;;
			*)
				if [ "$_cli_has_value" = false ] && [ "$#" -gt 0 ]; then
					shift
				fi
				;;
		esac
	done
	printf '%s' "$_cli_flags"
}

cli_flags="$(collect_cli_flags "$@")"

while IFS=' ' read -r flag env_name kind; do
	[ -n "$flag" ] || continue
	case "$cli_flags" in *"|$flag|"*) [ "$kind" = list ] || continue ;; esac
	if value="$(printenv "$env_name" 2>/dev/null)"; then
		if [ "$flag" = port ]; then
			case "$value" in
				tcp://*) continue ;;
			esac
		fi
		case "$kind" in
			value)
				set -- "-$flag" "$value" "$@"
				;;
			bool)
				case "$value" in
					true|1)
						set -- "-$flag" "$@"
						;;
					false|0)
						set -- "-$flag=false" "$@"
						;;
					*)
						printf 'error: %s must be true, false, 1, or 0\n' "$env_name" >&2
						exit 2
						;;
				esac
				;;
			list)
				# Reverse before prepending so list values retain their original order.
				reversed_value="$(printf '%s\n' "$value" | awk '{ values[NR] = $0 } END { for (i = NR; i > 0; i--) print values[i] }')"
				old_ifs=$IFS
				IFS='
'
				set -f
				for item in $reversed_value; do
					[ -n "$item" ] || continue
					set -- "-$flag" "$item" "$@"
				done
				set +f
				IFS=$old_ifs
				;;
		esac
	fi
done <<'ENV_FLAGS'
ip WEBHOOK_IP value
port WEBHOOK_PORT value
verbose WEBHOOK_VERBOSE bool
logfile WEBHOOK_LOGFILE value
debug WEBHOOK_DEBUG bool
nopanic WEBHOOK_NOPANIC bool
hotreload WEBHOOK_HOTRELOAD bool
urlprefix WEBHOOK_URLPREFIX value
secure WEBHOOK_SECURE bool
template WEBHOOK_TEMPLATE bool
cert WEBHOOK_CERT value
key WEBHOOK_KEY value
version WEBHOOK_VERSION bool
list-cipher-suites WEBHOOK_LIST_CIPHER_SUITES bool
tls-min-version WEBHOOK_TLS_MIN_VERSION value
cipher-suites WEBHOOK_CIPHER_SUITES value
x-request-id WEBHOOK_X_REQUEST_ID bool
x-request-id-limit WEBHOOK_X_REQUEST_ID_LIMIT value
max-multipart-mem WEBHOOK_MAX_MULTIPART_MEM value
http-methods WEBHOOK_HTTP_METHODS value
pidfile WEBHOOK_PIDFILE value
hooks WEBHOOK_HOOKS list
header WEBHOOK_HEADER list
socket WEBHOOK_SOCKET value
setgid WEBHOOK_SETGID value
setuid WEBHOOK_SETUID value
ENV_FLAGS

exec /usr/local/bin/webhook "$@"
