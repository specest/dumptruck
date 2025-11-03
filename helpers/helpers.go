package helpers

import (
	"log"
	"os"
	"strconv"
)

type conf struct {
	MySQLStartTimeout int
	DataDir           string
}

var Conf *conf

func LoadEnv() error {

	Conf = new(conf)

	if os.Getenv("MYSQL_START_TIMEOUT") == "" {
		Conf.MySQLStartTimeout = 30
	} else {
		t, err := strconv.Atoi(os.Getenv("MYSQL_START_TIMEOUT"))
		if err != nil {
			log.Println("Unable to convert MySQL start timeout to int. Check your .env", err)
		}
		Conf.MySQLStartTimeout = t
	}

	return nil
}
