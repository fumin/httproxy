# HTTP Proxy

## Deploy

Make sure `monero-wallet-rpc` is running:

```
curl --silent http://127.0.0.1:28088/json_rpc -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":"0","method":"refresh"}'
```

then run:

```
go build ./cmd/server
mv server $GOPATH/bin/httproxy
systemctl --user restart httproxy
systemctl --user status httproxy --no-pager -l
```

To backup the database, run `sqlite3 sqlite.db "VACUUM INTO 'backup_$(date +%Y%m%d_%H%M%S).db'"`

## Develop

Before developing and testing, make sure to

* create a file "./secret" in `key=value` format, populating the `config.Secret` struct.
* run `cmd/backend` to start the monero node and other backends.

## TODO
