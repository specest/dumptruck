package main

import (
	"dumptruck/helpers"
	"dumptruck/identify"
	"dumptruck/mysqldump"
	"encoding/json"
	"flag"
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

// jsonResult is the machine-readable result emitted to stdout when --json is set.
type jsonResult struct {
	Success   bool                     `json:"success"`
	Engine    *string                  `json:"engine"`
	Version   *string                  `json:"version"`
	DataDir   *string                  `json:"data_dir"`
	OutputDir *string                  `json:"output_dir"`
	Databases []mysqldump.DatabaseDump `json:"databases"`
	Errors    []string                 `json:"errors"`
}

func main() {
	// Load configuration from environment
	cfg, err := helpers.LoadEnv()
	if err != nil {
		// Flags (including --json) have not been parsed yet, so report the
		// environment error in human-readable form on stderr.
		log.Fatalf("Error: failed to load environment: %v", err)
	}

	// Parse command line flags (sets cfg.JSONOutput).
	positionalArgs := parseFlags(cfg)

	// Result is populated incrementally as the run progresses. The slices are
	// initialized so they serialize as [] rather than null.
	result := &jsonResult{
		Databases: []mysqldump.DatabaseDump{},
		Errors:    []string{},
	}

	runErr := run(cfg, positionalArgs, result)

	if cfg.JSONOutput {
		result.Success = runErr == nil
		if runErr != nil {
			result.Errors = append(result.Errors, runErr.Error())
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			log.Printf("failed to encode JSON result: %v", encErr)
		}
		if runErr != nil {
			os.Exit(1)
		}
		return
	}

	if runErr != nil {
		log.Fatalf("Error: %v", runErr)
	}
}

func run(cfg *helpers.Config, positionalArgs []string, result *jsonResult) error {
	// Parse command line arguments or prompt for data directory
	if err := parseArgs(cfg, positionalArgs); err != nil {
		return fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Validate the data directory
	if err := validateDataDirectory(cfg.DataDir); err != nil {
		return fmt.Errorf("invalid data directory: %w", err)
	}

	// Dumps are written into the bind-mounted data directory, so the output
	// directory is the data directory itself.
	dataDir := cfg.DataDir
	result.DataDir = &dataDir
	result.OutputDir = &dataDir

	// Ask user if they want to fix permissions
	if err := handlePermissions(cfg); err != nil {
		return fmt.Errorf("failed to handle permissions: %w", err)
	}

	// Identify or prompt for MySQL version
	containerImage, err := getContainerImage(cfg)
	if err != nil {
		return fmt.Errorf("failed to get container image: %w", err)
	}

	if engine, version, ok := splitImage(containerImage); ok {
		result.Engine = &engine
		result.Version = &version
	}

	log.Printf("Using container image: %s", containerImage)

	// Server-only mode: just start the MySQL server
	if cfg.ServerOnly {
		if err := mysqldump.StartServerOnly(containerImage, cfg); err != nil {
			return fmt.Errorf("failed to start server: %w", err)
		}
		log.Println("Server started successfully")
		return nil
	}

	// Dump the MySQL databases
	dumps, err := mysqldump.CreateMysqlDump(containerImage, cfg)
	if err != nil {
		return fmt.Errorf("failed to create MySQL dump: %w", err)
	}
	if dumps != nil {
		result.Databases = dumps
	}

	log.Println("Database dump completed successfully")
	return nil
}

// splitImage splits a "type:version" container image string (e.g. "mysql:8.0")
// into its engine and version parts. ok is false if the format is unexpected.
func splitImage(image string) (engine, version string, ok bool) {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseFlags(cfg *helpers.Config) []string {
	fs := flag.NewFlagSet("dumptruck", flag.ExitOnError)

	// Main non-interactive flag (implies --fix-permissions, auto-detect, dump user DBs, remove container)
	auto := fs.Bool("auto", false, "Non-interactive mode: auto-fix permissions, auto-detect version, dump all user databases, remove container")
	autoS := fs.Bool("a", false, "Shorthand for --auto")

	// Granular flags
	dataDir := fs.String("data-dir", "", "Path to MySQL data directory")
	dataDirS := fs.String("d", "", "Shorthand for --data-dir")
	dbVersion := fs.String("version", "", "Database type:version (e.g. mysql:8.0, mariadb:10.11). Skips version detection")
	dbVersionS := fs.String("v", "", "Shorthand for --version")
	fixPerms := fs.Bool("fix-permissions", false, "Automatically fix file permissions without asking")
	fixPermsS := fs.Bool("f", false, "Shorthand for --fix-permissions")
	noRemove := fs.Bool("no-remove", false, "Do not remove container after dump")
	noRemoveS := fs.Bool("k", false, "Shorthand for --no-remove")
	serverOnly := fs.Bool("server-only", false, "Start the MySQL server and exit without dumping anything")
	serverOnlyS := fs.Bool("s", false, "Shorthand for --server-only")
	jsonOut := fs.Bool("json", false, "Emit a machine-readable JSON result to stdout; all human-readable output goes to stderr")

	_ = fs.Parse(os.Args[1:])

	cfg.Auto = *auto || *autoS
	cfg.FixPermissions = *fixPerms || *fixPermsS || cfg.Auto
	cfg.NoRemove = *noRemove || *noRemoveS
	cfg.ServerOnly = *serverOnly || *serverOnlyS
	cfg.JSONOutput = *jsonOut
	cfg.DbImage = *dbVersion
	if *dbVersionS != "" {
		cfg.DbImage = *dbVersionS
	}

	// Resolve data directory from flag or positional arg
	dir := *dataDir
	if dir == "" {
		dir = *dataDirS
	}
	if dir == "" && len(fs.Args()) > 0 {
		dir = fs.Args()[0]
	}
	if dir != "" {
		resolved, err := getPath(dir)
		if err != nil {
			log.Fatalf("invalid path %q: %v", dir, err)
		}
		cfg.DataDir = resolved
	}

	// Return remaining positional args (skip the one consumed as data-dir)
	remaining := fs.Args()
	if *dataDir == "" && *dataDirS == "" && len(remaining) > 0 {
		return remaining[1:]
	}
	return remaining
}

func parseArgs(cfg *helpers.Config, positionalArgs []string) error {
	if cfg.DataDir != "" {
		// Data dir already set via flag or positional
		return nil
	}
	if !cfg.Auto {
		// Interactive mode: prompt for path
		path, err := input.Read("Path to mysql data directory root (eg /var/lib/mysql): ")
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		resolvedPath, err := getPath(path)
		if err != nil {
			return fmt.Errorf("invalid path: %w", err)
		}
		cfg.DataDir = resolvedPath
	} else {
		return fmt.Errorf("data directory is required in --auto mode. Use -d /path or provide it as the first argument (e.g., dumptruck -a /var/lib/mysql)")
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

func handlePermissions(cfg *helpers.Config) error {
	root := cfg.DataDir

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

	// In auto mode or explicit --fix-permissions, fix without asking
	if cfg.Auto || cfg.FixPermissions {
		log.Printf("Auto-fixing permissions for %d file(s)/directory(ies).", count)
		return fixPermissions(root)
	}

	// Interactive mode: ask the user
	fix, err := prompt.New().Ask(fmt.Sprintf("Found %d file(s)/directory(ies) with restrictive permissions. Fix them?", count)).
		Choose([]string{"Yes (recommended for container access)", "No (skip)"})
	if err != nil {
		return fmt.Errorf("failed to read user input: %w", err)
	}

	if fix == "No (skip)" {
		log.Println("Skipping permission changes. Note: Container may fail to access files.")
		return nil
	}

	log.Printf("Fixing permissions (files: 0644, directories: 0755).")
	return fixPermissions(root)
}

func fixPermissions(root string) error {
	fixed := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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

func getContainerImage(cfg *helpers.Config) (string, error) {
	dataDir := cfg.DataDir

	// If -v flag was provided, use it directly
	if cfg.DbImage != "" {
		ver := strings.TrimSpace(cfg.DbImage)
		if !strings.Contains(ver, ":") {
			return "", fmt.Errorf("version must be in format type:version (e.g. mysql:8.0)")
		}
		return strings.ToLower(ver), nil
	}

	if cfg.Auto {
		log.Println("Auto mode: attempting to detect database version...")
		version, err := identify.GetVersion(dataDir, true)
		if err != nil {
			log.Printf("Warning: automatic detection encountered an error: %v", err)
			return "", fmt.Errorf("could not auto-detect database version: %w. Use -v to specify version manually", err)
		}

		if version[0] != "" && version[1] != "" {
			containerImage := strings.ToLower(version[0]) + ":" + version[1]
			return containerImage, nil
		}

		return "", fmt.Errorf("could not determine database version automatically")
	}

	// Interactive mode
	detect, err := prompt.New().Ask("Database version:").
		Choose([]string{"Try to determine automatically", "Enter manually"})
	if err != nil {
		return "", fmt.Errorf("failed to read user choice: %w", err)
	}

	switch detect {
	case "Try to determine automatically":
		version, err := identify.GetVersion(dataDir, false)
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
	fmt.Fprintln(os.Stderr, "Setting database type and version manually")

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
