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
echo "key: $(chalkctl recovery-key w2 --flake "$(pwd)")"
nix run .#chalkctl -- status cp1 --config client.json
nix run github:trevex/chalkos#chalklab -- create --nodes cp1
sudo -E chalkctl status cp2
sudo -u root chalkctl status cp3
env FOO=1 chalkctl status cp4
env -i PATH=/bin chalkctl status cp5
time chalkctl status cp6
chalkctl status cp7 --flake $(pwd)
chalkctl logs cp1 > log.txt 2>&1 --unit chalkd.service
chalkctl logs cp1 &> log.txt -f
chalkctl logs cp1 -fh
chalkctl install <node> --fingerprint <fp> --image <image.raw>
chalkctl status cp17 < input.txt 2> /dev/null
```

```console
$ chalklab status
chalkctl bogus is output here, not a command
$ chalkctl install cp1 \
> --fingerprint 00ff
```

```nix
{ chalkctl = "bogus"; }
```

!!! note

    ```bash
    chalkctl completion bash
    ```

```SH
chalkctl reboot w1
```

```sh
$ chalkctl status cp8
nix develop -c chalkctl status cp9
nix develop .#ci --command chalklab status
timeout 60 chalkctl status cp10
timeout -s KILL --kill-after=10s 5m chalkctl status cp11
watch -n 5 chalkctl status cp12
watch 'chalkctl status cp13 | head'
exec chalkctl status cp14
sudo --user root chalkctl status cp15
echo "key: `chalkctl recovery-key w3`"
```

``` title="status.sh"
chalkctl status cp16
```

```{ .yaml title="values.yaml" }
chalkctl bogus
```
