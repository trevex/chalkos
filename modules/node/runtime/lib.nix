# The helpers chalkosLib offers extensions for outputs and effects: renderers of chalkos.outputs,
# and the effects that restart or reload units when outputs change.
{ lib, pkgs }:
let
  chalkos-render = pkgs.callPackage ../../../nix/chalkos-render.nix { };
  # The name of the file a renderer of path writes: the path's own, less suffix, and less the hash
  # a file the store holds by itself starts with.
  fileName =
    suffix: path:
    let
      name = baseNameOf (toString path);
      hashed = builtins.match "[0-9a-df-np-sv-z]{32}-(.+)" name;
    in
    lib.removeSuffix suffix (if hashed == null then name else lib.head hashed);
in
{
  render = {
    # A Go text/template, which writes the file named as the template less .tmpl.
    template = path: "${lib.getExe chalkos-render} template ${path} ${fileName ".tmpl" path}";
    # A jq program, whose output, raw for a string, writes the file named as the program less .jq.
    jq =
      path:
      toString (
        pkgs.writeShellScript "chalkos-render-jq-${fileName ".jq" path}" ''
          exec ${lib.getExe pkgs.jq} -r -f ${path} > "$OUT/${fileName ".jq" path}"
        ''
      );
    # The documents of kinds as they are, each as <Kind>/<name>.json.
    documents = kinds: "${lib.getExe chalkos-render} documents ${lib.concatStringsSep " " kinds}";
  };
  restartOn = outputs: units: {
    on = outputs;
    restart = units;
  };
  reloadOn = outputs: units: {
    on = outputs;
    reload = units;
  };
}
