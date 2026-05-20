package mysqldump

import (
	"bytes"
	"context"
	"dumptruck/helpers"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/specgen"
	"github.com/cqroot/prompt"
	"github.com/opencontainers/runtime-spec/specs-go"
)

const (
	// DefaultContextTimeout is the default timeout for context operations
	DefaultContextTimeout = 60 * time.Second
	// DefaultStartTimeout is the timeout for container start operations
	DefaultStartTimeout = 90 * time.Second
	// DefaultStopTimeout is the timeout for container stop operations
	DefaultStopTimeout = 30 * time.Second
	// DefaultLogTailLines is the number of log lines to show on error
	DefaultLogTailLines = 50
	// MySQLRetryDelay is the delay between MySQL readiness checks
	MySQLRetryDelay = time.Second
	// ContainerPrefix is the prefix for container names
	ContainerPrefix = "dumptruck_"
	// DockerRegistry is the default Docker registry
	DockerRegistry = "docker.io/library/"
	// MySQLDataDir is the MySQL data directory in the container
	MySQLDataDir = "/var/lib/mysql"
)

// CreateMysqlDump creates a MySQL dump by spinning up a container with the specified image.
// It handles the full lifecycle: pull image, create container, start it, dump databases, stop and optionally remove.
func CreateMysqlDump(containerImage string, cfg *helpers.Config) error {
	if containerImage == "" {
		return fmt.Errorf("container image cannot be empty")
	}
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	containerName := ContainerPrefix + strings.Replace(containerImage, ":", "_", -1)
	log.Printf("Using container name: %s", containerName)

	// Get Podman socket location
	socket, err := getPodmanSocket()
	if err != nil {
		return fmt.Errorf("failed to get Podman socket: %w", err)
	}

	// Create connection context
	ctx, err := bindings.NewConnection(context.Background(), socket)
	if err != nil {
		return fmt.Errorf("failed to connect to Podman: %w", err)
	}

	// Setup cleanup on failure
	var containerCreated bool
	defer func() {
		if containerCreated {
			// Attempt cleanup if something went wrong
			cleanupCtx, cancel := context.WithTimeout(context.Background(), DefaultStopTimeout)
			defer cancel()

			if err := stopContainer(cleanupCtx, containerName); err != nil {
				log.Printf("Warning: failed to stop container during cleanup: %v", err)
			}
		}
	}()

	// Check if image already exists
	if err := ensureImageExists(ctx, containerImage); err != nil {
		return fmt.Errorf("failed to ensure image exists: %w", err)
	}

	// Check if container already exists - remove if it does
	if err := ensureContainerRemoved(ctx, containerName); err != nil {
		return fmt.Errorf("failed to remove existing container: %w", err)
	}

	// Create container
	if err := createContainer(ctx, containerImage, containerName, cfg); err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	containerCreated = true

	// Start the container
	if err := startContainer(ctx, containerName, cfg.MySQLStartTimeout); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Dump databases
	if err := dumpDatabases(containerName); err != nil {
		return fmt.Errorf("failed to dump databases: %w", err)
	}

	// Stop container
	log.Println("Stopping the container...")
	stopCtx, stopCancel := context.WithTimeout(ctx, DefaultStopTimeout)
	defer stopCancel()

	if err := containers.Stop(stopCtx, containerName, nil); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	log.Println("Container stopped successfully")

	// Ask if user wants to remove the container
	if err := promptRemoveContainer(ctx, containerName); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	containerCreated = false // Disable cleanup since we're done
	return nil
}

// getPodmanSocket returns the Podman socket path based on the operating system
func getPodmanSocket() (string, error) {
	opsys := runtime.GOOS

	switch opsys {
	case "linux":
		sockDir := os.Getenv("XDG_RUNTIME_DIR")
		if sockDir == "" {
			return "", fmt.Errorf("XDG_RUNTIME_DIR environment variable not set")
		}
		socket := "unix:" + sockDir + "/podman/podman.sock"
		log.Printf("Using Linux Podman socket: %s", socket)
		return socket, nil

	case "darwin":
		socket, err := getMacOSPodmanSocket()
		if err != nil {
			return "", fmt.Errorf("failed to get macOS Podman socket: %w", err)
		}
		log.Printf("Using macOS Podman socket: %s", socket)
		return "unix://" + socket, nil

	default:
		return "", fmt.Errorf("unsupported operating system: %s (supported: linux, darwin)", opsys)
	}
}

// ensureImageExists checks if the image exists locally, and pulls it if not
func ensureImageExists(ctx context.Context, containerImage string) error {
	withTimeout, cancel := context.WithTimeout(ctx, DefaultContextTimeout)
	defer cancel()

	fullImageName := DockerRegistry + containerImage
	log.Printf("Checking if image exists: %s", fullImageName)

	exists, err := images.Exists(withTimeout, fullImageName, nil)
	if err != nil {
		return fmt.Errorf("failed to check if image exists: %w", err)
	}

	if exists {
		log.Println("Image already exists locally")
		return nil
	}

	// Image doesn't exist, need to pull it
	log.Printf("Pulling image: %s (this may take a few minutes)...", fullImageName)

	pullCtx, pullCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer pullCancel()

	var options images.PullOptions
	arch := "amd64"
	options.Arch = &arch

	_, err = images.Pull(pullCtx, fullImageName, &options)
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", fullImageName, err)
	}

	log.Println("Image pulled successfully")
	return nil
}

// ensureContainerRemoved removes a container if it exists
func ensureContainerRemoved(ctx context.Context, containerName string) error {
	withTimeout, cancel := context.WithTimeout(ctx, DefaultContextTimeout)
	defer cancel()

	exists, err := containers.Exists(withTimeout, containerName, nil)
	if err != nil {
		return fmt.Errorf("failed to check if container exists: %w", err)
	}

	if !exists {
		return nil
	}

	log.Printf("Container %s already exists, removing it...", containerName)

	// Try to stop first (ignore errors if not running)
	_ = containers.Stop(withTimeout, containerName, nil)

	// Remove the container
	_, err = containers.Remove(withTimeout, containerName, nil)
	if err != nil {
		return fmt.Errorf("failed to remove existing container: %w", err)
	}

	log.Println("Existing container removed")
	return nil
}

// createContainer creates a new container with the specified configuration
func createContainer(ctx context.Context, containerImage, containerName string, cfg *helpers.Config) error {
	withTimeout, cancel := context.WithTimeout(ctx, DefaultContextTimeout)
	defer cancel()

	log.Printf("Creating container %s from image %s...", containerName, containerImage)

	s := specgen.NewSpecGenerator(containerImage, false)
	s.Name = containerName

	// Mount the data directory
	mnt := specs.Mount{
		Type:        "bind",
		Source:      cfg.DataDir,
		Destination: MySQLDataDir,
		Options:     []string{"rbind", "z"},
	}
	s.Mounts = append(s.Mounts, mnt)
	// mnt2 := specs.Mount{
	// 	Type:        "bind",
	// 	Source:      "./mysql.sock",
	// 	Destination: "/var/run/mysqld/mysqld.sock",
	// 	Options:     []string{"rbind", "z"},
	// }
	// s.Mounts = append(s.Mounts, mnt2)

	// Configure MySQL to skip grant tables for access without password
	s.Command = append(s.Command, "--skip-grant-tables")
	s.Command = append(s.Command, "--socket=" + MySQLDataDir + "/mysql.sock")

	// Add InnoDB force recovery if configured (useful for hot-copied data directories)
	if cfg.InnoDBForceRecovery > 0 {
		s.Command = append(s.Command, fmt.Sprintf("--innodb-force-recovery=%d", cfg.InnoDBForceRecovery))
		log.Printf("Using --innodb-force-recovery=%d (data directory may be from a live server)", cfg.InnoDBForceRecovery)
	}

	s.Env = map[string]string{
		"MYSQL_ALLOW_EMPTY_PASSWORD": "True",
	}

	// Don't allocate a TTY - it prevents the logging driver from capturing output,
	// making it impossible to debug container failures via `podman logs` or journald.
	terminal := false
	s.Terminal = &terminal

	// Don't force user ID - let the MySQL/MariaDB entrypoint handle user switching.
	// The official images switch from root to the 'mysql' user internally.
	// Forcing a host UID breaks the entrypoint's data directory detection
	// and causes it to try re-initializing an existing data directory.
	// See: https://github.com/docker-library/mysql/issues/582

	// Create the container
	resp, err := containers.CreateWithSpec(withTimeout, s, nil)
	if err != nil {
		return fmt.Errorf("failed to create container spec: %w", err)
	}

	log.Printf("Container %s created successfully", containerName)

	if len(resp.Warnings) > 0 {
		log.Println("Container creation warnings:")
		for _, warning := range resp.Warnings {
			log.Printf("  - %s", warning)
		}
	}

	return nil
}

// startContainer starts the container and waits for MySQL to be ready
func startContainer(ctx context.Context, containerName string, mysqlStartTimeout int) error {
	startTimeout, cancel := context.WithTimeout(ctx, DefaultStartTimeout)
	defer cancel()

	log.Println("Starting container...")
	if err := containers.Start(startTimeout, containerName, nil); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	log.Println("Container started")

	// Wait for container to be running
	log.Println("Waiting for container to be in running state...")
	waitCtx, waitCancel := context.WithTimeout(ctx, DefaultContextTimeout)
	defer waitCancel()

	var opts containers.WaitOptions
	opts.Conditions = []string{"running"}
	_, err := containers.Wait(waitCtx, containerName, &opts)
	if err != nil {
		return fmt.Errorf("container failed to reach running state: %w", err)
	}

	// Wait for MySQL service to become ready
	log.Printf("Waiting for MySQL to be ready (timeout: %d seconds)...", mysqlStartTimeout)
	if err := waitForMySQL(containerName, mysqlStartTimeout, MySQLRetryDelay); err != nil {
		// Print container logs to help diagnose the issue
		log.Println("MySQL failed to start. Fetching container logs...")

		logsCtx, logsCancel := context.WithTimeout(ctx, 5*time.Second)
		defer logsCancel()

		stdoutChan := make(chan string)
		go func() {
			defer close(stdoutChan)
			logOpts := new(containers.LogOptions).WithTail(fmt.Sprintf("%d", DefaultLogTailLines))
			_ = containers.Logs(logsCtx, containerName, logOpts, stdoutChan, nil)
		}()

		log.Println("--- Container logs (last 50 lines) ---")
		for msg := range stdoutChan {
			fmt.Println(msg)
		}
		log.Println("--- End of container logs ---")

		return fmt.Errorf("MySQL did not become ready: %w", err)
	}

	log.Println("MySQL is ready")
	return nil
}

// stopContainer stops a container
func stopContainer(ctx context.Context, containerName string) error {
	return containers.Stop(ctx, containerName, nil)
}

// dumpDatabases lists databases and dumps the ones selected by the user
func dumpDatabases(containerName string) error {
	log.Println("Querying available databases...")

	// Get list of databases - try both mysql and mariadb commands
	cmd := exec.Command("podman", "exec", containerName, "mysql", "-u", "root", "-B", "-N", "-e", "SHOW DATABASES;")
	stdout, err := cmd.Output()
	if err != nil {
		// Try mariadb command (newer MariaDB versions)
		cmd = exec.Command("podman", "exec", containerName, "mariadb", "-u", "root", "-B", "-N", "-e", "SHOW DATABASES;")
		stdout, err = cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("failed to query databases: %w (stderr: %s)", err, string(exitErr.Stderr))
			}
			return fmt.Errorf("failed to query databases: %w", err)
		}
	}

	databases := strings.Fields(string(stdout))
	if len(databases) == 0 {
		return fmt.Errorf("no databases found")
	}

	log.Printf("Found %d database(s): %s", len(databases), strings.Join(databases, ", "))

	// Let user select databases to dump
	dbs, err := prompt.New().Ask("Select databases to dump:").MultiChoose(databases)
	if err != nil {
		return fmt.Errorf("failed to select databases: %w", err)
	}

	if len(dbs) == 0 {
		log.Println("No databases selected, skipping dump")
		return nil
	}

	log.Printf("Dumping %d database(s)...", len(dbs))

	// Dump each selected database
	for i, dbName := range dbs {
		log.Printf("[%d/%d] Dumping database: %s", i+1, len(dbs), dbName)

		dumpName := dbName + ".sql"

		// Try mysqldump first (MySQL and older MariaDB)
		dumpCmd := fmt.Sprintf("mysqldump --single-transaction --quick --lock-tables=false %s > %s/%s",
			dbName, MySQLDataDir, dumpName)
		cmd := exec.Command("podman", "exec", containerName, "sh", "-c", dumpCmd)
		output, err := cmd.CombinedOutput()

		if err != nil {
			// Try mariadb-dump (newer MariaDB versions)
			dumpCmd = fmt.Sprintf("mariadb-dump --single-transaction --quick --lock-tables=false %s > %s/%s",
				dbName, MySQLDataDir, dumpName)
			cmd = exec.Command("podman", "exec", containerName, "sh", "-c", dumpCmd)
			output, err = cmd.CombinedOutput()

			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return fmt.Errorf("failed to dump database %s: %w (output: %s)", dbName, err, string(exitErr.Stderr))
				}
				return fmt.Errorf("failed to dump database %s: %w", dbName, err)
			}
		}

		if len(output) > 0 {
			log.Printf("  Output: %s", string(output))
		}

		log.Printf("  ✓ Successfully dumped %s to %s", dbName, dumpName)
	}

	log.Println("All database dumps completed successfully")
	return nil
}

// waitForMySQL waits for MySQL to become ready by pinging it repeatedly
func waitForMySQL(containerName string, maxRetries int, delay time.Duration) error {
	var lastErr error

	for i := range maxRetries {
		// Try mysqladmin first (MySQL and older MariaDB)
		cmd := exec.Command("podman", "exec", containerName, "mysqladmin", "ping", "--silent")
		if err := cmd.Run(); err == nil {
			// Success: MySQL is ready
			return nil
		} else {
			// Try mariadb-admin (newer MariaDB versions)
			cmd = exec.Command("podman", "exec", containerName, "mariadb-admin", "ping", "--silent")
			if err := cmd.Run(); err == nil {
				// Success: MariaDB is ready
				return nil
			} else {
				lastErr = err
			}
		}

		if i < maxRetries-1 {
			log.Printf("MySQL not ready yet, retrying in %v... (attempt %d/%d)", delay, i+1, maxRetries)
			time.Sleep(delay)
		}
	}

	return fmt.Errorf("MySQL did not become ready after %d attempts: %w", maxRetries, lastErr)
}

// promptRemoveContainer asks the user if they want to remove the container
func promptRemoveContainer(ctx context.Context, containerName string) error {
	delete, err := prompt.New().Ask("Remove the container?").Choose([]string{"Yes", "No"})
	if err != nil {
		return fmt.Errorf("failed to read user input: %w", err)
	}

	if delete == "No" {
		log.Printf("Container %s kept for manual inspection", containerName)
		return nil
	}

	log.Println("Removing container...")

	removeCtx, cancel := context.WithTimeout(ctx, DefaultContextTimeout)
	defer cancel()

	_, err = containers.Remove(removeCtx, containerName, nil)
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	log.Println("Container removed successfully")
	return nil
}

// getMacOSPodmanSocket retrieves the Podman socket path on macOS
func getMacOSPodmanSocket() (string, error) {
	cmd := exec.Command("podman", "machine", "inspect")

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to execute 'podman machine inspect': %w", err)
	}

	// Parse JSON output
	var result []map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return "", fmt.Errorf("failed to parse podman machine inspect output: %w", err)
	}

	if len(result) == 0 {
		return "", fmt.Errorf("no podman machines found")
	}

	// Navigate the JSON structure to get the socket path
	connectionInfo, ok := result[0]["ConnectionInfo"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("ConnectionInfo not found in podman machine inspect output")
	}

	podmanSocket, ok := connectionInfo["PodmanSocket"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("PodmanSocket not found in ConnectionInfo")
	}

	path, ok := podmanSocket["Path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("PodmanSocket.Path not found or empty")
	}

	return path, nil
}
