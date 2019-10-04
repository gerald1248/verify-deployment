# verify-deployment

Lightweight controller for deployment pipelines.

A typical pipeline use case would be:

```
kubectl apply -f deployment.yaml && verify-deployment mydeployment
```

## Usage
```
$ verify-deployment -h
Usage: verify-deployment <DEPLOYMENT>
  -kubeconfig string
    	absolute path to the kubeconfig file
  -master string
    	master url
  -namespace string
    	namespace (default "default")
  -timeout int
    	timeout (s) (default 300)
```

## Scaling up a broken deployment
```
$ kubectl scale deployment analysis --replicas=3 && ./verify-deployment analysis
deployment.extensions/analysis scaled
2019/10/04 08:16:50 INFO: verifying deployment analysis in namespace default
2019/10/04 08:16:55 INFO: pod analysis-5658667675-2d8gb in waiting state (ContainerCreating)
2019/10/04 08:16:55 INFO: pod analysis-5658667675-fxnhh in waiting state (ContainerCreating)
2019/10/04 08:16:55 INFO: pod analysis-5658667675-x2vtv in waiting state (ContainerCreating)
2019/10/04 08:16:55 INFO: 0/3
2019/10/04 08:17:00 INFO: pod analysis-5658667675-2d8gb in waiting state (ContainerCreating)
2019/10/04 08:17:00 INFO: pod analysis-5658667675-fxnhh in waiting state (ContainerCreating)
2019/10/04 08:17:00 INFO: pod analysis-5658667675-x2vtv in waiting state (ContainerCreating)
2019/10/04 08:17:00 INFO: 0/3
2019/10/04 08:17:05 INFO: pod analysis-5658667675-2d8gb in waiting state (ContainerCreating)
2019/10/04 08:17:05 INFO: pod analysis-5658667675-fxnhh in waiting state (ContainerCreating)
2019/10/04 08:17:05 INFO: pod analysis-5658667675-x2vtv in waiting state (ContainerCreating)
2019/10/04 08:17:05 INFO: 0/3
2019/10/04 08:17:10 ERROR: pod analysis-5658667675-2d8gb in waiting state (ErrImagePull)
```

## Scaling down to zero
```
$ kubectl scale deployment analysis --replicas=0 && ./verify-deployment analysis
deployment.extensions/analysis scaled
2019/10/04 08:15:39 INFO: verifying deployment analysis in namespace default
2019/10/04 08:15:44 INFO: 0/0
2019/10/04 08:15:44 INFO: deployment analysis in namespace default verified
```
