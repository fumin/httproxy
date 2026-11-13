# HTTP Proxy

## Deploy

Make sure `monero-wallet-rpc` is running:

```
curl --silent http://127.0.0.1:28088/json_rpc -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":"0","method":"get_address"}'
```

then run:

```
go build ./cmd/server
mv server $GOPATH/bin/httproxy
kill $(lsof -t -i :28877)
$GOPATH/bin/httproxy -c=prod > $HOME/var/httproxy/log 2>&1 &
```

## Develop

Before developing and testing, make sure to

* create a file "./secret" in `key=value` format, populating the `config.Secret` struct.
* run `cmd/backend` to start the monero node and other backends.
