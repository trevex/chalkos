# A page with snippets

A node's file, which its schema accepts:

```yaml title="nodes/w1.yaml"
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: w1}
spec:
  hostname: node-metal
```

- A block in a list, indented, with a misspelled field:

    ```yaml
    apiVersion: chalkos.dev/v1alpha1
    kind: Node
    metadata: {name: w2}
    spec:
      hostnme: node-metal
    ```

A kind no image declares, and a Kubernetes object, which is not checked:

```yaml
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Manifest
metadata: {name: extra}
spec: {}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec: {replicas: 2}
```

A fragment, which is not checked:

```yaml
spec:
  hostnme: node-metal
```

An extension's kind, whose schema refuses the field:

```yaml
apiVersion: rack.example.org/v1alpha1
kind: Rack
metadata: {name: rack}
spec: {position: u12}
```

A block that is not YAML:

```yaml
key: [unclosed
```

A whole file and a fragment of one:

```nix title="flake.nix"
{
  outputs = _: { };
}
```

```nix
chalkos.kernel.moduleGroups = [ "gpu" ];
```

A list, which is not a config document:

```yaml
- one
- two
```

Two documents, the second with a misspelled field, and a syntax error on a block's second line:

```yaml
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: w4}
spec: {hostname: node-metal}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: w5}
spec: {hostnme: node-metal}
```

```yaml
key: value
other: a: b
```

A block that only includes a section of a file, which the image build evaluates:

```nix
--8<-- "examples/homelab/extensions/bgp.nix:kind"
```
