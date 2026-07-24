#!/usr/bin/env bash
#
# Maps a Conventional Commit pull request title to the labels release
# drafter reads for categories and version resolution, printing one per
# line. No output means no label applies.
#
# This lives outside the workflow so it can be exercised directly:
#
#     .github/pr-label.sh 'fix(fake)!: refuse a script the shell cannot run'
#
# Labelling was release drafter's own job until its v7 release, which
# forces dry-run mode whenever the ref is a pull request's ephemeral
# merge commit — every label write was silently discarded, and with no
# labels every draft resolved to a patch however much had changed. Doing
# it here is one fewer thing that can fail quietly, and one fewer
# third-party action holding a write token on pull request events.
set -euo pipefail

title=${1?usage: pr-label.sh <pull request title>}

# The breaking marker raises the minor, and pre-1.0 that is as far as it
# goes. Any type may carry it: "fix(fake)!:" as readily as "feat!:".
if [[ $title =~ ^[[:alnum:]]+(\(.+\))?!: ]]; then
	echo breaking
fi

# One label per Conventional Commit type, matching however the title
# spells the rest: an optional scope, an optional breaking marker.
for rule in \
	'feat:feature' \
	'fix:fix' \
	'test:test' \
	'docs:documentation' \
	'build|ci|refactor|chore|style|perf:chore'; do
	types=${rule%:*}
	label=${rule##*:}

	if [[ $title =~ ^($types)(\(.+\))?!?: ]]; then
		echo "$label"
	fi
done
