package podspec_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

func baseRequest() dockerapi.CreateRequest {
	return dockerapi.CreateRequest{
		Image: "redis",
		Env:   []string{"FOO=bar", "BAZ="},
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"6379/tcp": {{HostPort: "6379"}},
			},
		},
	}
}

func TestBuildHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	res, err := podspec.Build(podspec.BuildInput{
		Namespace:  "docker-in-kubernetes",
		DockerName: "myredis",
		Now:        now,
		Request:    baseRequest(),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Pod)

	pod := res.Pod
	assert.Equal(t, "myredis", pod.Name)
	assert.Equal(t, "myredis", res.PodName)
	assert.Equal(t, "docker-in-kubernetes", pod.Namespace)
	assert.Equal(t, "true", pod.Labels[podspec.LabelManaged])
	assert.Equal(t, "myredis", pod.Labels[podspec.LabelContainerName])
	assert.Equal(t, "default", pod.Labels[podspec.LabelProject])

	assert.Equal(t, "redis", pod.Annotations[podspec.AnnotationImage])
	assert.Equal(t, "myredis", pod.Annotations[podspec.AnnotationDockerName])
	assert.Equal(t, "2026-06-25T12:00:00Z", pod.Annotations[podspec.AnnotationCreatedAt])

	require.Len(t, pod.Spec.Containers, 1)
	c := pod.Spec.Containers[0]
	assert.Equal(t, "redis", c.Image)
	require.Len(t, c.Env, 2)
	assert.Equal(t, "FOO", c.Env[0].Name)
	assert.Equal(t, "bar", c.Env[0].Value)
	assert.Equal(t, "BAZ", c.Env[1].Name)
	assert.Equal(t, "", c.Env[1].Value)

	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(6379), c.Ports[0].ContainerPort)
	assert.Equal(t, corev1.ProtocolTCP, c.Ports[0].Protocol)

	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

	require.Len(t, res.PortMappings, 1)
	assert.Equal(t, "6379", res.PortMappings[0].HostPort)
	assert.Equal(t, uint16(6379), res.PortMappings[0].ContainerPort)
	assert.Equal(t, "tcp", res.PortMappings[0].Protocol)
}

func TestBuildGeneratesNameWhenDockerNameEmpty(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.PodName, "dik-redis-")
}

func TestBuildRejectsEmptyImage(t *testing.T) {
	_, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image")
}

func TestBuildRejectsEmptyNamespace(t *testing.T) {
	_, err := podspec.Build(podspec.BuildInput{
		Request: dockerapi.CreateRequest{Image: "redis"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace")
}

func TestBuildExposedPortsContributeToContainerPorts(t *testing.T) {
	req := dockerapi.CreateRequest{
		Image: "nginx",
		ExposedPorts: map[string]struct{}{
			"80/tcp":  {},
			"443/tcp": {},
		},
	}
	res, err := podspec.Build(podspec.BuildInput{Namespace: "ns", Request: req})
	require.NoError(t, err)
	require.Len(t, res.PortMappings, 2)
	// sorted ascending
	assert.Equal(t, uint16(80), res.PortMappings[0].ContainerPort)
	assert.Equal(t, uint16(443), res.PortMappings[1].ContainerPort)
	// no host port for exposed-only entries
	assert.Empty(t, res.PortMappings[0].HostPort)
}

func TestBuildMultipleHostPortsToSameContainerPort(t *testing.T) {
	req := dockerapi.CreateRequest{
		Image: "redis",
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"6379/tcp": {{HostPort: "6379"}, {HostPort: "6380"}},
			},
		},
	}
	res, err := podspec.Build(podspec.BuildInput{Namespace: "ns", Request: req})
	require.NoError(t, err)

	require.Len(t, res.PortMappings, 2)
	hostPorts := []string{res.PortMappings[0].HostPort, res.PortMappings[1].HostPort}
	assert.ElementsMatch(t, []string{"6379", "6380"}, hostPorts)

	// The k8s pod spec only needs one ContainerPort entry; k8s rejects duplicates.
	require.Len(t, res.Pod.Spec.Containers[0].Ports, 1)
	assert.Equal(t, int32(6379), res.Pod.Spec.Containers[0].Ports[0].ContainerPort)
}

func TestBuildPortBindingsTakePrecedence(t *testing.T) {
	req := dockerapi.CreateRequest{
		Image: "nginx",
		ExposedPorts: map[string]struct{}{
			"80/tcp": {},
		},
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"80/tcp": {{HostPort: "8080"}},
			},
		},
	}
	res, err := podspec.Build(podspec.BuildInput{Namespace: "ns", Request: req})
	require.NoError(t, err)
	require.Len(t, res.PortMappings, 1)
	assert.Equal(t, "8080", res.PortMappings[0].HostPort)
}

func TestBuildInvalidPortRejected(t *testing.T) {
	req := dockerapi.CreateRequest{
		Image: "x",
		HostConfig: dockerapi.HostConfig{
			PortBindings: map[string][]dockerapi.PortBinding{
				"abc/tcp": {{HostPort: "1"}},
			},
		},
	}
	_, err := podspec.Build(podspec.BuildInput{Namespace: "ns", Request: req})
	require.Error(t, err)
}

func TestBuildPortsAnnotationRoundTrip(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   baseRequest(),
	})
	require.NoError(t, err)
	raw := res.Pod.Annotations[podspec.AnnotationPorts]
	require.NotEmpty(t, raw)
	var got []podspec.PortMapping
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Equal(t, res.PortMappings, got)
}

func TestBuildEnvAnnotationOmittedWhenNoEnv(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis"},
	})
	require.NoError(t, err)
	_, has := res.Pod.Annotations[podspec.AnnotationEnv]
	assert.False(t, has)
}

func TestBuildPreservesCmdAndEntrypoint(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request: dockerapi.CreateRequest{
			Image:      "redis",
			Entrypoint: dockerapi.StringOrSlice{"/usr/local/bin/redis-server"},
			Cmd:        []string{"--port", "6379"},
		},
	})
	require.NoError(t, err)
	c := res.Pod.Spec.Containers[0]
	assert.Equal(t, []string{"/usr/local/bin/redis-server"}, c.Command)
	assert.Equal(t, []string{"--port", "6379"}, c.Args)
}

func TestBuildAppliesResources(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request: dockerapi.CreateRequest{
			Image: "redis",
			HostConfig: dockerapi.HostConfig{
				Memory:   128 * 1024 * 1024,
				NanoCPUs: 500_000_000, // 0.5 CPU
			},
		},
	})
	require.NoError(t, err)
	c := res.Pod.Spec.Containers[0]
	require.NotNil(t, c.Resources.Requests)
	require.NotNil(t, c.Resources.Limits)
	mem := c.Resources.Limits["memory"]
	cpu := c.Resources.Limits["cpu"]
	assert.Equal(t, "128Mi", mem.String())
	assert.Equal(t, "500m", cpu.String())
	memReq := c.Resources.Requests["memory"]
	cpuReq := c.Resources.Requests["cpu"]
	assert.Equal(t, mem.String(), memReq.String(), "request should equal limit")
	assert.Equal(t, cpu.String(), cpuReq.String(), "request should equal limit")
	assert.Equal(t, "134217728", res.Pod.Annotations[podspec.AnnotationMemory])
	assert.Equal(t, "500000000", res.Pod.Annotations[podspec.AnnotationNanoCPUs])
}

func TestBuildResourcesOmittedWhenZero(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis"},
	})
	require.NoError(t, err)
	c := res.Pod.Spec.Containers[0]
	assert.Empty(t, c.Resources.Requests)
	assert.Empty(t, c.Resources.Limits)
}

func TestBuildUserNumericUIDOnly(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis", User: "1000"},
	})
	require.NoError(t, err)
	sc := res.Pod.Spec.Containers[0].SecurityContext
	require.NotNil(t, sc)
	require.NotNil(t, sc.RunAsUser)
	assert.Equal(t, int64(1000), *sc.RunAsUser)
	assert.Nil(t, sc.RunAsGroup)
	assert.Equal(t, "1000", res.Pod.Annotations[podspec.AnnotationUser])
}

func TestBuildUserWithGID(t *testing.T) {
	res, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis", User: "1000:1001"},
	})
	require.NoError(t, err)
	sc := res.Pod.Spec.Containers[0].SecurityContext
	require.NotNil(t, sc)
	require.NotNil(t, sc.RunAsGroup)
	assert.Equal(t, int64(1000), *sc.RunAsUser)
	assert.Equal(t, int64(1001), *sc.RunAsGroup)
}

func TestBuildUserNonNumericRejected(t *testing.T) {
	_, err := podspec.Build(podspec.BuildInput{
		Namespace: "ns",
		Request:   dockerapi.CreateRequest{Image: "redis", User: "root"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "numeric uid")
}

func TestBuildUserRejectsNegative(t *testing.T) {
	for _, user := range []string{"-1", "1000:-2"} {
		_, err := podspec.Build(podspec.BuildInput{
			Namespace: "ns",
			Request:   dockerapi.CreateRequest{Image: "redis", User: user},
		})
		require.Error(t, err, "user=%q should be rejected", user)
	}
}
