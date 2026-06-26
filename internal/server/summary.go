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

func buildSummary(pod *corev1.Pod) dockerapi.ContainerSummary {
	id := podspec.ContainerID(pod.Namespace, pod.Name)
	state := dockerStateFromPhase(pod.Status.Phase)
	image := pod.Annotations[podspec.AnnotationImage]
	if image == "" && len(pod.Spec.Containers) > 0 {
		image = pod.Spec.Containers[0].Image
	}
	name := pod.Annotations[podspec.AnnotationDockerName]
	if name == "" {
		name = pod.Name
	}

	created := time.Time{}
	if pod.CreationTimestamp.IsZero() {
		if s := pod.Annotations[podspec.AnnotationCreatedAt]; s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				created = t
			}
		}
	} else {
		created = pod.CreationTimestamp.Time
	}

	term := terminationOf(pod)
	return dockerapi.ContainerSummary{
		ID:         id,
		Names:      []string{"/" + name},
		Image:      image,
		ImageID:    "",
		Command:    summaryCommand(pod),
		Created:    created.Unix(),
		Ports:      summaryPorts(pod),
		Labels:     userLabelsFromPod(pod),
		State:      state,
		Status:     status(state, created, term, time.Now()),
		HostConfig: dockerapi.SummaryHostConfig{NetworkMode: "default"},
	}
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
	id := podspec.ContainerID(pod.Namespace, pod.Name)
	image := pod.Annotations[podspec.AnnotationImage]
	if image == "" && len(pod.Spec.Containers) > 0 {
		image = pod.Spec.Containers[0].Image
	}
	name := pod.Annotations[podspec.AnnotationDockerName]
	if name == "" {
		name = pod.Name
	}

	state := dockerStateFromPhase(pod.Status.Phase)
	created := pod.CreationTimestamp.Time

	var env []string
	if raw := pod.Annotations[podspec.AnnotationEnv]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &env)
	}

	ports := portsFromAnnotation(pod)

	var startedAt time.Time
	if pod.Status.StartTime != nil {
		startedAt = pod.Status.StartTime.Time
	}
	inspectState := dockerapi.InspectState{
		Status:    state,
		Running:   state == "running",
		StartedAt: rfc3339(startedAt),
	}
	if term := terminationOf(pod); term != nil {
		inspectState.ExitCode = int(term.exitCode)
		inspectState.FinishedAt = rfc3339(term.finishedAt)
	} else if state == "exited" {
		inspectState.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Annotations are daemon-written; a parse error means tampering or skew —
	// fall back to zero rather than 500ing the inspect.
	memory, _ := strconv.ParseInt(pod.Annotations[podspec.AnnotationMemory], 10, 64)
	nanoCPUs, _ := strconv.ParseInt(pod.Annotations[podspec.AnnotationNanoCPUs], 10, 64)

	return dockerapi.ContainerInspect{
		ID:      id,
		Created: created.UTC().Format(time.RFC3339Nano),
		State:   inspectState,
		Image:   image,
		Name:    "/" + name,
		Config: dockerapi.InspectConfig{
			Image:    image,
			Hostname: pod.Spec.Hostname,
			Env:      env,
			User:     pod.Annotations[podspec.AnnotationUser],
			Labels:   userLabelsFromPod(pod),
		},
		HostConfig: dockerapi.HostConfig{
			NetworkMode:  "default",
			PortBindings: ports,
			Memory:       memory,
			NanoCPUs:     nanoCPUs,
		},
		NetworkSettings: dockerapi.InspectNetworkInfo{Ports: ports},
	}
}

func summaryCommand(pod *corev1.Pod) string {
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	c := pod.Spec.Containers[0]
	return strings.Join(append(append([]string{}, c.Command...), c.Args...), " ")
}

func summaryPorts(pod *corev1.Pod) []dockerapi.Port {
	raw := pod.Annotations[podspec.AnnotationPorts]
	if raw == "" {
		return nil
	}
	var mapped []podspec.PortMapping
	if err := json.Unmarshal([]byte(raw), &mapped); err != nil {
		return nil
	}
	out := make([]dockerapi.Port, 0, len(mapped))
	for _, m := range mapped {
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

func portsFromAnnotation(pod *corev1.Pod) map[string][]dockerapi.PortBinding {
	raw := pod.Annotations[podspec.AnnotationPorts]
	if raw == "" {
		return nil
	}
	var mapped []podspec.PortMapping
	if err := json.Unmarshal([]byte(raw), &mapped); err != nil {
		return nil
	}
	out := make(map[string][]dockerapi.PortBinding, len(mapped))
	for _, m := range mapped {
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
		return "running"
	case corev1.PodPending:
		return "created"
	case corev1.PodSucceeded:
		return "exited"
	case corev1.PodFailed:
		return "exited"
	case corev1.PodUnknown:
		return "dead"
	default:
		return "created"
	}
}

func status(state string, created time.Time, term *termination, now time.Time) string {
	switch state {
	case "running":
		if created.IsZero() {
			return "Up"
		}
		return "Up " + humanDuration(now.Sub(created))
	case "exited":
		code := int32(0)
		var ago string
		if term != nil {
			code = term.exitCode
			if !term.finishedAt.IsZero() {
				ago = " " + humanDuration(now.Sub(term.finishedAt)) + " ago"
			}
		}
		return fmt.Sprintf("Exited (%d)%s", code, ago)
	case "created":
		return "Created"
	case "dead":
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

func summaryForPending(p *pendingContainer) dockerapi.ContainerSummary {
	name := p.DockerName
	if name == "" {
		name = p.Spec.Name
	}
	return dockerapi.ContainerSummary{
		ID:         p.ID,
		Names:      []string{"/" + name},
		Image:      p.Spec.Annotations[podspec.AnnotationImage],
		Command:    summaryCommand(p.Spec),
		Created:    p.CreatedAt.Unix(),
		Ports:      summaryPorts(p.Spec),
		Labels:     userLabelsFromPod(p.Spec),
		State:      "created",
		Status:     "Created",
		HostConfig: dockerapi.SummaryHostConfig{NetworkMode: "default"},
	}
}

func inspectForPending(p *pendingContainer) dockerapi.ContainerInspect {
	name := p.DockerName
	if name == "" {
		name = p.Spec.Name
	}
	var env []string
	if raw := p.Spec.Annotations[podspec.AnnotationEnv]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &env)
	}
	ports := portsFromAnnotation(p.Spec)
	// See buildInspect: daemon-written annotations, fall back to zero on parse error.
	memory, _ := strconv.ParseInt(p.Spec.Annotations[podspec.AnnotationMemory], 10, 64)
	nanoCPUs, _ := strconv.ParseInt(p.Spec.Annotations[podspec.AnnotationNanoCPUs], 10, 64)
	return dockerapi.ContainerInspect{
		ID:      p.ID,
		Created: p.CreatedAt.UTC().Format(time.RFC3339Nano),
		Image:   p.Spec.Annotations[podspec.AnnotationImage],
		Name:    "/" + name,
		State:   dockerapi.InspectState{Status: "created"},
		Config: dockerapi.InspectConfig{
			Image:    p.Spec.Annotations[podspec.AnnotationImage],
			Hostname: p.Spec.Spec.Hostname,
			Env:      env,
			User:     p.Spec.Annotations[podspec.AnnotationUser],
			Labels:   userLabelsFromPod(p.Spec),
		},
		HostConfig: dockerapi.HostConfig{
			NetworkMode:  "default",
			PortBindings: ports,
			Memory:       memory,
			NanoCPUs:     nanoCPUs,
		},
		NetworkSettings: dockerapi.InspectNetworkInfo{Ports: ports},
	}
}
