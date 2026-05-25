package helpers

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the application configuration
type Config struct {
	MySQLStartTimeout   int
	DataDir             string
	DbType              string
	DbVersion           string
	InnoDBForceRecovery int // 0-6, where 0 means disabled
	Auto                bool // convenience: implies FixPermissions + auto-detect + dump user DBs + remove container
	FixPermissions      bool
	NoRemove            bool
	ServerOnly          bool
	DbImage             string // explicit "type:version" from -v flag
	Args                []string
}

const (
	// DefaultMySQLStartTimeout is the default timeout in seconds for MySQL to start
	DefaultMySQLStartTimeout = 300
	// EnvMySQLStartTimeout is the environment variable name for MySQL start timeout
	EnvMySQLStartTimeout = "MYSQL_START_TIMEOUT"
	// EnvInnoDBForceRecovery is the environment variable for InnoDB force recovery level (1-6)
	EnvInnoDBForceRecovery = "INNODB_FORCE_RECOVERY"
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

	// Load InnoDB force recovery level from environment if set
	if recoveryStr := os.Getenv(EnvInnoDBForceRecovery); recoveryStr != "" {
		recovery, err := strconv.Atoi(recoveryStr)
		if err != nil {
			return nil, fmt.Errorf("invalid %s value '%s': must be an integer 0-6: %w",
				EnvInnoDBForceRecovery, recoveryStr, err)
		}

		if recovery < 0 || recovery > 6 {
			return nil, fmt.Errorf("invalid %s value %d: must be between 0 and 6",
				EnvInnoDBForceRecovery, recovery)
		}

		cfg.InnoDBForceRecovery = recovery
	}

	return cfg, nil
}
