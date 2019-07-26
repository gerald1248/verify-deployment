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

	phase := obj.(*v1.Pod).Status.Phase
	readyCount := 0
	containerCount := 0
	for _, containerStatus := range obj.(*v1.Pod).Status.ContainerStatuses {
		containerCount++
		if containerStatus.Ready == true {
			readyCount++
		}
	}

	switch phase {
	case "Pending":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Cyan("INFO")), name, au.Bold(phase), readyCount, containerCount))
		return nil
	case "Running":
		log.Println(fmt.Sprintf("%s: pod %s %s (%d/%d)", au.Bold(au.Cyan("INFO")), name, au.Bold(phase), readyCount, containerCount))
		if readyCount < containerCount {
			return nil
		}
		c.mutex.Lock()
		c.state.running += 1
		c.mutex.Unlock()
	case "Succeeded":
		log.Println(fmt.Sprintf("%s: pod %s %s", au.Bold(au.Cyan("INFO")), name, au.Bold(phase)))
		c.mutex.Lock()
		c.state.running += 1
		c.mutex.Unlock()
	case "Failed":
		log.Println(fmt.Sprintf("%s: pod %s %s", au.Bold(au.Red("ERROR")), name, au.Bold(phase)))
		os.Exit(1)
	}

	if c.state.running >= c.state.replicas {
		log.Println(fmt.Sprintf("%s: deployment %s verified", au.Bold(au.Green("OK")), c.state.name))
		os.Exit(0)
	}

	// let historical events drain away first
	if c.queue.Len() == 0 {
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
	var replicas int
	var timeout int

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.StringVar(&namespace, "namespace", "default", "namespace")
	flag.IntVar(&replicas, "replicas", 1, "replicas")
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

	realMain(clientset, getState(name, namespace, replicas, time.Now().Local().Unix()))
}

func realMain(clientset kubernetes.Interface, state State) {
	var mutex = &sync.Mutex{}

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
