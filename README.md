### This is work in progress

`dumptruck` tries to identify the mysql server version by looking at the files in mysql data directory.
Currently looking for .frm and binlog files. If it doesn't identify the version, you can enter it manually.

It then spins up the corresponding mysql/mariadb container with podman, mounts the data directory, queries the database and dumps the databases of your choosing. 

It relies on the `file` and `find` utilities and should work on MacOS and Linux. 

### How to use

Just run the binary and follow the instructions or give the mysql data directory path as the first argument to the executable,
e.g: `dumptruck .` if you are in the mysql data directory or `dumptruck /path/to/data/dir`.

### Flags

```
Usage: dumptruck [flags]

Flags:
  -a, --auto             Non-interactive mode: auto-fix permissions, auto-detect version,
                         dump all user databases, remove container after
  -d, --data-dir string  Path to MySQL data directory
  -f, --fix-permissions  Automatically fix file permissions without asking
  -k, --no-remove        Do not remove container after dump
  -v, --version string   Database type:version (e.g. mysql:8.0, mariadb:10.11).
                         Skips version detection
```

All flags are optional. The default is fully interactive mode. Use `-a` for a fully
non-interactive run, or combine individual flags for finer control.

#### Auto mode (non-interactive)

Pass `-a` or `--auto` to run without prompts. This is useful for scripting or CI pipelines.
In auto mode:

- **Permissions** are automatically fixed if restrictive permissions are detected.
- **Database version** is auto-detected from the data directory (the most common version is selected if multiple are found).
- **User databases** are dumped automatically, excluding system databases (`information_schema`, `mysql`, `performance_schema`, `sys`).
- **Container** is removed automatically after the dump completes.

```
# Fully automatic with positional path
dumptruck -a /var/lib/mysql

# Fully automatic with explicit data-dir flag
dumptruck -a -d /var/lib/mysql

# Specify version explicitly, still prompt for other things
dumptruck -d /var/lib/mysql -v mysql:8.0

# Force-fix permissions but stay interactive for other prompts
dumptruck -d /var/lib/mysql -f

# Auto mode but keep container after dump
dumptruck -a /var/lib/mysql -k
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MYSQL_START_TIMEOUT` | `300` | Timeout in seconds to wait for MySQL to start |
| `INNODB_FORCE_RECOVERY` | `0` | InnoDB force recovery level (0-6). Use `1-6` for data directories copied from a live MySQL server (hot copies). Higher values are more aggressive: `1` prevents background threads, `3` skips undo log applies, `6` skips doublewrite checks and allows incomplete page updates. See [MySQL InnoDB Force Recovery](https://dev.mysql.com/doc/refman/8.0/forcing-innodb-recovery.html) for details. |

### Troubleshooting

**Container exits immediately with no logs:** This is usually caused by one of the following:

1. **User mismatch**: The container now runs as the MySQL user (not your host UID). If you get permission errors, ensure the data directory is readable.

2. **Hot-copied data directory**: If the data was copied while MySQL was running, InnoDB redo logs may be inconsistent. Try:
   ```bash
   INNODB_FORCE_RECOVERY=3 ./dumptruck /path/to/data/dir
   ```
   If recovery level 3 doesn't work, try higher levels (up to 6). Note: higher recovery levels may result in data loss.

3. **InnoDB redo log issues**: If you see "Found existing redo log files, but at least one is missing", the redo logs don't match the data files. You can try:
   - Using `INNODB_FORCE_RECOVERY` as above
   - Deleting the redo logs from `#innodb_redo/` (backup first!) and trying again
   - Restoring the original redo logs from the same point-in-time backup 
