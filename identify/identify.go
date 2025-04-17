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
	info, err := tryFrmFiles(path)
	if err == nil && info[0] != "" && info[1] != "" {
		return info, nil
	}

	// Try binlog files, e.g binlog.000003

	return info, err
}

func tryFrmFiles(path string) ([2]string, error) {
	var database string
	var version string
	var none [2]string

	cmd := exec.Command("find", path, "-iname", "*.frm", "-exec", "file", "{}", ";")
	stdout, err := cmd.Output()
	if err != nil {
		fmt.Println(err)
		return [2]string{"", ""}, err
	}
	lines := strings.Split(string(stdout), "\n")

	for _, line := range lines {
		if strings.Contains(line, "MySQL") || strings.Contains(line, "MariaDB") {
			fmt.Println("Found this:")
			fmt.Println(line)

			f := strings.Fields(line)
			if len(f) > 3 {
				database = f[len(f)-3]
				version = f[len(f)-1]
			}

			// Extract the version number
			intVer, err := strconv.Atoi(version)
			if err != nil {
				fmt.Println("Could not convert version number to an integer")
			}
			major := intVer / 10000
			minor := (intVer - major*10000) / 100
			if major >= 10 {
				database = "MariaDB"
			}
			version = strconv.Itoa(major) + "." + strconv.Itoa(minor)

			fmt.Println("Trying to determine database version based on .frm files")

			result, err := prompt.New().Ask("Identified database version:\n\t" + database + ":" + version).
				Choose([]string{"Continue with identified version", "Try another *.frm file", "Try another method"})
			if err != nil {
				log.Println("Couldn't get user input:", err)
			}

			if result == "Continue with identified version" {
				return [2]string{database, version}, nil
			} else if result == "Try another method" {
				return none, nil
			}
		}
	}

	return none, nil
}
