#!/bin/sh
#
# Brings an OpenBSD machine to the state citask needs. Run it as root, as often as you like.
#
# It creates what is missing and replaces the program when it differs. It never overwrites the login
# class, citask.json or the schedule, because a person may have edited them, and running this again
# must not undo that. It never reads the GitHub token.

set -eu
umask 022

# DESTDIR exists for the tests, which run this against a throwaway root.
destination_root=${DESTDIR:-}

ci_user=_ci
ci_user_identifier=499
ci_login_class=ci
ci_user_shell=/sbin/nologin
ci_home_directory=/var/ci
installed_program_path=/usr/local/libexec/citask/citask
schedule_entry="*/5 * * * * $ci_user -n -s $installed_program_path poll"

password_file=$destination_root/etc/master.passwd
group_file=$destination_root/etc/group
login_class_file=$destination_root/etc/login.conf.d/$ci_login_class
login_class_definition="$ci_login_class:datasize=4096M:coredumpsize=0:maxproc=256:tc=default:"
ci_home=$destination_root$ci_home_directory
token_file=$ci_home/etc/github-token
install_directory=$destination_root$(dirname "$installed_program_path")
installed_program=$destination_root$installed_program_path
installed_configuration=$install_directory/citask.json
system_crontab=$destination_root/etc/crontab
cron_deny_file=$destination_root/var/cron/cron.deny
at_deny_file=$destination_root/var/cron/at.deny

program_source=$(dirname "$0")/build/citask
configuration_source=
start_schedule=no
mail_recipient=
changes=0
partial_file=

trap 'rm -f "$partial_file"' EXIT

refuse() {
	echo "install-on-openbsd.sh: $1" >&2
	exit 1
}

report_change() {
	changes=$((changes + 1))
	echo "$1"
}

parse_arguments() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--program | --config | --mail-to)
			[ $# -ge 2 ] || refuse "usage: $1 needs a value"
			case "$1" in
			--program) program_source=$2 ;;
			--config) configuration_source=$2 ;;
			--mail-to) mail_recipient=$2 ;;
			esac
			shift 2
			;;
		--start-schedule)
			start_schedule=yes
			shift
			;;
		*)
			refuse "usage: install-on-openbsd.sh [--program FILE] [--config FILE] [--start-schedule --mail-to NAME]"
			;;
		esac
	done
}

refuse_unless_run_as_root() {
	[ "$(id -u)" -eq 0 ] || refuse "run this as root"
}

refuse_unless_program_is_built() {
	[ -f "$program_source" ] || refuse "$program_source does not exist, so run make build first"
}

refuse_unless_a_configuration_is_available() {
	if [ -z "$configuration_source" ] && [ ! -f "$installed_configuration" ]; then
		refuse "no citask.json is installed yet, so pass --config FILE"
	fi
	if [ -n "$configuration_source" ] && [ ! -f "$configuration_source" ]; then
		refuse "$configuration_source does not exist"
	fi
}

refuse_unless_schedule_can_start() {
	[ "$start_schedule" = yes ] || return 0
	[ -f "$token_file" ] || refuse "starting the schedule needs a GitHub token at $token_file"
	case "$mail_recipient" in
	'') refuse "starting the schedule needs --mail-to NAME" ;;
	-* | *[!A-Za-z0-9._@+-]*) refuse "--mail-to must be a plain user name or address" ;;
	esac
}

refuse_unless_existing_account_is_as_expected() {
	account=$(awk -F: -v user="$ci_user" '$1 == user { print $5 ":" $9 ":" $10 }' "$password_file")
	[ -n "$account" ] || return 0

	account_class=${account%%:*}
	account_home_and_shell=${account#*:}
	account_home=${account_home_and_shell%%:*}
	account_shell=${account_home_and_shell#*:}
	[ "$account_class" = "$ci_login_class" ] ||
		refuse "$ci_user already exists with login class $account_class, not $ci_login_class"
	[ "$account_home" = "$ci_home_directory" ] ||
		refuse "$ci_user already exists with home $account_home, not $ci_home_directory"
	[ "$account_shell" = "$ci_user_shell" ] ||
		refuse "$ci_user already exists with shell $account_shell, not $ci_user_shell"
	if awk -F: '$1 == "wheel" { print $4 }' "$group_file" | tr ',' '\n' | grep -qx "$ci_user"; then
		refuse "$ci_user is in wheel, and it runs third-party code, so remove it first"
	fi
}

# The rename means a poll already running keeps the old program, and nothing can see a half-written file.
install_standard_input_atomically() {
	target=$1
	mode=$2
	owner=$3
	partial_file=$(mktemp "$(dirname "$target")/.partial.XXXXXX")
	cat >"$partial_file"
	chmod "$mode" "$partial_file"
	chown "$owner" "$partial_file"
	mv -f "$partial_file" "$target"
	partial_file=
}

ensure_directory() {
	path=$1
	mode=$2
	owner=$3
	if [ ! -d "$path" ]; then
		mkdir -p "$path"
		report_change "created $path"
	fi
	chmod "$mode" "$path"
	chown "$owner" "$path"
}

create_login_class_if_missing() {
	ensure_directory "$(dirname "$login_class_file")" 0755 root:wheel
	if [ -f "$login_class_file" ]; then
		echo "kept $login_class_file"
		return
	fi
	install_standard_input_atomically "$login_class_file" 0644 root:wheel <<LOGIN_CLASS
$login_class_definition
LOGIN_CLASS
	report_change "created $login_class_file"
}

create_account_if_missing() {
	if grep -q "^$ci_user:" "$password_file"; then
		echo "unchanged account $ci_user"
		return
	fi
	mkdir -p "$ci_home"
	useradd -u "$ci_user_identifier" -d "$ci_home_directory" -s "$ci_user_shell" -L "$ci_login_class" -c 'CI runner' "$ci_user"
	report_change "created account $ci_user"
}

ensure_ci_home_folders() {
	ensure_directory "$ci_home" 0755 "$ci_user:$ci_user"
	ensure_directory "$ci_home/etc" 0700 "$ci_user:$ci_user"
}

install_program_if_it_differs() {
	ensure_directory "$install_directory" 0755 root:wheel
	if [ -f "$installed_program" ] && cmp -s "$program_source" "$installed_program"; then
		echo "unchanged $installed_program"
	else
		install_standard_input_atomically "$installed_program" 0755 root:wheel <"$program_source"
		report_change "installed $installed_program"
	fi
	chmod 0755 "$installed_program"
	chown root:wheel "$installed_program"
}

install_configuration_if_missing() {
	if [ ! -f "$installed_configuration" ]; then
		"$installed_program" check-config "$configuration_source" || refuse "$configuration_source is invalid, so it was not installed"
		install_standard_input_atomically "$installed_configuration" 0644 root:wheel <"$configuration_source"
		report_change "installed $installed_configuration"
	elif [ -n "$configuration_source" ] && ! cmp -s "$configuration_source" "$installed_configuration"; then
		echo "kept $installed_configuration, which differs from $configuration_source"
	else
		echo "unchanged $installed_configuration"
	fi
	chmod 0644 "$installed_configuration"
	chown root:wheel "$installed_configuration"
	"$installed_program" check-config "$installed_configuration" || refuse "$installed_configuration is invalid, so every poll would fail"
}

add_to_deny_file() {
	deny_file=$1
	if [ ! -f "$deny_file" ]; then
		install_standard_input_atomically "$deny_file" 0660 root:crontab <<DENY_FILE
$ci_user
DENY_FILE
		report_change "created $deny_file"
	elif grep -qx "$ci_user" "$deny_file"; then
		echo "unchanged $deny_file"
	else
		[ -z "$(tail -c 1 "$deny_file")" ] || echo >>"$deny_file"
		echo "$ci_user" >>"$deny_file"
		report_change "added $ci_user to $deny_file"
	fi
}

start_schedule_if_asked() {
	if [ "$start_schedule" = no ]; then
		echo "schedule not started: pass --start-schedule --mail-to NAME when ready"
		return
	fi
	if [ -f "$system_crontab" ] && grep -qxF "$schedule_entry" "$system_crontab"; then
		echo "unchanged $system_crontab"
	elif [ -f "$system_crontab" ]; then
		install_standard_input_atomically "$system_crontab" 0644 root:wheel <<CRONTAB
$(cat "$system_crontab")
MAILTO=$mail_recipient
$schedule_entry
CRONTAB
		report_change "added the schedule to $system_crontab"
	else
		install_standard_input_atomically "$system_crontab" 0644 root:wheel <<CRONTAB
MAILTO=$mail_recipient
$schedule_entry
CRONTAB
		report_change "created $system_crontab"
	fi
	chmod 0644 "$system_crontab"
	chown root:wheel "$system_crontab"
}

report_token_state() {
	if [ -f "$token_file" ]; then
		echo "GitHub token is present"
	else
		echo "GitHub token is missing: results cannot be posted until $token_file exists"
	fi
}

report_change_count() {
	if [ "$changes" -eq 1 ]; then
		echo "1 change"
	else
		echo "$changes changes"
	fi
}

refuse_unless_run_as_root
parse_arguments "$@"
refuse_unless_program_is_built
refuse_unless_a_configuration_is_available
refuse_unless_schedule_can_start
refuse_unless_existing_account_is_as_expected

create_login_class_if_missing
create_account_if_missing
ensure_ci_home_folders
install_program_if_it_differs
install_configuration_if_missing
add_to_deny_file "$cron_deny_file"
add_to_deny_file "$at_deny_file"
start_schedule_if_asked
report_token_state
report_change_count
