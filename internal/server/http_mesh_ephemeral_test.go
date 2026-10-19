package server

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestEphemeralClientPodIsLabelledLockedDownAndSelfExpiring(t *testing.T) {
	pod := ephemeralClientPod("kagent")
	if !regexp.MustCompile(`^flightbay-http-[a-z0-9]{6}$`).MatchString(pod.Name) || pod.Namespace != "kagent" {
		t.Fatalf("unexpected name %s/%s", pod.Namespace, pod.Name)
	}
	if other := ephemeralClientPod("kagent"); other.Name == pod.Name {
		t.Fatal("two temporary Pods got the same name")
	}
	for k, v := range map[string]string{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/purpose": "http-test", "app": "flightbay-http"} {
		if pod.Labels[k] != v {
			t.Fatalf("label %s = %q, want %q", k, pod.Labels[k], v)
		}
	}
	spec := pod.Spec
	if spec.ActiveDeadlineSeconds == nil || *spec.ActiveDeadlineSeconds != 900 || spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("Pod must expire by itself after 900s and never restart")
	}
	if spec.TerminationGracePeriodSeconds == nil || *spec.TerminationGracePeriodSeconds != 0 || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("grace period must be 0 and no service account token mounted")
	}
	if len(spec.Containers) != 1 {
		t.Fatalf("want one container, got %d", len(spec.Containers))
	}
	c := spec.Containers[0]
	if c.Name != "curl" || c.Image != "docker.io/curlimages/curl:8.12.1" || strings.Join(c.Command, " ") != "sleep 900" {
		t.Fatalf("unexpected container %s %s %v", c.Name, c.Image, c.Command)
	}
	sc := c.SecurityContext
	if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.RunAsUser == nil || *sc.RunAsUser != 100 || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("container security context is not locked down: %+v", sc)
	}
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot {
		t.Fatal("Pod security context must require non-root")
	}
	if c.Resources.Requests.Cpu().String() != "10m" || c.Resources.Requests.Memory().String() != "16Mi" || c.Resources.Limits.Cpu().String() != "200m" || c.Resources.Limits.Memory().String() != "64Mi" {
		t.Fatalf("unexpected resources %+v", c.Resources)
	}
}

// markReadyOnGet makes the fake API server report the Pod Ready after the
// given number of Gets, as a kubelet would after the image starts.
func markReadyOnGet(client *fake.Clientset, after int) {
	gets := 0
	client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < after {
			return false, nil, nil
		}
		name := action.(k8stesting.GetAction).GetName()
		obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), action.GetNamespace(), name)
		if err != nil {
			return true, nil, err
		}
		pod := obj.(*corev1.Pod).DeepCopy()
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		return true, pod, nil
	})
}

func TestStartEphemeralClientWaitsForReadyThenDeletes(t *testing.T) {
	client := fake.NewSimpleClientset()
	markReadyOnGet(client, 3)
	pod, err := startEphemeralClient(context.Background(), client, "kagent", time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !podReady(pod) {
		t.Fatal("returned Pod is not Ready")
	}
	if _, err := client.CoreV1().Pods("kagent").Get(context.Background(), pod.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("Pod should exist while the test runs: %v", err)
	}
	if err := deleteEphemeralClient(client, "kagent", pod.Name); err != nil {
		t.Fatal(err)
	}
	if list, _ := client.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), "kagent"); len(list.(*corev1.PodList).Items) != 0 {
		t.Fatal("Pod was not deleted")
	}
	if err := deleteEphemeralClient(client, "kagent", pod.Name); err != nil {
		t.Fatalf("deleting an already removed Pod must not be an error: %v", err)
	}
}

func TestStartEphemeralClientDeletesPodThatNeverBecomesReady(t *testing.T) {
	client := fake.NewSimpleClientset()
	_, err := startEphemeralClient(context.Background(), client, "kagent", time.Millisecond, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("want a not Ready error, got %v", err)
	}
	list, _ := client.CoreV1().Pods("kagent").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatalf("Pod left behind after a failed start: %d", len(list.Items))
	}
}

func TestStartEphemeralClientDeletesPodWhenCancelled(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, err := startEphemeralClient(ctx, client, "kagent", time.Millisecond, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("want a stopped error, got %v", err)
	}
	list, _ := client.CoreV1().Pods("kagent").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatal("Pod left behind after the test was cancelled")
	}
}

func TestSweepRemovesOnlyStaleTestPods(t *testing.T) {
	now := time.Now()
	old := metav1.NewTime(now.Add(-20 * time.Minute))
	fresh := metav1.NewTime(now.Add(-time.Minute))
	purpose := map[string]string{"flightbay.dev/purpose": "http-test"}
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "flightbay-http-old111", Namespace: "kagent", Labels: purpose, CreationTimestamp: old}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "flightbay-http-new222", Namespace: "kagent", Labels: purpose, CreationTimestamp: fresh}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: "kagent", CreationTimestamp: old}},
	)
	removed := sweepStaleEphemeralClients(context.Background(), client, "kagent", now)
	if len(removed) != 1 || removed[0] != "flightbay-http-old111" {
		t.Fatalf("unexpected sweep %v", removed)
	}
	list, _ := client.CoreV1().Pods("kagent").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 2 {
		t.Fatalf("sweep removed the wrong Pods: %d remain", len(list.Items))
	}
}
