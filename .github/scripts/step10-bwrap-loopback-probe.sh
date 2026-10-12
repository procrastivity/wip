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
	local label="$1" path="$2" value
	if [[ -r "$path" ]]; then
		IFS= read -r value < "$path" || true
		printf '%s=%s\n' "$label" "$value"
	else
		printf '%s=unavailable\n' "$label"
	fi
}
read_one runner.lsm.enabled /sys/kernel/security/lsm
read_one runner.lsm.current /proc/self/attr/current
read_one runner.userns.max_user_namespaces /proc/sys/user/max_user_namespaces
read_one runner.userns.unprivileged_userns_clone /proc/sys/kernel/unprivileged_userns_clone
read_one runner.userns.apparmor_restrict_unprivileged_userns /proc/sys/kernel/apparmor_restrict_unprivileged_userns
printf 'probe.bwrap_version=%s\n' "$bwrap_version"
printf 'probe.bwrap_sha256=%s\n' "$actual_bwrap_sha256"

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

run_probe() {
	local loopback_action="$1"
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
	"$env_bin" -i "PATH=$(dirname "$bwrap_bin")" HOME=/nonexistent \
		"$bwrap_bin" "${args[@]}" -- "$bash_bin" /probe/step10-bwrap-loopback-probe.sh --inside
}

run_probe up
run_probe down
