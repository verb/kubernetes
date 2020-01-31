/*
Copyright 2014 The Kubernetes Authors.

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

package versioned

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubectl/pkg/generate"
)

type DebugContainer struct{}

func (DebugContainer) ParamNames() []generate.GeneratorParam {
	return []generate.GeneratorParam{
		{Name: "default-name", Required: false},
		{Name: "name", Required: true},
		{Name: "image", Required: true},
		{Name: "image-pull-policy", Required: false},
		{Name: "stdin", Required: false},
		{Name: "leave-stdin-open", Required: false},
		{Name: "tty", Required: false},
		{Name: "command", Required: false},
		{Name: "args", Required: false},
		{Name: "env", Required: false},
		{Name: "target", Required: false},
	}
}

func (DebugContainer) Generate(genericParams map[string]interface{}) (runtime.Object, error) {
	args, err := getArgs(genericParams)
	if err != nil {
		return nil, err
	}

	envs, err := getEnvs(genericParams)
	if err != nil {
		return nil, err
	}

	params, err := getParams(genericParams)
	if err != nil {
		return nil, err
	}

	name, err := getName(params)
	if err != nil {
		return nil, err
	}

	stdin, err := generate.GetBool(params, "stdin", false)
	if err != nil {
		return nil, err
	}
	leaveStdinOpen, err := generate.GetBool(params, "leave-stdin-open", false)
	if err != nil {
		return nil, err
	}

	tty, err := generate.GetBool(params, "tty", false)
	if err != nil {
		return nil, err
	}

	ec := v1.EphemeralContainer{
		EphemeralContainerCommon: v1.EphemeralContainerCommon{
			Name:            name,
			Args:            args,
			Env:             envs,
			Image:           params["image"],
			ImagePullPolicy: v1.PullPolicy(params["image-pull-policy"]),
			Stdin:           stdin,
			StdinOnce:       !leaveStdinOpen && stdin,
			TTY:             tty,
		},
		TargetContainerName: params["target"],
	}

	return &v1.EphemeralContainers{
		EphemeralContainers: []v1.EphemeralContainer{ec},
	}, nil
}
