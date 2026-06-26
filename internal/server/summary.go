package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

// Container state strings as docker ps / inspect surface them. Keep in sync
// with dockerStateFromPhase below.
const (
	StateRunning = "running"
	StateCreated = "created"
	StateExited  = "exited"
	StateDead    = "dead"
)

func buildSummary(pod *corev1.Pod) dockerapi.ContainerSummary {
	state := dockerStateFromPhase(pod.Status.Phase)
	created := creationTime(pod)
	term := terminationOf(pod)
	mappings := parsePorts(pod)
	return dockerapi.ContainerSummary{
		ID:         podspec.ContainerID(pod.Namespace, pod.Name),
		Names:      []string{"/" + dockerName(pod)},
		Image:      podImage(pod),
		ImageID:    "",
		Command:    summaryCommand(pod),
		Created:    created.Unix(),
		Ports:      summaryPorts(mappings),
		Labels:     userLabelsFromPod(pod),
		State:      state,
		Status:     status(state, created, term, time.Now()),
		HostConfig: dockerapi.SummaryHostConfig{NetworkMode: "default"},
	}
}

// podImage returns the image the user asked for (annotation), falling back to
// the container spec.
func podImage(pod *corev1.Pod) string {
	if img := pod.Annotations[podspec.AnnotationImage]; img != "" {
		return img
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Image
	}
	return ""
}

// dockerName returns the user-supplied --name, falling back to the pod name.
func dockerName(pod *corev1.Pod) string {
	if n := pod.Annotations[podspec.AnnotationDockerName]; n != "" {
		return n
	}
	return pod.Name
}

// creationTime prefers CreationTimestamp (set by the apiserver), then the
// daemon-written annotation (the only source for /create'd-but-not-/start'ed
// pods whose Spec hasn't reached k8s yet).
func creationTime(pod *corev1.Pod) time.Time {
	if !pod.CreationTimestamp.IsZero() {
		return pod.CreationTimestamp.Time
	}
	if s := pod.Annotations[podspec.AnnotationCreatedAt]; s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// terminationOf returns the first container's terminated state, or nil.
type termination struct {
	exitCode   int32
	finishedAt time.Time
}

func terminationOf(pod *corev1.Pod) *termination {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			return &termination{
				exitCode:   cs.State.Terminated.ExitCode,
				finishedAt: cs.State.Terminated.FinishedAt.Time,
			}
		}
	}
	return nil
}

// userLabelsFromPod returns the labels the user passed via --label, falling
// back to the pod's k8s labels when no user labels were recorded.
func userLabelsFromPod(pod *corev1.Pod) map[string]string {
	if raw := pod.Annotations[podspec.AnnotationLabels]; raw != "" {
		var user map[string]string
		if err := json.Unmarshal([]byte(raw), &user); err == nil {
			return user
		}
	}
	return pod.Labels
}

func buildInspect(pod *corev1.Pod) dockerapi.ContainerInspect {
	state := dockerStateFromPhase(pod.Status.Phase)
	mappings := parsePorts(pod)
	bindings := portsBindings(mappings)
	image := podImage(pod)

	var startedAt time.Time
	if pod.Status.StartTime != nil {
		startedAt = pod.Status.StartTime.Time
	}
	inspectState := dockerapi.InspectState{
		Status:    state,
		Running:   state == StateRunning,
		StartedAt: rfc3339(startedAt),
	}
	if term := terminationOf(pod); term != nil {
		inspectState.ExitCode = int(term.exitCode)
		inspectState.FinishedAt = rfc3339(term.finishedAt)
	} else if state == StateExited {
		inspectState.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Annotations are daemon-written; a parse error means tampering or skew —
	// fall back to zero rather than 500ing the inspect.
	memory, _ := strconv.ParseInt(pod.Annotations[podspec.AnnotationMemory], 10, 64)
	nanoCPUs, _ := strconv.ParseInt(pod.Annotations[podspec.AnnotationNanoCPUs], 10, 64)

	return dockerapi.ContainerInspect{
		ID:      podspec.ContainerID(pod.Namespace, pod.Name),
		Created: creationTime(pod).UTC().Format(time.RFC3339Nano),
		State:   inspectState,
		Image:   image,
		Name:    "/" + dockerName(pod),
		Config: dockerapi.InspectConfig{
			Image:    image,
			Hostname: pod.Spec.Hostname,
			Env:      envFromPod(pod),
			User:     pod.Annotations[podspec.AnnotationUser],
			Labels:   userLabelsFromPod(pod),
		},
		HostConfig: dockerapi.HostConfig{
			NetworkMode:  "default",
			PortBindings: bindings,
			Memory:       memory,
			NanoCPUs:     nanoCPUs,
		},
		NetworkSettings: dockerapi.InspectNetworkInfo{Ports: bindings},
	}
}

// envFromPod decodes the daemon-written env annotation. Parse failure → nil.
func envFromPod(pod *corev1.Pod) []string {
	raw := pod.Annotations[podspec.AnnotationEnv]
	if raw == "" {
		return nil
	}
	var env []string
	_ = json.Unmarshal([]byte(raw), &env)
	return env
}

func summaryCommand(pod *corev1.Pod) string {
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	c := pod.Spec.Containers[0]
	return strings.Join(append(append([]string{}, c.Command...), c.Args...), " ")
}

// parsePorts decodes the daemon-written ports annotation. Empty/parse error → nil.
func parsePorts(pod *corev1.Pod) []podspec.PortMapping {
	raw := pod.Annotations[podspec.AnnotationPorts]
	if raw == "" {
		return nil
	}
	var mapped []podspec.PortMapping
	if err := json.Unmarshal([]byte(raw), &mapped); err != nil {
		return nil
	}
	return mapped
}

func summaryPorts(mappings []podspec.PortMapping) []dockerapi.Port {
	if len(mappings) == 0 {
		return nil
	}
	out := make([]dockerapi.Port, 0, len(mappings))
	for _, m := range mappings {
		p := dockerapi.Port{
			PrivatePort: m.ContainerPort,
			Type:        m.Protocol,
		}
		if m.HostPort != "" {
			if n, err := strconv.ParseUint(m.HostPort, 10, 16); err == nil {
				p.PublicPort = uint16(n)
				p.IP = "127.0.0.1"
			}
		}
		out = append(out, p)
	}
	return out
}

func portsBindings(mappings []podspec.PortMapping) map[string][]dockerapi.PortBinding {
	if len(mappings) == 0 {
		return nil
	}
	out := make(map[string][]dockerapi.PortBinding, len(mappings))
	for _, m := range mappings {
		key := fmt.Sprintf("%d/%s", m.ContainerPort, m.Protocol)
		var bindings []dockerapi.PortBinding
		if m.HostPort != "" {
			bindings = []dockerapi.PortBinding{{HostIP: "127.0.0.1", HostPort: m.HostPort}}
		}
		out[key] = bindings
	}
	return out
}

func dockerStateFromPhase(phase corev1.PodPhase) string {
	switch phase {
	case corev1.PodRunning:
		return StateRunning
	case corev1.PodPending:
		return StateCreated
	case corev1.PodSucceeded:
		return StateExited
	case corev1.PodFailed:
		return StateExited
	case corev1.PodUnknown:
		return StateDead
	default:
		return StateCreated
	}
}

func status(state string, created time.Time, term *termination, now time.Time) string {
	switch state {
	case StateRunning:
		if created.IsZero() {
			return "Up"
		}
		return "Up " + humanDuration(now.Sub(created))
	case StateExited:
		code := int32(0)
		var ago string
		if term != nil {
			code = term.exitCode
			if !term.finishedAt.IsZero() {
				ago = " " + humanDuration(now.Sub(term.finishedAt)) + " ago"
			}
		}
		return fmt.Sprintf("Exited (%d)%s", code, ago)
	case StateCreated:
		return "Created"
	case StateDead:
		return "Dead"
	}
	return state
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		s := int(d.Seconds())
		if s <= 1 {
			return "Less than a second"
		}
		return fmt.Sprintf("%d seconds", s)
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// summaryForPending and inspectForPending route a /create'd-but-not-/start'ed
// container through the same builders as a live pod. The pod Spec already
// carries every annotation buildSummary/buildInspect read (image, name, env,
// labels, ports, user, memory, cpus, created-at); the only thing it lacks is
// the apiserver's CreationTimestamp, which creationTime() falls back from to
// the AnnotationCreatedAt annotation.
func summaryForPending(p *pendingContainer) dockerapi.ContainerSummary {
	return buildSummary(p.Spec)
}

func inspectForPending(p *pendingContainer) dockerapi.ContainerInspect {
	return buildInspect(p.Spec)
}
