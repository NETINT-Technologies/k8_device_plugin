# Netint Device Plugin for Kubernetes 
## Instuctions

Complie
```
make build
```
Create docker image for k8s device plugin
```
make buildImage
```
Install docker image using helm. k8s device plugin needs privileged mode
```
make deploy         
```

After successfully deploy docker image, add a tag of each K8S node to enable netint card using below command
```
kubectl label nodes xxx netint-device=enable
```

- T4xx device shows in node resource as netint.ca/ASIC, 
- Quadra shows in node resource as netint.ca/Quadra
- pod-with-netint.yaml is a reference demo for NETINT release software


## Contact
Contact Netint for Application Notes and additional information

Email: support@netint.com
