#!/usr/bin/env bash
# Prints the normalized "path : type" list of a fixture file so that ES
# versions can be compared with diff. Node ids, index names, remote aliases,
# node attributes and array positions are collapsed.
# Usage: scripts/fixture-schema.sh testdata/es/8.19.22/nodes_stats.json
set -euo pipefail
f=$1
top=false
case $(basename "$f") in index_settings*|remote_info*) top=true ;; esac
jq -r --argjson top "$top" '
  . as $root | paths(scalars) as $p
  | ($p | map(if type == "number" then "[]" else . end)) as $n
  | [ $n | to_entries[]
      | if (.key == 0 and $top) then "*"
        elif (.key > 0 and ($n[.key-1] == "nodes" or $n[.key-1] == "attributes")) then "*"
        else .value end ]
  | join(".") + " : " + ($root | getpath($p) | type)
' "$f" | sort -u
