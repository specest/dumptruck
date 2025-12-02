package helpers

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the application configuration
type Config struct {
	MySQLStartTimeout int
	DataDir           string
	DbType            string
	DbVersion         string
}

const (
	// DefaultMySQLStartTimeout is the default timeout in seconds for MySQL to start
	DefaultMySQLStartTimeout = 30
	// EnvMySQLStartTimeout is the environment variable name for MySQL start timeout
	EnvMySQLStartTimeout = "MYSQL_START_TIMEOUT"
)

// LoadEnv loads configuration from environment variables and returns a Config instance.
// If environment variables are not set, it uses default values.
func LoadEnv() (*Config, error) {
	cfg := &Config{
		MySQLStartTimeout: DefaultMySQLStartTimeout,
	}

	// Load MySQL start timeout from environment if set
	if timeoutStr := os.Getenv(EnvMySQLStartTimeout); timeoutStr != "" {
		timeout, err := strconv.Atoi(timeoutStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s value '%s': must be an integer: %w",
				EnvMySQLStartTimeout, timeoutStr, err)
		}

		if timeout <= 0 {
			return nil, fmt.Errorf("invalid %s value %d: must be positive",
				EnvMySQLStartTimeout, timeout)
		}

		cfg.MySQLStartTimeout = timeout
	}

	return cfg, nil
}
