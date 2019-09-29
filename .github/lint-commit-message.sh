#!/usr/bin/env bash
set -e

lint_commit_message() {
    commitTitle="$(echo $1 | head -n1)"

    # ignore merge requests
    if echo "$commitTitle" | grep -qE "^Merge branch \'"; then
      echo "Commit hook: ignoring branch merge"
    fi

    # check semantic versioning scheme
    if ! echo "$commitTitle" | grep -qE '^(feat|fix|docs|style|refactor|perf|test|chore)\(?(\w+|\s|\-|_)?\)?:\s\w+'; then
      echo "Your commit title did not follow semantic versioning: $commitTitle"
      echo "Please see https://github.com/angular/angular.js/blob/master/DEVELOPERS.md#commit-message-format"
	  exit 1
    fi
}

if [ "$#" -eq 1 ]; then
   if [ ! -f "$1" ]; then
       echo "$0 was passed one argument, but was not a valid file"
       exit 1
   fi
   lint_commit_message "$(sed -n '/# Please enter the commit message for your changes. Lines starting/q;p' "$1")"
else
    # TRAVIS_COMMIT_RANGE is empty for initial branch commit
    if [[ "${TRAVIS_COMMIT_RANGE}" != *"..."* ]]; then
        parent=$(git log -n 1 --format="%P" ${TRAVIS_COMMIT_RANGE})
        TRAVIS_COMMIT_RANGE="${TRAVIS_COMMIT_RANGE}...$parent"
    fi

    for commit in $(git rev-list ${TRAVIS_COMMIT_RANGE/.../..}); do
      lint_commit_message "$(git log --format="%B" -n 1 $commit)"
    done
fi
