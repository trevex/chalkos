# Evaluation tests of the images a release publishes, nix/release.nix, and of version.nix, which
# release-please updates. Returns lib.runTests failures, empty on success.
{
  lib,
  self,
  images,
}:
let
  inherit (self.lib) version kubernetesReleases;
  tag = "v${version}";
  published = lib.attrNames (lib.filterAttrs (_: release: release.publish) kubernetesReleases);
  # Each published image by its repository's name: its image name, platform, layer and Kubernetes
  # release.
  expected = lib.listToAttrs (
    lib.concatMap
      (
        kind:
        map
          (
            platform:
            lib.nameValuePair "${kind.repository}-${platform}" {
              name = "${kind.name}-${platform}";
              inherit platform;
              inherit (kind) layer kubernetes;
            }
          )
          [
            "metal"
            "kvm"
          ]
      )
      (
        map (minor: {
          repository = "kubernetes-${minor}";
          name = "kubernetes";
          layer = "kubernetes";
          kubernetes = kubernetesReleases.${minor}.version;
        }) published
        ++ [
          {
            repository = "router";
            name = "router";
            layer = "router";
            kubernetes = null;
          }
        ]
      )
  );
  config = i: i.nixos.config;
  # The lines of each file release-please updates, an entry of its extra-files: a path or a
  # {type, path}.
  extraFiles =
    map (file: file.path or file)
      (lib.importJSON ../../release-please-config.json).packages.".".extra-files;
  extraFileLines = map (
    file: lib.splitString "\n" (builtins.readFile (../.. + "/${file}"))
  ) extraFiles;
  # The mentions of a release of chalkos a line holds: the chalkos input of a flake, a page or a
  # download of a release, and a published image by its tag.
  releaseMentions =
    line:
    map lib.head (
      lib.filter lib.isList (
        builtins.split "((github:)?trevex/chalkos/(releases/(download|tag)/)?v[0-9]+\\.[0-9]+\\.[0-9]+|ghcr\\.io/trevex/chalkos/[a-z0-9.-]+:v[0-9]+\\.[0-9]+\\.[0-9]+)" line
      )
    );
  # The pages, the README, and the files of the templates and the examples, which name releases of
  # chalkos.
  mentionFiles =
    let
      root = toString ../..;
      textual =
        f:
        lib.any (e: lib.hasSuffix e f) [
          ".md"
          ".nix"
          ".yaml"
          ".yml"
          ".sh"
          ".json5"
          ".txt"
        ];
    in
    [ "README.md" ]
    ++ lib.filter textual (
      map (f: lib.removePrefix "${root}/" (toString f)) (
        lib.concatMap lib.filesystem.listFilesRecursive [
          ../../docs
          ../../templates
          ../../examples
        ]
      )
    );
  # The x.y.z versions a line names.
  versions =
    line: lib.concatLists (lib.filter lib.isList (builtins.split "([0-9]+\\.[0-9]+\\.[0-9]+)" line));
in
lib.runTests {
  # One image of each published Kubernetes minor and of the router on each platform, named after
  # its repository below ghcr.io/trevex/chalkos and tagged v<version>.
  testImages = {
    expr = lib.mapAttrs (_: i: {
      inherit (i) name platform layer;
      kubernetes =
        if i.layer == "kubernetes" then (config i).chalkos.kubernetes.package.version else null;
    }) images;
    inherit expected;
  };
  testRepositories = {
    expr = lib.mapAttrs (_: i: { inherit (i) repository tag; }) images;
    expected = lib.mapAttrs (name: _: {
      repository = "ghcr.io/trevex/chalkos/${name}";
      inherit tag;
    }) expected;
  };
  # Every image and its OCI image layout evaluate to a derivation.
  testDerivations = {
    expr = lib.all (i: lib.isString i.drvPath && lib.isString i.oci.drvPath) (lib.attrValues images);
    expected = true;
  };
  # The images carry no OS CA and record the release's tag and the commit's revision.
  testProvenance = {
    expr = lib.mapAttrs (_: i: {
      inherit ((config i).chalkos.maintenance) osCA;
      inherit ((config i).chalkos) provenance;
    }) images;
    expected = lib.mapAttrs (_: _: {
      osCA = null;
      provenance = {
        inherit tag;
        revision = self.rev or self.dirtyRev or null;
      };
    }) expected;
  };
  # The manifests' annotations: the tag as the version, the revision, chalkos's source, the
  # commit's time in RFC 3339, and a Kubernetes image's release.
  testAnnotations = {
    expr = lib.mapAttrs (
      _: i:
      let
        a = (config i).chalkos.imageDirectory.annotations;
        created = a."org.opencontainers.image.created";
      in
      removeAttrs a [ "org.opencontainers.image.created" ]
      // {
        created = builtins.match "[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z" created != null;
      }
    ) images;
    expected = lib.mapAttrs (
      _: image:
      {
        "org.opencontainers.image.version" = tag;
        "org.opencontainers.image.source" = "https://github.com/trevex/chalkos";
        created = true;
      }
      // lib.optionalAttrs (self ? rev || self ? dirtyRev) {
        "org.opencontainers.image.revision" = self.rev or self.dirtyRev;
      }
      // lib.optionalAttrs (image.kubernetes != null) {
        "dev.chalkos.kubernetes.version" = image.kubernetes;
      }
    ) expected;
  };
  # release-please's generic updater sets the version on the line its marker comment ends.
  testVersionFile = {
    expr = map (builtins.match ''"([0-9]+\.[0-9]+\.[0-9]+)" # x-release-please-version'') (
      lib.filter (lib.hasInfix "x-release-please-version") (
        lib.splitString "\n" (builtins.readFile ../../version.nix)
      )
    );
    expected = [ [ version ] ];
  };
  # Between its start and end markers the generic updater replaces every x.y.z, so a block in the
  # files it updates names no other, such as an IPv4 address.
  testVersionBlocks =
    let
      blockLines =
        lines:
        (lib.foldl'
          (
            state: line:
            if lib.hasInfix "x-release-please-start" line then
              state // { inBlock = true; }
            else if lib.hasInfix "x-release-please-end" line then
              state // { inBlock = false; }
            else if state.inBlock then
              state // { lines = state.lines ++ [ line ]; }
            else
              state
          )
          {
            inBlock = false;
            lines = [ ];
          }
          lines
        ).lines;
    in
    {
      expr = lib.unique (lib.concatMap (lines: lib.concatMap versions (blockLines lines)) extraFileLines);
      expected = [ version ];
    };
  # Every mention of a chalkos release is one release-please updates: on a line with its marker,
  # or between its start and end markers, in a file of its extra-files. A mention it does not
  # update names an old release once the next is out. The result names each other mention by its
  # file and line.
  testReleaseMentions = {
    expr = lib.concatMap (
      file:
      let
        listed = lib.elem file extraFiles;
      in
      (lib.foldl'
        (
          state: line:
          let
            n = state.n + 1;
            marked = listed && (state.inBlock || lib.hasInfix "x-release-please-version" line);
            stale = releaseMentions line;
          in
          {
            inherit n;
            inBlock =
              if lib.hasInfix "x-release-please-start" line then
                true
              else if lib.hasInfix "x-release-please-end" line then
                false
              else
                state.inBlock;
            found =
              state.found ++ lib.optional (!marked && stale != [ ]) "${file}:${toString n}: ${toString stale}";
          }
        )
        {
          n = 0;
          inBlock = false;
          found = [ ];
        }
        (lib.splitString "\n" (builtins.readFile (../.. + "/${file}")))
      ).found
    ) mentionFiles;
    expected = [ ];
  };
  # The generic updater replaces every x.y.z of a line its marker comment ends, so such a line
  # names no other, such as the Kubernetes release 1.37.2.
  testVersionLines = {
    expr = lib.unique (
      lib.concatMap versions (
        lib.filter (lib.hasInfix "x-release-please-version") (lib.concatLists extraFileLines)
      )
    );
    expected = [ version ];
  };
}
