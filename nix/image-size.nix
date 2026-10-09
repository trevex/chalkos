# What an image's store and UKI take, from its raw image and repart's report:
#
#   chalkos-image-size measure RAW REPART_OUTPUT
#     prints the bytes of the store's data and of its hash tree as its dm-verity superblock counts
#     them, which an upgrade sends: the data is the erofs the root hash covers, less than its
#     partition holds.
#   chalkos-image-size fits NAME RAW REPART_OUTPUT UKI UKIS STORE_SIZE ESP_SIZE
#     fails when the store's data takes more than 80% of STORE_SIZE, or UKIS copies of the UKI
#     more than 80% of ESP_SIZE. Sizes are repart's, as 3G; a STORE_SIZE of - skips the store.
{
  writeShellApplication,
  coreutils,
  jq,
}:
writeShellApplication {
  name = "chalkos-image-size";
  runtimeInputs = [
    coreutils
    jq
  ];
  text = ''
    measure() {
      local raw=$1 partitions=$2 offset superblock dataBlockSize hashBlockSize dataBlocks
      local perBlock hashBlocks n
      offset=$(jq -er '[.[] | select(.type | endswith("-verity"))] | if length == 1 then .[0].offset else error("want one verity partition") end' \
        "$partitions")
      superblock=$(mktemp)
      dd if="$raw" of="$superblock" bs=512 skip=$((offset / 512)) count=1 status=none
      if [[ $(head -c 6 "$superblock") != verity ]]; then
        echo "error: $raw has no verity superblock at offset $offset" >&2
        exit 1
      fi
      field() { od -An -t"u$2" -j "$1" -N "$2" "$superblock" | tr -d ' '; }
      dataBlockSize=$(field 64 4)
      hashBlockSize=$(field 68 4)
      dataBlocks=$(field 72 8)
      rm "$superblock"

      # The tree as veritysetup lays it out: the superblock's block, then levels of SHA-256
      # digests up to a single block.
      perBlock=$((hashBlockSize / 32))
      hashBlocks=$(((512 + hashBlockSize - 1) / hashBlockSize))
      n=$dataBlocks
      while ((n > 1)); do
        n=$(((n + perBlock - 1) / perBlock))
        hashBlocks=$((hashBlocks + n))
      done
      echo "$((dataBlocks * dataBlockSize)) $((hashBlocks * hashBlockSize))"
    }

    fits() {
      local name=$1 raw=$2 partitions=$3 uki=$4 ukis=$5 storeSize=$6 espSize=$7
      local sizes data slot ukiBytes esp
      if [[ $storeSize != - ]]; then
        sizes=$(measure "$raw" "$partitions")
        read -r data _ <<<"$sizes"
        slot=$(numfmt --from=iec "$storeSize")
        if ((data * 5 > slot * 4)); then
          echo "error: $name: the store's data takes $data bytes, more than 80% of its slot of $slot (chalkos.disk.storeSize)" >&2
          exit 1
        fi
      fi
      ukiBytes=$(stat -L -c %s "$uki")
      esp=$(numfmt --from=iec "$espSize")
      if ((ukis * ukiBytes * 5 > esp * 4)); then
        echo "error: $name: $ukis UKIs of $ukiBytes bytes take more than 80% of the ESP's $esp (chalkos.disk.espSize)" >&2
        exit 1
      fi
    }

    case ''${1:-} in
      measure) measure "''${@:2}" ;;
      fits) fits "''${@:2}" ;;
      *)
        echo "usage: chalkos-image-size measure RAW REPART_OUTPUT | fits NAME RAW REPART_OUTPUT UKI UKIS STORE_SIZE ESP_SIZE" >&2
        exit 2
        ;;
    esac
  '';
}
