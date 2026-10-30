IMAGE_NAME     ?= my-http-server:local
CLUSTER_NAME   ?= obs-cluster
CLUSTER_CONFIG ?= kind-config.yml

# ingress-nginx is retired, controller-v1.15.1 (March 2026) is its last release.
# Fine for a local kind cluster, it just won't get fixes anymore
INGRESS_NGINX_MANIFEST ?= https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.15.1/deploy/static/provider/kind/deploy.yaml

# release name and namespace are baked into the service names used in k8s/deployment.yml
VMSTACK_RELEASE   ?= vmks
VMSTACK_NAMESPACE ?= monitoring
VMSTACK_CHART     ?= oci://ghcr.io/victoriametrics/helm-charts/victoria-metrics-k8s-stack
VMSTACK_VERSION   ?= 0.97.0
VMSTACK_VALUES    ?= deploy/vmstack/values.yml

.PHONY: all image cluster ingress vmstack vmstack-once deploy-local grafana vmui traces-ui clean clean-cluster

all: deploy-local

image:
	docker build -t $(IMAGE_NAME) .

cluster:
	@kind get clusters | grep -q "^$(CLUSTER_NAME)$$" || \
		kind create cluster --name $(CLUSTER_NAME) --config $(CLUSTER_CONFIG)

# the controller's admission webhook rejects Ingress objects until it is up, so wait for it
ingress: cluster
	kubectl apply -f $(INGRESS_NGINX_MANIFEST)
	kubectl wait --namespace ingress-nginx --for=condition=ready pod \
		--selector=app.kubernetes.io/component=controller --timeout=180s

# installs or upgrades the VM stack (operator, VMSingle, VMAgent, VTSingle, Grafana, ...),
# rerun it after changing the values file
vmstack: cluster
	helm upgrade --install $(VMSTACK_RELEASE) $(VMSTACK_CHART) --version $(VMSTACK_VERSION) \
		--namespace $(VMSTACK_NAMESPACE) --create-namespace -f $(VMSTACK_VALUES) --wait --timeout 10m

# installs the stack only if it isn't there yet, so repeated deploys stay fast
vmstack-once: cluster
	@helm status $(VMSTACK_RELEASE) --namespace $(VMSTACK_NAMESPACE) >/dev/null 2>&1 || $(MAKE) vmstack

# the image tag never changes, so the pod has to be restarted to run the image we just loaded
deploy-local: ingress vmstack-once image
	kind load docker-image $(IMAGE_NAME) --name $(CLUSTER_NAME)
	kubectl apply -f k8s/deployment.yml
	kubectl apply -f k8s/service.yml
	kubectl apply -f k8s/ingress.yml
	kubectl apply -f k8s/servicescrape.yml
	kubectl rollout restart deployment/my-http-server
	kubectl rollout status deployment/my-http-server --timeout=120s

grafana:
	@echo "grafana on http://localhost:3000 (admin / admin)"
	kubectl port-forward --namespace $(VMSTACK_NAMESPACE) svc/$(VMSTACK_RELEASE)-grafana 3000:80

vmui:
	@echo "VictoriaMetrics UI on http://localhost:8428/vmui"
	kubectl port-forward --namespace $(VMSTACK_NAMESPACE) svc/vmsingle-$(VMSTACK_RELEASE) 8428:8428

traces-ui:
	@echo "VictoriaTraces UI on http://localhost:10428/select/vmui"
	kubectl port-forward --namespace $(VMSTACK_NAMESPACE) svc/vtsingle-$(VMSTACK_RELEASE) 10428:10428

clean:
	kubectl delete -f k8s/ --ignore-not-found

clean-cluster:
	kind delete cluster --name $(CLUSTER_NAME)