IMAGE_VERSION = 1.5
REGISTRY = netint
IMAGE = ${REGISTRY}/device-plugin:${IMAGE_VERSION}

.PHONY: build deploy

build:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o netint main.go server.go

buildImage:
	docker build -t ${IMAGE} .

deploy:
	helm install netint deploy/helm/netint

undeploy:
	helm uninstall netint

upgrade:
	helm upgrade netint deploy/helm/netint

dry-run:
	helm install netint deploy/helm/netint --dry-run