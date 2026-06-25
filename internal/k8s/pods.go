package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bpaquet/docker-in-kubernetes/internal/podspec"
)

// LogOptions configures StreamLogs.
type LogOptions struct {
	Follow    bool
	TailLines int64 // <=0 means no tail limit
}

const (
	defaultPollInterval = 500 * time.Millisecond
	defaultReadyTimeout = 30 * time.Second
)

// ErrNotFound is returned by Pods operations when the pod does not exist or
// is not owned by this daemon.
var ErrNotFound = errors.New("pod not found")

// Pods provides the daemon's container-as-pod operations against a single
// namespace.
type Pods struct {
	cs           kubernetes.Interface
	namespace    string
	pollInterval time.Duration
	readyTimeout time.Duration
}

// NewPods wires a Pods store with default poll/timeout values.
func NewPods(cs kubernetes.Interface, namespace string) *Pods {
	return &Pods{
		cs:           cs,
		namespace:    namespace,
		pollInterval: defaultPollInterval,
		readyTimeout: defaultReadyTimeout,
	}
}

// Namespace returns the namespace this Pods instance writes to.
func (p *Pods) Namespace() string { return p.namespace }

// SetPollInterval is used by tests to make WaitForReady tight.
func (p *Pods) SetPollInterval(d time.Duration) { p.pollInterval = d }

// SetReadyTimeout overrides the default 30s ready timeout.
func (p *Pods) SetReadyTimeout(d time.Duration) { p.readyTimeout = d }

// Create posts the pod, returning the server's view (with ResourceVersion etc.).
func (p *Pods) Create(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	created, err := p.cs.CoreV1().Pods(p.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create pod %s: %w", pod.Name, err)
	}
	return created, nil
}

// Delete removes the pod. grace == 0 means immediate (no SIGTERM grace).
// Missing pods are reported as ErrNotFound.
func (p *Pods) Delete(ctx context.Context, name string, grace time.Duration) error {
	opts := metav1.DeleteOptions{}
	if grace >= 0 {
		seconds := int64(grace.Seconds())
		opts.GracePeriodSeconds = &seconds
	}
	if err := p.cs.CoreV1().Pods(p.namespace).Delete(ctx, name, opts); err != nil {
		if kerrors.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("delete pod %s: %w", name, err)
	}
	return nil
}

// Get fetches one pod by name. Returns ErrNotFound if it doesn't exist or
// isn't ours (missing the managed label).
func (p *Pods) Get(ctx context.Context, name string) (*corev1.Pod, error) {
	pod, err := p.cs.CoreV1().Pods(p.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get pod %s: %w", name, err)
	}
	if !isManaged(pod) {
		return nil, ErrNotFound
	}
	return pod, nil
}

// List returns every managed pod in the namespace.
func (p *Pods) List(ctx context.Context) ([]corev1.Pod, error) {
	list, err := p.cs.CoreV1().Pods(p.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: podspec.LabelManaged + "=true",
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return list.Items, nil
}

// FindByID resolves a docker CLI reference to a managed pod. References
// accepted, in order:
//
//   - the pod name (RFC 1123, what users see in `docker ps` NAMES column),
//   - the original `--name` value (stored in the docker-name annotation),
//   - the full 64-hex container ID (sha256 of namespace/name),
//   - the 12-char short ID.
//
// Returns ErrNotFound on no match.
func (p *Pods) FindByID(ctx context.Context, ref string) (*corev1.Pod, error) {
	if ref == "" {
		return nil, ErrNotFound
	}
	// Fast path: direct lookup by pod name. Suppress errors and fall through
	// to the label-selector scan so an invalid-as-k8s-name input (e.g. a
	// 64-hex container ID) still resolves below.
	if pod, err := p.Get(ctx, ref); err == nil {
		return pod, nil
	}
	pods, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range pods {
		if pods[i].Annotations[podspec.AnnotationDockerName] == ref {
			return &pods[i], nil
		}
		fullID := podspec.ContainerID(pods[i].Namespace, pods[i].Name)
		if fullID == ref || podspec.ShortID(fullID) == ref {
			return &pods[i], nil
		}
	}
	return nil, ErrNotFound
}

// ImagePullFailedError signals a fatal container-state reason that should
// surface to `docker run` as a non-zero exit.
type ImagePullFailedError struct {
	Reason  string
	Message string
}

func (e *ImagePullFailedError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Reason, e.Message)
	}
	return e.Reason
}

// WaitForReady blocks until pod `name` is Ready, or a fail-fast container
// state appears, or the per-call timeout elapses. ctx cancellation is honored.
func (p *Pods) WaitForReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(p.readyTimeout)
	for {
		pod, err := p.Get(ctx, name)
		if err != nil {
			return err
		}
		if isReady(pod) {
			return nil
		}
		if reason, message := fatalContainerWaitingState(pod); reason != "" {
			return &ImagePullFailedError{Reason: reason, Message: message}
		}
		if pod.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("pod %s entered Failed phase: %s", name, pod.Status.Reason)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for pod %s to be ready after %s", name, p.readyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.pollInterval):
		}
	}
}

func isManaged(pod *corev1.Pod) bool {
	return pod != nil && pod.Labels[podspec.LabelManaged] == "true"
}

func isReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// StreamLogs returns the pod's stdout+stderr stream. The caller closes.
func (p *Pods) StreamLogs(ctx context.Context, name string, opts LogOptions) (io.ReadCloser, error) {
	logOpts := &corev1.PodLogOptions{
		Follow: opts.Follow,
	}
	if opts.TailLines > 0 {
		t := opts.TailLines
		logOpts.TailLines = &t
	}
	req := p.cs.CoreV1().Pods(p.namespace).GetLogs(name, logOpts)
	rc, err := req.Stream(ctx)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("stream logs %s: %w", name, err)
	}
	return rc, nil
}

// PodIP returns the assigned PodIP, or "" if not yet assigned.
func (p *Pods) PodIP(ctx context.Context, namespace, name string) (string, error) {
	if namespace == "" {
		namespace = p.namespace
	}
	pod, err := p.cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("get pod for ip: %w", err)
	}
	return pod.Status.PodIP, nil
}

// fatalContainerWaitingState returns (reason, message) when any container is
// waiting in one of the unrecoverable image-pull / setup states.
func fatalContainerWaitingState(pod *corev1.Pod) (string, string) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil {
			continue
		}
		switch cs.State.Waiting.Reason {
		case "ImagePullBackOff", "ErrImagePull", "CreateContainerError", "InvalidImageName", "RegistryUnavailable":
			return cs.State.Waiting.Reason, cs.State.Waiting.Message
		}
	}
	return "", ""
}
