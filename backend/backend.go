package backend

import (
	"net/http/httputil"
	"errors"
	"fmt"
	"net"
	"net/http"
	"bufio"
	"encoding/json"
	"os"
	"time"
	"io/ioutil"

	"github.com/mijara/statspout/repo"
	"github.com/mijara/statspout/stats"
)

const (
	STATS_QUERY = "/containers/%s/stats?stream=0"
)

// Client holding data for the Backend.
type Client struct {
	service *Service       // the service to handle multiple daemons as a pipeline.
	daemons int            // the number of daemons.
	repo    repo.Interface // the repository to push stats.
	exit    bool           // did this client exited.

	clients   chan *httputil.ClientConn // queue of clients for daemons.
	dedicated *httputil.ClientConn      // dedicated client for side requests.
}

// Work to process by daemons.
type Workload struct {
	connection *httputil.ClientConn // connection on which the request is going to be made.
	name       string               // name of the docker container to request.
}

// Cpu Usage reported by the Docker Stats API.
type CpuUsage struct {
	Total  uint64   `json:"total_usage"`
	PerCpu []uint64 `json:"percpu_usage"`
}

// Cpu Stats reported by the Docker Stats API.
type CpuStats struct {
	Usage          CpuUsage `json:"cpu_usage"`
	SystemCpuUsage uint64   `json:"system_cpu_usage"`
}

// Memory Stats reported by the Docker Stats API.
type MemoryStats struct {
	Usage uint64 `json:"usage"`
	Limit uint64 `json:"limit"`
}

// Container Stats reported by the Docker Stats API.
type ContainerStats struct {
	Cpu    CpuStats `json:"cpu_stats"`
	PreCpu CpuStats `json:"precpu_stats"`

	Memory MemoryStats `json:"memory_stats"`

	Read time.Time `json:"read"`
}

// Container struct to unmarshal JSON response form Docker List Containers API.
type Container struct {
	Names []string `json:"Names"`
}

// Creates a new Backend Client, which uses the given repository, can be created as a HTTP or Socket
// client, specified by the http parameter. The address parameter must point to the endpoint or socket path,
// finally, n will be the number of daemons available to take requests.
func New(repo repo.Interface, http bool, address string, n int) (*Client, error) {
	// create a client with simple information.
	cli := &Client{
		repo:    repo,
		daemons: n,
	}

	// create the service to hold daemons.
	cli.service = NewService(n, cli.process, cli.onError)

	// create the channel for client connections.
	cli.clients = make(chan *httputil.ClientConn, n)

	// for each daemon, create one client connection for them to work with.
	for i := 0; i < n; i++ {
		conn, err := createClient(http, address)
		if err != nil {
			return nil, err
		}

		cli.clients <- httputil.NewClientConn(conn, nil)
	}

	// create a dedicated client connection for side requests.
	conn, err := createClient(http, address)
	if err != nil {
		return nil, err
	}
	cli.dedicated = httputil.NewClientConn(conn, nil)

	return cli, nil
}

// Queries the Docker Stats API for a container given by the canonical name.
func (cli *Client) Query(name string) {
	// take one client connection, will block until there's one available.
	conn := <-cli.clients

	// send the workload to the service, which will then select one daemon for the task.
	cli.service.Send(Workload{
		connection: conn,
		name:       name,
	})

	// send back the client connection (this will never block).
	cli.clients <- conn
}

// Get containers names currently available in the Docker instance (only the ones that are running).
func (cli *Client) GetContainers() ([]string, error) {
	req, err := http.NewRequest("GET", "/containers/json", nil)
	if err != nil {
		return nil, err
	}

	res, err := cli.dedicated.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var containers []Container
	json.Unmarshal(body, &containers)

	names := make([]string, len(containers))

	for i, container := range containers {
		name := container.Names[0]
		names[i] = name[1:]
	}

	return names, nil
}

// Closes all connections and Goroutines.
func (cli *Client) Close() {
	cli.exit = true

	cli.service.Close()

	for i := 0; i < cli.daemons; i++ {
		conn := <-cli.clients
		conn.Close()
	}

	cli.dedicated.Close()
}

// Process a single requests, this will be spawned by the some daemon and it meant to be used
// as a callback routine.
func (cli *Client) process(v interface{}) error {
	// client wants to exit, ignore workload.
	if cli.exit {
		return nil
	}

	// assert the type of the workload.
	wl, ok := v.(Workload)
	if !ok {
		return errors.New(fmt.Sprintf("This is not a workload %T", v))
	}

	// create the request for stats.
	req, err := http.NewRequest("GET", fmt.Sprintf(STATS_QUERY, wl.name), nil)
	if err != nil {
		return err
	}

	// request using the client.
	res, err := wl.connection.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// here, since the stats API is a stream, we have to read until the delimiter, and then break with EOF.
	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // EOF is an error.
		}

		container := &ContainerStats{}
		err = json.Unmarshal(line, container)
		if err != nil {
			// this error could mean that the container does not exists.
			return err
		}

		// push the stats to the repository, calculating relevant data.
		cli.repo.Push(&stats.Stats{
			MemoryPercent: calcMemoryPercent(container),
			CpuPercent:    calcCpuPercent(container),
			MemoryUsage:   container.Memory.Usage,
			Timestamp:     container.Read,
			Name:          wl.name,
		})
	}

	return nil
}

// Reports errors to STDERR.
func (cli *Client) onError(err error) {
	fmt.Fprintln(os.Stderr, "Error: "+err.Error())
}

// Creates a client for TCP (http) or Unix with the given address.
func createClient(http bool, address string) (net.Conn, error) {
	if http {
		return net.Dial("tcp", address)
	}

	return net.Dial("unix", address)
}
