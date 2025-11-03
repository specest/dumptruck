package main

import (
	h "dumptruck/helpers"
	identify "dumptruck/identify"
	mysqldump "dumptruck/mysqldump"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	input "github.com/JoaoDanielRufino/go-input-autocomplete"
	"github.com/cqroot/prompt"
)

func main() {

	var err error

	err = h.LoadEnv()
	if err != nil {
		log.Fatal(err)
	}

	parseArgs()

	chmodRecursively(h.Conf.DataDir)

	// Identify mysql version
	var containerImage string
	detect, err := prompt.New().Ask("Database version:").
		Choose([]string{"Try to determine automatically", "Enter manually"})
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	switch detect {
	case "Try to determine automatically":
		version, _ := identify.GetVersion(h.Conf.DataDir)
		if len(version[0]) > 0 && len(version[1]) > 0 {
			containerImage = strings.ToLower(version[0]) + ":" + version[1]
		} else {
			log.Println("Could not determine database version")
			containerImage = promptForDbVersion()
		}
	case "Enter manually":
		containerImage = promptForDbVersion()
	}

	// Dump the mysql databases
	err = mysqldump.CreateMysqlDump(containerImage)
	if err != nil {
		log.Println("Error during MySQL dump:", err)
	}
}

func parseArgs() {

	if len(os.Args) > 1 {
		h.Conf.DataDir = getPath(os.Args[1])
	} else {
		path, err := input.Read("Path to mysql data directory root (eg /var/lib/mysql): ")
		if err != nil {
			log.Fatal(err)
		}
		h.Conf.DataDir = getPath(path)
	}
}

func getPath(path string) string {
	var dataDir string

	//current working directory (mysql data dir root)
	if path == "." {
		wd, err := os.Getwd()
		dataDir = wd
		if err != nil {
			log.Fatal(err)
		}

	} else if path[0:1] == "/" { //absolute path
		dataDir = path

	} else { // relative path
		wd, err := os.Getwd()
		dataDir = filepath.Join(wd, path)
		if err != nil {
			log.Fatal(err)
		}
	}

	return dataDir

}

func chmodRecursively(root string) {
	err := filepath.Walk(root,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			err = os.Chmod(path, os.ModePerm)
			if err != nil {
				return err
			} else {
				log.Printf("Permissions of %s changed to 0777.\n", path)
			}
			return nil
		})
	if err != nil {
		log.Println(err)
	}
}

func promptForDbVersion() string {
	fmt.Println("Setting database type and version manually")
	db, err := prompt.New().Ask("Database type:").
		Choose([]string{"mariadb", "mysql"})
	if err != nil {
		log.Println("Couldn't read input", err)
		os.Exit(1)
	}

	ver, err := prompt.New().Ask("Database version major.minor, eg. 5.5, 8.3, 10.11 etc").Input("")
	if err != nil {
		log.Println("Couldn't read input", err)
		os.Exit(1)
	}

	return db + ":" + ver

}
