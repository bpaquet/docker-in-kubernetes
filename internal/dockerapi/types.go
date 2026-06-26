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

// InfoResponse is the minimal /info subset the docker CLI accepts.
type InfoResponse struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	ServerVersion     string `json:"ServerVersion"`
	OperatingSystem   string `json:"OperatingSystem"`
	OSType            string `json:"OSType"`
	Architecture      string `json:"Architecture"`
	KernelVersion     string `json:"KernelVersion"`
	NCPU              int    `json:"NCPU"`
	MemTotal          int64  `json:"MemTotal"`
	Driver            string `json:"Driver"`
	ContainersRunning int    `json:"ContainersRunning"`
	Containers        int    `json:"Containers"`
}
