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

	labels := pod.Labels
	if raw := pod.Annotations[podspec.AnnotationLabels]; raw != "" {
		var userLabels map[string]string
		if err := json.Unmarshal([]byte(raw), &userLabels); err == nil {
			labels = userLabels
		}
	}

	return dockerapi.ContainerSummary{
		ID:         id,
		Names:      []string{"/" + name},
		Image:      image,
		ImageID:    "",
		Command:    summaryCommand(pod),
		Created:    created.Unix(),
		Ports:      summaryPorts(pod),
		Labels:     labels,
		State:      state,
		Status:     status(state, created),
		HostConfig: dockerapi.SummaryHostConfig{NetworkMode: "default"},
	}
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
	if state == "exited" {
		inspectState.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}

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
			Labels:   pod.Labels,
		},
		HostConfig: dockerapi.HostConfig{
			NetworkMode:  "default",
			PortBindings: ports,
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

func status(state string, created time.Time) string {
	switch state {
	case "running":
		if created.IsZero() {
			return "Up"
		}
		return "Up " + humanDuration(time.Since(created))
	case "exited":
		return "Exited (0)"
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
