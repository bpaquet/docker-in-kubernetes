package podspec

// Label and annotation keys reserved by docker-in-kubernetes.
const (
	LabelManaged       = "docker-in-kubernetes.io/managed"
	LabelContainerName = "docker-in-kubernetes.io/container-name"
	LabelProject       = "docker-in-kubernetes.io/project"

	AnnotationCreatedAt  = "docker-in-kubernetes.io/created-at"
	AnnotationImage      = "docker-in-kubernetes.io/image"
	AnnotationPorts      = "docker-in-kubernetes.io/ports"
	AnnotationEnv        = "docker-in-kubernetes.io/env"
	AnnotationDockerName = "docker-in-kubernetes.io/docker-name"
	AnnotationLabels     = "docker-in-kubernetes.io/labels"
	AnnotationUser       = "docker-in-kubernetes.io/user"
	AnnotationMemory     = "docker-in-kubernetes.io/memory"
	AnnotationNanoCPUs   = "docker-in-kubernetes.io/nano-cpus"

	// DefaultProject is reserved for Docker Compose forward-compat.
	DefaultProject = "default"
)
