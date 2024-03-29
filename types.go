package main

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sync"
)

// Controller represents the controller state
type Controller struct {
	indexer   cache.Indexer
	queue     workqueue.RateLimitingInterface
	informer  cache.Controller
	clientset kubernetes.Interface
	mutex     *sync.Mutex
	state     State
}

type State struct {
	name            string
	namespace       string
	running         int32
	time            int64
	desiredReplicas int32
}
