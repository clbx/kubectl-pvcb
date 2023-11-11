package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh/terminal"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func main() {

	var namespace string
	var image string

	app := &cli.App{
		Name:  "kubectl browse",
		Usage: "Kubernetes PVC Browser",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name: "namespace",
				// Set the default to the current context instead of default
				Value:       "default",
				Usage:       "Specify namespace of ",
				Aliases:     []string{"n"},
				Destination: &namespace,
			},
			&cli.StringFlag{
				Name: "image",
				//use the pvcb image
				Value:       "clbx/pvcb-edit",
				Usage:       "Image to mount job to",
				Aliases:     []string{"i"},
				Destination: &image,
			},
		},
		Action: getCommand,
	}

	// app.Commands = []*cli.Command{
	// 	{
	// 		Name:   "get",
	// 		Usage:  "Open a terminal with the PVC mounted",
	// 		Action: getCommand,
	// 	},
	// }

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}

}

func getCommand(c *cli.Context) error {

	if c.Args().Len() == 0 {
		return cli.NewExitError("ERROR: No PVC Defined", 1)
	}

	clientset, config := getClientSetFromKubeconfig()

	fmt.Printf("Namespace %s", c.String("namespace"))

	targetPvcName := c.Args().Get(0)
	targetPvc, err := clientset.CoreV1().PersistentVolumeClaims(c.String("namespace")).Get(context.TODO(), targetPvcName, metav1.GetOptions{})

	if err != nil {
		return cli.NewExitError(err, 1)
	}

	nsPods, err := clientset.CoreV1().Pods(c.String("namespace")).List(context.TODO(), metav1.ListOptions{})
	attachedPod := findPodByPVC(*nsPods, *targetPvc)

	if attachedPod == nil {
	} else {
		return cli.NewExitError("ERROR: PVC attached to Pod", 1)
	}

	get(clientset, config, c.String("namespace"), *targetPvc)

	return nil
}

func get(clientset *kubernetes.Clientset, config *rest.Config, namespace string, targetPvc corev1.PersistentVolumeClaim) error {

	// Build the Job
	pvcbGetJob := buildPvcbGetJob(namespace, targetPvc)
	// Create Job
	pvcbGetJob, err := clientset.BatchV1().Jobs(namespace).Create(context.TODO(), pvcbGetJob, metav1.CreateOptions{})

	if err != nil {
		panic(err)
	}

	timeout := 30

	// Wait 30 seconds for the Job to start.

	jobSpinner := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
	jobSpinner.Suffix = " Waiting for Job to Start\n"
	jobSpinner.FinalMSG = "✓ Job Started\n"
	jobSpinner.Start()

	for timeout > 0 {
		pvcbGetJob, err = clientset.BatchV1().Jobs(namespace).Get(context.TODO(), pvcbGetJob.GetObjectMeta().GetName(), metav1.GetOptions{})

		if err != nil {
			panic(err.Error())
		}

		if pvcbGetJob.Status.Active > 0 {
			fmt.Printf("Job is running \n")
			jobSpinner.Stop()
			break
		}

		time.Sleep(time.Second)

		timeout--
	}

	// Find the created pod.
	podList, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: "job-name=" + pvcbGetJob.Name,
	})

	if err != nil {
		panic(err.Error())
	}

	if len(podList.Items) != 1 {
		fmt.Printf("%d\n", len(podList.Items))
		panic("Found more or less than one pod")
	}

	pod := &podList.Items[0]

	podSpinner := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
	podSpinner.Suffix = " Waiting for Pod to Start\n"
	podSpinner.FinalMSG = "✓ Pod Started\n"
	podSpinner.Start()

	for pod.Status.Phase != corev1.PodRunning && timeout > 0 {

		pod, err = clientset.CoreV1().Pods(namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
		if err != nil {
			panic(err.Error())
		}

		time.Sleep(time.Second)
		timeout--
	}

	podSpinner.Stop()
	if timeout == 0 {
		panic("Pod failed to start")
	}

	req := clientset.CoreV1().RESTClient().
		Post().Resource("pods").
		Name(pod.Name).
		Namespace(namespace).
		SubResource("exec").
		Param("stdin", "true").
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "true").
		Param("command", "bash").
		Param("command", "-c").
		Param("command", "cd /mnt && /bin/bash")

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		panic(err.Error())
	}

	oldState, err := terminal.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}

	defer terminal.Restore(int(os.Stdin.Fd()), oldState)

	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Tty:    true,
	})

	if err != nil {
		panic(err.Error())
	}

	return nil

}
