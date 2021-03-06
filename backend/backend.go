package backend

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/mijara/statspout/log"
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

	events *EventsMonitor // monitor attached to the events API.
}

// Work to process by daemons.
type Workload struct {
	connection *httputil.ClientConn // connection on which the request is going to be made.
	container  Container           // container object to request.
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

// Network Interface stats.
type InterfaceStats struct {
	RxBytes   uint32 `json:"rx_bytes"`
	RxDropped uint32 `json:"rx_dropped"`
	RxErrors  uint32 `json:"rx_errors"`
	RxPackets uint32 `json:"rx_packets"`

	TxBytes   uint32 `json:"tx_bytes"`
	TxDropped uint32 `json:"tx_dropped"`
	TxErrors  uint32 `json:"tx_errors"`
	TxPackets uint32 `json:"tx_packets"`
}

// Container Stats reported by the Docker Stats API.
type ContainerStats struct {
	Cpu    CpuStats `json:"cpu_stats"`
	PreCpu CpuStats `json:"precpu_stats"`

	Memory MemoryStats `json:"memory_stats"`

	Networks map[string]InterfaceStats `json:"networks"`

	Read time.Time `json:"read"`
}

// Container struct to unmarshal JSON response form Docker List Containers API.
type Container struct {
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`

	CanonicalName string
}

type ContainerInspect struct {
	Name string `json:"Name"`

	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
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
		conn, err := createConn(http, address)
		if err != nil {
			return nil, err
		}

		cli.clients <- httputil.NewClientConn(conn, nil)
	}

	log.Info.Printf("%d daemons clients created.", n)

	// create a dedicated client connection for side requests.
	conn, err := createConn(http, address)
	if err != nil {
		return nil, err
	}
	cli.dedicated = httputil.NewClientConn(conn, nil)

	cli.events, err = NewEventsMonitor(http, address)
	if err != nil {
		return nil, err
	}

	log.Info.Printf("Docker client created.")

	return cli, nil
}

// Queries the Docker Stats API for a container given by the canonical name.
func (cli *Client) Query(container Container) {
	// take one client connection, will block until there's one available.
	conn := <-cli.clients

	// send the workload to the service, which will then select one daemon for the task.
	cli.service.Send(Workload{
		connection: conn,
		container:  container,
	})

	// send back the client connection (this will never block).
	cli.clients <- conn
}

// Get containers names currently available in the Docker instance (only the ones that are running).
func (cli *Client) GetContainers() (map[string]Container, error) {
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

	result := make(map[string]Container)

	for _, container := range containers {
		container.CanonicalName = container.Names[0][1:]
		result[container.CanonicalName] = container
	}

	return result, nil
}

func (cli *Client) StartMonitor(containers map[string]Container) {
	cli.events.monitor(cli, containers)
}

// Closes all connections and Goroutines.
func (cli *Client) Close() {
	cli.exit = true

	cli.events.Close()
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
	req, err := http.NewRequest("GET", fmt.Sprintf(STATS_QUERY, wl.container.CanonicalName), nil)
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
			TxBytesTotal:  sumTxBytesTotal(container.Networks),
			RxBytesTotal:  sumRxBytesTotal(container.Networks),
			Timestamp:     container.Read,
			Name:          wl.container.CanonicalName,
			Labels:        wl.container.Labels,
		})
	}

	return nil
}

// Reports errors to STDERR.
func (cli *Client) onError(err error) {
	log.Error.Printf(err.Error())
}

// RequestContainer ask the docker API for a single container data.
func (cli *Client) RequestContainer(name string) (*Container, error) {
	req, err := http.NewRequest("GET", "/containers/"+name+"/json", nil)
	if err != nil {
		return nil, err
	}

	res, err := cli.dedicated.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	container := &ContainerInspect{}
	json.NewDecoder(res.Body).Decode(container)

	return &Container{
		Names:         []string{container.Name},
		CanonicalName: name,
		Labels:        container.Config.Labels,
	}, nil
}
