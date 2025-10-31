package identify

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cqroot/prompt"
)

func GetVersion(path string) ([2]string, error) {

	// Try .frm files
	ver, err := findFiles(path, "*.frm")
	if err == nil && ver[0] != "" && ver[1] != "" {
		return ver, nil
	}

	// Try binlog files, e.g binlog.000003
	ver, err = findFiles(path, "binlog.0*")
	if err == nil && ver[0] != "" && ver[1] != "" {
		return ver, nil
	}

	return ver, err
}

func findFiles(path, pattern string) ([2]string, error) {
	var database string
	var version string
	var minor, major int
	var none [2]string

	dbMap := make(map[string]int)

	cmd := exec.Command("find", path, "-iname", pattern, "-exec", "file", "-b", "{}", ";")
	stdout, err := cmd.Output()
	if err != nil {
		fmt.Println(err)
		return none, err
	}

	lines := strings.Split(string(stdout), "\n")

	log.Printf("Scanning for %s files", pattern)
	for _, line := range lines {
		if strings.Contains(line, "MySQL") || strings.Contains(line, "MariaDB") {

			fmt.Println(line)

			f := strings.Fields(line)
			if len(f) > 3 {
				version = f[len(f)-1]
			}
			if strings.Contains(line, "MySQL") {
				database = "MySQL"
			}

			if strings.Contains(line, "MariaDB") {
				database = "MariaDB"
			}

			// Extract the version number
			// 8.0 and newer show up as
			// MySQL replication log, server id 1 MySQL V5+, server version 8.0.44
			// 5.6 and older look like
			// MySQL table definition file Version 9, type MYISAM, MySQL version 50651

			if strings.Contains(version, ".") {
				v := strings.Split(version, ".")
				major, err = strconv.Atoi(v[0])
				if err != nil {
					fmt.Println("Could not convert version number to an integer")
				}
				minor, err = strconv.Atoi(v[1])
				if err != nil {
					fmt.Println("Could not convert version number to an integer")
				}

			} else {

				intVer, err := strconv.Atoi(version)
				if err != nil {
					fmt.Println("Could not convert version number to an integer")
				} else {
					major = intVer / 10000
					minor = (intVer - major*10000) / 100
					if major >= 10 {
						database = "MariaDB"
					}
				}

			}

			version = strconv.Itoa(major) + "." + strconv.Itoa(minor)

			if _, ok := dbMap[database+":"+version]; !ok {
				dbMap[database+":"+version] = 1
			} else {
				dbMap[database+":"+version] = dbMap[database+":"+version] + 1
			}
		}
	}

	for version, times := range dbMap {
		fmt.Printf("Found %d files pointing to version %s\n", times, version)
	}

	keys := make([]string, 0, len(dbMap))
	for k := range dbMap {
		keys = append(keys, k)
	}
	keys = append(keys, "Try other method")

	result, err := prompt.New().Ask("Choose version").Choose(keys)
	if err != nil {
		log.Println("Couldn't get user input:", err)
	}
	switch result {
	case "Try other method":
		return none, nil
	default:
		s := strings.Split(result, ":")
		return [2]string{s[0], s[1]}, nil
	}

}
