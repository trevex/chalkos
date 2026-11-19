# update-pins: after a change of a version in modules/node/kubernetes/releases.nix, sets what
# follows the Kubernetes releases there in the chalkos repository of the working directory:
# - the hash of each release's source, which kubelet.nix builds the kubelet from;
# - the images of nix/testing/kubernetes-images.json that follow the newest release: its control
#   plane and kube-proxy, those of the patch release before it, which the HA test starts from, and
#   the etcd, CoreDNS and pause releases kubeadm of the newest release names, each by the digest
#   of its amd64 manifest and the hash of its OCI layout;
# - every reference to an image it changes, in every file of the repository but the Go tests,
#   whose images are data of their own: those the test fixtures name by the amd64 manifest's
#   digest, those the templates, examples, kinds and documentation name by the image index's
#   digest, and the tag beside such a digest.
# A second run changes nothing. It needs network access: GitHub for the sources, the registries for
# the images.

releases=modules/node/kubernetes/releases.nix
images=nix/testing/kubernetes-images.json
if [[ ! -f $releases || ! -f $images ]]; then
  echo "error: update-pins runs in the root of the chalkos repository" >&2
  exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# The replacements, a line each: <from> TAB <to>, or <from> TAB <to> TAB <marker> for a tag that
# changes only in the files that hold the marker, an index digest, and only where no more of a tag
# or a digest follows.
pairs=$work/pairs
touch "$pairs"

# The source of each release, as fetchFromGitHub fetches it. A source whose hash is right and in
# the store needs no download.
nix eval --json --file "$releases" >"$work/releases.json"
newest=$(jq -r 'keys[]' "$work/releases.json" | sort -V | tail -n 1)
for minor in $(jq -r 'keys[]' "$work/releases.json"); do
  version=$(jq -r --arg minor "$minor" '.[$minor].version' "$work/releases.json")
  hash=$(jq -r --arg minor "$minor" '.[$minor].hash' "$work/releases.json")
  url=https://github.com/kubernetes/kubernetes/archive/refs/tags/v$version.tar.gz
  if ! nix store prefetch-file --json --unpack --expected-hash "$hash" "$url" >"$work/source.json" 2>/dev/null; then
    nix store prefetch-file --json --unpack "$url" >"$work/source.json"
  fi
  fetched=$(jq -r .hash "$work/source.json")
  if [[ $fetched != "$hash" ]]; then
    printf '%s\t%s\n' "$hash" "$fetched" >>"$pairs"
    echo "Kubernetes $version: source $fetched"
  fi
  if [[ $minor == "$newest" ]]; then
    kubernetes=$version
    newestSource=$(jq -r .storePath "$work/source.json")
  fi
done

# The releases of etcd, CoreDNS and pause that kubeadm of the newest release names.
constants=$newestSource/cmd/kubeadm/app/constants/constants.go
constant() {
  local value
  value=$(sed -nE "s/^[[:space:]]*$1 = \"([^\"]+)\"$/\1/p" "$constants")
  if [[ -z $value ]]; then
    echo "error: Kubernetes $kubernetes: $constants defines no $1" >&2
    exit 1
  fi
  echo "$value"
}
etcd=$(constant DefaultEtcdVersion)
coredns=$(constant CoreDNSVersion)
pause=$(constant PauseVersion)
patch=${kubernetes##*.}
if ((patch == 0)); then
  echo "error: the HA test starts from the patch release before Kubernetes $kubernetes, which $newest lacks" >&2
  exit 1
fi
previous=$newest.$((patch - 1))

count=$(jq length "$images")
for ((i = 0; i < count; i++)); do
  name=$(jq -r ".[$i].name" "$images")
  tag=$(jq -r ".[$i].tag" "$images")
  digest=$(jq -r ".[$i].digest" "$images")
  haStart=$(jq -r ".[$i].haStart // false" "$images")
  case $name in
  registry.k8s.io/kube-*)
    if [[ $haStart == true ]]; then
      want=v$previous
    else
      want=v$kubernetes
    fi
    ;;
  registry.k8s.io/etcd) want=$etcd ;;
  registry.k8s.io/coredns/coredns) want=$coredns ;;
  registry.k8s.io/pause) want=$pause ;;
  *) continue ;;
  esac
  wantDigest=$(crane digest --platform linux/amd64 "$name:$want")
  if [[ $want == "$tag" && $wantDigest == "$digest" ]]; then
    continue
  fi
  image-layout "$name@$wantDigest" "$work/layout-$i"
  wantHash=$(nix hash path "$work/layout-$i")
  jq --argjson i "$i" --arg tag "$want" --arg digest "$wantDigest" --arg hash "$wantHash" \
    '.[$i] += {$tag, $digest, $hash}' "$images" >"$work/images.json"
  cat "$work/images.json" >"$images"
  printf '%s\t%s\n' "$name:$tag@$digest" "$name:$want@$wantDigest" >>"$pairs"
  if [[ $haStart != true ]]; then
    index=$(crane digest "$name:$tag")
    wantIndex=$(crane digest "$name:$want")
    printf '%s\t%s\n' "$name:$tag@$index" "$name:$want@$wantIndex" "$index" "$wantIndex" >>"$pairs"
    printf '%s\t%s\t%s\n' "$name:$tag" "$name:$want" "$index" >>"$pairs"
  fi
  echo "$name:$want: $wantDigest"
done

# Every replacement at once, so that a reference already replaced is not replaced again, as the
# HA test's start becomes the release the others leave.
if [[ -s $pairs ]]; then
  git ls-files -z -- ':!*_test.go' | xargs -0 perl -e '
    my $table = shift;
    open my $t, "<", $table or die "$table: $!";
    my @pairs = map { chomp; [split /\t/] } <$t>;
    for my $file (@ARGV) {
      next if -l $file || !-f $file;
      open my $in, "<", $file or die "$file: $!";
      my $text = do { local $/; <$in> };
      close $in;
      my (%to, @patterns);
      for (@pairs) {
        my ($from, $to, $marker) = @$_;
        if (defined $marker) {
          next if index($text, $marker) < 0;
          push @patterns, quotemeta($from) . "(?![\\w.\@-])";
        } else {
          next if index($text, $from) < 0;
          push @patterns, quotemeta($from);
        }
        $to{$from} = $to;
      }
      next unless @patterns;
      my $pattern = join "|", sort { length($b) <=> length($a) } @patterns;
      (my $changed = $text) =~ s/($pattern)/$to{$1}/g;
      next if $changed eq $text;
      open my $out, ">", $file or die "$file: $!";
      print $out $changed;
      close $out;
      print "updated $file\n";
    }
  ' "$pairs"
fi
