package main

import (
	"context"
	identify "dumptruck/identify"
	mysqldump "dumptruck/mysqldump"
	"log"
	"os"
	"strings"

	input "github.com/JoaoDanielRufino/go-input-autocomplete"
	"github.com/cqroot/prompt"
)

func main() {
	// Set data directory path
	var dataDir string
	var err error
	if len(os.Args) > 1 {
		path := os.Args[1]
		if path == "." {
			wd, err := os.Getwd()
			dataDir = wd
			if err != nil {
				log.Fatal(err)
			}
		} else {
			dataDir = path
		}

	} else {
		dataDir, err = input.Read("Path to directory which contains mysql data dir: ")
		if err != nil {
			log.Fatal(err)
		}
	}

	// Identify mysql version
	var containerImage string
	detect, err := prompt.New().Ask("Database version:").
		Choose([]string{"Try to determine automatically", "Enter manually"})
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	if detect == "Try to determine automatically" {
		version, _ := identify.GetVersion(dataDir)
		if len(version[0]) > 0 && len(version[1]) > 0 {
			containerImage = strings.ToLower(version[0]) + ":" + version[1]
		} else {
			log.Println("Could not determine database version")
			containerImage = promptForDbVersion()
		}
	} else if detect == "Enter manually" {
		containerImage = promptForDbVersion()
	}

	// Dump the mysql databases
	err = mysqldump.CreateMysqlDump(containerImage, context.Background(), dataDir)
	if err != nil {
		log.Println(err)
	}
}

func promptForDbVersion() string {
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
