package podspec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

// PortMapping captures one host:container port pair, preserved as JSON in the
// pod annotation so it survives daemon restarts.
type PortMapping struct {
	HostPort      string `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// BuildInput is everything Build needs from outside.
type BuildInput struct {
	Namespace  string
	DockerName string // raw --name from the docker CLI, may be empty
	Project    string // optional; defaults to DefaultProject
	Now        time.Time
	Request    dockerapi.CreateRequest
}

// BuildResult is the spec plus the per-container metadata downstream callers
// need without re-parsing annotations.
type BuildResult struct {
	Pod          *corev1.Pod
	PodName      string
	PortMappings []PortMapping
}

// Build constructs a Pod spec from a Docker create request.
func Build(in BuildInput) (*BuildResult, error) {
	if in.Namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	if in.Request.Image == "" {
		return nil, fmt.Errorf("image is required")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	project := in.Project
	if project == "" {
		project = DefaultProject
	}

	podName, err := derivePodName(in.DockerName, in.Request.Image)
	if err != nil {
		return nil, err
	}

	ports, err := parsePortBindings(in.Request.HostConfig.PortBindings, in.Request.ExposedPorts)
	if err != nil {
		return nil, err
	}

	containerPorts := make([]corev1.ContainerPort, 0, len(ports))
	for _, p := range ports {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			ContainerPort: int32(p.ContainerPort),
			Protocol:      protocolFromString(p.Protocol),
		})
	}

	envVars := make([]corev1.EnvVar, 0, len(in.Request.Env))
	for _, e := range in.Request.Env {
		k, v, _ := strings.Cut(e, "=")
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	annotations, err := buildAnnotations(in, ports)
	if err != nil {
		return nil, err
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: in.Namespace,
			Labels: map[string]string{
				LabelManaged:       "true",
				LabelContainerName: podName,
				LabelProject:       project,
			},
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    podName,
					Image:   in.Request.Image,
					Env:     envVars,
					Ports:   containerPorts,
					Command: []string(in.Request.Entrypoint),
					Args:    in.Request.Cmd,
				},
			},
		},
	}
	if in.Request.WorkingDir != "" {
		pod.Spec.Containers[0].WorkingDir = in.Request.WorkingDir
	}

	return &BuildResult{Pod: pod, PodName: podName, PortMappings: ports}, nil
}

func derivePodName(dockerName, image string) (string, error) {
	if dockerName != "" {
		return SanitizeName(dockerName)
	}
	return GeneratedName(image), nil
}

func parsePortBindings(
	bindings map[string][]dockerapi.PortBinding,
	exposed map[string]struct{},
) ([]PortMapping, error) {
	// Deduplicate by "containerPort/proto" key. PortBindings is authoritative;
	// ExposedPorts fills in the rest.
	type key struct {
		port  uint16
		proto string
	}
	seen := make(map[key]PortMapping)

	for spec, binds := range bindings {
		port, proto, err := parsePortSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", spec, err)
		}
		hostPort := ""
		if len(binds) > 0 {
			hostPort = binds[0].HostPort
		}
		seen[key{port, proto}] = PortMapping{
			HostPort:      hostPort,
			ContainerPort: port,
			Protocol:      proto,
		}
	}
	for spec := range exposed {
		port, proto, err := parsePortSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("exposed port %q: %w", spec, err)
		}
		if _, ok := seen[key{port, proto}]; ok {
			continue
		}
		seen[key{port, proto}] = PortMapping{
			ContainerPort: port,
			Protocol:      proto,
		}
	}

	out := make([]PortMapping, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sortPortMappings(out)
	return out, nil
}

func parsePortSpec(s string) (uint16, string, error) {
	port, proto, ok := strings.Cut(s, "/")
	if !ok {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" && proto != "sctp" {
		return 0, "", fmt.Errorf("unsupported protocol %q", proto)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, "", fmt.Errorf("invalid port number %q", port)
	}
	if n == 0 {
		return 0, "", fmt.Errorf("port 0 is not allowed")
	}
	return uint16(n), proto, nil
}

func protocolFromString(s string) corev1.Protocol {
	switch s {
	case "udp":
		return corev1.ProtocolUDP
	case "sctp":
		return corev1.ProtocolSCTP
	default:
		return corev1.ProtocolTCP
	}
}

// sortPortMappings gives Build a stable output order so tests and ps results
// don't flap.
func sortPortMappings(ms []PortMapping) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0; j-- {
			a, b := ms[j-1], ms[j]
			if a.ContainerPort < b.ContainerPort ||
				(a.ContainerPort == b.ContainerPort && a.Protocol <= b.Protocol) {
				break
			}
			ms[j-1], ms[j] = b, a
		}
	}
}

func buildAnnotations(in BuildInput, ports []PortMapping) (map[string]string, error) {
	out := map[string]string{
		AnnotationCreatedAt:  in.Now.UTC().Format(time.RFC3339),
		AnnotationImage:      in.Request.Image,
		AnnotationDockerName: in.DockerName,
	}
	if len(ports) > 0 {
		b, err := json.Marshal(ports)
		if err != nil {
			return nil, fmt.Errorf("marshal ports annotation: %w", err)
		}
		out[AnnotationPorts] = string(b)
	}
	if len(in.Request.Env) > 0 {
		b, err := json.Marshal(in.Request.Env)
		if err != nil {
			return nil, fmt.Errorf("marshal env annotation: %w", err)
		}
		out[AnnotationEnv] = string(b)
	}
	if len(in.Request.Labels) > 0 {
		b, err := json.Marshal(in.Request.Labels)
		if err != nil {
			return nil, fmt.Errorf("marshal labels annotation: %w", err)
		}
		out[AnnotationLabels] = string(b)
	}
	return out, nil
}
