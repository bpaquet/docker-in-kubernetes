// Package dockerapi declares the Docker Engine API JSON shapes we use.
package dockerapi

// CreateRequest is the body of POST /containers/create.
type CreateRequest struct {
	Hostname     string              `json:"Hostname,omitempty"`
	Image        string              `json:"Image"`
	Env          []string            `json:"Env,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Entrypoint   StringOrSlice       `json:"Entrypoint,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	User         string              `json:"User,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   HostConfig          `json:"HostConfig,omitempty"`
	Tty          bool                `json:"Tty,omitempty"`
	AttachStdin  bool                `json:"AttachStdin,omitempty"`
	AttachStdout bool                `json:"AttachStdout,omitempty"`
	AttachStderr bool                `json:"AttachStderr,omitempty"`
	OpenStdin    bool                `json:"OpenStdin,omitempty"`
}

// HostConfig contains host-level container options.
type HostConfig struct {
	PortBindings map[string][]PortBinding `json:"PortBindings,omitempty"`
	NetworkMode  string                   `json:"NetworkMode,omitempty"`
	AutoRemove   bool                     `json:"AutoRemove,omitempty"`
	// Memory is the container memory limit in bytes (docker --memory / -m).
	Memory int64 `json:"Memory,omitempty"`
	// NanoCPUs is the CPU limit in billionths of a CPU (docker --cpus).
	NanoCPUs int64 `json:"NanoCpus,omitempty"`
}

// PortBinding is one host:container port mapping. HostIP is ignored (we always bind 127.0.0.1).
type PortBinding struct {
	HostIP   string `json:"HostIp,omitempty"`
	HostPort string `json:"HostPort,omitempty"`
}

// CreateResponse is returned by POST /containers/create.
type CreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// ContainerSummary is one element of GET /containers/json.
type ContainerSummary struct {
	ID         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	ImageID    string            `json:"ImageID"`
	Command    string            `json:"Command"`
	Created    int64             `json:"Created"`
	Ports      []Port            `json:"Ports"`
	Labels     map[string]string `json:"Labels"`
	State      string            `json:"State"`
	Status     string            `json:"Status"`
	HostConfig SummaryHostConfig `json:"HostConfig"`
}

// SummaryHostConfig is the slim HostConfig embedded in ContainerSummary.
type SummaryHostConfig struct {
	NetworkMode string `json:"NetworkMode"`
}

// Port is one entry of ContainerSummary.Ports.
type Port struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

// ContainerInspect is returned by GET /containers/{id}/json.
type ContainerInspect struct {
	ID              string             `json:"Id"`
	Created         string             `json:"Created"`
	Path            string             `json:"Path"`
	Args            []string           `json:"Args"`
	State           InspectState       `json:"State"`
	Image           string             `json:"Image"`
	Name            string             `json:"Name"`
	Config          InspectConfig      `json:"Config"`
	HostConfig      HostConfig         `json:"HostConfig"`
	NetworkSettings InspectNetworkInfo `json:"NetworkSettings"`
}

// InspectState mirrors Docker's State substructure.
type InspectState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	Dead       bool   `json:"Dead"`
	Pid        int    `json:"Pid"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

// InspectConfig mirrors Docker's Config substructure.
type InspectConfig struct {
	Hostname     string              `json:"Hostname"`
	Image        string              `json:"Image"`
	Env          []string            `json:"Env"`
	Cmd          []string            `json:"Cmd"`
	Entrypoint   []string            `json:"Entrypoint"`
	WorkingDir   string              `json:"WorkingDir"`
	User         string              `json:"User"`
	Labels       map[string]string   `json:"Labels"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
}

// InspectNetworkInfo carries published-port info docker CLI uses.
type InspectNetworkInfo struct {
	Ports map[string][]PortBinding `json:"Ports"`
}

// ErrorResponse is the Docker-style error body: {"message": "..."}.
type ErrorResponse struct {
	Message string `json:"message"`
}

// WaitResponse is returned by POST /containers/{id}/wait.
type WaitResponse struct {
	StatusCode int64      `json:"StatusCode"`
	Error      *WaitError `json:"Error,omitempty"`
}

// WaitError is the optional Error in WaitResponse.
type WaitError struct {
	Message string `json:"Message"`
}

// ExecCreateRequest is the body of POST /containers/{id}/exec.
type ExecCreateRequest struct {
	AttachStdin  bool     `json:"AttachStdin,omitempty"`
	AttachStdout bool     `json:"AttachStdout,omitempty"`
	AttachStderr bool     `json:"AttachStderr,omitempty"`
	Tty          bool     `json:"Tty,omitempty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	User         string   `json:"User,omitempty"`
}

// ExecCreateResponse is returned by POST /containers/{id}/exec.
type ExecCreateResponse struct {
	ID string `json:"Id"`
}

// ExecStartRequest is the body of POST /exec/{id}/start.
type ExecStartRequest struct {
	Detach bool `json:"Detach,omitempty"`
	Tty    bool `json:"Tty,omitempty"`
}

// ExecInspect is returned by GET /exec/{id}/json.
type ExecInspect struct {
	ID            string        `json:"ID"`
	Running       bool          `json:"Running"`
	ExitCode      int           `json:"ExitCode"`
	ProcessConfig ProcessConfig `json:"ProcessConfig"`
	OpenStdin     bool          `json:"OpenStdin"`
	OpenStdout    bool          `json:"OpenStdout"`
	OpenStderr    bool          `json:"OpenStderr"`
	ContainerID   string        `json:"ContainerID"`
	Pid           int           `json:"Pid"`
}

// ProcessConfig is the slim Process subobject of ExecInspect.
type ProcessConfig struct {
	Tty        bool     `json:"tty"`
	EntryPoint string   `json:"entrypoint"`
	Arguments  []string `json:"arguments"`
}

// ImageSummary is one element of GET /images/json.
type ImageSummary struct {
	ID          string            `json:"Id"`
	ParentID    string            `json:"ParentId"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	SharedSize  int64             `json:"SharedSize"`
	VirtualSize int64             `json:"VirtualSize"`
	Labels      map[string]string `json:"Labels"`
	Containers  int               `json:"Containers"`
}

// ImageInspect is returned by GET /images/{name}/json.
type ImageInspect struct {
	ID            string   `json:"Id"`
	RepoTags      []string `json:"RepoTags"`
	RepoDigests   []string `json:"RepoDigests"`
	Parent        string   `json:"Parent"`
	Comment       string   `json:"Comment"`
	Created       string   `json:"Created"`
	DockerVersion string   `json:"DockerVersion"`
	Author        string   `json:"Author"`
	Architecture  string   `json:"Architecture"`
	Os            string   `json:"Os"`
	Size          int64    `json:"Size"`
	VirtualSize   int64    `json:"VirtualSize"`
}

// ImageDeleteItem is one element of the array returned by DELETE /images/{name}.
type ImageDeleteItem struct {
	Untagged string `json:"Untagged,omitempty"`
	Deleted  string `json:"Deleted,omitempty"`
}

// Network is returned by GET /networks/{name} and one element of GET /networks.
type Network struct {
	Name       string                      `json:"Name"`
	ID         string                      `json:"Id"`
	Created    string                      `json:"Created"`
	Scope      string                      `json:"Scope"`
	Driver     string                      `json:"Driver"`
	EnableIPv6 bool                        `json:"EnableIPv6"`
	Internal   bool                        `json:"Internal"`
	Attachable bool                        `json:"Attachable"`
	Ingress    bool                        `json:"Ingress"`
	IPAM       NetworkIPAM                 `json:"IPAM"`
	Containers map[string]NetworkContainer `json:"Containers"`
	Options    map[string]string           `json:"Options"`
	Labels     map[string]string           `json:"Labels"`
}

// NetworkIPAM mirrors Docker's IPAM substructure.
type NetworkIPAM struct {
	Driver  string              `json:"Driver"`
	Options map[string]string   `json:"Options"`
	Config  []map[string]string `json:"Config"`
}

// NetworkContainer is one entry of Network.Containers.
type NetworkContainer struct {
	Name        string `json:"Name"`
	EndpointID  string `json:"EndpointID"`
	MacAddress  string `json:"MacAddress"`
	IPv4Address string `json:"IPv4Address"`
	IPv6Address string `json:"IPv6Address"`
}

// NetworkCreateRequest is the body of POST /networks/create.
type NetworkCreateRequest struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver,omitempty"`
	Internal   bool              `json:"Internal,omitempty"`
	Attachable bool              `json:"Attachable,omitempty"`
	EnableIPv6 bool              `json:"EnableIPv6,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	Options    map[string]string `json:"Options,omitempty"`
}

// NetworkCreateResponse is returned by POST /networks/create.
type NetworkCreateResponse struct {
	ID      string `json:"Id"`
	Warning string `json:"Warning"`
}

// InfoResponse is the minimal /info subset the docker CLI accepts.
type InfoResponse struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	ServerVersion     string `json:"ServerVersion"`
	OperatingSystem   string `json:"OperatingSystem"`
	OSType            string `json:"OSType"`
	Architecture      string `json:"Architecture"`
	NCPU              int    `json:"NCPU"`
	Driver            string `json:"Driver"`
	ContainersRunning int    `json:"ContainersRunning"`
	Containers        int    `json:"Containers"`
}
