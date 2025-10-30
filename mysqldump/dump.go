package mysqldump

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
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

func CreateMysqlDump(containerImage string, ctx context.Context, dataDir string) error {

	containerName := "dumptruck_" + strings.Replace(containerImage, ":", "", 1)

	// Get Podman socket location
	var socket string
	opsys := runtime.GOOS
	switch opsys {
	case "linux":
		sock_dir := os.Getenv("XDG_RUNTIME_DIR")
		socket = "unix:" + sock_dir + "/podman/podman.sock"
	case "darwin":
		socket = "unix://" + getMacOSPodmanSocket()
	default:
		fmt.Printf("Unsupported operating system: %s\nTry using linux or macos instead!", opsys)
		os.Exit(1)
	}

	ctx, err := bindings.NewConnection(ctx, socket)
	if err != nil {
		return err
	}

	withTimeout, _ := context.WithTimeout(ctx, 60*time.Second)

	// Check if image exists. If not, then pull it
	if ok, imageExistsErr := images.Exists(withTimeout, "docker.io/library/"+containerImage, nil); !ok {
		var options images.PullOptions
		arch := "amd64"
		options.Arch = &arch
		_, pullErr := images.Pull(ctx, "docker.io/library/"+containerImage, &options)
		if pullErr != nil {
			return pullErr
		}

		if imageExistsErr != nil {
			return imageExistsErr
		}

	}

	// Check if container alreay exists - remove if it does
	exists, containerExistsErr := containers.Exists(withTimeout, containerName, nil)
	if containerExistsErr != nil {
		return containerExistsErr
	}
	if exists {
		//Remove container
		withTimeout, _ = context.WithTimeout(ctx, 30*time.Second)
		_, err = containers.Remove(withTimeout, containerName, nil)
		if err != nil {
			log.Println("Failed to remove the container", err)
		}
	}

	// Create container
	s := specgen.NewSpecGenerator(containerImage, false)
	s.Name = containerName
	t := true
	mnt := specs.Mount{
		Type:        "bind",
		Source:      dataDir,
		Destination: "/var/lib/mysql",
		Options:     []string{"rbind", "z"},
	}
	s.Mounts = append(s.Mounts, mnt)
	s.Command = append(s.Command, "--skip-grant-tables")
	s.Env = map[string]string{
		"MYSQL_ALLOW_EMPTY_PASSWORD": "True",
	}
	s.Terminal = &t
	currentUser, err := user.Current()
	if err != nil {
		log.Fatal(err.Error())
	}

	userid := currentUser.Uid
	s.User = userid

	_, err = containers.CreateWithSpec(ctx, s, nil)
	if err != nil {
		return err
	}
	log.Println("Container created.")

	// Start the container
	err = start(ctx, containerName)
	if err != nil {
		return err
	}

	cmd := exec.Command("podman", "exec", "-it", containerName, "mysql", "-u", "root", "-B", "-N", "-e", "SHOW DATABASES;")
	stdout, err := cmd.Output()
	if err != nil {
		log.Println("Error querying database:", err)
	}
	databases := strings.Fields(string(stdout))
	dumpDatabases(containerName, databases)

	// Stop container
	log.Println("Stopping the container...")
	withTimeout, _ = context.WithTimeout(ctx, 30*time.Second)
	err = containers.Stop(withTimeout, containerName, nil)
	if err != nil {
		return err
	}

	return nil
}

func start(ctx context.Context, containerName string) error {

	if err := containers.Start(ctx, containerName, nil); err != nil {
		return err
	}

	log.Println("Container started.")

	withTimeout, _ := context.WithTimeout(ctx, 60*time.Second)

	// Block until container is running
	var opts containers.WaitOptions
	opts.Conditions = []string{"running"}
	_, err := containers.Wait(withTimeout, containerName, &opts)
	if err != nil {
		return err
	}

	// Wait for MySQL service to become ready
	if err := waitForMySQL(containerName, 30, time.Second); err != nil {
		log.Fatalf("Error waiting for MySQL: %v", err)
	}
	return nil
}

func dumpDatabases(containerName string, dbs []string) error {

	dbs, err := prompt.New().Ask("Select databases to dump:").
		MultiChoose(dbs)
	if err != nil {
		log.Println("Choosing databases failed: ", err)
	}

	for _, dbName := range dbs {
		dumpName := dbName + ".sql"
		cmd := exec.Command("podman", "exec", "-it", containerName, "sh", "-c", "mysqldump --single-transaction --quick --lock-tables=false "+dbName+" > /var/lib/mysql/"+dumpName)

		stdout, err := cmd.Output()
		if err != nil {
			log.Println("Error querying database:", err)
		}
		fmt.Println(string(stdout))
	}

	return nil
}

func waitForMySQL(containerName string, maxRetries int, delay time.Duration) error {
	var err error
	cmd := exec.Command("podman", "exec", containerName, "mysqladmin", "-u", "root", "ping", "--silent")
	for range maxRetries {
		// Check if MySQL is ready by pinging it

		if err = cmd.Run(); err == nil {
			// Success: MySQL is ready
			return nil
		}
		log.Printf("MySQL not ready, retrying in %v...", delay)
		time.Sleep(delay)
	}
	return fmt.Errorf("MySQL did not become ready in time after %d attempts: %v", maxRetries, err)
}

func getMacOSPodmanSocket() string {
	// Prepare the command to execute
	cmd := exec.Command("podman", "machine", "inspect")

	// Create a buffer to capture the output
	var out bytes.Buffer
	cmd.Stdout = &out

	// Run the command
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error executing command: %v\n", err)
		return ""
	}
	// Use jq to parse the output
	var result []map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		fmt.Printf("Error parsing JSON: %v\n", err)
		return ""
	}

	// Extract the PodmanSocket.Path
	if len(result) > 0 {
		if connectionInfo, ok := result[0]["ConnectionInfo"].(map[string]interface{}); ok {
			if podmanSocket, ok := connectionInfo["PodmanSocket"].(map[string]interface{}); ok {
				if path, ok := podmanSocket["Path"].(string); ok {
					return path
				} else {
					fmt.Println("PodmanSocket.Path not found")
					os.Exit(1)
				}
			} else {
				fmt.Println("ConnectionInfo.PodmanSocket not found")
				os.Exit(1)
			}
		} else {
			fmt.Println("ConnectionInfo not found")
			os.Exit(1)
		}
	} else {
		fmt.Println("No results found")
		os.Exit(1)
	}

	return ""
}
