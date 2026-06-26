package podspec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

// PortMapping is one host:container port pair, persisted as a pod annotation.
type PortMapping struct {
	HostPort      string `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// BuildInput is the input to Build.
type BuildInput struct {
	Namespace  string
	DockerName string
	Project    string
	Now        time.Time
	Request    dockerapi.CreateRequest
}

// BuildResult is the output of Build.
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

	podName := GeneratedName(in.Request.Image)
	if in.DockerName != "" {
		var err error
		podName, err = SanitizeName(in.DockerName)
		if err != nil {
			return nil, err
		}
	}

	ports, err := parsePortBindings(in.Request.HostConfig.PortBindings, in.Request.ExposedPorts)
	if err != nil {
		return nil, err
	}

	containerPorts := dedupContainerPorts(ports)

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
					Name:      podName,
					Image:     in.Request.Image,
					Env:       envVars,
					Ports:     containerPorts,
					Command:   []string(in.Request.Entrypoint),
					Args:      in.Request.Cmd,
					Stdin:     in.Request.OpenStdin || in.Request.AttachStdin,
					StdinOnce: in.Request.OpenStdin || in.Request.AttachStdin,
					TTY:       in.Request.Tty,
				},
			},
		},
	}
	if in.Request.WorkingDir != "" {
		pod.Spec.Containers[0].WorkingDir = in.Request.WorkingDir
	}

	res, err := buildResources(in.Request.HostConfig)
	if err != nil {
		return nil, err
	}
	pod.Spec.Containers[0].Resources = res

	if in.Request.User != "" {
		sc, err := buildSecurityContext(in.Request.User)
		if err != nil {
			return nil, err
		}
		pod.Spec.Containers[0].SecurityContext = sc
	}

	return &BuildResult{Pod: pod, PodName: podName, PortMappings: ports}, nil
}

// buildResources translates Docker --memory and --cpus into k8s requests/limits.
// Per project policy: request == limit.
func buildResources(hc dockerapi.HostConfig) (corev1.ResourceRequirements, error) {
	if hc.Memory == 0 && hc.NanoCPUs == 0 {
		return corev1.ResourceRequirements{}, nil
	}
	rl := corev1.ResourceList{}
	if hc.Memory > 0 {
		rl[corev1.ResourceMemory] = *resource.NewQuantity(hc.Memory, resource.BinarySI)
	}
	if hc.NanoCPUs > 0 {
		// k8s milli-cores from billionths of a CPU.
		milli := (hc.NanoCPUs + 999_999) / 1_000_000
		rl[corev1.ResourceCPU] = *resource.NewMilliQuantity(milli, resource.DecimalSI)
	}
	return corev1.ResourceRequirements{Requests: rl.DeepCopy(), Limits: rl}, nil
}

// buildSecurityContext parses `--user uid[:gid]` (numeric only — k8s can't
// resolve container-side usernames). Returns nil if the input is empty.
func buildSecurityContext(user string) (*corev1.SecurityContext, error) {
	uidStr, gidStr, hasGid := strings.Cut(user, ":")
	uid, err := strconv.ParseInt(strings.TrimSpace(uidStr), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("--user requires numeric uid[:gid], got %q", user)
	}
	sc := &corev1.SecurityContext{RunAsUser: &uid}
	if hasGid {
		gid, err := strconv.ParseInt(strings.TrimSpace(gidStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--user requires numeric uid[:gid], got %q", user)
		}
		sc.RunAsGroup = &gid
	}
	return sc, nil
}

func parsePortBindings(
	bindings map[string][]dockerapi.PortBinding,
	exposed map[string]struct{},
) ([]PortMapping, error) {
	type key struct {
		port  uint16
		proto string
	}
	bound := make(map[key]bool)
	out := []PortMapping{}

	for spec, binds := range bindings {
		port, proto, err := parsePortSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", spec, err)
		}
		bound[key{port, proto}] = true
		if len(binds) == 0 {
			out = append(out, PortMapping{ContainerPort: port, Protocol: proto})
			continue
		}
		for _, b := range binds {
			out = append(out, PortMapping{HostPort: b.HostPort, ContainerPort: port, Protocol: proto})
		}
	}
	for spec := range exposed {
		port, proto, err := parsePortSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("exposed port %q: %w", spec, err)
		}
		if !bound[key{port, proto}] {
			out = append(out, PortMapping{ContainerPort: port, Protocol: proto})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ContainerPort != out[j].ContainerPort {
			return out[i].ContainerPort < out[j].ContainerPort
		}
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].HostPort < out[j].HostPort
	})
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

// dedupContainerPorts collapses multiple host-side mappings into one
// corev1.ContainerPort per (port, proto), as required by the pod spec.
func dedupContainerPorts(ports []PortMapping) []corev1.ContainerPort {
	type key struct {
		port  uint16
		proto string
	}
	seen := make(map[key]bool, len(ports))
	out := make([]corev1.ContainerPort, 0, len(ports))
	for _, p := range ports {
		k := key{p.ContainerPort, p.Protocol}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, corev1.ContainerPort{
			ContainerPort: int32(p.ContainerPort),
			Protocol:      protocolFromString(p.Protocol),
		})
	}
	return out
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

func buildAnnotations(in BuildInput, ports []PortMapping) (map[string]string, error) {
	out := map[string]string{
		AnnotationCreatedAt:  in.Now.UTC().Format(time.RFC3339),
		AnnotationImage:      in.Request.Image,
		AnnotationDockerName: in.DockerName,
	}
	if len(ports) > 0 {
		if err := marshalAnnotation(out, AnnotationPorts, ports); err != nil {
			return nil, err
		}
	}
	if len(in.Request.Env) > 0 {
		if err := marshalAnnotation(out, AnnotationEnv, in.Request.Env); err != nil {
			return nil, err
		}
	}
	if len(in.Request.Labels) > 0 {
		if err := marshalAnnotation(out, AnnotationLabels, in.Request.Labels); err != nil {
			return nil, err
		}
	}
	if in.Request.User != "" {
		out[AnnotationUser] = in.Request.User
	}
	if in.Request.HostConfig.Memory > 0 {
		out[AnnotationMemory] = strconv.FormatInt(in.Request.HostConfig.Memory, 10)
	}
	if in.Request.HostConfig.NanoCPUs > 0 {
		out[AnnotationNanoCPUs] = strconv.FormatInt(in.Request.HostConfig.NanoCPUs, 10)
	}
	return out, nil
}

func marshalAnnotation(m map[string]string, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s annotation: %w", key, err)
	}
	m[key] = string(b)
	return nil
}
