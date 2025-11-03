### This is work in progress

`dumptruck` tries to identify the mysql server version by looking at the files in mysql data directory.
Currently looking for .frm and binlog files. If it doesn't identify the version, you can enter it manually.

It then spins up the corresponding mysql/mariadb container with podman, mounts the data directory, queries the database and dumps the databases of your choosing. 


It relies on the `file` and `find` utilities and should work on MacOS and Linux. 

### How to use

Just run the binary and follow the instructions or give the mysql data directory path as the first argument to the executable,
e.g: `dumptruck .` if you are in the mysql data directory or `dumptruck /path/to/data/dir`. 
