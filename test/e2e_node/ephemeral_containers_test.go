/*
Copyright 2021 The Kubernetes Authors.

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

package e2enode

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/features"
	kubeletconfig "k8s.io/kubernetes/pkg/kubelet/apis/config"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	"github.com/onsi/ginkgo"
)

var _ = framework.KubeDescribe("EphemeralContainers", func() {
	f := framework.NewDefaultFramework("ephemeral-containers-test")
	/*
		var podClient *framework.PodClient
		ginkgo.BeforeEach(func() {
			podClient = f.PodClient()
		})
	*/
	var ctx context.Context
	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.Context("when enabled [Feature:EphemeralContainers][NodeFeature:EphemeralContainers]", func() {
		// TODO: Figure out the magic incantation to get feature gates set. They're not being set on the kubelet, no idea about API server
		//defer withFeatureGate(features.EphemeralContainers, true)()
		tempSetCurrentKubeletConfig(f, func(initialConfig *kubeletconfig.KubeletConfiguration) {
			//defer withFeatureGate(features.EphemeralContainers, true)()
			if initialConfig.FeatureGates == nil {
				initialConfig.FeatureGates = make(map[string]bool)
			}
			initialConfig.FeatureGates[string(features.EphemeralContainers)] = true
		})

		ginkgo.It("causes the kubelet to create new containers when they're added", func() {
			ginkgo.By("creating a target pod")
			pod := f.PodClient().CreateSync(&v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "ephemeral-containers-target-pod"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:    "test-container-1",
							Image:   imageutils.GetE2EImage(imageutils.BusyBox),
							Command: []string{"/bin/sleep"},
							Args:    []string{"10000"},
						},
					},
				},
			})

			ginkgo.By("adding an ephemeral container")
			//GetEphemeralContainers(ctx context.Context, podName string, options metav1.GetOptions) (*v1.EphemeralContainers, error)
			ec, err := f.PodClient().GetEphemeralContainers(ctx, pod.Name, metav1.GetOptions{})
			framework.ExpectNoError(err)

			//UpdateEphemeralContainers(ctx context.Context, podName string, ephemeralContainers *v1.EphemeralContainers, opts metav1.UpdateOptions) (*v1.EphemeralContainers, error)
			_, err = f.PodClient().UpdateEphemeralContainers(ctx, pod.Name, ec, metav1.UpdateOptions{})
			framework.ExpectNoError(err)
		})

	})

})
