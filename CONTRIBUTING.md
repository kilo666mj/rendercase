# Contributing

Issues and pull requests are welcome. Before submitting a change, run:

```sh
go test ./...
go vet ./...
```

Do not commit credentials, access tokens, private certificates, real user data,
or private network details. Use `example.com` and the documentation address
ranges from RFC 5737 and RFC 3849 in tests and examples.
