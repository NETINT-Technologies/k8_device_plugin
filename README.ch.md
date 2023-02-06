基于kubelet v1.17.7开发

编译
```
make build
```
根据编译结果制作K8S插件所用镜像
```
make buildImage
```
是用helm安装镜像。 k8s插件用的镜像需要用到特却模式，请确保kubelt启动时允许该模式
```
make deploy
``` 

安装完成后使用以下命令在K8S节点始能netint设备
```
kubectl label nodes xxx netint-device=enable
```

- 插件安装成功以后，
- 会在每个插有NETINT转码卡的节点显示资源信息，
- T4XX名称为netint.ca/ASIC,Quadra
- 名称为netint.ca/Quadra


pod-with-netint.yaml是一个基于NETINT 2.0.0 release的pod镜像示例，可以用来参考。
