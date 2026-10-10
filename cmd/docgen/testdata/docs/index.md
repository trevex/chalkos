# Good commands

```sh
# A comment that names chalkctl bogus is no command.
chalkctl install cp1 --fingerprint 00ff --image ./image
chalkctl gen secrets --plaintext
KUBECONFIG=kubeconfig sudo chalkctl status cp1 --config=client.json | less
chalklab create --nodes cp1,w1 \
  --memory 1024
chalkctl logs cp1 -f --unit chalkd.service > log.txt 2>&1
chalklab console cp1 -f=false
chalkctl etcd members --help
chalkctl upgrade --max-unavailable N # placeholders are not checked
echo "$(chalkctl recovery-key w1)"
```

```console
$ chalklab status
chalkctl bogus is output here, not a command
```

```nix
{ chalkctl = "bogus"; }
```

!!! note

    ```bash
    chalkctl completion bash
    ```
