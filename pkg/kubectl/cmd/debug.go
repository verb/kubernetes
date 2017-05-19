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

package cmd

import (
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubernetes/pkg/api"
	coreclient "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset/typed/core/internalversion"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubectl/cmd/templates"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/util/i18n"
)

var (
	// TODO(verb)
	debug_example = templates.Examples(`
		# Get output from running 'date' from pod 123456-7890, using the first container by default
		kubectl debug 123456-7890 date

		# Get output from running 'date' in ruby-container from pod 123456-7890
		kubectl debug 123456-7890 -c ruby-container date

		# Switch to raw terminal mode, sends stdin to 'bash' in ruby-container from pod 123456-7890
		# and sends stdout/stderr from 'bash' back to the client
		kubectl debug 123456-7890 -c ruby-container -i -t -- bash -il`)
)

const (
	debugUsageStr             = "expected 'debug POD_NAME COMMAND [ARG1] [ARG2] ... [ARGN]'.\nPOD_NAME and COMMAND are required arguments for the debug command"
	debugDefaultContainerName = "debug"
	debugDefaultImageName     = "debian"
)

func NewCmdDebug(f cmdutil.Factory, cmdIn io.Reader, cmdOut, cmdErr io.Writer) *cobra.Command {
	if !utilfeature.DefaultFeatureGate.Enabled(features.DebugContainers) {
		return nil
	}

	options := &DebugOptions{
		StreamOptions: StreamOptions{
			In:  cmdIn,
			Out: cmdOut,
			Err: cmdErr,
		},

		Debugger: &DefaultRemoteDebugger{},
	}
	cmd := &cobra.Command{
		Use:     "debug POD [-c CONTAINER] -- COMMAND [args...]",
		Short:   i18n.T("Debug a command in a container"),
		Long:    "Debug a command in a container.",
		Example: debug_example,
		Run: func(cmd *cobra.Command, args []string) {
			argsLenAtDash := cmd.ArgsLenAtDash()
			cmdutil.CheckErr(options.Complete(f, cmd, args, argsLenAtDash))
			cmdutil.CheckErr(options.Validate())
			cmdutil.CheckErr(options.Run())
		},
	}
	cmd.Flags().StringVarP(&options.PodName, "pod", "p", "", "Pod name")
	// TODO support UID
	cmd.Flags().StringVarP(&options.ContainerName, "container", "c", "", fmt.Sprintf("Container name. [default=%s]", debugDefaultContainerName))
	cmd.Flags().StringVarP(&options.ImageName, "image", "m", "", fmt.Sprintf("Image name. [default=%s]", debugDefaultImageName))
	cmd.Flags().BoolVarP(&options.Stdin, "stdin", "i", false, "Pass stdin to the container")
	cmd.Flags().BoolVarP(&options.TTY, "tty", "t", false, "Stdin is a TTY")
	return cmd
}

// RemoteDebugger defines the interface accepted by the Debug command - provided for test stubbing
type RemoteDebugger interface {
	Debug(method string, url *url.URL, config *restclient.Config, stdin io.Reader, stdout, stderr io.Writer, tty bool, terminalSizeQueue remotecommand.TerminalSizeQueue) error
}

// DefaultRemoteDebugger is the standard implementation of remote command debugging
type DefaultRemoteDebugger struct{}

func (*DefaultRemoteDebugger) Debug(method string, url *url.URL, config *restclient.Config, stdin io.Reader, stdout, stderr io.Writer, tty bool, terminalSizeQueue remotecommand.TerminalSizeQueue) error {
	debug, err := remotecommand.NewExecutor(config, method, url)
	if err != nil {
		return err
	}
	return debug.Stream(remotecommand.StreamOptions{
		SupportedProtocols: remotecommandconsts.SupportedStreamingProtocols,
		Stdin:              stdin,
		Stdout:             stdout,
		Stderr:             stderr,
		Tty:                tty,
		TerminalSizeQueue:  terminalSizeQueue,
	})
}

// DebugOptions declare the arguments accepted by the Debug command
type DebugOptions struct {
	StreamOptions
	ImageName string

	Command []string

	FullCmdName       string
	SuggestedCmdUsage string

	Debugger  RemoteDebugger
	PodClient coreclient.PodsGetter
	Config    *restclient.Config
}

// Complete verifies command line arguments and loads data from the command environment
func (p *DebugOptions) Complete(f cmdutil.Factory, cmd *cobra.Command, argsIn []string, argsLenAtDash int) error {
	// Let kubectl debug follow rules for `--`, see #13004 issue
	if len(p.PodName) == 0 && (len(argsIn) == 0 || argsLenAtDash == 0) {
		return cmdutil.UsageError(cmd, debugUsageStr)
	}
	if len(p.PodName) != 0 {
		printDeprecationWarning("debug POD_NAME", "-p POD_NAME")
		if len(argsIn) < 1 {
			return cmdutil.UsageError(cmd, debugUsageStr)
		}
		p.Command = argsIn
	} else {
		p.PodName = argsIn[0]
		p.Command = argsIn[1:]
		if len(p.Command) < 1 {
			return cmdutil.UsageError(cmd, debugUsageStr)
		}
	}

	cmdParent := cmd.Parent()
	if cmdParent != nil {
		p.FullCmdName = cmdParent.CommandPath()
	}
	if len(p.FullCmdName) > 0 && cmdutil.IsSiblingCommandExists(cmd, "describe") {
		p.SuggestedCmdUsage = fmt.Sprintf("Use '%s describe pod/%s' to see all of the containers in this pod.", p.FullCmdName, p.PodName)
	}

	namespace, _, err := f.DefaultNamespace()
	if err != nil {
		return err
	}
	p.Namespace = namespace

	config, err := f.ClientConfig()
	if err != nil {
		return err
	}
	p.Config = config

	clientset, err := f.ClientSet()
	if err != nil {
		return err
	}
	p.PodClient = clientset.Core()

	return nil
}

// Validate checks that the provided debug options are specified.
func (p *DebugOptions) Validate() error {
	if len(p.PodName) == 0 {
		return fmt.Errorf("pod name must be specified")
	}
	if len(p.Command) == 0 {
		return fmt.Errorf("you must specify at least one command for the container")
	}
	if p.Out == nil || p.Err == nil {
		return fmt.Errorf("both output and error output must be provided")
	}
	if p.Debugger == nil || p.PodClient == nil || p.Config == nil {
		return fmt.Errorf("client, client config, and debugutor must be provided")
	}
	return nil
}

// Run debugs a validated remote debugging against a pod.
func (p *DebugOptions) Run() error {
	pod, err := p.PodClient.Pods(p.Namespace).Get(p.PodName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	/*
		if pod.Status.Phase == api.PodSucceeded || pod.Status.Phase == api.PodFailed {
			return fmt.Errorf("cannot debug into a container in a completed pod; current phase is %s", pod.Status.Phase)
		}
	*/

	containerName := p.ContainerName
	if len(containerName) == 0 {
		usageString := fmt.Sprintf("Defaulting container name to %s.", debugDefaultContainerName)
		if len(p.SuggestedCmdUsage) > 0 {
			usageString = fmt.Sprintf("%s\n%s", usageString, p.SuggestedCmdUsage)
		}
		fmt.Fprintf(p.Err, "%s\n", usageString)
		containerName = debugDefaultContainerName
	}

	imageName := p.ImageName
	if len(imageName) == 0 {
		usageString := fmt.Sprintf("Defaulting image name to %s.", debugDefaultImageName)
		if len(p.SuggestedCmdUsage) > 0 {
			usageString = fmt.Sprintf("%s\n%s", usageString, p.SuggestedCmdUsage)
		}
		fmt.Fprintf(p.Err, "%s\n", usageString)
		imageName = debugDefaultImageName
	}

	// ensure we can recover the terminal while attached
	t := p.setupTTY()

	var sizeQueue remotecommand.TerminalSizeQueue
	if t.Raw {
		// this call spawns a goroutine to monitor/update the terminal size
		sizeQueue = t.MonitorSize(t.GetSize())

		// unset p.Err if it was previously set because both stdout and stderr go over p.Out when tty is
		// true
		p.Err = nil
	}

	fn := func() error {
		restClient, err := restclient.RESTClientFor(p.Config)
		if err != nil {
			return err
		}

		// TODO: consider abstracting into a client invocation or client helper
		req := restClient.Post().
			Resource("pods").
			Name(pod.Name).
			Namespace(pod.Namespace).
			SubResource("debug")
		req.VersionedParams(&api.PodDebugOptions{
			Container: containerName,
			Image:     imageName,
			Command:   p.Command,
			Stdin:     p.Stdin,
			Stdout:    p.Out != nil,
			Stderr:    p.Err != nil,
			TTY:       t.Raw,
		}, api.ParameterCodec)

		return p.Debugger.Debug("POST", req.URL(), p.Config, p.In, p.Out, p.Err, t.Raw, sizeQueue)
	}

	if err := t.Safe(fn); err != nil {
		return err
	}

	return nil
}
