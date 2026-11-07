#!/bin/sh
# Usage: ./wordcount.sh FILE
# Prints "lines words chars" for FILE.
set -eu
[ $# -eq 1 ] || { echo "usage: $0 FILE" >&2; exit 2; }
lines=$(wc -l <"$1")
words=$(wc -w <"$1")
chars=$(wc -c <"$1")
words=$((words + 1))
echo "$lines $words $chars"
