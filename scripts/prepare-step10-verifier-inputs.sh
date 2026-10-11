#!/usr/bin/env bash
set -euo pipefail

# Validate two separately pinned, credential-free checkouts and materialize
# only their exact files for the authenticated process test. Acquisition is
# performed by the caller; this script never reads or transports credentials.

readonly model_commit='7caaf6e07f0b22d778f8431b37b2629a910b9b45'
readonly workplan_commit='92d3bd5dbe9d39203f3722ba916c768002a133ae'
readonly model_sha256='77fb90f96d7a43cac041cb18fea8b27345e1b43b5b1b661b21cb04cb4ced8388'
readonly workplan_sha256='8c4a01850a07a97bd71b19ccbed97853950226d6f82c846fbeea8b5d7ed3ca71'
readonly repository='https://github.com/simensen/wip-reboot'
readonly checkout_action='actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd'
readonly workplan_path='workplans/daemon-operation-parity-continuation.md'

fail() {
	printf 'step10 input preparation: %s\n' "$1" >&2
	exit 1
}

if [[ $# -ne 3 ]]; then
	fail 'usage: prepare-step10-verifier-inputs.sh MODEL_CHECKOUT WORKPLAN_CHECKOUT OUTPUT_DIR'
fi

model_checkout=$1
workplan_checkout=$2
output_dir=$3

if [[ ! -d $model_checkout || -L $model_checkout || ! -d $workplan_checkout || -L $workplan_checkout ]]; then
	fail 'both pinned source checkout directories must exist and must not be symlinks'
fi
if [[ ! $output_dir = /* || -e $output_dir || -L $output_dir ]]; then
	fail 'OUTPUT_DIR must be an absolute, fresh path that does not already exist'
fi

checkout_commit() {
	local name=$1
	local checkout=$2
	local expected=$3
	local actual

	actual=$(git -C "$checkout" rev-parse --verify HEAD 2>/dev/null) || fail "$name source is not a Git checkout"
	if [[ $actual != "$expected" ]]; then
		fail "$name source commit is $actual; expected pinned commit $expected"
	fi
	if git -C "$checkout" config --local --get-regexp 'http\..*\.extraheader' >/dev/null 2>&1; then
		fail "$name source checkout retains an HTTP credential header; disable credential persistence"
	fi
}

sha256_file() {
	local file=$1
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$file" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$file" | awk '{print $1}'
	else
		fail 'sha256sum or shasum is required to verify pinned sources'
	fi
}

checkout_commit MODEL "$model_checkout" "$model_commit"
checkout_commit Workplan "$workplan_checkout" "$workplan_commit"

model_source=$model_checkout/MODEL.md
workplan_source=$workplan_checkout/$workplan_path
if [[ ! -f $model_source || -L $model_source || ! -f $workplan_source || -L $workplan_source ]]; then
	fail 'a pinned source path is missing, not a regular file, or is a symlink'
fi
if [[ $(sha256_file "$model_source") != "$model_sha256" ]]; then
	fail 'MODEL.md content hash does not match its immutable pin'
fi
if [[ $(sha256_file "$workplan_source") != "$workplan_sha256" ]]; then
	fail 'Workplan content hash does not match its immutable pin'
fi

mkdir -m 700 "$output_dir"
complete=0
cleanup_output() {
	if [[ $complete -ne 1 ]]; then
		rm -rf "$output_dir"
	fi
}
trap cleanup_output EXIT

cat "$model_source" > "$output_dir/MODEL.md"
cat "$workplan_source" > "$output_dir/step10-workplan.md"
chmod 644 "$output_dir/MODEL.md" "$output_dir/step10-workplan.md"
printf '{"schema":"wipd.step10-test-inputs/1","repository":"%s","preparation":"detached-commit-checkouts-validated","ci_checkout_action":"%s","ci_fetch_depth":1,"ci_credentials_persisted":false,"verifier_network":"denied","model":{"commit":"%s","path":"MODEL.md","ci_sparse_checkout":"MODEL.md","sha256":"%s"},"workplan":{"commit":"%s","path":"%s","ci_sparse_checkout":"%s","sha256":"%s"}}\n' \
	"$repository" "$checkout_action" "$model_commit" "$model_sha256" \
	"$workplan_commit" "$workplan_path" "$workplan_path" "$workplan_sha256" \
	> "$output_dir/manifest.json"

if [[ $(sha256_file "$output_dir/MODEL.md") != "$model_sha256" ||
	$(sha256_file "$output_dir/step10-workplan.md") != "$workplan_sha256" ]]; then
	fail 'materialized pinned input changed during preparation'
fi

printf 'prepared pinned Step 10 verifier inputs; model=%s workplan=%s manifest=%s\n' \
	"$model_commit" "$workplan_commit" "$output_dir/manifest.json"
complete=1
