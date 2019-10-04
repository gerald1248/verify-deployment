package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	rest "k8s.io/client-go/rest"

	au "github.com/logrusorgru/aurora"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <DEPLOYMENT>\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(1)
	}
	var kubeconfig string
	var master string
	var namespace string
	var timeout int

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.StringVar(&namespace, "namespace", "default", "namespace")
	flag.IntVar(&timeout, "timeout", 300, "timeout (s)")
	flag.Parse()

	if len(flag.Args()) == 0 {
		fmt.Fprintf(os.Stderr, "%s: deployment name required\n", au.Bold(au.Red("ERROR")))
		flag.Usage()
	}
	name := flag.Args()[0]

	// support out-of-cluster deployments (param, env var only)
	if len(kubeconfig) == 0 {
		kubeconfig = os.Getenv("KUBECONFIG")
	}

	var config *rest.Config
	var configError error

	if len(kubeconfig) > 0 {
		config, configError = clientcmd.BuildConfigFromFlags(master, kubeconfig)
		if configError != nil {
			fmt.Fprintf(os.Stderr, "%s (out-of-cluster): %s\n", au.Bold(au.Red("ERROR")), configError)
			os.Exit(1)
		}
	} else {
		config, configError = rest.InClusterConfig()
		if configError != nil {
			fmt.Fprintf(os.Stderr, "%s (in-cluster): %s\n", au.Bold(au.Red("ERROR")), configError)
			os.Exit(1)
		}
	}

	// creates clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", au.Bold(au.Red("ERROR")), err)
		os.Exit(1)
	}

	realMain(clientset, namespace, name, timeout)
}

func deploymentProgress(clientset kubernetes.Interface, namespace string, name string) (int32, int32, bool, error) {
	deployments, err := clientset.AppsV1().Deployments(namespace).List(metav1.ListOptions{})
	if err != nil {
		return 0, 0, false, err
	} else if len(deployments.Items) == 0 {
		return 0, 0, false, fmt.Errorf("no deployments in namespace %s", namespace)
	}
	for _, deployment := range deployments.Items {
		if deployment.ObjectMeta.Name == name {
			return deploymentObjectProgress(clientset, deployment, namespace)
		}
	}
	return 0, 0, false, fmt.Errorf("no deployment %s in namespace %s", name, namespace)
}

// deploymentObjectProgress returns:
// ready replicas (int32)
// desired replicas (int32)
// whether the images have been verified (bool)
// an optional error parameter
func deploymentObjectProgress(clientset kubernetes.Interface, deployment appsv1.Deployment, namespace string) (int32, int32, bool, error) {
	var desired int32
	var ready int32

	// fetch images
	containers := deployment.Spec.Template.Spec.Containers
	imageMap := make(map[string]int)
	for _, container := range containers {
		imageMap[container.Image] = 1
	}

	// fetch desired replica count
	desired = *deployment.Spec.Replicas

	// fetch selector
	set := labels.Set(deployment.Spec.Selector.MatchLabels)
	listOptions := metav1.ListOptions{LabelSelector: set.AsSelector().String()}

	pods, err := clientset.CoreV1().Pods(namespace).List(listOptions)
	if err != nil {
		return ready, desired, false, fmt.Errorf("can't fetch pods matching selector %v", deployment.Spec.Selector.MatchLabels)
	}

	// reset ready count
	ready = 0
	for _, pod := range pods.Items {
		var containerReady int
		var containerNotReady int

		name := pod.ObjectMeta.Name

		// process container image info
		podContainers := pod.Spec.Containers
		for _, podContainer := range podContainers {
			_, ok := imageMap[podContainer.Image]
			if !ok {
				// skip pod but don't throw error
				log.Println(fmt.Sprintf("%s: skipping pod with image %s", au.Bold(au.Cyan("INFO")), podContainer.Image))
				return ready, desired, false, nil
			}
		}

		// process status
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.Ready == true {
				containerReady++
			} else {
				containerNotReady++
			}

			// fail immediately in case of pod restarts
			restartCount := containerStatus.RestartCount
			if restartCount > 0 {
				plural := "s"
				if restartCount == 1 {
					plural = ""
				}
				return ready, desired, true, fmt.Errorf("pod %s has restarted %d time%s", name, restartCount, plural)
			}

			// also fail when deployment is in waiting state (e.g. ImagePullBackOff)
			if containerStatus.State.Waiting != nil {
				reason := containerStatus.State.Waiting.Reason
				switch reason {
					case "ErrImagePull":
						fallthrough
					case "ImagePullBackOff":
						return ready, desired, true, fmt.Errorf("pod %s in waiting state (%s)", name, reason)
					default:
						log.Println(fmt.Sprintf("%s: pod %s in waiting state (%s)", au.Bold(au.Cyan("INFO")), name, reason))
				}
			}
		}
		if containerNotReady == 0 && containerReady > 0 {
			ready++
		}
	}
	return ready, desired, true, nil
}

func realMain(clientset kubernetes.Interface, namespace string, name string, timeout int) {
	probeIntervalSeconds := 5

	log.Println(fmt.Sprintf("%s: verifying deployment %s in namespace %s", au.Bold(au.Cyan("INFO")), name, namespace))

	// set timeout ticker
	timeoutTicker := time.NewTicker(time.Millisecond * time.Duration(1000) * time.Duration(timeout))
	go func() {
		for range timeoutTicker.C {
			log.Println(fmt.Sprintf("%s: deployment %s timed out", au.Bold(au.Red("ERROR")), name))
			os.Exit(1)
		}
	}()

	// set polling ticker
	ticker := time.NewTicker(time.Millisecond * time.Duration(1000) * time.Duration(probeIntervalSeconds))
	go func() {
		for range ticker.C {
			ready, desired, imagesVerified, err := deploymentProgress(clientset, namespace, name)
			if err != nil {
				log.Println(fmt.Sprintf("%s: %v", au.Bold(au.Red("ERROR")), err))
				os.Exit(1)
			}

			log.Println(fmt.Sprintf("%s: %d/%d", au.Bold(au.Cyan("INFO")), au.Bold(ready), au.Bold(desired)))

			if ready == desired && imagesVerified {
				log.Println(fmt.Sprintf("%s: deployment %s in namespace %s verified", au.Bold(au.Cyan("INFO")), name, namespace))
				os.Exit(0)
			}
		}
	}()
	select {}
}
