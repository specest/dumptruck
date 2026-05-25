package identify

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cqroot/prompt"
)

// GetVersion attempts to identify the MySQL/MariaDB version by examining files
// in the data directory. It tries multiple detection methods in order:
// 1. .frm files (table format files)
// 2. binlog files (binary log files)
// When auto is true, it picks the most common version instead of prompting.
// Returns [database_type, version] or an error if detection fails completely.
func GetVersion(path string, auto bool) ([2]string, error) {
	var none [2]string

	if path == "" {
		return none, fmt.Errorf("path cannot be empty")
	}

	log.Println("Attempting to identify database version...")

	// Try .frm files first
	log.Println("Method 1: Scanning for .frm files...")
	ver, err := findFiles(path, "*.frm", auto)
	if err == nil && ver[0] != "" && ver[1] != "" {
		log.Printf("Successfully identified version from .frm files: %s %s", ver[0], ver[1])
		return ver, nil
	}
	if err != nil {
		log.Printf("Warning: error scanning .frm files: %v", err)
	} else {
		log.Println("No conclusive version found from .frm files")
	}

	// Try binlog files as fallback
	log.Println("Method 2: Scanning for binlog files...")
	ver, err = findFiles(path, "binlog.0*", auto)
	if err == nil && ver[0] != "" && ver[1] != "" {
		log.Printf("Successfully identified version from binlog files: %s %s", ver[0], ver[1])
		return ver, nil
	}
	if err != nil {
		log.Printf("Warning: error scanning binlog files: %v", err)
	} else {
		log.Println("No conclusive version found from binlog files")
	}

	return none, fmt.Errorf("could not identify database version using any method")
}

// findFiles searches for files matching the pattern and attempts to identify
// the database version from their metadata using the 'file' utility.
func findFiles(path, pattern string, auto bool) ([2]string, error) {
	var none [2]string

	// Execute find command with file utility
	cmd := exec.Command("find", path, "-iname", pattern, "-exec", "file", "-b", "{}", ";")
	stdout, err := cmd.Output()
	if err != nil {
		// Check if it's an ExitError to get stderr
		if exitErr, ok := err.(*exec.ExitError); ok {
			return none, fmt.Errorf("find command failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return none, fmt.Errorf("find command failed: %w", err)
	}

	// Parse the output
	lines := strings.Split(string(stdout), "\n")
	if len(lines) == 0 {
		return none, fmt.Errorf("no files found matching pattern %s", pattern)
	}

	// Map to track found versions: "database:version" -> count
	dbMap := make(map[string]int)
	successfulParses := 0

	log.Printf("Scanning %d file(s) matching pattern %s", len(lines), pattern)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Only process lines that mention MySQL or MariaDB
		if !strings.Contains(line, "MySQL") && !strings.Contains(line, "MariaDB") {
			continue
		}

		database, version, err := parseDatabaseInfo(line)
		if err != nil {
			// Log parsing errors but continue processing other files
			log.Printf("Debug: skipping line - %v: %s", err, line)
			continue
		}

		if database != "" && version != "" {
			key := database + ":" + version
			dbMap[key]++
			successfulParses++
		}
	}

	if successfulParses == 0 {
		return none, fmt.Errorf("found %d files but none contained valid MySQL/MariaDB version info", len(lines))
	}

	// Display findings to user
	if len(dbMap) == 0 {
		return none, fmt.Errorf("no valid database versions identified")
	}

	log.Println("Version detection results:")
	for version, count := range dbMap {
		log.Printf("  - Found %d file(s) indicating version %s", count, version)
	}

	// Select version based on mode
	return selectVersion(dbMap, auto)
}

// parseDatabaseInfo extracts database type and version from a file utility output line
func parseDatabaseInfo(line string) (database, version string, err error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", "", fmt.Errorf("insufficient fields in line")
	}

	// Determine database type
	if strings.Contains(line, "MariaDB") {
		database = "MariaDB"
	} else if strings.Contains(line, "MySQL") {
		database = "MySQL"
	} else {
		return "", "", fmt.Errorf("no database type found")
	}

	// Extract version string (last field typically)
	versionStr := fields[len(fields)-1]

	// Parse version number
	// Modern format (8.0+): "MySQL replication log, server id 1 MySQL V5+, server version 8.0.44"
	// Old format (5.6-): "MySQL table definition file Version 9, type MYISAM, MySQL version 50651"

	var major, minor int

	if strings.Contains(versionStr, ".") {
		// Format: "8.0.44" or "10.11.2"
		parts := strings.Split(versionStr, ".")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("invalid version format: %s", versionStr)
		}

		major, err = strconv.Atoi(parts[0])
		if err != nil {
			return "", "", fmt.Errorf("invalid major version: %s", parts[0])
		}

		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", "", fmt.Errorf("invalid minor version: %s", parts[1])
		}
	} else {
		// Format: "50651" (old MySQL format)
		intVer, err := strconv.Atoi(versionStr)
		if err != nil {
			return "", "", fmt.Errorf("invalid version number: %s", versionStr)
		}

		// Decode: 50651 -> 5.06.51 -> 5.6
		major = intVer / 10000
		minor = (intVer - major*10000) / 100

		// MariaDB versions are >= 10 in this format
		if major >= 10 {
			database = "MariaDB"
		}
	}

	// Validate parsed version
	if major < 5 || major > 20 {
		return "", "", fmt.Errorf("version out of expected range: %d.%d", major, minor)
	}

	if minor < 0 || minor > 99 {
		return "", "", fmt.Errorf("invalid minor version: %d", minor)
	}

	version = fmt.Sprintf("%d.%d", major, minor)
	return database, version, nil
}

// selectVersion chooses a version from the detected map.
// In auto mode, picks the version with the highest file count.
// In interactive mode, prompts the user to choose.
func selectVersion(dbMap map[string]int, auto bool) ([2]string, error) {
	if auto {
		return selectMostCommonVersion(dbMap)
	}

	return promptUserForVersion(dbMap)
}

// selectMostCommonVersion returns the version that appears in the most files.
func selectMostCommonVersion(dbMap map[string]int) ([2]string, error) {
	var none [2]string

	if len(dbMap) == 0 {
		return none, fmt.Errorf("no versions detected")
	}

	best := ""
	bestCount := 0
	for v, count := range dbMap {
		if count > bestCount {
			best = v
			bestCount = count
		}
	}

	if best == "" {
		return none, fmt.Errorf("no valid version found")
	}

	parts := strings.SplitN(best, ":", 2)
	if len(parts) != 2 {
		return none, fmt.Errorf("invalid version format: %s", best)
	}

	log.Printf("Auto-selected version %s (found in %d file(s))", best, bestCount)
	return [2]string{parts[0], parts[1]}, nil
}

// promptUserForVersion presents detected versions to the user and returns their choice
func promptUserForVersion(dbMap map[string]int) ([2]string, error) {
	var none [2]string

	// Build list of options
	keys := make([]string, 0, len(dbMap))
	for k := range dbMap {
		keys = append(keys, k)
	}

	// Add option to try other detection method
	keys = append(keys, "Try other method")

	// Prompt user to choose
	result, err := prompt.New().Ask("Choose version:").Choose(keys)
	if err != nil {
		return none, fmt.Errorf("failed to get user input: %w", err)
	}

	// Handle user's choice
	switch result {
	case "Try other method":
		return none, nil
	default:
		parts := strings.Split(result, ":")
		if len(parts) != 2 {
			return none, fmt.Errorf("invalid version format selected: %s", result)
		}
		return [2]string{parts[0], parts[1]}, nil
	}
}