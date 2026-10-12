#!/usr/bin/env bash
set -euo pipefail

readonly expected_bwrap_sha256=4ef793eab2ad4d2840fc5da493193e558b4c428aec65e4259958a535363d217e

probe_child() {
	printf 'probe.payload_started=1\n'
	child_user_ns="$("$readlink_bin" /proc/self/ns/user)"
	child_net_ns="$("$readlink_bin" /proc/self/ns/net)"
	printf 'probe.child.namespace.user=%s\n' "$child_user_ns"
	printf 'probe.child.namespace.net=%s\n' "$child_net_ns"
	[[ "$child_user_ns" != "$parent_user_ns" ]]
	[[ "$child_net_ns" != "$parent_net_ns" ]]
	caps="$("$grep_bin" -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' "/proc/$$/status")"
	printf 'probe.child.capabilities:\n%s\n' "$caps"
	child_seccomp="$("$grep_bin" -E '^Seccomp(_filters)?:' "/proc/$$/status")"
	printf 'probe.child.seccomp:\n%s\n' "$child_seccomp"
	expected_caps=$'CapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000001000\nNoNewPrivs:\t1'
	[[ "$caps" == "$expected_caps" ]]
	interfaces="$("$ip_bin" -o link show)"
	printf 'probe.child.interfaces=%s\n' "$interfaces"
	[[ "$interfaces" == '1: lo: <LOOPBACK,'* ]]
	[[ "$interfaces" != *$'\n'* ]]
	addresses="$("$ip_bin" -o addr show dev lo)"
	printf 'probe.child.loopback.addresses=%s\n' "$addresses"
	[[ "$addresses" == *127.0.0.1/8* ]]
	"$ip_bin" link set dev lo "$loopback_action"
	after="$("$ip_bin" -o link show dev lo)"
	printf 'probe.child.loopback.after=%s\n' "$after"
	flags="${after#*<}"
	flags="${flags%%>*}"
	case ",$flags," in *,UP,*) is_up=yes ;; *) is_up=no ;; esac
	if [[ "$loopback_action" == up ]]; then
		[[ "$is_up" == yes ]]
	else
		[[ "$is_up" == no ]]
	fi
	[[ "$("$grep_bin" -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' "/proc/$$/status")" == "$expected_caps" ]]
	printf 'probe.payload_result=pass\n'
}

if [[ "${1:-}" == --inside ]]; then
	probe_child
	exit
fi

resolve() { command -v "$1"; }
bwrap_bin="$(resolve bwrap)"
readonly bwrap_bin
bash_bin="$(resolve bash)"
readonly bash_bin
ip_bin="$(resolve ip)"
readonly ip_bin
readlink_bin="$(resolve readlink)"
readonly readlink_bin
grep_bin="$(resolve grep)"
readonly grep_bin
env_bin="$(resolve env)"
readonly env_bin
sort_bin="$(resolve sort)"
readonly sort_bin
sha256sum_bin="$(resolve sha256sum)"
readonly sha256sum_bin
uname_bin="$(resolve uname)"
readonly uname_bin
strace_bin="$(resolve strace)"
readonly strace_bin
awk_bin="$(resolve awk)"
readonly awk_bin
date_bin="$(resolve date)"
readonly date_bin
sleep_bin="$(resolve sleep)"
readonly sleep_bin
timeout_bin="$(resolve timeout)"
readonly timeout_bin
mktemp_bin="$(resolve mktemp)"
readonly mktemp_bin
mkfifo_bin="$(resolve mkfifo)"
readonly mkfifo_bin
cat_bin="$(resolve cat)"
readonly cat_bin
rm_bin="$(resolve rm)"
readonly rm_bin
head_bin="$(resolve head)"
readonly head_bin
journalctl_bin="$(command -v journalctl 2>/dev/null || true)"
readonly journalctl_bin
script_path="$(readlink -f "${BASH_SOURCE[0]}")"
readonly script_path

[[ "$bwrap_bin" == /nix/store/*/bin/bwrap ]]
bwrap_version="$("$bwrap_bin" --version)"
[[ "$bwrap_version" == 'bubblewrap 0.11.0' ]]
bwrap_hash_line="$("$sha256sum_bin" "$bwrap_bin")"
actual_bwrap_sha256="${bwrap_hash_line%% *}"
[[ "$actual_bwrap_sha256" == "$expected_bwrap_sha256" ]]

printf 'runner.kernel=%s\n' "$($uname_bin -sr)"
for namespace in user net pid; do printf 'runner.namespace.%s=%s\n' "$namespace" "$("$readlink_bin" "/proc/self/ns/$namespace")"; done
status_lines="$("$grep_bin" -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp|Seccomp_filters):' "/proc/$$/status")"
while IFS= read -r line; do printf 'runner.status.%s\n' "$line"; done <<< "$status_lines"

read_one() {
	local label="$1" path="$2" required="${3:-no}" value=''
	if [[ -r "$path" ]]; then
		value="$("$cat_bin" "$path")" || return 1
		printf '%s=%s\n' "$label" "$value"
	else
		printf '%s=unavailable\n' "$label" || return 1
		[[ "$required" != yes ]]
	fi
}
read_one runner.lsm.enabled /sys/kernel/security/lsm
read_one runner.lsm.current /proc/self/attr/current
read_one runner.userns.max_user_namespaces /proc/sys/user/max_user_namespaces
read_one runner.userns.unprivileged_userns_clone /proc/sys/kernel/unprivileged_userns_clone
read_one runner.userns.apparmor_restrict_unprivileged_userns /proc/sys/kernel/apparmor_restrict_unprivileged_userns
printf 'probe.bwrap_version=%s\n' "$bwrap_version"
printf 'probe.bwrap_sha256=%s\n' "$actual_bwrap_sha256"
printf 'probe.bwrap_path=%s\n' "$bwrap_bin"
printf 'probe.strace_path=%s\n' "$strace_bin"
printf 'probe.strace_version=%s\n' "$("$strace_bin" --version | "$head_bin" -n 1)"
printf 'runner.image.os=%s\n' "${ImageOS:-unavailable}"
printf 'runner.image.version=%s\n' "${ImageVersion:-unavailable}"

store_root() {
	local path="$1" rest
	[[ "$path" == /nix/store/* ]]
	rest="${path#/nix/store/}"
	printf '/nix/store/%s\n' "${rest%%/*}"
}
store_roots=("$(store_root "$bash_bin")" "$(store_root "$ip_bin")" "$(store_root "$readlink_bin")" "$(store_root "$grep_bin")")
closure_output="$(nix-store -qR "${store_roots[@]}")"
mapfile -t closure_paths < <(printf '%s\n' "$closure_output" | "$sort_bin" -u)
parent_user_ns="$("$readlink_bin" /proc/self/ns/user)"
readonly parent_user_ns
parent_net_ns="$("$readlink_bin" /proc/self/ns/net)"
readonly parent_net_ns
parent_pid_ns="$("$readlink_bin" /proc/self/ns/pid)"
readonly parent_pid_ns

capture_child_snapshot() {
	local stage="$1" child_pid="$2" child_exe='' apparmor_label='' status_lines='' line='' namespace_value='' required_field=''
	printf 'probe.child.snapshot.stage=%s\n' "$stage" || return 1
	child_exe="$("$readlink_bin" "/proc/$child_pid/exe" 2>/dev/null || true)"
	printf 'probe.child.exe=%s\n' "${child_exe:-unavailable}" || return 1
	[[ "$child_exe" == "$bwrap_bin" ]] || return 1
	if [[ -r "/proc/$child_pid/attr/current" ]]; then
		IFS= read -r apparmor_label < "/proc/$child_pid/attr/current" || true
	else
		printf 'probe.child.apparmor=unavailable\n' || return 1
		return 1
	fi
	printf 'probe.child.apparmor=%s\n' "${apparmor_label:-unavailable}" || return 1
	[[ -n "$apparmor_label" ]] || return 1
	read_one probe.child.uid_map "/proc/$child_pid/uid_map" yes || return 1
	read_one probe.child.gid_map "/proc/$child_pid/gid_map" yes || return 1
	for namespace in user net pid; do
		namespace_value="$("$readlink_bin" "/proc/$child_pid/ns/$namespace" 2>/dev/null || printf unavailable)"
		printf 'probe.child.namespace.%s=%s\n' "$namespace" "$namespace_value" || return 1
		[[ "$namespace_value" != unavailable ]] || return 1
		case "$namespace" in
			user) [[ "$namespace_value" != "$parent_user_ns" ]] || return 1 ;;
			net) [[ "$namespace_value" != "$parent_net_ns" ]] || return 1 ;;
			pid) [[ "$namespace_value" != "$parent_pid_ns" ]] || return 1 ;;
		esac
	done
	status_lines="$("$grep_bin" -E '^(Name|State|Uid|Gid|CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp|Seccomp_filters):' "/proc/$child_pid/status")" || return 1
	for required_field in CapInh CapPrm CapEff CapBnd CapAmb NoNewPrivs Seccomp Seccomp_filters; do
		"$grep_bin" -q "^${required_field}:" <<< "$status_lines" || return 1
	done
	while IFS= read -r line; do printf 'probe.child.status.%s\n' "$line" || return 1; done <<< "$status_lines"
}

collect_denials() {
	local child_pid="$1" start_epoch="$2" end_epoch="$3" start_time="$4" end_time="$5"
	local denial_kind='(apparmor="DENIED"|type=(AVC|USER_AVC|SECCOMP|1326|1400))'
	local pid_field="(^|[[:space:]])pid=${child_pid}([[:space:]]|$)"
	local denial_pattern="(${denial_kind}.*${pid_field}|${pid_field}.*${denial_kind})"
	local journal_file="$probe_tmpdir/journal-denials.log" audit_file="$probe_tmpdir/audit-denials.log"
	local journal_status=command-not-found journal_rc audit_status collection_failed=no
	if [[ -x "$journalctl_bin" ]]; then
		if "$journalctl_bin" --dmesg --since "$start_time" --until "$end_time" --no-pager --output=cat --grep="$denial_pattern" >"$journal_file" 2>"$probe_tmpdir/journalctl.stderr"; then
			journal_status=readable
		else
			journal_rc=$?
			if "$grep_bin" -qiE 'permission denied|access denied|not permitted' "$probe_tmpdir/journalctl.stderr"; then
				journal_status=permission-denied
			elif "$grep_bin" -qi 'no journal files' "$probe_tmpdir/journalctl.stderr"; then
				journal_status=no-journal-files
			elif [[ "$journal_rc" == 1 && ! -s "$journal_file" && ! -s "$probe_tmpdir/journalctl.stderr" ]]; then
				journal_status=no-match-or-unavailable-exit-1
			else
				journal_status="query-failed-$journal_rc"
			fi
			collection_failed=yes
		fi
	else
		collection_failed=yes
	fi
	if printf 'runner.audit.journal.read=%s\n' "$journal_status"; then
		:
	else
		collection_failed=yes
	fi
	if [[ "$journal_status" == readable ]]; then
		# The AWK program intentionally uses literals supplied through -v values.
		# shellcheck disable=SC2016
		if "$awk_bin" -v pid="$child_pid" '
			{
				pid_pattern = "(^|[[:space:]])pid=" pid "([[:space:]]|$)"
				if ($0 ~ pid_pattern) {
					print "runner.audit.journal.exact=" $0
					exact++
				}
			}
			END {
				printf "runner.audit.journal.exact_count=%d\n", exact
			}' "$journal_file"; then
			:
		else
			journal_rc=$?
			printf 'runner.audit.journal.parse=failed-%s\n' "$journal_rc"
			collection_failed=yes
		fi
	fi
	if [[ -r /var/log/audit/audit.log ]]; then
		# shellcheck disable=SC2016
		if "$awk_bin" -v pid="$child_pid" -v start="$start_epoch" -v end="$end_epoch" '
			/apparmor="DENIED"|type=AVC|type=USER_AVC|type=SECCOMP|type=1326|type=1400/ {
				if (!match($0, /audit\([0-9]+\.[0-9]+:/)) next
				timestamp = substr($0, RSTART + 6, RLENGTH - 7)
				sub(/\..*$/, "", timestamp)
				if (timestamp + 0 < start || timestamp + 0 > end) next
				pid_pattern = "(^|[[:space:]])pid=" pid "([[:space:]]|$)"
				if ($0 ~ pid_pattern) {
					print "runner.audit.file.exact=" $0
					exact++
				}
			}
			END {
				printf "runner.audit.file.exact_count=%d\n", exact
			}' /var/log/audit/audit.log >"$audit_file"; then
			audit_status=readable
		else
			journal_rc=$?
			audit_status="query-failed-$journal_rc"
			collection_failed=yes
		fi
	elif [[ -e /var/log/audit/audit.log ]]; then
		audit_status=permission-denied
		collection_failed=yes
	else
		audit_status=not-present
	fi
	if printf 'runner.audit.file.read=%s\n' "$audit_status"; then
		:
	else
		collection_failed=yes
	fi
	if [[ "$audit_status" == readable ]]; then
		if "$cat_bin" "$audit_file"; then
			:
		else
			journal_rc=$?
			printf 'runner.audit.file.output=failed-%s\n' "$journal_rc"
			collection_failed=yes
		fi
	fi
	[[ "$collection_failed" == no ]]
}

run_probe() {
	local loopback_action="$1"
	local host_arch='' expected_sendto_nr='' start_epoch='' end_epoch='' start_time='' end_time=''
	local trace_pid='' trace_status='' child_pid='' info_text='' attempt=0 state_line='' syscall_line='' syscall_nr=''
	local trace_waited=no denial_status=''
	local trace_lines='' trace_parse_ok=no first_sendto='' reply_line='' request_sequence='' reply_error='' probe_tmpdir='' probe_tmpdir_parent='' block_fd='' info_fd=''
	local block_fd_open=no info_fd_open=no block_released=no
	local syscall_entry_observed=no child_snapshot_observed=no raw_request_observed=no raw_reply_observed=no
	local collection_complete=yes
	local -a args=(
		--die-with-parent
		--unshare-user --unshare-pid --unshare-net --unshare-ipc --unshare-uts --new-session
		--proc /proc --dev /dev --tmpfs /tmp
		--dir /nix --dir /nix/store
	)
	for path in "${closure_paths[@]}"; do args+=(--ro-bind "$path" "$path"); done
	args+=(
		--dir /probe --ro-bind "$(dirname "$script_path")" /probe
		--uid 0 --gid 0 --cap-add CAP_NET_ADMIN --clearenv
		--setenv HOME /nonexistent
		--setenv PATH "$(dirname "$bash_bin"):$(dirname "$ip_bin"):$(dirname "$readlink_bin"):$(dirname "$grep_bin")"
		--setenv ip_bin "$ip_bin" --setenv readlink_bin "$readlink_bin" --setenv grep_bin "$grep_bin"
		--setenv parent_user_ns "$parent_user_ns" --setenv parent_net_ns "$parent_net_ns"
		--setenv loopback_action "$loopback_action"
	)
	printf 'probe.loopback_action=%s\n' "$loopback_action"
	printf 'probe.uid_gid_maps=left-unmapped-through-loopback-for-pre-map-state\n'
	probe_tmpdir_parent="${RUNNER_TEMP:-/tmp}"
	probe_tmpdir="$("$mktemp_bin" -d "$probe_tmpdir_parent/step10-bwrap-diagnostic.XXXXXX")"
	trap 'if [[ "${block_fd_open:-no}" == yes ]]; then if [[ "${block_released:-no}" != yes ]]; then printf x >&"$block_fd" 2>/dev/null || true; fi; exec {block_fd}>&-; fi; if [[ "${info_fd_open:-no}" == yes ]]; then exec {info_fd}>&-; fi; if [[ -n "${trace_pid:-}" ]]; then wait "$trace_pid" 2>/dev/null || true; fi; "$rm_bin" -rf "$probe_tmpdir"' RETURN
	report_incomplete_probe() {
		local reason="$1" known_child_pid="${2:-}" end_epoch end_time trace_lines
		trap - ERR
		set +e
		printf 'probe.collection_error=%s\n' "$reason"
		if [[ "$block_fd_open" == yes ]]; then
			if [[ "$block_released" != yes ]]; then printf x 2>/dev/null 1>&"$block_fd" || true; fi
			exec {block_fd}>&-
			block_fd_open=no
		fi
		if [[ "$info_fd_open" == yes ]]; then
			exec {info_fd}>&-
			info_fd_open=no
		fi
		if [[ "$trace_waited" != yes && -n "$trace_pid" ]]; then
			wait "$trace_pid"
			trace_status=$?
			trace_waited=yes
		elif [[ "$trace_waited" != yes ]]; then
			trace_status=not-started
		fi
		end_epoch="$("$date_bin" +%s)"
		end_time="$("$date_bin" -u -d "@$((end_epoch + 2))" '+%Y-%m-%d %H:%M:%S UTC')"
		printf 'probe.ended_utc=%s\n' "$("$date_bin" -u -d "@$end_epoch" '+%Y-%m-%d %H:%M:%S UTC')"
		printf 'probe.traced_command_exit=%s\n' "$trace_status"
		printf 'probe.bwrap.stdout.begin\n'
		if [[ -r "$probe_tmpdir/stdout.log" ]]; then
			"$cat_bin" "$probe_tmpdir/stdout.log"
		else
			printf 'probe.bwrap.stdout=unavailable\n'
		fi
		printf 'probe.bwrap.stdout.end\nprobe.bwrap.stderr.begin\n'
		if [[ -r "$probe_tmpdir/stderr.log" ]]; then
			"$cat_bin" "$probe_tmpdir/stderr.log"
		else
			printf 'probe.bwrap.stderr=unavailable\n'
		fi
		printf 'probe.bwrap.stderr.end\n'
		if [[ -n "$known_child_pid" ]]; then
			if [[ -r "$probe_tmpdir/netlink.trace" ]]; then
				# shellcheck disable=SC2016
				trace_lines="$("$awk_bin" -v pid="$known_child_pid" '$1 == pid && $2 ~ /^(sendto|recvfrom)[(]/ { print }' "$probe_tmpdir/netlink.trace")"
				if [[ -n "$trace_lines" ]]; then
					printf 'probe.netlink.trace.begin\n%s\nprobe.netlink.trace.end\n' "$trace_lines"
				else
					printf 'probe.netlink.trace=unavailable-for-child\n'
				fi
			fi
			collect_denials "$known_child_pid" "$start_epoch" "$end_epoch" "$start_time" "$end_time" || printf 'runner.audit.collection=failed\n'
		else
			printf 'probe.netlink.trace=unavailable-child-pid\n'
			printf 'runner.audit.collection=skipped-child-pid-unavailable\n'
		fi
		trap - RETURN
		"$rm_bin" -rf "$probe_tmpdir"
		printf 'probe.collection_result=incomplete\n'
		set -e
	}
	handle_probe_error() {
		local failure_status="$1" failure_line="$2"
		trap - ERR
		set +e
		report_incomplete_probe "unexpected-command-failure-line-${failure_line}" "${child_pid:-}"
		exit "$failure_status"
	}
	trap 'handle_probe_error "$?" "$LINENO"' ERR
	"$mkfifo_bin" "$probe_tmpdir/userns-block"
	exec {block_fd}<>"$probe_tmpdir/userns-block"
	block_fd_open=yes
	exec {info_fd}>"$probe_tmpdir/info.json"
	info_fd_open=yes
	# bubblewrap 0.11.0 pauses this child before loopback_setup() and maps IDs
	# only after loopback_setup() on its ordinary unprivileged path. Do not map
	# IDs here: the target child state must remain the pre-RTM_NEWADDR state.
	args+=(--info-fd "$info_fd" --userns-block-fd "$block_fd")
	start_epoch="$("$date_bin" +%s)"
	start_time="$("$date_bin" -u -d "@$((start_epoch - 2))" '+%Y-%m-%d %H:%M:%S UTC')"
	printf 'probe.started_utc=%s\n' "$("$date_bin" -u -d "@$start_epoch" '+%Y-%m-%d %H:%M:%S UTC')"
	# Delay only the first sendto entry so /proc can snapshot this bwrap child
	# at its RTM_NEWADDR request; no syscall arguments or results are injected.
	"$timeout_bin" --signal=TERM --kill-after=5s 6m "$strace_bin" -f -qq -xx -s 256 -yy -v \
		-e trace=sendto,recvfrom \
		-e inject=sendto:delay_enter=2000000:when=1 \
		-o "$probe_tmpdir/netlink.trace" \
		"$env_bin" -i "PATH=$(dirname "$bwrap_bin")" HOME=/nonexistent \
		"$bwrap_bin" "${args[@]}" -- "$bash_bin" /probe/step10-bwrap-loopback-probe.sh --inside \
		>"$probe_tmpdir/stdout.log" 2>"$probe_tmpdir/stderr.log" &
	trace_pid=$!
	for ((attempt = 0; attempt < 500; attempt++)); do
		[[ -s "$probe_tmpdir/info.json" ]] && break
		kill -0 "$trace_pid" 2>/dev/null || break
		"$sleep_bin" 0.01
	done
	if [[ ! -s "$probe_tmpdir/info.json" ]]; then
		printf 'probe.child.info=unavailable\n'
		report_incomplete_probe child-info-unavailable
		return 1
	fi
	info_text="$(<"$probe_tmpdir/info.json")"
	printf 'probe.bwrap.info.begin\n%s\nprobe.bwrap.info.end\n' "$info_text"
	if [[ "$info_text" =~ \"child-pid\"[[:space:]]*:[[:space:]]*([0-9]+) ]]; then
		child_pid="${BASH_REMATCH[1]}"
	else
		printf 'probe.child.pid=unavailable\n'
		report_incomplete_probe child-pid-unavailable
		return 1
	fi
	printf 'probe.child.host_pid=%s\n' "$child_pid"
	for ((attempt = 0; attempt < 500; attempt++)); do
		[[ -r "/proc/$child_pid/status" ]] && break
		"$sleep_bin" 0.01
	done
	if ! capture_child_snapshot userns-blocked-before-loopback "$child_pid"; then
		printf 'probe.child.snapshot=failed-before-loopback\n'
		report_incomplete_probe child-snapshot-unavailable "$child_pid"
		return 1
	fi
	exec {info_fd}>&-
	info_fd_open=no
	printf x >&"$block_fd"
	block_released=yes
	host_arch="$("$uname_bin" -m)"
	case "$host_arch" in
		x86_64) expected_sendto_nr=44 ;;
		aarch64) expected_sendto_nr=206 ;;
		*) expected_sendto_nr=unknown ;;
	esac
	printf 'probe.runner.arch=%s\n' "$host_arch"
	for ((attempt = 0; attempt < 600; attempt++)); do
		if [[ -r "/proc/$child_pid/status" && -r "/proc/$child_pid/syscall" ]]; then
			state_line="$("$grep_bin" '^State:' "/proc/$child_pid/status" || true)"
			syscall_line="$("$cat_bin" "/proc/$child_pid/syscall" 2>/dev/null || true)"
			read -r syscall_nr _ <<< "$syscall_line"
			if [[ "$state_line" =~ ^State:[[:space:]]+t && "$expected_sendto_nr" != unknown && "$syscall_nr" == "$expected_sendto_nr" ]]; then
				printf 'probe.child.syscall_entry=sendto\n'
				syscall_entry_observed=yes
				if capture_child_snapshot rtm-newaddr-sendto-entry "$child_pid"; then
					child_snapshot_observed=yes
				else
				printf 'probe.child.snapshot=failed-at-sendto-entry\n'
				collection_complete=no
			fi
				break
		fi
			fi
		"$sleep_bin" 0.01
	done
	if [[ "$syscall_entry_observed" != yes ]]; then
		printf 'probe.child.syscall_entry=not-observed\n'
		collection_complete=no
	fi
	exec {block_fd}>&-
	block_fd_open=no
	if wait "$trace_pid"; then
		trace_status=0
	else
		trace_status=$?
	fi
	trace_waited=yes
	end_epoch="$("$date_bin" +%s)"
	end_time="$("$date_bin" -u -d "@$((end_epoch + 2))" '+%Y-%m-%d %H:%M:%S UTC')"
	printf 'probe.ended_utc=%s\n' "$("$date_bin" -u -d "@$end_epoch" '+%Y-%m-%d %H:%M:%S UTC')"
	printf 'probe.traced_command_exit=%s\n' "$trace_status"
	if [[ "$trace_status" != 0 ]]; then
		printf 'probe.collection_error=traced-command-failed-status-%s\n' "$trace_status"
		collection_complete=no
	fi
	printf 'probe.bwrap.stdout.begin\n'
	"$cat_bin" "$probe_tmpdir/stdout.log"
	printf 'probe.bwrap.stdout.end\nprobe.bwrap.stderr.begin\n'
	"$cat_bin" "$probe_tmpdir/stderr.log"
	printf 'probe.bwrap.stderr.end\n'
	if [[ -r "$probe_tmpdir/netlink.trace" ]]; then
		# The AWK program intentionally uses its own variables and field syntax.
		# shellcheck disable=SC2016
		if trace_lines="$("$awk_bin" -v pid="$child_pid" '$1 == pid && $2 ~ /^(sendto|recvfrom)[(]/ { print }' "$probe_tmpdir/netlink.trace")"; then
			trace_parse_ok=yes
		else
			printf 'probe.netlink.trace=unreadable\n'
		fi
	else
		printf 'probe.netlink.trace=unavailable\n'
	fi
	if [[ "$trace_parse_ok" == yes && -n "$trace_lines" ]]; then
		printf 'probe.netlink.trace.begin\n%s\nprobe.netlink.trace.end\n' "$trace_lines"
		# shellcheck disable=SC2016
		first_sendto="$("$awk_bin" '$2 ~ /^sendto[(]/ { print; exit }' <<< "$trace_lines")"
		if [[ "$first_sendto" == *'nlmsg_type=0x14 '* || "$first_sendto" == *'RTM_NEWADDR'* ]]; then
			printf 'probe.netlink.first_request=RTM_NEWADDR\n'
			request_sequence="${first_sendto#*nlmsg_seq=}"
			request_sequence="${request_sequence%%,*}"
			if [[ "$request_sequence" =~ ^[0-9]+$ ]]; then
				raw_request_observed=yes
			else
				printf 'probe.netlink.request_sequence=unparsed\n'
				collection_complete=no
			fi
		else
			printf 'probe.netlink.first_request=not-rtm-newaddr-or-unparsed\n'
			collection_complete=no
		fi
		if [[ "$raw_request_observed" == yes ]]; then
			while IFS= read -r line; do
				if [[ "$line" == "$child_pid recvfrom("* && "$line" == *'nlmsg_type=NLMSG_ERROR'* && ( "$line" == *"nlmsg_seq=$request_sequence,"* || "$line" == *"nlmsg_seq=$request_sequence}"* ) ]]; then
					reply_line="$line"
					break
				fi
			done <<< "$trace_lines"
			if [[ "$reply_line" == *'{error='* ]]; then
				reply_error="${reply_line#*'{error='}"
				reply_error="${reply_error%%,*}"
				reply_error="${reply_error%%\}*}"
				[[ -n "$reply_error" ]] || collection_complete=no
				raw_reply_observed=yes
				printf 'probe.netlink.nlmsg_error=%s\n' "$reply_error"
			else
				printf 'probe.netlink.nlmsg_error=missing-or-unparsed-see-raw-trace\n'
				collection_complete=no
			fi
		fi
	else
		if [[ "$trace_parse_ok" == yes ]]; then printf 'probe.netlink.trace=empty\n'; fi
		collection_complete=no
	fi
	if ( set -e; collect_denials "$child_pid" "$start_epoch" "$end_epoch" "$start_time" "$end_time" ); then
		:
	else
		denial_status=$?
		printf 'probe.collection_error=denial-collection-failed-status-%s\n' "$denial_status"
		printf 'runner.audit.collection=failed\n'
		collection_complete=no
	fi
	"$rm_bin" -rf "$probe_tmpdir"
	trap - ERR
	trap - RETURN
	if [[ "$syscall_entry_observed" == yes && "$child_snapshot_observed" == yes && "$raw_request_observed" == yes && "$raw_reply_observed" == yes && "$collection_complete" == yes ]]; then
		printf 'probe.collection_result=complete\n'
	else
		printf 'probe.collection_result=incomplete\n'
		return 1
	fi
}

run_probe up
