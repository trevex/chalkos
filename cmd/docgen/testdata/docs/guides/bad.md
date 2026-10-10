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
```

```console
$ chalkctl status cp1 --bogus
$ chalkctl install cp1 \
> --fingerprnt 00ff
```

```Bash
chalkctl bogus
```
