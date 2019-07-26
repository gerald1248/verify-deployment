# verify-deployment

Lightweight controller for deployment pipelines.

A typical pipeline use case would be:

```
kubectl apply -f deployment.yaml && verify-deployment mydeployment
```

By default verification will pass if at least one pod reaches state Ready. You can raise the requirement using the ``--replicas`` flag as desired.

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
  -replicas int
    	replicas (default 1)
  -timeout int
    	timeout (s) (default 300)
```
