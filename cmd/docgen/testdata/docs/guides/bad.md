# Bad commands

```sh
chalkctl instal cp1
chalkctl etcd membres
chalkctl install cp1 --fingerprnt 00ff
chalklab create --nodes
chalkctl install cp1 -insecure
chalkctl logs cp1 -x
ls | chalklab destroy --force
echo "key: $(chalkctl recovery-ky w1)"
nix run .#chalkctl -- statsu cp1
sudo -E chalkctl rebot w1
env FOO=1 chalkctl disks w1 --bogus
time chalklab strat
chalkctl logs cp1 > log.txt 2>&1 --unti chalkd.service
chalkctl logs cp1 -fx
chalkctl install <node> --fingerprnt <fp>
chalkctl instal <node> < input.txt 2> /dev/null
```

```console
$ chalkctl status cp1 --bogus
$ chalkctl install cp1 \
> --fingerprnt 00ff
```

```Bash
chalkctl bogus
```

```sh
$ chalkctl statuss cp1
nix develop -c chalkctl instal cp1
nix develop .#ci --command chalklab creat
timeout 60 chalkctl rebot w1
timeout -s KILL 5m chalkctl logs cp1 --bogus
watch -n 5 chalkctl statsu cp1
watch 'chalklab statsu'
exec chalkctl disk w1
sudo --user root chalkctl staus cp1
echo "key: `chalkctl recovery-ky w2`"
```

``` title="install.sh"
chalkctl install cp1 --fingerprnt 00ff
```
