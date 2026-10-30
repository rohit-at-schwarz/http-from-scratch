IMAGE_NAME     ?= my-http-server:local
CLUSTER_NAME   ?= obs-cluster
CLUSTER_CONFIG ?= kind-config.yml

.PHONY: all image cluster deploy-local clean clean-cluster

all: deploy-local

image:
	docker build -t $(IMAGE_NAME) .

cluster:
	@kind get clusters | grep -q "^$(CLUSTER_NAME)$$" || \
		kind create cluster --name $(CLUSTER_NAME) --config $(CLUSTER_CONFIG)

deploy-local: cluster image
	kind load docker-image $(IMAGE_NAME) --name $(CLUSTER_NAME)
	kubectl apply -f k8s/deployment.yml
	kubectl apply -f k8s/service.yml
	kubectl apply -f k8s/ingress.yml

clean:
	kubectl delete -f k8s/ --ignore-not-found

clean-cluster:
	kind delete cluster --name $(CLUSTER_NAME)