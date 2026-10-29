IMAGE_NAME   ?= my-http-server:local
CLUSTER_NAME ?= obs-cluster

.PHONY: all image deploy-local clean

all: deploy-local

#build docker image
image:
	docker build -t $(IMAGE_NAME) .

#depends on image, uploads to kind, applies manifests
deploy-local: image
	kind load docker-image $(IMAGE_NAME) --name $(CLUSTER_NAME)
	kubectl apply -f k8s/deployment.yaml
	kubectl apply -f k8s/service.yaml
	kubectl apply -f k8s/ingress.yaml

clean:
	kubectl delete -f k8s/ --ignore-not-found