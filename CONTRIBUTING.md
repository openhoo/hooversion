# Contributing

Open an issue before changing release semantics, repository mutation, GitHub
integration, or the public action contract. Small fixes may go directly to a
pull request.

## Development

Use the Go version declared by `go.mod`.

```sh
gofmt -w cmd internal
go vet ./...
go test -race -count=1 ./...
go build ./...
```

Release behavior needs temporary-repository tests covering dry runs, recovery,
atomic writes, and failed remote operations.

Commits use Conventional Commits. Pull requests must explain compatibility and
security impact. Maintainers squash-merge using the Conventional Commit pull
request title. Generated files and module sums must accompany source changes.
