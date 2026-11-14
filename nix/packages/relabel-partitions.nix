# Sets the GPT labels, and optionally the partition UUIDs, of a disk image's partitions:
#
#   chalkos-relabel-partitions IMAGE FIELD < LINES
#     reads lines "<value>\t<label>[\t<uuid>]" and gives the partition whose sfdisk FIELD, uuid or
#     type, equals value, compared without case, the label and, when the line has one, the UUID.
{
  writeShellApplication,
  jq,
  util-linux,
}:
writeShellApplication {
  name = "chalkos-relabel-partitions";
  runtimeInputs = [
    jq
    util-linux
  ];
  text = ''
    if [[ $# != 2 ]]; then
      echo "usage: chalkos-relabel-partitions IMAGE FIELD < LINES" >&2
      exit 2
    fi
    image=$1 field=$2
    table=$(mktemp)
    trap 'rm -f "$table"' EXIT
    sfdisk --json "$image" >"$table"
    while IFS=$'\t' read -r value label uuid; do
      n=$(jq -r --arg field "$field" --arg value "$value" \
        '.partitiontable.partitions[] | select((.[$field] | ascii_downcase) == ($value | ascii_downcase)) | .node | capture("(?<n>[0-9]+)$").n' "$table")
      if [[ -n $uuid ]]; then
        sfdisk --part-uuid "$image" "$n" "$uuid"
      fi
      sfdisk --part-label "$image" "$n" "$label"
    done
  '';
}
