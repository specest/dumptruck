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

func CreateMysqlDump(containerImage string, dataDir string) error {

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

	ctx, err := bindings.NewConnection(context.Background(), socket)
	if err != nil {
		return err
	}
	withTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Check if image alreay exists
	err = checkImageExists(withTimeout, containerImage)
	if err != nil {
		return err
	}

	// Check if container alreay exists - remove if it does
	err = checkContainerExists(withTimeout, containerName)
	if err != nil {
		return err
	}

	// Create container
	err = createContainer(withTimeout, containerImage, containerName, dataDir)
	if err != nil {
		return err
	}

	// Start the container
	startTimeoutCtx, startCancel := context.WithTimeout(ctx, 120*time.Second)
	defer startCancel()
	err = start(startTimeoutCtx, containerName)
	if err != nil {
		return err
	}

	err = dumpDatabases(containerName)
	if err != nil {
		return err
	}

	// Stop container
	log.Println("Stopping the container...")
	err = containers.Stop(withTimeout, containerName, nil)
	if err != nil {
		return err
	}
	log.Println("Container stopped")

	delete, err := prompt.New().Ask("Remove the container?").Choose([]string{"Yes", "No"})
	if err != nil {
		return err
	}
	if delete == "Yes" {
		//Delete/remove the container
		rm, err := containers.Remove(withTimeout, containerName, nil)
		if err != nil {
			log.Println("Unable to remove container", err)
			log.Println(rm)
			return err
		}
	}

	return nil
}

// Check if image exists. If not, then pull it
func checkImageExists(ctx context.Context, containerImage string) error {

	if ok, imageExistsErr := images.Exists(ctx, "docker.io/library/"+containerImage, nil); !ok {
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
	return nil
}

func checkContainerExists(ctx context.Context, containerName string) error {
	// Check if container alreay exists - remove if it does
	exists, containerExistsErr := containers.Exists(ctx, containerName, nil)
	if containerExistsErr != nil {
		return containerExistsErr
	}
	if exists {
		//Remove container
		// ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// defer cancel()
		rm, err := containers.Remove(ctx, containerName, nil)
		if err != nil {
			log.Println("Failed to remove the container", err, rm)
		}
	}

	return nil
}

func createContainer(ctx context.Context, containerImage, containerName, dataDir string) error {

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

	// Setting the user breaks mysql >= 8
	//2025-10-31T13:32:12.372748Z 0 [Warning] [MY-010122] [Server] One can only use the --user switch if running as root
	// However it is necessary for rootless container with SELinux I think ...
	// Needs testing!

	currentUser, err := user.Current()
	if err != nil {
		log.Fatal(err.Error())
	}

	userid := currentUser.Uid
	s.User = userid

	resp, err := containers.CreateWithSpec(ctx, s, nil)
	if err != nil {
		return err
	}

	log.Printf("Container %s created\n", containerName)
	if len(resp.Warnings) > 0 {
		log.Println("Warnings:", resp.Warnings)
	}

	return nil
}

func start(ctx context.Context, containerName string) error {

	if err := containers.Start(ctx, containerName, nil); err != nil {
		return err
	}

	log.Println("Container started.")

	withTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
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

func dumpDatabases(containerName string) error {

	cmd := exec.Command("podman", "exec", "-it", containerName, "mysql", "-u", "root", "-B", "-N", "-e", "SHOW DATABASES;")
	stdout, err := cmd.Output()
	if err != nil {
		log.Println("Error querying database:", err)
		return err
	}
	databases := strings.Fields(string(stdout))

	dbs, err := prompt.New().Ask("Select databases to dump:").
		MultiChoose(databases)
	if err != nil {
		log.Println("Choosing databases failed: ", err)
		return err
	}

	for _, dbName := range dbs {
		dumpName := dbName + ".sql"
		cmd := exec.Command("podman", "exec", "-it", containerName, "sh", "-c", "mysqldump --single-transaction --quick --lock-tables=false "+dbName+" > /var/lib/mysql/"+dumpName)

		stdout, err := cmd.Output()
		if err != nil {
			log.Println("Error querying database:", err)
			return err
		}
		fmt.Println(string(stdout))
	}

	return nil
}

func waitForMySQL(containerName string, maxRetries int, delay time.Duration) error {
	var err error

	for range maxRetries {
		cmd := exec.Command("podman", "exec", containerName, "mysqladmin", "ping", "--silent")
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
