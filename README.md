# pgh

Single-file PostgreSQL databases, sqlite3-style.

`pgh` stores an entire PostgreSQL database in one ordinary file. The file is
an ext4 filesystem image, mounted without root — via a udisks kernel loop
mount where available, or [fuse2fs] anywhere FUSE works — containing a
PostgreSQL data directory. Copy the file, and you've copied the database.

```console
$ pgh temp.pdb
psql (14.23)
Type "help" for help.

postgres=#
```

That one command creates the file (if needed), formats it, mounts it, runs
`initdb`, starts PostgreSQL, and drops you into `psql`. When `psql` exits,
the server is stopped and the file unmounted.

[fuse2fs]: https://manpages.debian.org/testing/fuse2fs/fuse2fs.1.en.html

## Requirements

- Linux
- `udisks2` (fast kernel loop mounts, local sessions) and/or
  `fuse2fs` + `fusermount3` (fallback that works anywhere FUSE does)
- `mkfs.ext4` (e2fsprogs)
- PostgreSQL server binaries (`initdb`, `pg_ctl`, `postgres`) and `psql`

On Debian/Ubuntu:

```console
$ sudo apt install fuse2fs e2fsprogs postgresql postgresql-client
```

No root is required at runtime: the image is formatted with
`root_owner=$(id -u)` and mounted with fuse2fs, and PostgreSQL runs as you.

## Install

```console
$ go install github.com/cwarden/pgh@latest
```

## Usage

### Interactive shell

```console
$ pgh temp.pdb                     # create (1G sparse by default) and connect
$ pgh -s 4G big.pdb                # create with a different size
$ pgh temp.pdb -c 'select 1'       # everything after the file goes to psql
```

If the server is already running (e.g. via `pgh start`), `pgh temp.pdb` just
connects and leaves the server running when psql exits. Otherwise pgh starts
it and tears it down again when psql exits.

### Background server

```console
$ DB_URL=$(pgh start temp.pdb)     # start, print connection string on stdout
$ psql "$DB_URL"                   # connect from any other process
$ pgh stop temp.pdb                # stop the server and unmount the file
```

`pgh start` is idempotent: if the database is already running it just prints
the connection string.

By default the server only listens on a Unix socket in a private runtime
directory. Add `--port` to also listen on TCP:

```console
$ pgh start -p 5433 temp.pdb
$ psql -h 127.0.0.1 -p 5433 -U $USER postgres
```

### Status

```console
$ pgh status                       # all databases pgh knows about
$ pgh status temp.pdb              # one database
temp.pdb: running (pid 797492, kernel mount)
  postgresql://cwarden@/postgres?host=%2Frun%2Fuser%2F1000%2Fpgh%2Ftemp-00e34412%2Fsock&port=5432
```

Runtime state for database files that have since been deleted is cleaned up
(and not reported) as part of `pgh status`.

### Deleting a database

Stop it, then delete the file:

```console
$ pgh stop temp.pdb && rm temp.pdb
```

## Flags

| Flag | Commands | Description |
|------|----------|-------------|
| `-s, --size` | shell, `start` | Size of a newly created database file (default `1G`). The file is sparse, so unused space costs nothing. |
| `-p, --port` | shell, `start` | Also listen on `127.0.0.1:PORT` (default: Unix socket only). |
| `--durable` | shell, `start` | Make commits wait for the WAL to reach disk (PostgreSQL's default behavior). |
| `--bindir` | all | PostgreSQL binary directory (default: autodetect via `pg_config`, `PATH`, then `/usr/lib/postgresql/*/bin` and friends). `PGH_BINDIR` works too. |

## How it works

1. A sparse file is created and formatted as ext4 with
   `mkfs.ext4 -E root_owner=$(id -u):$(id -g)`, so the filesystem is owned by
   you rather than root.
2. The image is mounted, trying the fastest available strategy:
   1. **kernel loop mount via udisks** (`udisksctl loop-setup` + `mount`) —
      the real in-kernel ext4 driver, near-native performance. Polkit
      typically authorizes this for local desktop sessions without root.
      udisks picks the mountpoint (under `/media`), so
      `$XDG_RUNTIME_DIR/pgh/<name>-<hash>/mnt` becomes a symlink to it.
   2. **fuse2fs** — works anywhere FUSE does (including over ssh, where
      polkit usually denies udisks), but the userspace ext4 driver is
      roughly 3x slower on queries and 5x on bulk loads.

   Set `PGH_MOUNT=fuse2fs` to skip udisks. `pgh status` shows which backend
   a mounted database is using.
3. `initdb` creates a data directory inside the image on first use
   (`trust` auth, superuser = your username — the socket directory is
   only accessible to you).
4. `pg_ctl` starts PostgreSQL with its Unix socket in the runtime directory
   (kept outside the image for socket-path-length reasons). The server log
   lives inside the image at `postgres.log`.

   By default the server runs with `synchronous_commit = off`, so commits
   don't wait for the WAL to be flushed to disk. fsync round-trips are the
   dominant cost of the FUSE mount, and skipping them gives roughly 9x the
   commit throughput. The trade-off is that a crash can lose the last few
   hundred milliseconds of commits — it can never corrupt the database
   (unlike `fsync = off`). Pass `--durable` for full durability.
5. `psql` connects to the `postgres` database.

The runtime state (mountpoint, socket, lock) lives under
`$XDG_RUNTIME_DIR/pgh/` and is keyed by a hash of the image's absolute path,
so concurrent databases don't collide and `pgh` invocations on the same file
serialize on a lock.

## Caveats

- The database file must not be moved or modified while mounted.
- This is a convenience tool for local development and scratch databases,
  not a production setup. As a rough guide (pgbench, scale 10, 4 clients):
  ~12,600 tps on a kernel mount vs ~8,600 tps on fuse2fs vs ~24,300 tps on
  a plain data directory with the same settings.
- If a `pgh` shell that started the server exits while other clients are
  connected, they are disconnected (fast shutdown). Use `pgh start` first
  if multiple processes need the database.

## Development

```console
$ go build ./...
$ go test ./internal/db/ ./cmd/    # includes a full lifecycle integration test
$ go test -short ./...             # unit tests only
```
