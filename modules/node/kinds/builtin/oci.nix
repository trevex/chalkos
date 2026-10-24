# The OCI registries chalkd and containerd pull from: their credentials, transport and mirrors.
{ lib, ... }:
let
  inherit (lib) mkOption types;
  inherit (import ./lib.nix) builtin dnsName groups;
  # A host as registries are named: a DNS name, an IPv4 address or an IPv6 address in brackets.
  # The closing bracket stays unescaped: POSIX ERE has no \], and a lone ] is literal in ERE,
  # ECMA-262 and Go alike.
  host = "(${dnsName}|\\[[0-9A-Fa-f:.]+])";
  # A rule that no two documents of kind have the same spec.<field>. The message names the
  # documents that do, and says the kind is given once per `per`.
  uniqueBy =
    {
      kind,
      field,
      same,
      per,
    }:
    {
      description = "No two ${kind} documents ${same}.";
      rule = "documents.all(n, documents.all(m, n == m || documents[n].spec.${field} != documents[m].spec.${field}))";
      messageExpression = ''"the ${kind} documents " + documents.filter(n, documents.exists(m, m != n && documents[m].spec.${field} == documents[n].spec.${field})).join(", ") + " ${same}; give one ${kind} per ${per}"'';
    };
in
{
  chalkos.kinds = {
    Registry = builtin groups.oci // {
      multiple = true;
      description = "The credentials and transport of an OCI registry host, with which chalkd pulls OS images and, on images that run Kubernetes, containerd pulls container images.";
      # chalkd authenticates a pull with the Registry of the image's host, so a host has one; it
      # sends a user and a password together, so a Registry gives both or neither.
      rules = [
        (uniqueBy {
          kind = "Registry";
          field = "host";
          same = "name the same host";
          per = "host";
        })
        {
          description = "A Registry gives username and password together, or neither.";
          rule = "documents.all(n, (documents[n].spec.username == null) == (documents[n].spec.password == null))";
          messageExpression = ''documents.filter(n, (documents[n].spec.username == null) != (documents[n].spec.password == null)).map(n, "Registry " + n).join(", ") + ": give username and password together, or neither to pull anonymously"'';
        }
      ];
      spec = {
        host = mkOption {
          type = types.strMatching "${host}(:[0-9]+)?";
          example = "ghcr.io";
          description = "Host of a registry or of a mirror's endpoint, as image references and endpoints name it, with its port unless it is 443; an IPv6 address in brackets. chalkd pulls OS images from this host with the Registry's credentials, over plain HTTP when insecure is set. On images that run Kubernetes, the kubelet hands these credentials to containerd for the container images of this host, and containerd sends them to every endpoint of the host's Mirror too, over plain HTTP to an endpoint whose host is insecure. The kubelet compares the host, in lower case and with its port, case-sensitively with the image's host as the pod spec writes it, so pods name the host in lower case. An IPv6 host with credentials needs its port, 443 included, for the kubelet to match it.";
        };
        username = mkOption {
          type = types.nullOr types.nonEmptyStr;
          default = null;
          description = "User chalkd authenticates to the host as, with the password, and containerd too on images that run Kubernetes; null pulls anonymously.";
        };
        password = mkOption {
          type = types.nullOr lib.chalkos.types.secretRef;
          default = null;
          description = "Password or token of the user, from a Secret; null pulls anonymously.";
        };
        insecure = mkOption {
          type = types.bool;
          default = false;
          description = "Whether chalkd and containerd reach this host over plain HTTP, with credentials and images unencrypted, and a Mirror may name an http:// endpoint on it. Without it, chalkd pulls over HTTPS alone, also from private and loopback addresses.";
        };
      };
    };
    Mirror = builtin groups.oci // {
      multiple = true;
      description = "The mirror endpoints that chalkd and containerd try, in order, before a registry itself.";
      # chalkd and containerd try one list of endpoints for a registry, so a registry has one.
      rules = [
        (uniqueBy {
          kind = "Mirror";
          field = "registry";
          same = "mirror the same registry";
          per = "registry";
        })
      ];
      spec = {
        registry = mkOption {
          type = types.strMatching "${host}(:[0-9]+)?";
          example = "docker.io";
          description = "Host of the registry as image references name it, with its port unless it is 443; an IPv6 address in brackets.";
        };
        endpoints = mkOption {
          type = types.nonEmptyListOf (types.strMatching "https?://${host}(:[0-9]+)?(/[A-Za-z0-9._~%/-]*)?");
          example = [ "https://mirror.example.com" ];
          description = "URLs of the mirror's endpoints, which chalkd and containerd try in order before the registry itself. A path is where the endpoint serves the registry API, below <path>/v2 as containerd takes it. chalkd authenticates to an endpoint with the Registry of the endpoint's host, and an http:// endpoint needs that Registry to set insecure.";
        };
      };
    };
  };
}
