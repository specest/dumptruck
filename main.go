package main

import (
	"dumptruck/helpers"
	"dumptruck/identify"
	"dumptruck/mysqldump"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	input "github.com/JoaoDanielRufino/go-input-autocomplete"
	"github.com/cqroot/prompt"
)

const (
	// File permissions for database files (owner rw, group/other r)
	// The container runs as the mysql user which is different from the host owner,
	// so the "other" read bit is needed for the container to access the files.
	filePermission = 0644
	// Directory permissions (owner rwx, group/other rx)
	// MySQL needs write access to the data directory to create log files.
	// 0755 allows traversal and reading. If MySQL needs to write (e.g., redo logs),
	// the user may need to increase to 0777.
	dirPermission = 0755
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	// Load configuration from environment
	cfg, err := helpers.LoadEnv()
	if err != nil {
		return fmt.Errorf("failed to load environment: %w", err)
	}

	// Parse command line arguments or prompt for data directory
	if err := parseArgs(cfg); err != nil {
		return fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Validate the data directory
	if err := validateDataDirectory(cfg.DataDir); err != nil {
		return fmt.Errorf("invalid data directory: %w", err)
	}

	// Ask user if they want to fix permissions
	if err := handlePermissions(cfg.DataDir); err != nil {
		return fmt.Errorf("failed to handle permissions: %w", err)
	}

	// Identify or prompt for MySQL version
	containerImage, err := getContainerImage(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("failed to get container image: %w", err)
	}

	log.Printf("Using container image: %s", containerImage)

	// Dump the MySQL databases
	if err := mysqldump.CreateMysqlDump(containerImage, cfg); err != nil {
		return fmt.Errorf("failed to create MySQL dump: %w", err)
	}

	log.Println("Database dump completed successfully")
	return nil
}

func parseArgs(cfg *helpers.Config) error {
	if len(os.Args) > 1 {
		path, err := getPath(os.Args[1])
		if err != nil {
			return fmt.Errorf("invalid path argument: %w", err)
		}
		cfg.DataDir = path
	} else {
		path, err := input.Read("Path to mysql data directory root (eg /var/lib/mysql): ")
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		resolvedPath, err := getPath(path)
		if err != nil {
			return fmt.Errorf("invalid path: %w", err)
		}
		cfg.DataDir = resolvedPath
	}
	if len(os.Args) > 2 {
		cfg.Args = os.Args[2:]
	}
	return nil
}

func getPath(path string) (string, error) {
	var dataDir string

	// Handle current working directory
	if path == "." {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		dataDir = wd
	} else if filepath.IsAbs(path) {
		// Absolute path
		dataDir = path
	} else {
		// Relative path - resolve to absolute
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		dataDir = filepath.Join(wd, path)
	}

	// Clean the path to remove any .. or . components
	dataDir = filepath.Clean(dataDir)

	return dataDir, nil
}

func validateDataDirectory(path string) error {
	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("path does not exist: %s", path)
		}
		return fmt.Errorf("cannot access path: %w", err)
	}

	// Ensure it's a directory
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}

	// Check if directory is readable
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("cannot read directory (permission denied?): %w", err)
	}

	// Warn if directory appears empty
	if len(entries) == 0 {
		log.Printf("Warning: directory appears to be empty: %s", path)
	}

	return nil
}

func handlePermissions(root string) error {
	// First, count how many files/directories actually need permission changes
	needsFix := false
	count := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		var targetPerm os.FileMode
		if info.IsDir() {
			targetPerm = dirPermission
		} else {
			targetPerm = filePermission
		}
		if info.Mode().Perm() != targetPerm {
			count++
			needsFix = true
		}
		return nil
	})

	// If permissions are already correct, skip entirely
	if !needsFix {
		log.Println("Permissions look fine, skipping permission changes.")
		return nil
	}

	// Some files need fixing - ask the user
	fix, err := prompt.New().Ask(fmt.Sprintf("Found %d file(s)/directory(ies) with restrictive permissions. Fix them?", count)).
		Choose([]string{"Yes (recommended for container access)", "No (skip)"})
	if err != nil {
		return fmt.Errorf("failed to read user input: %w", err)
	}

	if fix == "No (skip)" {
		log.Println("Skipping permission changes. Note: Container may fail to access files.")
		return nil
	}

	log.Printf("Fixing permissions (files: 0644, directories: 0755)...")

	fixed := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Warning: skipping %s: %v", path, err)
			return nil
		}

		var targetPerm os.FileMode
		if info.IsDir() {
			targetPerm = dirPermission
		} else {
			targetPerm = filePermission
		}

		if info.Mode().Perm() != targetPerm {
			if err := os.Chmod(path, targetPerm); err != nil {
				log.Printf("Warning: failed to change permissions for %s: %v", path, err)
				return nil
			}
			fixed++
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}

	log.Printf("Fixed permissions for %d files/directories", fixed)
	return nil
}

func getContainerImage(dataDir string) (string, error) {
	detect, err := prompt.New().Ask("Database version:").
		Choose([]string{"Try to determine automatically", "Enter manually"})
	if err != nil {
		return "", fmt.Errorf("failed to read user choice: %w", err)
	}

	switch detect {
	case "Try to determine automatically":
		version, err := identify.GetVersion(dataDir)
		if err != nil {
			log.Printf("Warning: automatic detection encountered an error: %v", err)
			log.Println("Falling back to manual entry...")
			return promptForDbVersion()
		}

		if version[0] != "" && version[1] != "" {
			containerImage := strings.ToLower(version[0]) + ":" + version[1]
			return containerImage, nil
		}

		log.Println("Could not determine database version automatically")
		return promptForDbVersion()

	case "Enter manually":
		return promptForDbVersion()

	default:
		return "", fmt.Errorf("unexpected choice: %s", detect)
	}
}

func promptForDbVersion() (string, error) {
	fmt.Println("Setting database type and version manually")

	db, err := prompt.New().Ask("Database type:").
		Choose([]string{"mysql", "mariadb"})
	if err != nil {
		return "", fmt.Errorf("failed to read database type: %w", err)
	}

	ver, err := prompt.New().Ask("Database version (major.minor, e.g., 5.5, 8.3, 10.11): ").Input("")
	if err != nil {
		return "", fmt.Errorf("failed to read database version: %w", err)
	}

	// Basic validation of version format
	ver = strings.TrimSpace(ver)
	if ver == "" {
		return "", fmt.Errorf("version cannot be empty")
	}

	if !strings.Contains(ver, ".") {
		return "", fmt.Errorf("version must be in format major.minor (e.g., 8.0)")
	}

	containerImage := strings.ToLower(db) + ":" + ver
	return containerImage, nil
}
