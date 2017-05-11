/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kuberuntime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/kubernetes/pkg/api/v1"
	runtimeapi "k8s.io/kubernetes/pkg/kubelet/apis/cri/v1alpha1"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	containertest "k8s.io/kubernetes/pkg/kubelet/container/testing"
)

// TestRemoveContainer tests removing the container and its corresponding container logs.
func TestRemoveContainer(t *testing.T) {
	fakeRuntime, _, m, err := createTestRuntimeManager()
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "12345678",
			Name:      "bar",
			Namespace: "new",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "foo",
					Image:           "busybox",
					ImagePullPolicy: v1.PullIfNotPresent,
				},
			},
		},
	}

	// Create fake sandbox and container
	_, fakeContainers := makeAndSetFakePod(t, m, fakeRuntime, pod)
	assert.Equal(t, len(fakeContainers), 1)

	containerId := fakeContainers[0].Id
	fakeOS := m.osInterface.(*containertest.FakeOS)
	err = m.removeContainer(containerId)
	assert.NoError(t, err)
	// Verify container log is removed
	expectedContainerLogPath := filepath.Join(podLogsRootDirectory, "12345678", "foo_0.log")
	expectedContainerLogSymlink := legacyLogSymlink(containerId, "foo", "bar", "new")
	assert.Equal(t, fakeOS.Removes, []string{expectedContainerLogPath, expectedContainerLogSymlink})
	// Verify container is removed
	assert.Contains(t, fakeRuntime.Called, "RemoveContainer")
	containers, err := fakeRuntime.ListContainers(&runtimeapi.ContainerFilter{Id: containerId})
	assert.NoError(t, err)
	assert.Empty(t, containers)
}

// TestToKubeContainerStatus tests the converting the CRI container status to
// the internal type (i.e., toKubeContainerStatus()) for containers in
// different states.
func TestToKubeContainerStatus(t *testing.T) {
	cid := &kubecontainer.ContainerID{Type: "testRuntime", ID: "dummyid"}
	meta := &runtimeapi.ContainerMetadata{Name: "cname", Attempt: 3}
	imageSpec := &runtimeapi.ImageSpec{Image: "fimage"}
	var (
		createdAt  int64 = 327
		startedAt  int64 = 999
		finishedAt int64 = 1278
	)

	for desc, test := range map[string]struct {
		input    *runtimeapi.ContainerStatus
		expected *kubecontainer.ContainerStatus
	}{
		"created container": {
			input: &runtimeapi.ContainerStatus{
				Id:        cid.ID,
				Metadata:  meta,
				Image:     imageSpec,
				State:     runtimeapi.ContainerState_CONTAINER_CREATED,
				CreatedAt: createdAt,
			},
			expected: &kubecontainer.ContainerStatus{
				ID:        *cid,
				Image:     imageSpec.Image,
				State:     kubecontainer.ContainerStateCreated,
				CreatedAt: time.Unix(0, createdAt),
			},
		},
		"running container": {
			input: &runtimeapi.ContainerStatus{
				Id:        cid.ID,
				Metadata:  meta,
				Image:     imageSpec,
				State:     runtimeapi.ContainerState_CONTAINER_RUNNING,
				CreatedAt: createdAt,
				StartedAt: startedAt,
			},
			expected: &kubecontainer.ContainerStatus{
				ID:        *cid,
				Image:     imageSpec.Image,
				State:     kubecontainer.ContainerStateRunning,
				CreatedAt: time.Unix(0, createdAt),
				StartedAt: time.Unix(0, startedAt),
			},
		},
		"exited container": {
			input: &runtimeapi.ContainerStatus{
				Id:         cid.ID,
				Metadata:   meta,
				Image:      imageSpec,
				State:      runtimeapi.ContainerState_CONTAINER_EXITED,
				CreatedAt:  createdAt,
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
				ExitCode:   int32(121),
				Reason:     "GotKilled",
				Message:    "The container was killed",
			},
			expected: &kubecontainer.ContainerStatus{
				ID:         *cid,
				Image:      imageSpec.Image,
				State:      kubecontainer.ContainerStateExited,
				CreatedAt:  time.Unix(0, createdAt),
				StartedAt:  time.Unix(0, startedAt),
				FinishedAt: time.Unix(0, finishedAt),
				ExitCode:   121,
				Reason:     "GotKilled",
				Message:    "The container was killed",
			},
		},
		"unknown container": {
			input: &runtimeapi.ContainerStatus{
				Id:        cid.ID,
				Metadata:  meta,
				Image:     imageSpec,
				State:     runtimeapi.ContainerState_CONTAINER_UNKNOWN,
				CreatedAt: createdAt,
				StartedAt: startedAt,
			},
			expected: &kubecontainer.ContainerStatus{
				ID:        *cid,
				Image:     imageSpec.Image,
				State:     kubecontainer.ContainerStateUnknown,
				CreatedAt: time.Unix(0, createdAt),
				StartedAt: time.Unix(0, startedAt),
			},
		},
	} {
		actual := toKubeContainerStatus(test.input, cid.Type)
		assert.Equal(t, test.expected, actual, desc)
	}
}

func podDebugTarget(name string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(name + "12345678"),
			Name:      name,
			Namespace: "new",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "existingcontainer",
					Image:           "application",
					ImagePullPolicy: v1.PullIfNotPresent,
				},
			},
		},
	}
}

// TestRunDebugContainer tests creating and starting a Debug Container in a pod
func TestRunDebugContainer(t *testing.T) {
	utilfeature.DefaultFeatureGate.Set("DebugContainers=True")
	defer utilfeature.DefaultFeatureGate.Set("DebugContainers=False")

	existingPodName := "targetpod"
	testcases := []struct {
		description string
		targetPod   *v1.Pod
		container   v1.Container
		expectError bool
	}{{
		"success case",
		podDebugTarget(existingPodName),
		v1.Container{
			Name:            "debug",
			Image:           "busybox",
			ImagePullPolicy: v1.PullIfNotPresent,
		},
		false,
	}, {
		"no such pod",
		podDebugTarget("otherpod"),
		v1.Container{
			Name:            "debug",
			Image:           "busybox",
			ImagePullPolicy: v1.PullIfNotPresent,
		},
		true,
	}, {
		"container spec collision",
		podDebugTarget(existingPodName),
		v1.Container{
			Name:            "existingcontainer",
			Image:           "busybox",
			ImagePullPolicy: v1.PullIfNotPresent,
		},
		true,
	}}

	for _, tc := range testcases {
		fakeRuntime, _, m, err := createTestRuntimeManager()
		assert.NoError(t, err, tc.description)
		makeAndSetFakePod(t, m, fakeRuntime, podDebugTarget(existingPodName))

		if err := m.RunDebugContainer(tc.targetPod, &tc.container, nil); tc.expectError {
			assert.Error(t, err, tc.description)
		} else {
			assert.NoError(t, err)

			// Verify debug container is started
			assert.Contains(t, fakeRuntime.Called, "CreateContainer", tc.description)
			assert.Contains(t, fakeRuntime.Called, "StartContainer", tc.description)
		}
	}
}

// TestRunDebugContainerDisabled tests that debug containers cannot be created if disabled
func TestRunDebugContainerDisabled(t *testing.T) {
	utilfeature.DefaultFeatureGate.Set("DebugContainers=False")
	fakeRuntime, _, m, err := createTestRuntimeManager()
	assert.NoError(t, err)

	pod := podDebugTarget("pod")
	makeAndSetFakePod(t, m, fakeRuntime, pod)

	debugContainer := &v1.Container{
		Name:            "debug",
		Image:           "busybox",
		ImagePullPolicy: v1.PullIfNotPresent,
	}
	err = m.RunDebugContainer(pod, debugContainer, nil)
	assert.Error(t, err)
	assert.NoError(t, fakeRuntime.AssertCalls([]string{"Version"})) // "Version" is always called
}
