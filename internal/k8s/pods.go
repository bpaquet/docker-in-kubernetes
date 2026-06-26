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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

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

// ErrNotFound covers both "no such pod" and "pod is not managed by us".
var ErrNotFound = errors.New("pod not found")

// Pods is the daemon's pod CRUD against one namespace.
type Pods struct {
	cs           kubernetes.Interface
	rest         *rest.Config
	namespace    string
	pollInterval time.Duration
	readyTimeout time.Duration
}

// NewPods returns a Pods bound to namespace.
func NewPods(cs kubernetes.Interface, namespace string) *Pods {
	return &Pods{
		cs:           cs,
		namespace:    namespace,
		pollInterval: defaultPollInterval,
		readyTimeout: defaultReadyTimeout,
	}
}

// WithREST sets the rest.Config used for attach/exec (which need SPDY,
// not the clientset RESTClient alone).
func (p *Pods) WithREST(cfg *rest.Config) *Pods {
	p.rest = cfg
	return p
}

// Namespace returns the bound namespace.
func (p *Pods) Namespace() string { return p.namespace }

// SetPollInterval tightens WaitForReady polling in tests.
func (p *Pods) SetPollInterval(d time.Duration) { p.pollInterval = d }

// SetReadyTimeout overrides WaitForReady's per-call timeout.
func (p *Pods) SetReadyTimeout(d time.Duration) { p.readyTimeout = d }

// Create posts the pod.
func (p *Pods) Create(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	created, err := p.cs.CoreV1().Pods(p.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create pod %s: %w", pod.Name, err)
	}
	return created, nil
}

// Delete removes the pod (grace=0 skips SIGTERM). Returns ErrNotFound if missing.
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

// Get fetches one managed pod by name. ErrNotFound if missing or unmanaged.
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

// FindByID resolves a pod name, --name annotation, full ID, or short ID.
func (p *Pods) FindByID(ctx context.Context, ref string) (*corev1.Pod, error) {
	if ref == "" {
		return nil, ErrNotFound
	}
	// Direct Get errors (incl. invalid-as-k8s-name for 64-hex IDs) fall
	// through to the label-selector scan below.
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

// ImagePullFailedError is the fail-fast error surfaced by WaitForReady.
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

// WaitForReady blocks until Ready or terminated, a fatal waiting state, or
// the timeout. PodSucceeded counts as "done starting" so a fast-exiting
// container doesn't trip the readiness probe loop.
func (p *Pods) WaitForReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(p.readyTimeout)
	for {
		pod, err := p.Get(ctx, name)
		if err != nil {
			return err
		}
		if isReady(pod) || pod.Status.Phase == corev1.PodSucceeded {
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

// StreamOptions is what callers feed Attach/Exec.
type StreamOptions struct {
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	TTY               bool
	TerminalSizeQueue remotecommand.TerminalSizeQueue
}

// Attach streams to/from the pod's main container via the attach subresource.
func (p *Pods) Attach(ctx context.Context, podName string, opts StreamOptions) error {
	po := &corev1.PodAttachOptions{
		Stdin:  opts.Stdin != nil,
		Stdout: opts.Stdout != nil,
		Stderr: opts.Stderr != nil,
		TTY:    opts.TTY,
	}
	return p.runSPDY(ctx, podName, "attach", po, opts)
}

// Exec runs cmd inside the pod's main container.
func (p *Pods) Exec(ctx context.Context, podName string, cmd []string, opts StreamOptions) error {
	po := &corev1.PodExecOptions{
		Command: cmd,
		Stdin:   opts.Stdin != nil,
		Stdout:  opts.Stdout != nil,
		Stderr:  opts.Stderr != nil,
		TTY:     opts.TTY,
	}
	return p.runSPDY(ctx, podName, "exec", po, opts)
}

func (p *Pods) runSPDY(ctx context.Context, podName, subresource string, params runtime.Object, opts StreamOptions) error {
	if p.rest == nil {
		return errors.New("pods: rest.Config not set; call WithREST")
	}
	req := p.cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(p.namespace).
		SubResource(subresource).
		VersionedParams(params, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(p.rest, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("spdy executor: %w", err)
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             opts.Stdin,
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
		Tty:               opts.TTY,
		TerminalSizeQueue: opts.TerminalSizeQueue,
	})
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

// FatalContainerWaitingState returns the kubelet Waiting reason/message when
// the container is stuck in a state that won't recover on its own (image pull
// failure etc.). Empty strings mean "not stuck".
func FatalContainerWaitingState(pod *corev1.Pod) (reason, message string) {
	return fatalContainerWaitingState(pod)
}

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
