# Contributing

Build from a checkout of this repository with Go 1.22+ and Git 2.31+ on
macOS or Linux.

```sh
go build -o agent-qa .
./agent-qa --help
```

Run the test suite before submitting changes.

```sh
go test ./...
```

The integration tests create temporary Git repositories, linked worktrees,
and loopback HTTP servers. They require no browser or native device.
