package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// A temporary test client lives only for the length of one Service test.
// activeDeadlineSeconds is the backstop when the desk dies before it deletes
// the Pod; the stale sweep removes what the deadline leaves behind.
const (
	ephemeralImage        = "docker.io/curlimages/curl:8.12.1"
	ephemeralContainer    = "curl"
	ephemeralPurposeKey   = "flightbay.dev/purpose"
	ephemeralPurposeValue = "http-test"
	ephemeralLifetime     = 900
	ephemeralStaleAfter   = 15 * time.Minute
	ephemeralReadyTimeout = 90 * time.Second
)

const ephemeralNameAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func ephemeralSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = ephemeralNameAlphabet[int(b[i])%len(ephemeralNameAlphabet)]
	}
	return string(b)
}

func ephemeralClientPod(ns string) *corev1.Pod {
	deadline := int64(ephemeralLifetime)
	grace := int64(0)
	no, yes := false, true
	user := int64(100)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "flightbay-http-" + ephemeralSuffix(),
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "flightbay",
				ephemeralPurposeKey:            ephemeralPurposeValue,
				"app":                          "flightbay-http",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         &deadline,
			TerminationGracePeriodSeconds: &grace,
			AutomountServiceAccountToken:  &no,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &yes,
				RunAsUser:      &user,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:    ephemeralContainer,
				Image:   ephemeralImage,
				Command: []string{"sleep", fmt.Sprint(ephemeralLifetime)},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &yes,
					RunAsUser:                &user,
					AllowPrivilegeEscalation: &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
			}},
		},
	}
}

// sweepStaleEphemeralClients deletes test Pods left behind by an earlier run.
// Best effort: the caller does not fail the test on a sweep error.
func sweepStaleEphemeralClients(ctx context.Context, client kubernetes.Interface, ns string, now time.Time) []string {
	list, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: ephemeralPurposeKey + "=" + ephemeralPurposeValue})
	if err != nil {
		return nil
	}
	removed := []string{}
	grace := int64(0)
	for _, pod := range list.Items {
		if now.Sub(pod.CreationTimestamp.Time) < ephemeralStaleAfter {
			continue
		}
		if client.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace}) == nil {
			removed = append(removed, pod.Name)
		}
	}
	return removed
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podWaitingReason(pod *corev1.Pod) string {
	for _, s := range pod.Status.ContainerStatuses {
		if s.State.Waiting != nil && s.State.Waiting.Reason != "" {
			reason := s.State.Waiting.Reason
			if s.State.Waiting.Message != "" {
				reason += ": " + s.State.Waiting.Message
			}
			return reason
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Message != "" {
			return c.Message
		}
	}
	return ""
}

// startEphemeralClient creates the Pod and waits for it to become Ready. On
// any failure after the create it deletes the Pod before returning, so the
// caller only owns a Pod when the error is nil.
func startEphemeralClient(ctx context.Context, client kubernetes.Interface, ns string, poll, timeout time.Duration) (*corev1.Pod, error) {
	created, err := client.CoreV1().Pods(ns).Create(ctx, ephemeralClientPod(ns), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not create the temporary test Pod in %s: %w", ns, err)
	}
	fail := func(err error) (*corev1.Pod, error) {
		_ = deleteEphemeralClient(client, ns, created.Name)
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	last := created
	for {
		pod, err := client.CoreV1().Pods(ns).Get(waitCtx, created.Name, metav1.GetOptions{})
		if err == nil {
			last = pod
			if podReady(pod) {
				return pod, nil
			}
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				return fail(fmt.Errorf("temporary test Pod %s/%s stopped before it was Ready (phase %s)", ns, created.Name, pod.Status.Phase))
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return fail(fmt.Errorf("test stopped while the temporary Pod was starting"))
			}
			detail := podWaitingReason(last)
			if detail != "" {
				detail = " (" + detail + ")"
			}
			return fail(fmt.Errorf("temporary test Pod %s/%s was not Ready within %s%s", ns, created.Name, timeout, detail))
		case <-ticker.C:
		}
	}
}

// deleteEphemeralClient uses its own context: it runs after the request
// context has been cancelled by a stopped or failed test.
func deleteEphemeralClient(client kubernetes.Interface, ns, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grace := int64(0)
	err := client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
