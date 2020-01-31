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

package debug

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/distribution/reference"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/kubectl/pkg/cmd/attach"
	"k8s.io/kubectl/pkg/cmd/exec"
	"k8s.io/kubectl/pkg/cmd/logs"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/generate"
	generateversioned "k8s.io/kubectl/pkg/generate/versioned"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/kubectl/pkg/util"
	"k8s.io/kubectl/pkg/util/i18n"
	"k8s.io/kubectl/pkg/util/interrupt"
	"k8s.io/kubectl/pkg/util/templates"
	uexec "k8s.io/utils/exec"
)

var (
	debugLong = templates.LongDesc(i18n.T(`Tools for debugging Kubernetes resources`))

	debugExample = templates.Examples(i18n.T(`
		# Create a debugging container in pod mypod using defaults (a busybox image named "debugger")
		# and immediately attach to it using "kubectl attach".
		kubectl alpha debug mypod --attach

		# Create a debugging container in pod mypod using a custom debugging tools image and name.
		kubectl alpha debug -m gcr.io/verb-images/debug-tools -c debug-tools mypod

		# Current Limitations of kubectl alpha debug:
		# - kubectl alpha debug requires requires the EphemeralContainers feature to be enabled in the cluster.
		#   This feature is in alpha it is not enabled by default. See
		#   https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/ for
		#   enabling features.
		# - Container namespace targeting is not yet available. To view processes in other
		#   containers shareProcessNamespace must be enabled for the target pod.`))
)

const (
	defaultPodAttachTimeout = 60 * time.Second
)

var metadataAccessor = meta.NewAccessor()

type DebugObject struct {
	Object  runtime.Object
	Mapping *meta.RESTMapping
}

type DebugOptions struct {
	PrintFlags *genericclioptions.PrintFlags

	DryRun bool

	PrintObj func(runtime.Object) error

	DynamicClient dynamic.Interface

	ArgsLenAtDash  int
	Attach         bool
	Container      string
	Generator      string
	Image          string
	Interactive    bool
	LeaveStdinOpen bool
	Quiet          bool
	Schedule       string
	TTY            bool

	genericclioptions.IOStreams
}

func NewDebugOptions(streams genericclioptions.IOStreams) *DebugOptions {
	return &DebugOptions{
		PrintFlags: genericclioptions.NewPrintFlags("created").WithTypeSetter(scheme.Scheme),

		Generator: generateversioned.DebugContainerGeneratorName,

		IOStreams: streams,
	}
}

func NewCmdDebug(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := NewDebugOptions(streams)

	cmd := &cobra.Command{
		Use:                   "debug NAME --image=image -- [COMMAND] [args...]",
		DisableFlagsInUseLine: true,
		Short:                 i18n.T("Attach a debug container to a running pod"),
		Long:                  debugLong,
		Example:               debugExample,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, cmd))
			cmdutil.CheckErr(o.Run(f, cmd, args))
		},
	}

	o.PrintFlags.AddFlags(cmd)
	addDebugFlags(cmd, o)
	return cmd
}

func addDebugFlags(cmd *cobra.Command, opt *DebugOptions) {
	cmdutil.AddDryRunFlag(cmd)
	cmd.Flags().StringVar(&opt.Container, "conatiner", opt.Container, i18n.T("Container name to use for debug container."))
	cmd.Flags().StringVar(&opt.Image, "image", opt.Image, i18n.T("Container image to use for debug container."))
	cmd.MarkFlagRequired("image")
	cmd.Flags().String("image-pull-policy", "", i18n.T("The image pull policy for the container. If left empty, this value will not be specified by the client and defaulted by the server"))
	cmd.Flags().StringArray("env", []string{}, "Environment variables to set in the container.")
	cmd.Flags().StringP("labels", "l", "", "Comma separated labels to apply to the pod(s). Will override previous values.")
	cmd.Flags().BoolVarP(&opt.Interactive, "stdin", "i", opt.Interactive, "Keep stdin open on the container(s) in the pod, even if nothing is attached.")
	cmd.Flags().BoolVarP(&opt.TTY, "tty", "t", opt.TTY, "Allocated a TTY for each container in the pod.")
	cmd.Flags().BoolVar(&opt.Attach, "attach", opt.Attach, "If true, wait for the Pod to start running, and then attach to the Pod as if 'kubectl attach ...' were called.  Default false, unless '-i/--stdin' is set, in which case the default is true.")
	cmd.Flags().BoolVar(&opt.LeaveStdinOpen, "leave-stdin-open", opt.LeaveStdinOpen, "If the pod is started in interactive mode or with stdin, leave stdin open after the first attach completes. By default, stdin will be closed after the first attach completes.")
	cmd.Flags().Bool("command", false, "If true and extra arguments are present, use them as the 'command' field in the container, rather than the 'args' field which is the default.")
	cmd.Flags().String("target", "", i18n.T("Target processes in this container name."))
	cmd.Flags().BoolVar(&opt.Quiet, "quiet", opt.Quiet, "If true, suppress prompt messages.")
}

func (o *DebugOptions) Complete(f cmdutil.Factory, cmd *cobra.Command) error {
	var err error

	o.DynamicClient, err = f.DynamicClient()
	if err != nil {
		return err
	}

	o.ArgsLenAtDash = cmd.ArgsLenAtDash()
	o.DryRun = cmdutil.GetFlagBool(cmd, "dry-run")

	attachFlag := cmd.Flags().Lookup("attach")
	if !attachFlag.Changed && o.Interactive {
		o.Attach = true
	}

	if o.DryRun {
		o.PrintFlags.Complete("%s (dry run)")
	}
	printer, err := o.PrintFlags.ToPrinter()
	if err != nil {
		return err
	}
	o.PrintObj = func(obj runtime.Object) error {
		return printer.PrintObj(obj, o.Out)
	}

	return nil
}

func (o *DebugOptions) Run(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	// Let kubectl run follow rules for `--`, see #13004 issue
	if len(args) == 0 || o.ArgsLenAtDash == 0 {
		return cmdutil.UsageErrorf(cmd, "NAME is required for run")
	}

	// validate image name
	imageName := o.Image
	if imageName == "" {
		return fmt.Errorf("--image is required")
	}
	if !reference.ReferenceRegexp.MatchString(imageName) {
		return fmt.Errorf("Invalid image name %q: %v", imageName, reference.ErrReferenceInvalidFormat)
	}

	if o.TTY && !o.Interactive {
		return cmdutil.UsageErrorf(cmd, "-i/--stdin is required for containers with -t/--tty=true")
	}

	namespace, _, err := f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	if o.Attach && o.DryRun {
		return cmdutil.UsageErrorf(cmd, "--dry-run can't be used with attached containers options (--attach, --stdin, or --tty)")
	}

	if err := verifyImagePullPolicy(cmd); err != nil {
		return err
	}

	generators := generateversioned.GeneratorFn("debug")
	generator, found := generators[o.Generator]
	if !found {
		return cmdutil.UsageErrorf(cmd, "generator %q not found", o.Generator)
	}

	names := generator.ParamNames()
	params := generate.MakeParams(cmd, names)
	params["name"] = args[0]
	if len(args) > 1 {
		params["args"] = args[1:]
	}

	params["env"] = cmdutil.GetFlagStringArray(cmd, "env")

	var createdObjects = []*DebugObject{}
	debugObject, err := o.createGeneratedObject(f, cmd, generator, names, params, cmdutil.GetFlagString(cmd, "overrides"), namespace)
	if err != nil {
		return err
	}
	createdObjects = append(createdObjects, debugObject)

	allErrs := []error{}

	if o.Attach {
		opts := &attach.AttachOptions{
			StreamOptions: exec.StreamOptions{
				IOStreams: o.IOStreams,
				Stdin:     o.Interactive,
				TTY:       o.TTY,
				Quiet:     o.Quiet,
			},
			CommandName: cmd.Parent().CommandPath() + " attach",

			Attach: &attach.DefaultRemoteAttach{},
		}
		config, err := f.ToRESTConfig()
		if err != nil {
			return err
		}
		opts.Config = config
		opts.AttachFunc = attach.DefaultAttachFunc

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}

		attachablePod, err := polymorphichelpers.AttachablePodForObjectFn(f, debugObject.Object, opts.GetPodTimeout)
		if err != nil {
			return err
		}
		err = handleAttachPod(f, clientset.CoreV1(), attachablePod.Namespace, attachablePod.Name, opts)
		if err != nil {
			return err
		}

		var pod *corev1.Pod
		leaveStdinOpen := o.LeaveStdinOpen
		waitForExitCode := !leaveStdinOpen
		if waitForExitCode {
			pod, err = waitForPod(clientset.CoreV1(), attachablePod.Namespace, attachablePod.Name, podCompleted)
			if err != nil {
				return err
			}
		}

		// after removal is done, return successfully if we are not interested in the exit code
		if !waitForExitCode {
			return nil
		}

		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			unknownRcErr := fmt.Errorf("pod %s/%s failed with unknown exit code", pod.Namespace, pod.Name)
			if len(pod.Status.ContainerStatuses) == 0 || pod.Status.ContainerStatuses[0].State.Terminated == nil {
				return unknownRcErr
			}
			// assume here that we have at most one status because kubectl-run only creates one container per pod
			rc := pod.Status.ContainerStatuses[0].State.Terminated.ExitCode
			if rc == 0 {
				return unknownRcErr
			}
			return uexec.CodeExitError{
				Err:  fmt.Errorf("pod %s/%s terminated (%s)\n%s", pod.Namespace, pod.Name, pod.Status.ContainerStatuses[0].State.Terminated.Reason, pod.Status.ContainerStatuses[0].State.Terminated.Message),
				Code: int(rc),
			}
		default:
			return fmt.Errorf("pod %s/%s left in phase %s", pod.Namespace, pod.Name, pod.Status.Phase)
		}

	}
	if debugObject != nil {
		if err := o.PrintObj(debugObject.Object); err != nil {
			return err
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// waitForPod watches the given pod until the exitCondition is true
func waitForPod(podClient corev1client.PodsGetter, ns, name string, exitCondition watchtools.ConditionFunc) (*corev1.Pod, error) {
	// TODO: expose the timeout
	ctx, cancel := watchtools.ContextWithOptionalTimeout(context.Background(), 0*time.Second)
	defer cancel()

	preconditionFunc := func(store cache.Store) (bool, error) {
		_, exists, err := store.Get(&metav1.ObjectMeta{Namespace: ns, Name: name})
		if err != nil {
			return true, err
		}
		if !exists {
			// We need to make sure we see the object in the cache before we start waiting for events
			// or we would be waiting for the timeout if such object didn't exist.
			// (e.g. it was deleted before we started informers so they wouldn't even see the delete event)
			return true, errors.NewNotFound(corev1.Resource("pods"), name)
		}

		return false, nil
	}

	fieldSelector := fields.OneTermEqualSelector("metadata.name", name).String()
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fieldSelector
			return podClient.Pods(ns).List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fieldSelector
			return podClient.Pods(ns).Watch(options)
		},
	}

	intr := interrupt.New(nil, cancel)
	var result *corev1.Pod
	err := intr.Run(func() error {
		ev, err := watchtools.UntilWithSync(ctx, lw, &corev1.Pod{}, preconditionFunc, func(ev watch.Event) (bool, error) {
			return exitCondition(ev)
		})
		if ev != nil {
			result = ev.Object.(*corev1.Pod)
		}
		return err
	})

	return result, err
}

func handleAttachPod(f cmdutil.Factory, podClient corev1client.PodsGetter, ns, name string, opts *attach.AttachOptions) error {
	pod, err := waitForPod(podClient, ns, name, podRunningAndReady)
	if err != nil && err != ErrPodCompleted {
		return err
	}

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return logOpts(f, pod, opts)
	}

	opts.Pod = pod
	opts.PodName = name
	opts.Namespace = ns

	if opts.AttachFunc == nil {
		opts.AttachFunc = attach.DefaultAttachFunc
	}

	if err := opts.Run(); err != nil {
		fmt.Fprintf(opts.ErrOut, "Error attaching, falling back to logs: %v\n", err)
		return logOpts(f, pod, opts)
	}
	return nil
}

// logOpts logs output from opts to the pods log.
func logOpts(restClientGetter genericclioptions.RESTClientGetter, pod *corev1.Pod, opts *attach.AttachOptions) error {
	ctrName, err := opts.GetContainerName(pod)
	if err != nil {
		return err
	}

	requests, err := polymorphichelpers.LogsForObjectFn(restClientGetter, pod, &corev1.PodLogOptions{Container: ctrName}, opts.GetPodTimeout, false)
	if err != nil {
		return err
	}
	for _, request := range requests {
		if err := logs.DefaultConsumeRequest(request, opts.Out); err != nil {
			return err
		}
	}

	return nil
}

func verifyImagePullPolicy(cmd *cobra.Command) error {
	pullPolicy := cmdutil.GetFlagString(cmd, "image-pull-policy")
	switch corev1.PullPolicy(pullPolicy) {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		return nil
	case "":
		return nil
	}
	return cmdutil.UsageErrorf(cmd, "invalid image pull policy: %s", pullPolicy)
}

func (o *DebugOptions) createGeneratedObject(f cmdutil.Factory, cmd *cobra.Command, generator generate.Generator, names []generate.GeneratorParam, params map[string]interface{}, overrides, namespace string) (*DebugObject, error) {
	err := generate.ValidateParams(names, params)
	if err != nil {
		return nil, err
	}

	// TODO: Validate flag usage against selected generator. More tricky since --expose was added.
	obj, err := generator.Generate(params)
	if err != nil {
		return nil, err
	}

	mapper, err := f.ToRESTMapper()
	if err != nil {
		return nil, err
	}
	// run has compiled knowledge of the thing is creating
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil {
		return nil, err
	}
	mapping, err := mapper.RESTMapping(gvks[0].GroupKind(), gvks[0].Version)
	if err != nil {
		return nil, err
	}

	if len(overrides) > 0 {
		codec := runtime.NewCodec(scheme.DefaultJSONEncoder(), scheme.Codecs.UniversalDecoder(scheme.Scheme.PrioritizedVersionsAllGroups()...))
		obj, err = cmdutil.Merge(codec, obj, overrides)
		if err != nil {
			return nil, err
		}
	}

	actualObj := obj
	if !o.DryRun {
		if err := util.CreateOrUpdateAnnotation(cmdutil.GetFlagBool(cmd, cmdutil.ApplyAnnotationsFlag), obj, scheme.DefaultJSONEncoder()); err != nil {
			return nil, err
		}
		client, err := f.ClientForMapping(mapping)
		if err != nil {
			return nil, err
		}
		actualObj, err = resource.NewHelper(client, mapping).Create(namespace, false, obj)
		if err != nil {
			return nil, err
		}
	}

	return &DebugObject{
		Object:  actualObj,
		Mapping: mapping,
	}, nil
}

// ErrPodCompleted is returned by PodRunning or PodContainerRunning to indicate that
// the pod has already reached completed state.
var ErrPodCompleted = fmt.Errorf("pod ran to completion")

// podCompleted returns true if the pod has run to completion, false if the pod has not yet
// reached running state, or an error in any other case.
func podCompleted(event watch.Event) (bool, error) {
	switch event.Type {
	case watch.Deleted:
		return false, errors.NewNotFound(schema.GroupResource{Resource: "pods"}, "")
	}
	switch t := event.Object.(type) {
	case *corev1.Pod:
		switch t.Status.Phase {
		case corev1.PodFailed, corev1.PodSucceeded:
			return true, nil
		}
	}
	return false, nil
}

// podRunningAndReady returns true if the pod is running and ready, false if the pod has not
// yet reached those states, returns ErrPodCompleted if the pod has run to completion, or
// an error in any other case.
func podRunningAndReady(event watch.Event) (bool, error) {
	switch event.Type {
	case watch.Deleted:
		return false, errors.NewNotFound(schema.GroupResource{Resource: "pods"}, "")
	}
	switch t := event.Object.(type) {
	case *corev1.Pod:
		switch t.Status.Phase {
		case corev1.PodFailed, corev1.PodSucceeded:
			return false, ErrPodCompleted
		case corev1.PodRunning:
			conditions := t.Status.Conditions
			if conditions == nil {
				return false, nil
			}
			for i := range conditions {
				if conditions[i].Type == corev1.PodReady &&
					conditions[i].Status == corev1.ConditionTrue {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
