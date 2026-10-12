#!/usr/bin/env bash
# Check every commit in <base>..<head> follows Conventional Commits, which
# release-please reads to choose the next version and write the changelog (a
# non-conventional commit is silently left out of both), and is signed off
# under the DCO. CI runs this on every pull request.
#
#   hack/check-commits.sh <base> <head>
#
# Types: feat and fix bump the version (feat: minor, fix: patch, while 0.x);
# a "!" after the type, or a BREAKING CHANGE footer, marks a breaking change.
# The rest appear in the changelog or are hidden, and do not bump.
set -euo pipefail

base="$1" head="$2"

types='feat|fix|perf|revert|docs|security|deps|refactor|test|build|ci|chore'
pattern="^($types)(\([a-z0-9/._-]+\))?!?: .+"

bad=0
for sha in $(git rev-list "$base..$head"); do
	subject="$(git log -1 --format=%s "$sha")"
	case "$subject" in "Merge "*) continue ;; esac
	if ! [[ "$subject" =~ $pattern ]]; then
		echo "commit ${sha:0:7}: '$subject' is not a Conventional Commit"
		bad=1
	fi
	if ! git log -1 --format=%B "$sha" | grep -q '^Signed-off-by: '; then
		echo "commit ${sha:0:7}: no Signed-off-by line (git commit -s)"
		bad=1
	fi
done

if [ "$bad" -ne 0 ]; then
	echo
	echo "Use <type>[(scope)][!]: <description>, with type one of: ${types//|/, },"
	echo "and sign off with git commit -s. See CONTRIBUTING.md, Commit messages."
	exit 1
fi
echo "commit messages OK"
