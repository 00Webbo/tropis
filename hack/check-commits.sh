#!/usr/bin/env bash
# Check commit subjects (and optionally a PR title) follow Conventional
# Commits, which release-please reads to choose the next version and write
# the changelog. A non-conventional commit is silently left out of both.
#
#   hack/check-commits.sh <base> <head> [pr-title]
#
# Types: feat and fix bump the version (feat: minor, fix: patch, while 0.x);
# a "!" after the type, or a BREAKING CHANGE footer, marks a breaking change.
# The rest appear in the changelog or are hidden, and do not bump.
set -euo pipefail

base="$1" head="$2" title="${3:-}"

types='feat|fix|perf|revert|docs|security|deps|refactor|test|build|ci|chore'
pattern="^($types)(\([a-z0-9/._-]+\))?!?: .+"

# Commits from before the project adopted Conventional Commits. Only PRs that
# still contain them (the first one) are affected; they are exempt.
adopted_after=88b3be530bfac4581b704a5e8f626ffa7d44c3bc
exempt=()
if git cat-file -e "$adopted_after^{commit}" 2>/dev/null; then
	exempt=(--not "$adopted_after")
fi

bad=0
while IFS= read -r line; do
	sha="${line%% *}" subject="${line#* }"
	case "$subject" in "Merge "*) continue ;; esac
	if ! [[ "$subject" =~ $pattern ]]; then
		echo "commit ${sha:0:7}: '$subject' is not a Conventional Commit"
		bad=1
	fi
done < <(git log --format='%H %s' "$base..$head" "${exempt[@]}")

if [ -n "$title" ] && ! [[ "$title" =~ $pattern ]]; then
	echo "PR title '$title' is not a Conventional Commit (it becomes the commit message on a squash merge)"
	bad=1
fi

if [ "$bad" -ne 0 ]; then
	echo
	echo "Use <type>[(scope)][!]: <description>, with type one of: ${types//|/, }."
	echo "feat and fix change the version; see CONTRIBUTING.md, Commit messages."
	exit 1
fi
echo "commit messages OK"
