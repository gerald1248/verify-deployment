// TODO: flag first restart as failure

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rest "k8s.io/client-go/rest"

	au "github.com/logrusorgru/aurora"
)

// NewController constructs the central controller state
func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller, clientset kubernetes.Interface, mutex *sync.Mutex, state State) *Controller {
	return &Controller{
		informer:  informer,
		indexer:   indexer,
		queue:     queue,
		clientset: clientset,
		mutex:     mutex,
		state:     state,
	}
}

func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.syncToStdout(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *Controller) syncToStdout(key string) error {
	obj, _, err := c.indexer.GetByKey(key)
	if err != nil {
		log.Println(fmt.Sprintf("%s: fetching object with key %s from store failed with %v", au.Bold(au.Red("ERROR")), key, err))
		return err
	}

	// exit condition 1: nil interface received (e.g. after manual resource deletion)
	// nothing to do here; return gracefully
	if obj == nil {
		return nil
	}

	// exit condition 2: no conditions recorded
	conditions := obj.(*v1.Pod).Status.Conditions
	if len(conditions) == 0 {
		return nil
	}

	// exit condition 3: last transition time predates verifier
	lastTransitionTime := conditions[len(conditions)-1].LastTransitionTime
	if lastTransitionTime.Unix() < c.state.time {
		return nil
	}

	name := obj.(*v1.Pod).ObjectMeta.Name

	// pods managed by deployments carry hashed information after trailing dash
	if (!strings.HasPrefix(name, c.state.name)) {
		return nil
	}

	// now we know we're dealing with a phase change for our deployment
	// we'd be done if deployments reported on pods' ready status,
	// but as they don't we count the running pods
	runningReplicas, err := getRunningReplicas(c.clientset, c.state)
	if err != nil {
		log.Println(fmt.Sprintf("%s: can't determine number of running replicas", au.Bold(au.Red("ERROR"))))
		return nil
	}

	phase := obj.(*v1.Pod).Status.Phase
	switch phase {
	case "Pending":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Cyan("INFO")), name, au.Bold(phase), runningReplicas, c.state.desiredReplicas))
	case "Running":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Cyan("INFO")), name, au.Bold(phase), runningReplicas, c.state.desiredReplicas))
	case "Succeeded":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Cyan("INFO")), name, au.Bold(phase), runningReplicas, c.state.desiredReplicas))
	case "Failed":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Red("ERROR")), name, au.Bold(phase), runningReplicas, c.state.desiredReplicas))
		os.Exit(1)
        }

	if runningReplicas == c.state.desiredReplicas {
                log.Println(fmt.Sprintf("%s: deployment %s verified", au.Bold(au.Green("OK")), c.state.name))
                os.Exit(0)
	}

	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < 5 {
		log.Println(fmt.Sprintf("%s: can't sync event %v: %v", au.Bold(au.Red("ERROR")), key, err))
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	runtime.HandleError(err)
	log.Println(fmt.Sprintf("%s: dropping event %q from the queue: %v", au.Bold(au.Cyan("INFO")), key, err))
}

// Run manages the controller lifecycle
func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	defer c.queue.ShutDown()
	log.Println(fmt.Sprintf("%s: verifying deployment %s", au.Bold(au.Cyan("INFO")), c.state.name))
	plural := ""
	if c.state.desiredReplicas != 1 {
		plural = "s"
	}
	log.Println(fmt.Sprintf("%s: %d replica%s desired", au.Bold(au.Cyan("INFO")), au.Bold(c.state.desiredReplicas), plural))

	go c.informer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	log.Println(fmt.Sprintf("%s: stopped verifying deployment", au.Bold(au.Cyan("INFO"))))
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

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

	// set timeout ticker
	ticker := time.NewTicker(time.Millisecond * time.Duration(1000) * time.Duration(timeout))
	go func() {
		for range ticker.C {
			fmt.Fprintf(os.Stderr, "%s: deployment %s timed out\n", au.Bold(au.Red("ERROR")), name)
			os.Exit(1)
		}
	}()

	// creates clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", au.Bold(au.Red("ERROR")), err)
		os.Exit(1)
	}

	realMain(clientset, getState(name, namespace, time.Now().Local().Unix()))
}

func getDesiredReplicas(clientset kubernetes.Interface, state State) (int32, error) {
        deployments, err := clientset.AppsV1().Deployments(state.namespace).List(metav1.ListOptions{})
	if err != nil {
		return 0, err
	} else if len(deployments.Items) == 0 {
		return 0, fmt.Errorf("no deployments in namespace %s", state.namespace)
	}
	for _, deployment := range deployments.Items {
		if deployment.ObjectMeta.Name != state.name {
			continue
		}
		return *deployment.Spec.Replicas, nil
	}
	return 0, fmt.Errorf("no deployment %s in namespace %s", state.name, state.namespace)
}

func getRunningReplicas(clientset kubernetes.Interface, state State) (int32, error) {
        pods, err := clientset.CoreV1().Pods(state.namespace).List(metav1.ListOptions{})

        if err != nil {
                return 0, err
        }

        var ready int32
	ready = 0
        for _, pod := range pods.Items {
                podName := pod.ObjectMeta.Name
                if strings.HasPrefix(podName, state.name) {
			var containerReady int
			var containerNonReady int
                        for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Ready == true {
					containerReady++
				} else {
					containerNonReady++
				}
			}
			if containerNonReady == 0 && containerReady > 0 {
				ready++
			}
                }
	}

	return ready, nil
}

func realMain(clientset kubernetes.Interface, state State) {
	var mutex = &sync.Mutex{}

	desiredReplicas, err := getDesiredReplicas(clientset, state)
	if err != nil {
		log.Println(fmt.Sprintf("%s: %v", au.Bold(au.Red("ERROR")), err))
		return
	}
	state.desiredReplicas = desiredReplicas

	eventListWatcher := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "pods", state.namespace, fields.Everything())

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	indexer, informer := cache.NewIndexerInformer(eventListWatcher, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(queue, indexer, informer, clientset, mutex, state)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	select {}
}
